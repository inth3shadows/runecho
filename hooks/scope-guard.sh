#!/bin/bash
# Scope Guard — PreToolUse hook on Edit and Write tools.
# Enforces per-project file write restrictions via .ai/scope-lock.json.
# Opt-in: if scope-lock.json does not exist, everything is allowed.
#
# Always-on (regardless of scope-lock): protects settings files, keys, certs.
#
# scope-lock.json format:
# {
#   "allowed_paths": ["hooks/", "cmd/", "internal/", ".ai/"],  // optional whitelist
#   "denied_paths": [".claude/settings", ".env"],               // always-deny prefixes
#   "denied_patterns": ["\\.key$", "\\.pem$", "secret"]        // always-deny regex
# }

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")
TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // ""' 2>/dev/null || echo "")
FILE_PATH=$(echo "$INPUT" | jq -r '.tool_input.file_path // ""' 2>/dev/null || echo "")

CONFIG_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
STATE_DIR="$CONFIG_DIR/hooks/.governor-state"
mkdir -p "$STATE_DIR" 2>/dev/null

allow() {
  echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
  exit 0
}

deny() {
  local reason="$1"
  local ts
  ts=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || echo "unknown")
  echo "{\"ts\":\"$ts\",\"session\":\"$SESSION_ID\",\"hook\":\"scope-guard\",\"decision\":\"deny\",\"reason\":$(echo "$reason" | jq -Rs .),\"file_path\":$(echo "$FILE_PATH" | jq -Rs .)}" \
    >> "$STATE_DIR/safety-audit.jsonl" 2>/dev/null || true
  echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":$(echo "SCOPE GUARD: $reason" | jq -Rs .)}}"
  exit 0
}

# No file path — allow (tool may be called without it)
[ -z "$FILE_PATH" ] && allow

# Normalize file path (resolve relative to CWD if needed)
# Strip leading drive prefix for pattern matching (e.g., /c/Users/... or C:\...)
FILE_NORM=$(echo "$FILE_PATH" | sed 's|\\|/|g' | tr '[:upper:]' '[:lower:]')

# --- Always-On Protections (independent of scope-lock.json) ---

# Protect Claude settings files — model must not modify its own constraints
if echo "$FILE_NORM" | grep -qE '\.claude/settings|\.claude-work/settings'; then
  deny "Claude settings files (.claude/settings*.json) cannot be modified. They define hook enforcement rules."
fi

# Protect hook scripts themselves
if echo "$FILE_NORM" | grep -qE '(\.claude/hooks/|\.claude-work/hooks/).*\.(sh|json)$'; then
  deny "Claude hook files cannot be modified mid-session. Changes take effect only after session restart with user review."
fi

# Protect credential and key files
if echo "$FILE_NORM" | grep -qE '\.(key|pem|p12|pfx|crt|cer)$|credentials\.json$|\.netrc$|anthropic.*\.key'; then
  deny "Credential/key file write blocked. Path: $FILE_PATH"
fi

# Protect .env files
if echo "$FILE_NORM" | grep -qE '(^|/)\.env(\.[a-z]+)?$'; then
  deny ".env file write blocked. Use explicit user approval for environment file changes."
fi

# --- Scope-Lock (opt-in per project) ---

SCOPE_LOCK="$PWD/.ai/scope-lock.json"
[ ! -f "$SCOPE_LOCK" ] && allow

# Load scope-lock config
ALLOWED_PATHS=$(jq -r '.allowed_paths // [] | .[]' "$SCOPE_LOCK" 2>/dev/null || echo "")
DENIED_PATHS=$(jq -r '.denied_paths // [] | .[]' "$SCOPE_LOCK" 2>/dev/null || echo "")
DENIED_PATTERNS=$(jq -r '.denied_patterns // [] | .[]' "$SCOPE_LOCK" 2>/dev/null || echo "")

# Compute path relative to CWD for scope checking
CWD_NORM=$(echo "$PWD" | sed 's|\\|/|g' | tr '[:upper:]' '[:lower:]')
REL_PATH="${FILE_NORM#$CWD_NORM/}"
# If path didn't start with CWD, use as-is
[ "$REL_PATH" = "$FILE_NORM" ] && REL_PATH="$FILE_NORM"

# Check denied_paths (prefix match)
while IFS= read -r dp; do
  [ -z "$dp" ] && continue
  DP_NORM=$(echo "$dp" | tr '[:upper:]' '[:lower:]')
  if echo "$REL_PATH" | grep -q "^$DP_NORM"; then
    deny "Path '$FILE_PATH' matches denied_paths entry '$dp' in .ai/scope-lock.json"
  fi
done <<< "$DENIED_PATHS"

# Check denied_patterns (regex match)
while IFS= read -r pattern; do
  [ -z "$pattern" ] && continue
  if echo "$REL_PATH" | grep -qE "$pattern"; then
    deny "Path '$FILE_PATH' matches denied_patterns '$pattern' in .ai/scope-lock.json"
  fi
done <<< "$DENIED_PATTERNS"

# Check allowed_paths (if non-empty, path must match one of them)
if [ -n "$ALLOWED_PATHS" ]; then
  MATCHED=false
  while IFS= read -r ap; do
    [ -z "$ap" ] && continue
    AP_NORM=$(echo "$ap" | tr '[:upper:]' '[:lower:]')
    if echo "$REL_PATH" | grep -q "^$AP_NORM"; then
      MATCHED=true
      break
    fi
  done <<< "$ALLOWED_PATHS"

  if [ "$MATCHED" = "false" ]; then
    ALLOWED_LIST=$(echo "$ALLOWED_PATHS" | tr '\n' ',' | sed 's/,$//')
    deny "Path '$FILE_PATH' is outside allowed scope. Allowed: $ALLOWED_LIST. Edit .ai/scope-lock.json to adjust."
  fi
fi

allow
