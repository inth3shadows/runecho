#!/usr/bin/env bash
# IR Injector — UserPromptSubmit hook. On turn 1: index, snapshot, inject context.

# shellcheck disable=SC1091
. "$(dirname "$0")/fault-emitter.sh"

_hook_start=$SECONDS

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")
WORKSPACE=$(echo "$INPUT" | jq -r '.cwd // ""' 2>/dev/null)
[ -z "$WORKSPACE" ] && WORKSPACE="$PWD"
STATE_DIR="$HOME/.claude/hooks/.governor-state"

STATE_FILE="$STATE_DIR/$SESSION_ID"
COUNT=$(cat "$STATE_FILE" 2>/dev/null || echo "0")
[ "$COUNT" != "1" ] && exit 0

if command -v ai-ir &>/dev/null; then
  ai-ir "$WORKSPACE" &>/dev/null || true
  ai-ir snapshot --label=session-start --session="$SESSION_ID" "$WORKSPACE" &>/dev/null || true
fi

_output=""
if command -v ai-context &>/dev/null; then
  PROMPT=$(echo "$INPUT" | jq -r '.prompt // ""' 2>/dev/null || true)
  _output=$(ai-context --budget=4000 --session="$SESSION_ID" --prompt="$PROMPT" "$WORKSPACE")
  echo "$_output"
fi

_hook_latency_ms=$(( (SECONDS - _hook_start) * 1000 ))
_output_size=${#_output}
emit_hook_latency "ir-injector" "$SESSION_ID" "0" "$_hook_latency_ms" "$_output_size" "$WORKSPACE" "$STATE_DIR"

exit 0
