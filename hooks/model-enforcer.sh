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
    # Router said opus — allow opus and haiku, but warn if model not set explicitly.
    # Claude Code's subagent_type dispatch doesn't require a model param, so the enforcer
    # can't block it — but we surface it so Claude is aware.
    if [ "$REQUESTED_MODEL" = "default" ]; then
      echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\",\"additionalContext\":\"MODEL ENFORCER AUDIT: Route was opus but Task called without explicit model parameter. Native subagent_type routing may not use opus. Set model: \\\"opus\\\" on this Task call to comply with routing.\"}}"
      exit 0
    fi
    ;;
  pipeline)
    # Pipeline mode — haiku and opus both allowed, but warn if model not set.
    if [ "$REQUESTED_MODEL" = "default" ]; then
      echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\",\"additionalContext\":\"MODEL ENFORCER AUDIT: Route was pipeline (haiku explore → opus reason) but Task called without explicit model parameter. Set model: \\\"haiku\\\" for exploration subagents and model: \\\"opus\\\" for the reasoning subagent.\"}}"
      exit 0
    fi
    ;;
  sonnet)
    # Direct sonnet work — no subagent constraint
    ;;
esac

# Default: allow
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}'
exit 0
