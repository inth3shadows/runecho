#!/bin/bash
# IR Injector — UserPromptSubmit hook
# Fires on every user message. On turn 1 only, injects IR CONTEXT block from .ai/ir.json.
# Silent on all other turns or if .ai/ir.json is not found.
#
# Requires: jq (gracefully skips if missing)
# Depends on: session-governor.sh having already written the turn count to the state file.
#             Wire governor first in settings.json so it runs before this hook.

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")

# Check if we're on turn 1
STATE_FILE="$HOME/.claude/hooks/.governor-state/$SESSION_ID"
if [ ! -f "$STATE_FILE" ]; then
  exit 0
fi

COUNT=$(cat "$STATE_FILE" 2>/dev/null || echo "0")
if [ "$COUNT" != "1" ]; then
  exit 0
fi

# Auto-index: run ai-ir on turn 1 if the binary is available.
# Incremental — only re-parses changed files, so this is typically <1s.
# Silent on failure; falls through to whatever ir.json exists (or none).
if command -v ai-ir &>/dev/null; then
  ai-ir "$PWD" &>/dev/null || true
fi

# Look for .ai/ir.json in current working directory (project root)
IR_FILE="$PWD/.ai/ir.json"
if [ ! -f "$IR_FILE" ]; then
  exit 0
fi

# Require jq
if ! command -v jq &>/dev/null; then
  exit 0
fi

# Parse IR fields
ROOT_HASH=$(jq -r '.root_hash // ""' "$IR_FILE" 2>/dev/null)
SHORT_HASH="${ROOT_HASH:0:12}"

FILE_COUNT=$(jq '.files | length' "$IR_FILE" 2>/dev/null || echo "0")
FILE_LIST=$(jq -r '.files | keys | join(", ")' "$IR_FILE" 2>/dev/null || echo "")

# Collect all symbols: functions + classes across all files, deduplicated and sorted
SYMBOLS=$(jq -r '
  [.files[].functions[], .files[].classes[]] | unique | sort | join(", ")
' "$IR_FILE" 2>/dev/null || echo "")
SYMBOL_COUNT=$(jq -r '
  [.files[].functions[], .files[].classes[]] | unique | length
' "$IR_FILE" 2>/dev/null || echo "0")

echo "IR CONTEXT [root_hash: ${SHORT_HASH}...]:"
echo "${FILE_COUNT} files — ${FILE_LIST}"
echo "Symbols (${SYMBOL_COUNT}): ${SYMBOLS}"

# --- Session Handoff Injection ---
HANDOFF_FILE="$PWD/.ai/handoff.md"
HANDOFF_DIR="$PWD/.ai/handoffs"

if [ -f "$HANDOFF_FILE" ]; then
  # Rotate: archive before injecting (cp not mv — original stays until Claude overwrites it)
  mkdir -p "$HANDOFF_DIR" 2>/dev/null
  TIMESTAMP=$(date '+%Y-%m-%dT%H%M' 2>/dev/null || date '+%Y%m%d%H%M')
  cp "$HANDOFF_FILE" "$HANDOFF_DIR/${TIMESTAMP}.md" 2>/dev/null || true
  # Prune: keep last 10
  ls -t "$HANDOFF_DIR"/*.md 2>/dev/null | tail -n +11 | xargs rm -f 2>/dev/null || true

  # Staleness gate: only inject if < 7 days old
  # Use `find -mtime -7` — works in Git Bash on Windows (date -r does NOT)
  if find "$HANDOFF_FILE" -mtime -7 2>/dev/null | grep -q .; then
    echo ""
    echo "PREVIOUS SESSION HANDOFF [$(date -r "$HANDOFF_FILE" '+%Y-%m-%d' 2>/dev/null || date '+%Y-%m-%d')]:"
    cat "$HANDOFF_FILE"
  fi
fi

exit 0
