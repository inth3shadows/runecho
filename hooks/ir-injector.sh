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

exit 0
