#!/bin/bash
# Stop hook — fires after each Claude response
# Writes .ai/checkpoint.json with current session state
# Used by session-end.sh for failure recovery

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")
CWD=$(echo "$INPUT" | jq -r '.cwd // ""' 2>/dev/null)
[ -z "$CWD" ] && CWD="$PWD"

STATE_DIR="$HOME/.claude/hooks/.governor-state"
STATE_FILE="$STATE_DIR/$SESSION_ID"
COUNT=$(cat "$STATE_FILE" 2>/dev/null || echo "0")
IR_HASH=$(jq -r '.root_hash // ""' "$CWD/.ai/ir.json" 2>/dev/null | head -c 12 || echo "")
LAST_MSG=$(echo "$INPUT" | jq -r '.last_assistant_message // ""' 2>/dev/null | head -c 200)
TS=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date '+%Y-%m-%dT%H:%M:%S')

mkdir -p "$CWD/.ai" 2>/dev/null

jq -n \
  --arg ts "$TS" \
  --arg sid "$SESSION_ID" \
  --argjson turn "$COUNT" \
  --arg ir_hash "$IR_HASH" \
  --arg last_msg "$LAST_MSG" \
  '{ts: $ts, session_id: $sid, turn: $turn, ir_hash: $ir_hash, last_assistant_message: $last_msg}' \
  > "$CWD/.ai/checkpoint.json" 2>/dev/null || true

exit 0
