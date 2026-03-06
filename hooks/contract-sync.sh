#!/usr/bin/env bash
# contract-sync.sh — UserPromptSubmit hook.
# Reads .ai/CONTRACT.yaml and auto-creates a task in tasks.json if not already present.
# Idempotent and silent: no-op if no contract exists or task already present.

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

[[ -z "$root" ]] && exit 0
[[ -f "$root/.ai/CONTRACT.yaml" ]] || exit 0

cd "$root" && ai-task sync --quiet 2>/dev/null
exit 0
