#!/bin/bash
# IR Injector — UserPromptSubmit hook. On turn 1: index, snapshot, inject context.
INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")

STATE_FILE="$HOME/.claude/hooks/.governor-state/$SESSION_ID"
COUNT=$(cat "$STATE_FILE" 2>/dev/null || echo "0")
[ "$COUNT" != "1" ] && exit 0

if command -v ai-ir &>/dev/null; then
  ai-ir "$PWD" &>/dev/null || true
  ai-ir snapshot --label=session-start --session="$SESSION_ID" "$PWD" &>/dev/null || true
fi

if command -v ai-context &>/dev/null; then
  PROMPT=$(echo "$INPUT" | jq -r '.prompt // ""' 2>/dev/null || true)
  ai-context --budget=4000 --session="$SESSION_ID" --prompt="$PROMPT" "$PWD"
fi

exit 0
