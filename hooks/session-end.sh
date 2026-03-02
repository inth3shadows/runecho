#!/bin/bash
# SessionEnd hook — fires on session termination (normal or abnormal)
# If .ai/handoff.md doesn't exist, synthesizes a minimal one from .ai/checkpoint.json
# If handoff already exists, leave it alone (Claude wrote a proper one)

INPUT=$(cat)
CWD=$(echo "$INPUT" | jq -r '.cwd // ""' 2>/dev/null)
[ -z "$CWD" ] && CWD="$PWD"
REASON=$(echo "$INPUT" | jq -r '.reason // "unknown"' 2>/dev/null || echo "unknown")

HANDOFF_FILE="$CWD/.ai/handoff.md"
CHECKPOINT_FILE="$CWD/.ai/checkpoint.json"

# Take session-end snapshot (always — even if handoff already exists).
SESSION_ID_VAL=$(echo "$INPUT" | jq -r '.session_id // ""' 2>/dev/null || echo "")
SESSION_ID_ARG=""
[ -n "$SESSION_ID_VAL" ] && SESSION_ID_ARG="--session=$SESSION_ID_VAL"

if command -v ai-ir &>/dev/null && [ -f "$CWD/.ai/ir.json" ]; then
  ai-ir "$CWD" &>/dev/null || true  # re-index to capture final file state
  ai-ir snapshot --label=session-end $SESSION_ID_ARG "$CWD" &>/dev/null || true
fi

# Compute verify summary for embedding in auto-generated handoff.
VERIFY_SUMMARY=""
if command -v ai-ir &>/dev/null && [ -f "$CWD/.ai/history.db" ]; then
  VERIFY_SUMMARY=$(ai-ir verify $SESSION_ID_ARG "$CWD" 2>/dev/null || true)
fi

# Don't overwrite an existing handoff
[ -f "$HANDOFF_FILE" ] && exit 0

# Try ai-session first — reads the full JSONL log for ground-truth facts
if command -v ai-session &>/dev/null && [ -n "$SESSION_ID_VAL" ]; then
  ai-session --session="$SESSION_ID_VAL" --out="$HANDOFF_FILE" "$CWD" 2>/dev/null && exit 0
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

exit 0
