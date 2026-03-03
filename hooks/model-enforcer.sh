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
#   - If router said "haiku", subagents MUST use haiku (default or opus/sonnet → DENY)
#   - If router said "opus", subagents MUST use opus or haiku (default or sonnet → DENY)
#   - If router said "pipeline", haiku and opus are both allowed (audit-only on default)
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

# Item 8: Build model lookup table from .ai/agents/*.yaml persona files.
# Falls back to hardcoded defaults if no persona files found.
# Format: "haiku" maps to persona names that use haiku, etc.
_load_persona_models() {
  local agents_dir="$PWD/.ai/agents"
  if [ ! -d "$agents_dir" ]; then
    return
  fi
  # Emit "name=model" pairs by parsing flat YAML key: value lines.
  for f in "$agents_dir"/*.yaml; do
    [ -f "$f" ] || continue
    local name="" model=""
    while IFS= read -r line; do
      case "$line" in
        name:*) name=$(echo "$line" | sed 's/^name:[[:space:]]*//' | tr -d '"' | tr -d "'") ;;
        model:*) model=$(echo "$line" | sed 's/^model:[[:space:]]*//' | tr -d '"' | tr -d "'") ;;
      esac
    done < "$f"
    [ -n "$name" ] && [ -n "$model" ] && echo "${name}=${model}"
  done
}

# Determine if a persona model should override enforcement for this route.
# Currently unused for routing logic (route is already haiku/opus/sonnet/pipeline)
# but persona table is available for future subagent-name–based routing.
# Load once for audit output.
_PERSONA_TABLE=$(_load_persona_models 2>/dev/null || true)

case "$ROUTE" in
  haiku)
    # Router said haiku — only allow haiku or default (no model specified)
    if [ "$REQUESTED_MODEL" = "opus" ] || [ "$REQUESTED_MODEL" = "sonnet" ]; then
      echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"MODEL ENFORCER: Router directed haiku for this task. Subagent requested '$REQUESTED_MODEL'. Re-run with model: \\\"haiku\\\".\"}}"
      exit 0
    fi
    ;;
  opus)
    # Router said opus — only allow opus or haiku; deny if model not explicitly set.
    # Relying on Claude to set the param from system-prompt instructions is unreliable.
    # Hard deny forces a retry with model: "opus" explicit.
    if [ "$REQUESTED_MODEL" = "default" ]; then
      echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"MODEL ENFORCER: Router directed opus for this task. Task called without explicit model parameter. Re-run with model: \\\"opus\\\" to comply with routing.\"}}"
      exit 0
    fi
    if [ "$REQUESTED_MODEL" = "sonnet" ]; then
      echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"MODEL ENFORCER: Router directed opus for this task. Subagent requested 'sonnet'. Re-run with model: \\\"opus\\\".\"}}"
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
