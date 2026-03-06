#!/usr/bin/env bash
# SessionEnd hook — fires on session termination (normal or abnormal)
# If .ai/handoff.md doesn't exist, synthesizes a minimal one from .ai/checkpoint.json
# If handoff already exists, leave it alone (Claude wrote a proper one)

# shellcheck disable=SC1091
. "$(dirname "$0")/fault-emitter.sh"

INPUT=$(cat)
CWD=$(echo "$INPUT" | jq -r '.cwd // ""' 2>/dev/null)
[ -z "$CWD" ] && CWD="$PWD"
REASON=$(echo "$INPUT" | jq -r '.reason // "unknown"' 2>/dev/null || echo "unknown")
SESSION_ID_VAL=$(echo "$INPUT" | jq -r '.session_id // ""' 2>/dev/null || echo "")

# _append_progress_record: write one JSONL line to .ai/progress.jsonl.
# Idempotent: skips if session_id already present in the file.
_append_progress_record() {
  local cwd="$1" sid="$2" handoff="$3" checkpoint="$4" scope_drift_json="$5" cost_usd="${6:-0}" verify_passed="${7:-}"
  local ledger="$cwd/.ai/progress.jsonl"

  # Idempotency guard
  if [ -f "$ledger" ] && grep -q "\"session_id\":\"$sid\"" "$ledger" 2>/dev/null; then
    return
  fi

  # Extract fields from handoff front-matter (between --- delimiters)
  local fm_status="" fm_tasks="" fm_files_changed="" fm_turns=""
  if command -v jq &>/dev/null && [ -f "$handoff" ]; then
    local fm_block
    fm_block=$(awk 'BEGIN{found=0} /^---$/{found++; next} found==1{print} found==2{exit}' "$handoff" 2>/dev/null || true)
    fm_status=$(echo "$fm_block" | grep '^status:' | head -1 | sed 's/^status:[[:space:]]*//' | tr -d '"' || echo "")
    fm_tasks=$(echo "$fm_block" | grep '^tasks_touched:' | head -1 | sed 's/^tasks_touched:[[:space:]]*//' || echo "[]")
    fm_files_changed=$(echo "$fm_block" | grep '^files_changed:' | head -1 | sed 's/^files_changed:[[:space:]]*//' || echo "[]")
  fi

  # Extract from checkpoint
  local ir_hash_start="" cp_turns=""
  if command -v jq &>/dev/null && [ -f "$checkpoint" ]; then
    ir_hash_start=$(jq -r '.ir_hash // ""' "$checkpoint" 2>/dev/null || echo "")
    cp_turns=$(jq -r '.turn // 0' "$checkpoint" 2>/dev/null || echo "0")
  fi
  [ -z "$fm_turns" ] && fm_turns="$cp_turns"

  # Current IR hash (session-end)
  local ir_hash_end=""
  ir_hash_end=$(jq -r '.root_hash // ""' "$cwd/.ai/ir.json" 2>/dev/null | head -c 8 || echo "")

  # Derive files_changed count
  local files_count=0
  if command -v jq &>/dev/null && [ -n "$fm_files_changed" ] && [ "$fm_files_changed" != "[]" ]; then
    files_count=$(echo "$fm_files_changed" | jq 'length' 2>/dev/null || echo 0)
  fi

  local ts
  ts=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date '+%Y-%m-%dT%H:%M:%SZ')

  # Normalise tasks array (default to empty)
  [ -z "$fm_tasks" ] && fm_tasks="[]"
  [ -z "$fm_status" ] && fm_status="unknown"
  [ -z "$scope_drift_json" ] && scope_drift_json='{"drifted":false,"files":[],"task_scope":""}'

  jq -cn \
    --arg sid "$sid" \
    --arg ts "$ts" \
    --argjson turns "${fm_turns:-0}" \
    --argjson cost "${cost_usd}" \
    --arg ir_start "$ir_hash_start" \
    --arg ir_end "$ir_hash_end" \
    --argjson files_count "${files_count:-0}" \
    --argjson tasks_touched "$fm_tasks" \
    --arg handoff_path "$handoff" \
    --arg status "$fm_status" \
    --argjson scope_drift "$scope_drift_json" \
    --arg verify_passed "$verify_passed" \
    '{session_id:$sid, timestamp:$ts, turns:$turns, cost_usd:$cost,
      ir_hash_start:$ir_start, ir_hash_end:$ir_end,
      files_changed:$files_count, tasks_touched:$tasks_touched,
      handoff_path:$handoff_path, status:$status,
      scope_drift:$scope_drift}
    | if $verify_passed != "" then . + {verify_passed: ($verify_passed == "true")} else . end' \
    >> "$ledger" 2>/dev/null || true
}

# --- Scope Drift Detection (M3) ---
# Get the active task's scope glob from tasks.json.
# Compare git-changed files against scope. Emit SCOPE_DRIFT fault if any drift found.
SCOPE_DRIFT_JSON='{"drifted":false,"files":[],"task_scope":""}'
_ACTIVE_SCOPE=""
_ACTIVE_TASK_ID=""

if command -v jq &>/dev/null && [ -f "$CWD/.ai/tasks.json" ]; then
  _TASK_INFO=$(jq -c '
    .tasks
    | map(select(.status != "done"))
    | map(select(.blockedBy == null or .blockedBy == ""))
    | first // empty
  ' "$CWD/.ai/tasks.json" 2>/dev/null || true)
  if [ -n "$_TASK_INFO" ] && [ "$_TASK_INFO" != "null" ]; then
    _ACTIVE_SCOPE=$(echo "$_TASK_INFO" | jq -r '.scope // ""' 2>/dev/null || true)
    _ACTIVE_TASK_ID=$(echo "$_TASK_INFO" | jq -r '.id // ""' 2>/dev/null || true)
  fi
fi

if [ -n "$_ACTIVE_SCOPE" ] && command -v git &>/dev/null; then
  # Files changed since the session-start snapshot (or HEAD~1 as fallback)
  _CHANGED=$(git -C "$CWD" diff --name-only HEAD 2>/dev/null || true)
  if [ -z "$_CHANGED" ]; then
    _CHANGED=$(git -C "$CWD" diff --name-only HEAD~1 2>/dev/null || true)
  fi

  _DRIFT_FILES=()
  while IFS= read -r _file; do
    [ -z "$_file" ] && continue
    _in_scope=false
    # Check against each comma-separated scope glob
    IFS=',' read -ra _globs <<< "$_ACTIVE_SCOPE"
    for _glob in "${_globs[@]}"; do
      _glob=$(echo "$_glob" | tr -d ' ')
      # shellcheck disable=SC2254
      case "$_file" in
        $_glob) _in_scope=true; break ;;
      esac
    done
    if [ "$_in_scope" = "false" ]; then
      _DRIFT_FILES+=("$_file")
    fi
  done <<< "$_CHANGED"

  if [ "${#_DRIFT_FILES[@]}" -gt 0 ]; then
    _DRIFT_LIST=$(printf '%s\n' "${_DRIFT_FILES[@]}" | jq -Rs 'split("\n") | map(select(. != ""))' 2>/dev/null || echo "[]")
    _DRIFT_COUNT="${#_DRIFT_FILES[@]}"
    _DRIFT_SUMMARY=$(printf '%s,' "${_DRIFT_FILES[@]}" | sed 's/,$//')
    SCOPE_DRIFT_JSON=$(jq -n \
      --argjson drifted true \
      --argjson files "$_DRIFT_LIST" \
      --arg task_scope "$_ACTIVE_SCOPE" \
      --arg task_id "$_ACTIVE_TASK_ID" \
      '{drifted:$drifted, files:$files, task_scope:$task_scope, task_id:$task_id}' 2>/dev/null \
      || echo '{"drifted":true,"files":[],"task_scope":"","task_id":""}')
    emit_fault "SCOPE_DRIFT" "$_DRIFT_COUNT" "$_DRIFT_SUMMARY" "$CWD" "${SESSION_ID_VAL:-unknown}"
  fi
fi

HANDOFF_FILE="$CWD/.ai/handoff.md"
CHECKPOINT_FILE="$CWD/.ai/checkpoint.json"

# Take session-end snapshot (always — even if handoff already exists).
SESSION_ID_ARG=""
[ -n "$SESSION_ID_VAL" ] && SESSION_ID_ARG="--session=$SESSION_ID_VAL"

if command -v ai-ir &>/dev/null && [ -f "$CWD/.ai/ir.json" ]; then
  ai-ir "$CWD" &>/dev/null || true  # re-index to capture final file state
  ai-ir snapshot --label=session-end ${SESSION_ID_ARG:+"$SESSION_ID_ARG"} "$CWD" &>/dev/null || true
  # Cache top-20 churn files for relevance scoring on next session start
  ai-ir churn --compact --n=20 "$CWD" > "$CWD/.ai/churn-cache.txt" 2>/dev/null || true
fi

# Write execution envelope for pipeline-routed sessions
if command -v ai-pipeline >/dev/null 2>&1; then
  _route=$(cat "$HOME/.claude/hooks/.governor-state/${SESSION_ID_VAL}.route" 2>/dev/null || echo "")
  if [ "$_route" = "pipeline" ]; then
    ai-pipeline envelope \
      --session="$SESSION_ID_VAL" \
      --pipeline="default" \
      --status="complete" \
      "$CWD" 2>/dev/null || true
  fi
fi

# Run verify command for active task — emit VERIFY_FAIL fault on failure (M8)
_VERIFY_PASSED=""
if command -v ai-task &>/dev/null && [ -n "$_ACTIVE_TASK_ID" ]; then
  _verify_exit=0
  ai-task verify "$_ACTIVE_TASK_ID" ${SESSION_ID_VAL:+"--session=$SESSION_ID_VAL"} "$CWD" 2>/dev/null
  _verify_exit=$?
  if [ "$_verify_exit" = "0" ]; then
    _VERIFY_PASSED="true"
  elif [ "$_verify_exit" = "1" ]; then
    _VERIFY_PASSED="false"
    emit_fault "VERIFY_FAIL" "1" "task $_ACTIVE_TASK_ID verify failed" "$CWD" "${SESSION_ID_VAL:-unknown}"
  fi
  # exit 2 = no verify cmd; leave _VERIFY_PASSED empty
fi

# Compute verify summary for embedding in auto-generated handoff.
VERIFY_SUMMARY=""
if command -v ai-ir &>/dev/null && [ -f "$CWD/.ai/history.db" ]; then
  VERIFY_SUMMARY=$(ai-ir verify ${SESSION_ID_ARG:+"$SESSION_ID_ARG"} "$CWD" 2>/dev/null || true)
fi

# Don't overwrite an existing handoff
[ -f "$HANDOFF_FILE" ] && _append_progress_record "$CWD" "$SESSION_ID_VAL" "$HANDOFF_FILE" "$CHECKPOINT_FILE" "$SCOPE_DRIFT_JSON" "" "$_VERIFY_PASSED" && exit 0

# Try ai-session first — reads the full JSONL log for ground-truth facts
if command -v ai-session &>/dev/null && [ -n "$SESSION_ID_VAL" ]; then
  if _SESSION_OUT=$(ai-session --session="$SESSION_ID_VAL" --out="$HANDOFF_FILE" "$CWD" 2>/dev/null); then
    # Extract cost from ai-session stdout: "Session abc: 30 turns, ~$1.23 (...)"
    _COST=$(echo "$_SESSION_OUT" | grep -oP '~\$\K[0-9.]+' 2>/dev/null | head -1)
    _COST="${_COST:-0}"

    # ai-document: update project docs, change-gated by IR diff (non-fatal)
    if command -v ai-document &>/dev/null; then
      ai-document --ir-diff="$VERIFY_SUMMARY" "$CWD" &>/dev/null || true
    fi

    # Progress Ledger — append JSONL record to .ai/progress.jsonl
    _append_progress_record "$CWD" "$SESSION_ID_VAL" "$HANDOFF_FILE" "$CHECKPOINT_FILE" "$SCOPE_DRIFT_JSON" "$_COST" "$_VERIFY_PASSED"

    # Validate handoff schema (warn on exit 1, abort on exit 2)
    if command -v ai-session &>/dev/null; then
      _validate_code=$(ai-session validate "$HANDOFF_FILE" 2>&1; echo $?)
      _validate_exit=${_validate_code##*$'\n'}
      if [ "$_validate_exit" = "2" ]; then
        echo "session-end: WARNING: handoff validation fatal error — check $HANDOFF_FILE" >&2
      fi
    fi

    exit 0
  fi
fi

# Fallback: minimal template from checkpoint.json (ai-session unavailable or failed)
[ ! -f "$CHECKPOINT_FILE" ] && exit 0

TURN=$(jq -r '.turn // "unknown"' "$CHECKPOINT_FILE" 2>/dev/null || echo "unknown")
TS=$(jq -r '.ts // "unknown"' "$CHECKPOINT_FILE" 2>/dev/null || echo "unknown")
IR_HASH=$(jq -r '.ir_hash // ""' "$CHECKPOINT_FILE" 2>/dev/null || echo "")
LAST_MSG=$(jq -r '.last_assistant_message // ""' "$CHECKPOINT_FILE" 2>/dev/null || echo "")

cat > "$HANDOFF_FILE" <<EOF
# Session Handoff (fallback — ai-session unavailable)
**Date:** ${TS}
**IR snapshot:** ${IR_HASH}
**Session length:** ~${TURN} turns
**Termination reason:** ${REASON}

## Accomplished
- (install ai-session for ground-truth handoffs)
- Last message: ${LAST_MSG}

## Next Steps
1. Review git log for changes made this session
2. Re-orient with IR context on next session start

## Structural Changes
${VERIFY_SUMMARY:-"(no session-start snapshot)"}
EOF

_append_progress_record "$CWD" "$SESSION_ID_VAL" "$HANDOFF_FILE" "$CHECKPOINT_FILE" "$SCOPE_DRIFT_JSON" "" "$_VERIFY_PASSED"

exit 0
