#!/usr/bin/env bash
# fault-emitter.sh — sourced by hooks that need to emit fault signals.
# Do NOT execute directly. Source with: . "$(dirname "$0")/fault-emitter.sh"
#
# Provides emit_fault() — writes one JSON line to .ai/faults.jsonl.
# Also queues the signal in a per-session pending file so session-governor.sh
# can inject it into Claude's context on the next turn.
#
# Usage:
#   emit_fault <SIGNAL> <value> <context> <workspace_dir> <session_id> [state_dir]
#
# Signals: IR_DRIFT, HALLUCINATION, TURN_FATIGUE, COST_FATIGUE, OPUS_BLOCKED, SCOPE_DRIFT, VERIFY_FAIL
# value:   numeric (change count, turn count, cost in cents, etc.)
# context: short human-readable string

emit_fault() {
  local signal="$1"
  local value="$2"
  local context="$3"
  local workspace="$4"
  local session_id="$5"
  local state_dir="${6:-$HOME/.claude/hooks/.governor-state}"

  local ts
  ts=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date '+%Y-%m-%dT%H:%M:%S')

  # Append to workspace audit log
  local faults_file="$workspace/.ai/faults.jsonl"
  mkdir -p "$workspace/.ai" 2>/dev/null
  printf '%s\n' "$(jq -cn \
    --arg signal "$signal" \
    --arg session_id "$session_id" \
    --arg ts "$ts" \
    --argjson value "${value:-0}" \
    --arg context "$context" \
    '{signal: $signal, session_id: $session_id, ts: $ts, value: $value, context: $context}'
  )" >> "$faults_file" 2>/dev/null || true

  # Queue for context injection on next governor turn
  # IR_DRIFT and HALLUCINATION come from stop-checkpoint.sh (between turns);
  # TURN_FATIGUE, COST_FATIGUE, OPUS_BLOCKED are emitted inline by the governor
  # and don't need separate queuing (they're already in the output block).
  # HOOK_FAILURE queued so it surfaces in the next turn's context.
  case "$signal" in
    IR_DRIFT|HALLUCINATION|VERIFY_FAIL|HOOK_FAILURE)
      local pending_file="$state_dir/${session_id}.pending-faults"
      printf '%s\n' "$(jq -cn \
        --arg signal "$signal" \
        --argjson value "${value:-0}" \
        --arg context "$context" \
        '{signal: $signal, value: $value, context: $context}'
      )" >> "$pending_file" 2>/dev/null || true
      ;;
  esac
}

# run_with_fault_guard — runs a command and emits HOOK_FAILURE on non-zero exit.
# Stores elapsed time in HOOK_LATENCY_MS for use by emit_hook_latency.
#
# Usage: run_with_fault_guard <hook_name> <workspace> <session_id> <state_dir> <command...>
run_with_fault_guard() {
  local hook_name="$1"
  local workspace="$2"
  local session_id="$3"
  local state_dir="$4"
  shift 4

  local start_s=$SECONDS
  "$@"
  local exit_code=$?
  local elapsed_s=$(( SECONDS - start_s ))
  HOOK_LATENCY_MS=$(( elapsed_s * 1000 ))

  if [ "$exit_code" -ne 0 ]; then
    emit_fault "HOOK_FAILURE" 1 \
      "${hook_name}: exit ${exit_code}" \
      "$workspace" "$session_id" "$state_dir"
  fi

  return 0  # never block the session
}

# emit_hook_latency — records hook execution timing to .ai/hooks.jsonl.
# Emits HOOK_SLOW or HOOK_FAILED faults when thresholds are exceeded.
#
# Usage: emit_hook_latency <hook_name> <session_id> <exit_code> <latency_ms> <output_size> <workspace> [state_dir]
emit_hook_latency() {
  local hook_name="$1"
  local session_id="$2"
  local exit_code="$3"
  local latency_ms="$4"
  local output_size="$5"
  local workspace="$6"
  local state_dir="${7:-$HOME/.claude/hooks/.governor-state}"

  local ts
  ts=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date '+%Y-%m-%dT%H:%M:%S')

  # Append telemetry record
  local hooks_file="$workspace/.ai/hooks.jsonl"
  mkdir -p "$workspace/.ai" 2>/dev/null
  printf '%s\n' "$(jq -cn \
    --arg ts "$ts" \
    --arg hook_name "$hook_name" \
    --arg session_id "$session_id" \
    --argjson exit_code "${exit_code:-0}" \
    --argjson latency_ms "${latency_ms:-0}" \
    --argjson output_size "${output_size:-0}" \
    '{ts: $ts, hook_name: $hook_name, session_id: $session_id, exit_code: $exit_code, latency_ms: $latency_ms, output_size: $output_size}'
  )" >> "$hooks_file" 2>/dev/null || true

  # Emit HOOK_SLOW if latency exceeds 3 seconds
  if [ "${latency_ms:-0}" -gt 3000 ] 2>/dev/null; then
    emit_fault "HOOK_SLOW" "$latency_ms" \
      "${hook_name} took ${latency_ms}ms" \
      "$workspace" "$session_id" "$state_dir"
  fi

  # Emit HOOK_FAILED on non-zero exit
  if [ "${exit_code:-0}" -ne 0 ] 2>/dev/null; then
    emit_fault "HOOK_FAILED" "$exit_code" \
      "${hook_name} failed: exit ${exit_code}" \
      "$workspace" "$session_id" "$state_dir"
  fi
}
