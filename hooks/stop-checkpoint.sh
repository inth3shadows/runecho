#!/bin/bash
# Stop hook — fires after each Claude response
# Writes .ai/checkpoint.json with current session state
# Used by session-end.sh for failure recovery

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")
CWD=$(echo "$INPUT" | jq -r '.cwd // ""' 2>/dev/null)
[ -z "$CWD" ] && CWD="$PWD"

STATE_DIR="$HOME/.claude/hooks/.governor-state"
STATE_FILE="$STATE_DIR/$SESSION_ID"
COUNT=$(cat "$STATE_FILE" 2>/dev/null || echo "0")
IR_HASH=$(jq -r '.root_hash // ""' "$CWD/.ai/ir.json" 2>/dev/null | head -c 12 || echo "")
LAST_MSG=$(echo "$INPUT" | jq -r '.last_assistant_message // ""' 2>/dev/null | head -c 200)
TS=$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date '+%Y-%m-%dT%H:%M:%S')

mkdir -p "$CWD/.ai" 2>/dev/null

jq -n \
  --arg ts "$TS" \
  --arg sid "$SESSION_ID" \
  --argjson turn "$COUNT" \
  --arg ir_hash "$IR_HASH" \
  --arg last_msg "$LAST_MSG" \
  '{ts: $ts, session_id: $sid, turn: $turn, ir_hash: $ir_hash, last_assistant_message: $last_msg}' \
  > "$CWD/.ai/checkpoint.json" 2>/dev/null || true

# Feature 2: IR verify delta — re-index then diff against session-start snapshot.
# Delta written to per-session warnings file; consumed + cleared by session-governor.sh
# on the next UserPromptSubmit turn. No-op if ai-ir unavailable or no snapshot exists.
VERIFY_FILE="$STATE_DIR/${SESSION_ID}.verify-warnings"
if command -v ai-ir &>/dev/null; then
  ai-ir "$CWD" &>/dev/null || true  # incremental re-index
  VERIFY=$(timeout 3 ai-ir verify --session="$SESSION_ID" "$CWD" 2>/dev/null || true)
  if [ -n "$VERIFY" ]; then
    echo "$VERIFY" > "$VERIFY_FILE"
  else
    rm -f "$VERIFY_FILE"
  fi
fi

# Item 6: Anti-hallucination — validate symbol claims in last assistant message.
# Appends CLAIM MISMATCH warnings to .verify-warnings; session-governor surfaces them next turn.
if command -v ai-ir &>/dev/null && [ -f "$CWD/.ai/ir.json" ]; then
  _LAST_MSG=$(echo "$INPUT" | jq -r '.last_assistant_message // ""' 2>/dev/null || true)
  if [ -n "$_LAST_MSG" ]; then
    _CLAIM_TMP=$(mktemp 2>/dev/null || echo "/tmp/runecho-claims-$$")
    echo "$_LAST_MSG" > "$_CLAIM_TMP"
    _CLAIM_OUT=$(timeout 5 ai-ir validate-claims \
      --text="$_CLAIM_TMP" --ir="$CWD/.ai/ir.json" 2>/dev/null || true)
    rm -f "$_CLAIM_TMP"
    if [ -n "$_CLAIM_OUT" ]; then
      _MISMATCH_COUNT=$(echo "$_CLAIM_OUT" | jq '.mismatches | length' 2>/dev/null || echo 0)
      if [ "${_MISMATCH_COUNT:-0}" -gt 0 ]; then
        echo "$_CLAIM_OUT" | jq -r '.mismatches[] | "CLAIM MISMATCH: Referenced \(.ref | @json) not found in IR. Context: \(.context)"' \
          >> "$VERIFY_FILE" 2>/dev/null || true
      fi
    fi
  fi
fi

exit 0
