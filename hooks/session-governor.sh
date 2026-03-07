#!/usr/bin/env bash
# Session Governor + Model Router — thin wrapper around ai-governor binary.
# See internal/governor/ for implementation.

# shellcheck disable=SC1091
. "$(dirname "$0")/fault-emitter.sh"

_hook_start=$SECONDS
_input=$(cat)
SESSION_ID=$(echo "$_input" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")
WORKSPACE=$(echo "$_input" | jq -r '.cwd // ""' 2>/dev/null)
[ -z "$WORKSPACE" ] && WORKSPACE="$PWD"
STATE_DIR="$HOME/.claude/hooks/.governor-state"

_output=$(echo "$_input" | ai-governor)
_exit_code=$?
echo "$_output"

_hook_latency_ms=$(( (SECONDS - _hook_start) * 1000 ))
_output_size=${#_output}
emit_hook_latency "session-governor" "$SESSION_ID" "$_exit_code" "$_hook_latency_ms" "$_output_size" "$WORKSPACE" "$STATE_DIR"
