#!/usr/bin/env bash
# contract-sync.sh — UserPromptSubmit hook.
# Reads .ai/CONTRACT.yaml and auto-creates a task in tasks.json if not already present.
# Idempotent and silent: no-op if no contract exists or task already present.

# shellcheck disable=SC1091
. "$(dirname "$0")/fault-emitter.sh"

_hook_start=$SECONDS

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")
STATE_DIR="$HOME/.claude/hooks/.governor-state"

# Find project root (walk up from CWD looking for .ai/)
dir="$(pwd)"
root=""
while true; do
  if [[ -d "$dir/.ai" ]]; then
    root="$dir"
    break
  fi
  parent="$(dirname "$dir")"
  [[ "$parent" == "$dir" ]] && break
  dir="$parent"
done

if [[ -z "$root" ]] || [[ ! -f "$root/.ai/CONTRACT.yaml" ]]; then
  exit 0
fi

cd "$root" && ai-task sync --quiet 2>/dev/null
_exit_code=$?

_hook_latency_ms=$(( (SECONDS - _hook_start) * 1000 ))
emit_hook_latency "contract-sync" "$SESSION_ID" "$_exit_code" "$_hook_latency_ms" "0" "$root" "$STATE_DIR"

exit 0
