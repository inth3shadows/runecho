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

# Nothing to recover from
[ ! -f "$CHECKPOINT_FILE" ] && exit 0

TURN=$(jq -r '.turn // "unknown"' "$CHECKPOINT_FILE" 2>/dev/null || echo "unknown")
TS=$(jq -r '.ts // "unknown"' "$CHECKPOINT_FILE" 2>/dev/null || echo "unknown")
IR_HASH=$(jq -r '.ir_hash // ""' "$CHECKPOINT_FILE" 2>/dev/null || echo "")
LAST_MSG=$(jq -r '.last_assistant_message // ""' "$CHECKPOINT_FILE" 2>/dev/null || echo "")

cat > "$HANDOFF_FILE" <<EOF
# Session Handoff (auto-generated on termination)
**Date:** ${TS}
**IR snapshot:** ${IR_HASH}
**Session length:** ~${TURN} turns
**Termination reason:** ${REASON}

## Accomplished
- (session ended before handoff was written — check git log for recent changes)

## Decisions
- unknown

## In Progress
- unknown — last assistant message (truncated):
  ${LAST_MSG}

## Blocked
- unknown

## Next Steps
1. Review recent changes in git log
2. Re-orient with IR context on next session start

## Notes
- This handoff was auto-generated from checkpoint.json (reason: ${REASON})
- A Claude-written handoff would have more detail

## Structural Changes
${VERIFY_SUMMARY:-"(no session-start snapshot — run ai-ir snapshot --label=session-start)"}
EOF

exit 0
