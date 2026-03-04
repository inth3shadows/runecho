#!/usr/bin/env bash
# Pre-Compact Snapshot — PreCompact hook.
# Captures session state before context compaction destroys it.
# This state is read by constraint-reinjector.sh after compaction.
#
# PreCompact has no decision control — side-effects only.

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")
TRIGGER=$(echo "$INPUT" | jq -r '.trigger // "unknown"' 2>/dev/null || echo "unknown")

CONFIG_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
STATE_DIR="$CONFIG_DIR/hooks/.governor-state"
mkdir -p "$STATE_DIR" 2>/dev/null

# Read current governor state
TURN_COUNT=$(cat "$STATE_DIR/$SESSION_ID" 2>/dev/null || echo "0")
CURRENT_ROUTE=$(cat "$STATE_DIR/${SESSION_ID}.route" 2>/dev/null || echo "sonnet")

# Read session cost from JSONL
SESSION_COST="0"
JSONL_FILE=$(find "$HOME/.claude/projects" -name "${SESSION_ID}.jsonl" 2>/dev/null | head -1)
if [ -n "$JSONL_FILE" ] && command -v jq &>/dev/null; then
  SESSION_COST=$(jq -sr '
    [.[] | select(.type == "assistant") | select(.message.usage) |
      .message |
      { m: .model, u: .usage } |
      if .m | test("haiku"; "i") then
        ((.u.input_tokens // 0) * 0.80 + (.u.output_tokens // 0) * 4.0 + (.u.cache_read_input_tokens // 0) * 0.08) / 1000000
      elif .m | test("opus"; "i") then
        ((.u.input_tokens // 0) * 15.0 + (.u.output_tokens // 0) * 75.0 + (.u.cache_read_input_tokens // 0) * 1.5) / 1000000
      else
        ((.u.input_tokens // 0) * 3.0 + (.u.output_tokens // 0) * 15.0 + (.u.cache_read_input_tokens // 0) * 0.30) / 1000000
      end
    ] | add // 0
  ' "$JSONL_FILE" 2>/dev/null || echo "0")
fi
COST_FMT=$(awk -v c="$SESSION_COST" 'BEGIN { printf "%.2f", c+0 }')

# Read IR hash if available
IR_HASH=$(jq -r '.root_hash // ""' "$PWD/.ai/ir.json" 2>/dev/null | head -c 12 || echo "unknown")

# Read scope-lock status
SCOPE_LOCK_ACTIVE="false"
SCOPE_LOCK_SUMMARY="inactive"
if [ -f "$PWD/.ai/scope-lock.json" ]; then
  SCOPE_LOCK_ACTIVE="true"
  ALLOWED=$(jq -r '.allowed_paths // [] | join(", ")' "$PWD/.ai/scope-lock.json" 2>/dev/null || echo "")
  SCOPE_LOCK_SUMMARY="active — allowed: $ALLOWED"
fi

# Write snapshot
SNAPSHOT_FILE="$STATE_DIR/${SESSION_ID}.pre-compact"
ts=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || echo "unknown")
jq -n \
  --arg ts "$ts" \
  --arg trigger "$TRIGGER" \
  --arg turn_count "$TURN_COUNT" \
  --arg session_cost "$COST_FMT" \
  --arg route "$CURRENT_ROUTE" \
  --arg ir_hash "$IR_HASH" \
  --arg scope_lock "$SCOPE_LOCK_ACTIVE" \
  --arg scope_summary "$SCOPE_LOCK_SUMMARY" \
  '{
    ts: $ts,
    trigger: $trigger,
    turn_count: $turn_count,
    session_cost: $session_cost,
    route: $route,
    ir_hash: $ir_hash,
    scope_lock_active: $scope_lock,
    scope_summary: $scope_summary
  }' > "$SNAPSHOT_FILE" 2>/dev/null || true

exit 0
