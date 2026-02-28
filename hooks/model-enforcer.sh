#!/bin/bash
# Model Enforcer — PreToolUse hook on Task tool.
# Ensures subagents use the model dictated by the session governor's routing.
#
# How it works:
#   1. session-governor.sh writes routing decision to a state file
#   2. This hook reads that state file when Claude spawns a Task subagent
#   3. If the subagent's model doesn't match the routing, DENY it
#
# What this enforces:
#   - If router said "haiku", subagents MUST use haiku (or no model = default)
#   - If router said "opus", subagents CAN use opus
#   - If router said "pipeline", haiku and opus are both allowed
#   - If no routing (Sonnet direct), any model is fine (no constraint)
#
# What this can NOT enforce:
#   - Claude choosing to do work directly instead of delegating

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")
TOOL_INPUT=$(echo "$INPUT" | jq -r '.tool_input // {}' 2>/dev/null)
REQUESTED_MODEL=$(echo "$TOOL_INPUT" | jq -r '.model // "default"' 2>/dev/null || echo "default")

# Read the routing state written by session-governor.sh
STATE_DIR="$HOME/.claude/hooks/.governor-state"
ROUTE_FILE="$STATE_DIR/${SESSION_ID}.route"

if [ ! -f "$ROUTE_FILE" ]; then
  # No routing guidance — allow anything
  echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
  exit 0
fi

ROUTE=$(cat "$ROUTE_FILE" 2>/dev/null || echo "none")

case "$ROUTE" in
  haiku)
    # Router said haiku — only allow haiku or default (no model specified)
    if [ "$REQUESTED_MODEL" = "opus" ] || [ "$REQUESTED_MODEL" = "sonnet" ]; then
      echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"MODEL ENFORCER: Router directed haiku for this task. Subagent requested '$REQUESTED_MODEL'. Re-run with model: \\\"haiku\\\".\"}}"
      exit 0
    fi
    ;;
  opus)
    # Router said opus — allow opus and haiku (for exploration support), deny nothing
    ;;
  pipeline)
    # Pipeline mode — haiku and opus both allowed
    ;;
  sonnet)
    # Direct sonnet work — no subagent constraint (shouldn't need subagents, but allow if used)
    ;;
esac

# Default: allow
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
exit 0
