#!/usr/bin/env bash
# Destructive Bash Guard — PreToolUse hook on Bash tool.
# Intercepts destructive shell commands before execution.
# Memory-independent: runs on every Bash call regardless of model context.
#
# Two tiers:
#   HARD DENY  — catastrophic/unrecoverable ops. Cannot be overridden.
#   SOFT DENY  — dangerous but potentially legitimate. Escalates to user approval (ask).
#
# Logging: every decision is appended to $STATE_DIR/safety-audit.jsonl

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")
COMMAND=$(echo "$INPUT" | jq -r '.tool_input.command // ""' 2>/dev/null || echo "")

CONFIG_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
STATE_DIR="$CONFIG_DIR/hooks/.governor-state"
mkdir -p "$STATE_DIR" 2>/dev/null

# --- Helpers ---

log_decision() {
  local decision="$1" pattern="$2"
  local ts
  ts=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || echo "unknown")
  echo "{\"ts\":\"$ts\",\"session\":\"$SESSION_ID\",\"hook\":\"destructive-bash-guard\",\"decision\":\"$decision\",\"pattern\":$(echo "$pattern" | jq -Rs .),\"command\":$(echo "$COMMAND" | jq -Rs .)}" \
    >> "$STATE_DIR/safety-audit.jsonl" 2>/dev/null || true
}

deny() {
  local reason="$1"
  log_decision "deny" "$2"
  echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":$(echo "SAFETY GUARD (hard deny): $reason" | jq -Rs .)}}"
  exit 0
}

ask() {
  local reason="$1"
  log_decision "ask" "$2"
  echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"ask\",\"permissionDecisionReason\":$(echo "SAFETY GUARD: $reason" | jq -Rs .)}}"
  exit 0
}

allow() {
  echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
  exit 0
}

# Normalize: lowercase, collapse whitespace
CMD_NORM=$(echo "$COMMAND" | tr '[:upper:]' '[:lower:]' | tr -s ' \t' ' ')

# --- HARD DENY: Catastrophic / unrecoverable ---
# These patterns wipe filesystems, homes, or entire repos.
# No user override exists at the hook level.

# rm -rf targeting /, ~, ., or glob * (root/home/cwd/all wipe)
if echo "$CMD_NORM" | grep -qE 'rm[[:space:]]+-[a-z]*rf[[:space:]]+(/[[:space:]]|/\*|~[[:space:]]|~\/\*|\.[[:space:]]|\.\*|"\."|\./\*)'; then
  deny "Recursive force delete targeting root, home, cwd, or glob. This cannot be overridden." "rm-rf-catastrophic"
fi
if echo "$CMD_NORM" | grep -qE 'rm[[:space:]]+-[a-z]*r[[:space:]]+-[a-z]*f[[:space:]]+(/[[:space:]]|/\*|~[[:space:]]|~\/\*|\.[[:space:]]|\.\*|"\."|\./\*)'; then
  deny "Recursive force delete targeting root, home, cwd, or glob. This cannot be overridden." "rm-rf-catastrophic"
fi

# mkfs — filesystem format
if echo "$CMD_NORM" | grep -qE '^[[:space:]]*mkfs'; then
  deny "Filesystem format command (mkfs) blocked." "mkfs"
fi

# dd writing to a raw device
if echo "$CMD_NORM" | grep -qE 'dd[[:space:]].*of=/dev/'; then
  deny "dd writing to raw device blocked." "dd-device"
fi

# Fork bomb
if echo "$CMD_NORM" | grep -qE ':\(\)\{.*:\|:'; then
  deny "Fork bomb pattern detected." "fork-bomb"
fi

# git push --force to main/master
if echo "$CMD_NORM" | grep -qE 'git[[:space:]]+push[[:space:]]+.*--force[[:space:]]+.*\b(main|master)\b'; then
  deny "Force push to main/master is blocked." "git-force-push-main"
fi
if echo "$CMD_NORM" | grep -qE 'git[[:space:]]+push[[:space:]]+-f[[:space:]]+.*\b(main|master)\b'; then
  deny "Force push to main/master is blocked." "git-force-push-main"
fi

# Writing over critical Claude settings files via shell redirect
if echo "$CMD_NORM" | grep -qE '>[[:space:]]*(.*\.claude/settings|.*\.claude-work/settings)'; then
  deny "Overwriting Claude settings files via shell redirect is blocked." "settings-overwrite"
fi

# --- SOFT DENY: Destructive but potentially legitimate — escalate to user ---

# rm -rf any path (not already caught above)
if echo "$CMD_NORM" | grep -qE 'rm[[:space:]]+-[a-z]*rf|rm[[:space:]]+-[a-z]*r[[:space:]]+-[a-z]*f'; then
  # Already hard-denied above if targeting / ~ . *
  # This catches everything else: rm -rf some/specific/path
  ask "Recursive force delete detected. Confirm this is intentional." "rm-rf-path"
fi

# git reset --hard
if echo "$CMD_NORM" | grep -qE 'git[[:space:]]+reset[[:space:]]+--hard'; then
  ask "git reset --hard will discard uncommitted changes permanently. Confirm." "git-reset-hard"
fi

# git clean -f or -fd or -fdx
if echo "$CMD_NORM" | grep -qE 'git[[:space:]]+clean[[:space:]]+-[a-z]*f'; then
  ask "git clean -f will permanently delete untracked files. Confirm." "git-clean"
fi

# git checkout -- . or git restore .
if echo "$CMD_NORM" | grep -qE 'git[[:space:]]+(checkout|restore)[[:space:]]+-+[[:space:]]*\.'; then
  ask "Discarding all working tree changes. Confirm." "git-discard-all"
fi

# git push --force (not to main/master — those are hard denied above)
if echo "$CMD_NORM" | grep -qE 'git[[:space:]]+push[[:space:]]+.*(--force|-f)\b'; then
  ask "Force push detected. Confirm this is intentional." "git-force-push"
fi

# SQL destructive patterns
if echo "$CMD_NORM" | grep -qE '\b(drop[[:space:]]+table|truncate[[:space:]]+(table[[:space:]]+)?[a-z]|delete[[:space:]]+from[[:space:]]+[a-z]+[[:space:]]+where[[:space:]]+(1=1|true|1))'; then
  ask "Destructive SQL operation detected (DROP TABLE / TRUNCATE / mass DELETE). Confirm." "sql-destructive"
fi

# Pipe-to-shell patterns (remote code execution)
if echo "$CMD_NORM" | grep -qE '(curl|wget)[[:space:]].*[|][[:space:]]*(bash|sh|zsh|python|node)|eval[[:space:]]*"\$\(curl'; then
  ask "Pipe-to-shell or eval-curl pattern detected. Confirm this is from a trusted source." "pipe-to-shell"
fi

# --- Allow ---
allow
