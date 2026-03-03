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

# Take session-start snapshot (turn 1 only — gate already active above).
if command -v ai-ir &>/dev/null; then
  ai-ir snapshot --label=session-start --session="$SESSION_ID" "$PWD" &>/dev/null || true
fi

# Capture compact structural diff from prior session-end (if any).
DIFF_LINE=""
if command -v ai-ir &>/dev/null && [ -f "$PWD/.ai/history.db" ]; then
  DIFF_LINE=$(ai-ir diff --since=session-end --compact "$PWD" 2>/dev/null || true)
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

# Feature 3: Relevance-scored IR injection.
# Scores each file by: prompt word overlap (path=3pts, symbol=2pts) + churn bonus (5pts)
# + handoff recency bonus (5pts). Top 15 shown with per-file symbols; rest summarized.
# Fallback to flat dump if PROMPT_WORDS empty or scoring fails.
PROMPT_WORDS=$(echo "$INPUT" | jq -r '.prompt // ""' 2>/dev/null | tr '[:upper:]' '[:lower:]' \
  | grep -oE '[a-z_][a-z0-9_]+' | sort -u | tr '\n' '|' | sed 's/|$//')
CHURN_LIST=$(cat "$PWD/.ai/churn-cache.txt" 2>/dev/null || true)
HANDOFF_CONTENT=$(cat "$PWD/.ai/handoff.md" 2>/dev/null || true)

SCORED_OK=false
if [ -n "$PROMPT_WORDS" ]; then
  SCORED=$(jq -r \
    --arg words "$PROMPT_WORDS" \
    --arg churn "$CHURN_LIST" \
    --arg handoff "$HANDOFF_CONTENT" '
    .files | to_entries[] |
    . as $e |
    ($e.key) as $path |
    (($e.value.functions // []) + ($e.value.classes // []) | unique | sort) as $syms |
    (($words | split("|") | map(select(. != ""))) as $wlist |
      ([$wlist[] | select($path | ascii_downcase | contains(.))] | length * 3) +
      ([$wlist[] | . as $w | ($syms[] | ascii_downcase) | select(contains($w))] | length * 2) +
      (if ($churn | length > 0) and ($churn | contains($path)) then 5 else 0 end) +
      (if ($handoff | length > 0) and ($handoff | contains($path)) then 5 else 0 end)
    ) as $score |
    "\($score)|\($path)|\($syms | join(", "))"
  ' "$IR_FILE" 2>/dev/null | sort -t'|' -k1 -rn) || true

  if [ -n "$SCORED" ]; then
    SHOWN=$(echo "$SCORED" | head -15)
    SHOWN_COUNT=$(echo "$SHOWN" | wc -l | tr -d ' ')
    REST_COUNT=$(echo "$SCORED" | tail -n +16 | grep -c . 2>/dev/null || echo 0)

    echo "IR CONTEXT [root_hash: ${SHORT_HASH}...]:"
    echo "Symbols in relevant files (${SHOWN_COUNT}/${FILE_COUNT}):"
    while IFS='|' read -r _score filepath syms; do
      if [ -n "$syms" ]; then
        echo "  ${filepath}: ${syms}"
      else
        echo "  ${filepath}: (no exported symbols)"
      fi
    done <<< "$SHOWN"
    if [ "$REST_COUNT" -gt 0 ]; then
      echo "+ ${REST_COUNT} more files (lower relevance, run \`ai-ir log\` for full IR)"
    fi
    SCORED_OK=true
  fi
fi

# Fallback: flat dump (original behavior — empty prompt, or scoring produced no output)
if [ "$SCORED_OK" = "false" ]; then
  FILE_LIST=$(jq -r '.files | keys | join(", ")' "$IR_FILE" 2>/dev/null || echo "")
  SYMBOLS=$(jq -r '
    [.files[].functions[], .files[].classes[]] | unique | sort | join(", ")
  ' "$IR_FILE" 2>/dev/null || echo "")
  SYMBOL_COUNT=$(jq -r '
    [.files[].functions[], .files[].classes[]] | unique | length
  ' "$IR_FILE" 2>/dev/null || echo "0")
  echo "IR CONTEXT [root_hash: ${SHORT_HASH}...]:"
  echo "${FILE_COUNT} files — ${FILE_LIST}"
  echo "Symbols (${SYMBOL_COUNT}): ${SYMBOLS}"
fi

# Inject compact diff if there were structural changes since last session-end.
if [ -n "$DIFF_LINE" ]; then
  echo ""
  echo "$DIFF_LINE"
fi

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

  # --- GUPP Directive (Item 5) ---
  # Extract next_session_intent from handoff front-matter + first unblocked task.
  _HANDOFF_INTENT=""
  if [ -f "$HANDOFF_FILE" ]; then
    _FM_BLOCK=$(awk 'BEGIN{found=0} /^---$/{found++; next} found==1{print} found==2{exit}' "$HANDOFF_FILE" 2>/dev/null || true)
    _HANDOFF_INTENT=$(echo "$_FM_BLOCK" | grep '^next_session_intent:' | head -1 \
      | sed "s/^next_session_intent:[[:space:]]*//" | tr -d "\"'" | sed "s/''/'/g" || true)
  fi

  _NEXT_TASK=""
  if command -v ai-task &>/dev/null; then
    _NEXT_TASK=$(ai-task next 2>/dev/null || true)
  fi

  if [ -n "$_HANDOFF_INTENT" ] || [ -n "$_NEXT_TASK" ]; then
    echo ""
    echo "HANDOFF DIRECTIVE:"
    [ -n "$_HANDOFF_INTENT" ] && echo "  Prior intent: $_HANDOFF_INTENT"
    [ -n "$_NEXT_TASK" ] && echo "  Next task: $_NEXT_TASK"
    echo "  → Acknowledge this context and confirm your plan before proceeding."
  fi
fi

# --- Task Queue Injection ---
# Reads .ai/tasks.json and injects all non-done tasks into context.
# Claude updates task status (pending → in-progress → done) as work progresses.
# Missing file or malformed JSON → silently skipped.
TASK_JSON="$PWD/.ai/tasks.json"
if command -v jq &>/dev/null && [ -f "$TASK_JSON" ]; then
  ACTIVE=$(jq '[.tasks[] | select(.status != "done")]' "$TASK_JSON" 2>/dev/null)
  TASK_COUNT=$(echo "$ACTIVE" | jq 'length' 2>/dev/null || echo 0)
  if [ "${TASK_COUNT:-0}" -gt 0 ]; then
    echo ""
    echo "TASK QUEUE [${TASK_COUNT} active]:"
    echo "$ACTIVE" | jq -r '.[] | "  [\(.status)] #\(.id): \(.title)\(if .blockedBy then " (blocked by #\(.blockedBy))" else "" end)"'
  fi
fi

exit 0
