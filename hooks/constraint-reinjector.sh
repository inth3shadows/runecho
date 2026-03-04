#!/usr/bin/env bash
# Constraint Reinjector — SessionStart hook, matcher: compact.
# Fires after context compaction completes. Re-injects active constraints
# into the model's context so they survive the compaction boundary.
#
# Why this works: hooks are memory-independent. The model's instructions
# were in the compressed context — this hook re-anchors them from state
# files and config, not from model memory.
#
# Even if the model ignores this text, PreToolUse hooks still enforce the rules.
# This is defense-in-depth: reminder + immutable enforcement.

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")

CONFIG_DIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
STATE_DIR="$CONFIG_DIR/hooks/.governor-state"

# Read pre-compact snapshot if available
SNAPSHOT_FILE="$STATE_DIR/${SESSION_ID}.pre-compact"
TURN_COUNT="unknown"
SESSION_COST="unknown"
CURRENT_ROUTE="sonnet"
IR_HASH="unknown"
SCOPE_SUMMARY="inactive"

if [ -f "$SNAPSHOT_FILE" ]; then
  TURN_COUNT=$(jq -r '.turn_count // "unknown"' "$SNAPSHOT_FILE" 2>/dev/null || echo "unknown")
  SESSION_COST=$(jq -r '.session_cost // "unknown"' "$SNAPSHOT_FILE" 2>/dev/null || echo "unknown")
  CURRENT_ROUTE=$(jq -r '.route // "sonnet"' "$SNAPSHOT_FILE" 2>/dev/null || echo "sonnet")
  IR_HASH=$(jq -r '.ir_hash // "unknown"' "$SNAPSHOT_FILE" 2>/dev/null || echo "unknown")
  SCOPE_SUMMARY=$(jq -r '.scope_summary // "inactive"' "$SNAPSHOT_FILE" 2>/dev/null || echo "inactive")
else
  # Fallback: read live state
  TURN_COUNT=$(cat "$STATE_DIR/$SESSION_ID" 2>/dev/null || echo "unknown")
  CURRENT_ROUTE=$(cat "$STATE_DIR/${SESSION_ID}.route" 2>/dev/null || echo "sonnet")
  IR_HASH=$(jq -r '.root_hash // ""' "$PWD/.ai/ir.json" 2>/dev/null | head -c 12 || echo "unknown")
fi

# Compute remaining turns
STOP_AT=35
if [ "$TURN_COUNT" != "unknown" ] && [ "$TURN_COUNT" -gt 0 ] 2>/dev/null; then
  REMAINING=$((STOP_AT - TURN_COUNT))
  [ "$REMAINING" -lt 0 ] && REMAINING=0
  TURNS_INFO="Turn $TURN_COUNT/$STOP_AT (~$REMAINING remaining before forced handoff)"
else
  TURNS_INFO="Turn counter unavailable"
fi

cat <<EOF
=== POST-COMPACT CONSTRAINT REINSTATEMENT ===
Context was just compacted. These constraints are enforced by shell hooks — not model memory.

Session: $SESSION_ID | $TURNS_INFO | Cost: \$${SESSION_COST}
IR hash: $IR_HASH | Model routing: $CURRENT_ROUTE | Scope lock: $SCOPE_SUMMARY

ACTIVE SAFETY GUARDS (enforced at hook layer — cannot be forgotten or argued around):
- Destructive bash commands (rm -rf, git reset --hard, DROP TABLE, pipe-to-shell) require
  explicit user approval or are hard-blocked. This is enforced on every Bash tool call.
- Scope lock ($SCOPE_SUMMARY): file writes are restricted per .ai/scope-lock.json if present.
- Settings files (.claude/settings*.json, *.key, *.pem) cannot be overwritten.
- Model routing: $CURRENT_ROUTE. Opus blocked above \$8 session cost.
- Turn limit: forced handoff at turn $STOP_AT.

CLAUDE.md rules remain active. BPB v3 enforcement continues.
=== END CONSTRAINT REINSTATEMENT ===
EOF

# --- IR Re-injection ---
# Re-inject full IR context so the model knows what files/symbols exist.
# Uses a flat dump (not relevance-scored) — post-compact we need the full picture.
# Re-indexes first if ai-ir is available to capture any changes made before compact.
IR_FILE="$PWD/.ai/ir.json"
if [ -f "$IR_FILE" ] && command -v jq &>/dev/null; then
  # Incremental re-index (fast — only parses changed files)
  if command -v ai-ir &>/dev/null; then
    ai-ir "$PWD" &>/dev/null || true
  fi

  SHORT_HASH=$(jq -r '.root_hash // ""' "$IR_FILE" 2>/dev/null | head -c 12)
  FILE_COUNT=$(jq '.files | length' "$IR_FILE" 2>/dev/null || echo "0")
  FILE_LIST=$(jq -r '.files | keys | join(", ")' "$IR_FILE" 2>/dev/null || echo "")
  SYMBOLS=$(jq -r '[.files[].functions[], .files[].classes[]] | unique | sort | join(", ")' "$IR_FILE" 2>/dev/null || echo "")
  SYMBOL_COUNT=$(jq -r '[.files[].functions[], .files[].classes[]] | unique | length' "$IR_FILE" 2>/dev/null || echo "0")

  echo ""
  echo "IR CONTEXT [re-injected after compact, root_hash: ${SHORT_HASH}...]:"
  echo "${FILE_COUNT} files — ${FILE_LIST}"
  echo "Symbols (${SYMBOL_COUNT}): ${SYMBOLS}"
fi

exit 0
