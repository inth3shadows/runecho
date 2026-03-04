#!/bin/bash
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
# Signals: IR_DRIFT, HALLUCINATION, TURN_FATIGUE, COST_FATIGUE, OPUS_BLOCKED
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
  printf '%s\n' "$(jq -n \
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
  case "$signal" in
    IR_DRIFT|HALLUCINATION)
      local pending_file="$state_dir/${session_id}.pending-faults"
      printf '%s\n' "$(jq -n \
        --arg signal "$signal" \
        --argjson value "${value:-0}" \
        --arg context "$context" \
        '{signal: $signal, value: $value, context: $context}'
      )" >> "$pending_file" 2>/dev/null || true
      ;;
  esac
}
