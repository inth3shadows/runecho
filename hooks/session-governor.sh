#!/bin/bash
# Session Governor + Model Router
# Installed as a UserPromptSubmit hook in ~/.claude/settings.json.
# Fires on every user message. Two jobs:
#   1. Track turn count, warn when sessions get long
#   2. Analyze prompt, inject model routing guidance
#
# Model Pipeline Philosophy:
#   Haiku  = eyes (search, read, summarize, explore)      — cheap
#   Sonnet = hands (write code, implement, fix bugs)       — base model
#   Opus   = brain (architecture, design, complex reasoning) — expensive
#
#   For multi-step work (plans, features):
#     1. Haiku subagents gather information
#     2. Opus subagent reasons about it, produces the design
#     3. Sonnet (base) writes code informed by Opus's output
#   Subagent results flow back to Sonnet. Sonnet has the full picture.

INPUT=$(cat)
SESSION_ID=$(echo "$INPUT" | jq -r '.session_id // "unknown"' 2>/dev/null || echo "unknown")
PROMPT=$(echo "$INPUT" | jq -r '.prompt // ""' 2>/dev/null || echo "")
PROMPT_LOWER=$(echo "$PROMPT" | tr '[:upper:]' '[:lower:]')

# --- Turn Counter ---
STATE_DIR="$HOME/.claude/hooks/.governor-state"
mkdir -p "$STATE_DIR" 2>/dev/null
STATE_FILE="$STATE_DIR/$SESSION_ID"

if [ -f "$STATE_FILE" ]; then
  COUNT=$(cat "$STATE_FILE" 2>/dev/null || echo "0")
else
  COUNT=0
fi

# --- Weighted Turn Increment ---
# Weight by the PREVIOUS turn's route (the expensive work that just happened).
# pipeline=5, opus=3, haiku/sonnet=1. Prevents cheap rename sessions and
# expensive opus-pipeline sessions from looking identical to the governor.
ROUTE_FILE="$STATE_DIR/${SESSION_ID}.route"
LAST_ROUTE=$(cat "$ROUTE_FILE" 2>/dev/null || echo "sonnet")
case "$LAST_ROUTE" in
  pipeline) WEIGHT=5 ;;
  opus)     WEIGHT=3 ;;
  *)        WEIGHT=1 ;;
esac
COUNT=$((COUNT + WEIGHT))
echo "$COUNT" > "$STATE_FILE"

find "$STATE_DIR" -type f -mtime +1 -delete 2>/dev/null || true

# --- Session Cost (from JSONL) ---
# Read the session's JSONL directly — no network, no external tool.
# Apply per-model rates: haiku < sonnet < opus.
SESSION_COST="0"
JSONL_FILE=$(find "$HOME/.claude/projects" -name "${SESSION_ID}.jsonl" 2>/dev/null | head -1)
if [ -n "$JSONL_FILE" ] && command -v jq &>/dev/null; then
  SESSION_COST=$(jq -sr '
    [.[] | select(.type == "assistant") | select(.message.usage) |
      .message |
      { m: .model, u: .usage } |
      if .m | test("haiku"; "i") then
        ((.u.input_tokens // 0) * 0.80 +
         (.u.output_tokens // 0) * 4.0 +
         (.u.cache_read_input_tokens // 0) * 0.08) / 1000000
      elif .m | test("opus"; "i") then
        ((.u.input_tokens // 0) * 15.0 +
         (.u.output_tokens // 0) * 75.0 +
         (.u.cache_read_input_tokens // 0) * 1.5) / 1000000
      else
        ((.u.input_tokens // 0) * 3.0 +
         (.u.output_tokens // 0) * 15.0 +
         (.u.cache_read_input_tokens // 0) * 0.30) / 1000000
      end
    ] | add // 0
  ' "$JSONL_FILE" 2>/dev/null || echo "0")
fi
COST_FMT=$(awk -v c="$SESSION_COST" 'BEGIN { printf "~$%.2f", c+0 }')

# Cost thresholds (USD). On Pro these are informational; on API billing they're active controls.
COST_WARN="1.00"
COST_STRONG="3.00"
COST_STOP="8.00"

# Cost-based routing cap. When session cost >= COST_STOP, opus and pipeline routes are
# downgraded to sonnet. Set to false to disable (opus always available regardless of cost).
BLOCK_OPUS_ON_COST=true
COST_LEVEL=$(awk -v c="$SESSION_COST" -v stop="$COST_STOP" -v strong="$COST_STRONG" -v warn="$COST_WARN" '
  BEGIN {
    if (c+0 >= stop+0) print "stop"
    else if (c+0 >= strong+0) print "strong"
    else if (c+0 >= warn+0) print "warn"
    else print "ok"
  }')

OUTPUT=""

# --- IR Delta Warnings (Feature 2) ---
# If stop-checkpoint.sh detected structural changes since session-start, inject and clear.
# Stored separately so session warnings below don't overwrite it.
IR_DELTA_OUTPUT=""
VERIFY_FILE="$STATE_DIR/${SESSION_ID}.verify-warnings"
if [ -f "$VERIFY_FILE" ]; then
  IR_DELTA=$(cat "$VERIFY_FILE" 2>/dev/null)
  if [ -n "$IR_DELTA" ]; then
    IR_DELTA_OUTPUT="IR DELTA (since session-start):
${IR_DELTA}"
  fi
  rm -f "$VERIFY_FILE"
fi

# --- Session Warnings (turn + cost, harder signal wins) ---
WARN_AT=15
STRONG_WARN_AT=25
STOP_AT=35

if [ "$COUNT" -ge "$STOP_AT" ] || [ "$COST_LEVEL" = "stop" ]; then
  IR_HASH=$(jq -r '.root_hash // ""' "$PWD/.ai/ir.json" 2>/dev/null | head -c 12 || echo "unknown")
  OUTPUT="SESSION GOVERNOR [turn $COUNT, $COST_FMT]: Session limit reached — context degrading, cost accumulating. Wrap up and start a new session.
ACTION REQUIRED: Write session handoff now.
  > Create .ai/handoff.md using the canonical format (accomplished, decisions, in-progress, blocked, next steps).
  > IR snapshot hash: ${IR_HASH}"
elif [ "$COUNT" -ge "$STRONG_WARN_AT" ] || [ "$COST_LEVEL" = "strong" ]; then
  OUTPUT="SESSION GOVERNOR [turn $COUNT, $COST_FMT]: Session is expensive. Finish current task, suggest /compact or new session."
elif [ "$COUNT" -ge "$WARN_AT" ] || [ "$COST_LEVEL" = "warn" ]; then
  OUTPUT="SESSION GOVERNOR [turn $COUNT, $COST_FMT]: Cost rising. Consider wrapping up soon or /compact."
fi

# Append IR delta after any session warning (never drop it).
if [ -n "$IR_DELTA_OUTPUT" ]; then
  if [ -n "$OUTPUT" ]; then
    OUTPUT="${OUTPUT}

${IR_DELTA_OUTPUT}"
  else
    OUTPUT="$IR_DELTA_OUTPUT"
  fi
fi

# --- Model Router ---
# Detect task type and inject routing guidance.
# The base model (Sonnet) handles direct coding work.
# Subagents are spawned via the Task tool with model parameter.
#
# ORDER MATTERS: opus check runs before pipeline.
# "review the plan" and "is this the right direction" are opus tasks, not pipeline.
# Pipeline only fires when there is clear implementation intent with no analysis signal.
#
# Classifier runs first (LLM-based intent classification via haiku).
# Regex fires as fallback when classifier is unavailable or fails.

# --- LLM Classifier ---
classify_route() {
  local prompt_trunc key api_url start_ms response route latency

  key="${RUNECHO_CLASSIFIER_KEY:-}"
  [ -z "$key" ] && return

  prompt_trunc=$(echo "$PROMPT" | head -c 200)
  api_url="https://api.anthropic.com/v1/messages"
  start_ms=$(date +%s%3N 2>/dev/null || echo "0")

  response=$(curl --max-time 2 -s \
    -H "x-api-key: $key" \
    -H "anthropic-version: 2023-06-01" \
    -H "content-type: application/json" \
    "$api_url" \
    -d "$(jq -n \
      --arg prompt "$prompt_trunc" \
      '{
        model: "claude-haiku-4-5-20251001",
        max_tokens: 20,
        system: "Classify the prompt as exactly one: haiku, sonnet, opus, pipeline.\nhaiku: read-only tasks (search, summarize, explain, find, describe, document, write handoff)\nsonnet: direct code tasks (fix bug, refactor, write tests, edit file, rename)\nopus: reasoning tasks (architecture, design, review, trade-offs, strategy, feasibility, alignment, is this right)\npipeline: multi-phase implementation (build new feature, implement from scratch, scaffold, end-to-end)\nRespond with JSON only: {\"route\":\"haiku|sonnet|opus|pipeline\"}",
        messages: [{role: "user", content: $prompt}]
      }')" 2>/dev/null) || true

  route=$(echo "$response" | jq -r '.content[0].text // ""' 2>/dev/null | jq -r '.route // ""' 2>/dev/null || true)
  latency=$(( $(date +%s%3N 2>/dev/null || echo "0") - start_ms ))

  # Validate — must be one of the 4 known values
  case "$route" in
    haiku|sonnet|opus|pipeline) ;;
    *) route="" ;;
  esac

  # Log every call
  echo "{\"ts\":\"$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null)\",\"prompt\":$(echo "$prompt_trunc" | jq -Rs .),\"route\":\"$route\",\"source\":\"classifier\",\"latency_ms\":$latency}" \
    >> "$STATE_DIR/classifier-log.jsonl" 2>/dev/null || true

  echo "$route"
}

ROUTE=""

# Try classifier first; fall through to regex on empty result
CLASSIFIER_ROUTE=$(classify_route)

if [ -n "$CLASSIFIER_ROUTE" ]; then
  case "$CLASSIFIER_ROUTE" in
    opus)
      ROUTE="MODEL ROUTER: Deep reasoning task. Delegate to an opus subagent (Task tool, model: \"opus\") for analysis. Use haiku subagents for any file gathering opus needs. Then implement opus's recommendations yourself (Sonnet)."
      ;;
    pipeline)
      ROUTE="MODEL ROUTER — MULTI-STEP PIPELINE:
  This task has multiple phases. Use this pipeline:
  1. EXPLORE (haiku subagents): Search codebase, read files, gather context. Launch in parallel where possible.
  2. REASON (opus subagent): Feed exploration results into a single opus subagent for architecture/design decisions. Opus returns the plan and key decisions.
  3. IMPLEMENT (you, Sonnet): Write the code yourself based on Opus's design. You have the exploration results and the design in your context.
  This maximizes quality while minimizing cost. Opus only processes the distilled context, not raw files."
      ;;
    haiku)
      ROUTE="MODEL ROUTER: Lightweight task. Delegate to a haiku subagent (Task tool, model: \"haiku\"). Only synthesize or review the result yourself if needed."
      ;;
    sonnet)
      ROUTE=""  # Sonnet direct — no message needed
      ;;
  esac
else
  # Fallback: regex routing (unchanged behavior when classifier unavailable)
  # OPUS-ONLY signals: pure reasoning, no implementation.
  # Checked FIRST to prevent pipeline from stealing analysis/review prompts.
  if echo "$PROMPT_LOWER" | grep -qE '(architect|design.*system|review.*(security|code|pr|approach|plan|direction|design|strategy)|trade.?off|compare.*approach|strategy|evaluate.*option|assess.*risk|critique|redesign|migrate|overhaul|debug.*complex|root.cause|right direction|right approach|right track|work together|do these.*work|make sure.*align|they.*align|are.*aligned|is this.*right|feasib|how much work|realisti|really want|actually want|market.*want|market.*need|would.*market)'; then
    ROUTE="MODEL ROUTER: Deep reasoning task. Delegate to an opus subagent (Task tool, model: \"opus\") for analysis. Use haiku subagents for any file gathering opus needs. Then implement opus's recommendations yourself (Sonnet)."

  # PLAN / MULTI-STEP signals: needs the full pipeline.
  # Only fires when there is explicit implementation intent (build, implement, create, add, scaffold).
  # Does NOT fire on review/analysis prompts — those are caught by the opus check above.
  elif echo "$PROMPT_LOWER" | grep -qE '(implement.*feature|build.*new|create.*system|add.*feature|full.*implementation|end.to.end|start.to.finish|from scratch|scaffold|implement the plan|execute the plan|build this out)'; then
    ROUTE="MODEL ROUTER — MULTI-STEP PIPELINE:
  This task has multiple phases. Use this pipeline:
  1. EXPLORE (haiku subagents): Search codebase, read files, gather context. Launch in parallel where possible.
  2. REASON (opus subagent): Feed exploration results into a single opus subagent for architecture/design decisions. Opus returns the plan and key decisions.
  3. IMPLEMENT (you, Sonnet): Write the code yourself based on Opus's design. You have the exploration results and the design in your context.
  This maximizes quality while minimizing cost. Opus only processes the distilled context, not raw files."

  # HAIKU signals: cheap standalone work.
  # Use specific multi-word phrases and start-of-word patterns to avoid
  # false positives (e.g., "log" matching "login").
  elif echo " $PROMPT_LOWER " | grep -qE '( summariz| summary | tl;?dr | recap | search | find | explore | grep | look for | check if | where is | what files | show me | scan | browse | format | boilerplate | template | document | explain .* code| what does .* do| how does .* work| describe | generate .* docs| write .*(readme|comment|doc)| add .* comment| rename | move .* file| diff | compare .* file| git status| git log | git history | write.*handoff| create.*handoff| session handoff| write.*\.ai/handoff)'; then
    ROUTE="MODEL ROUTER: Lightweight task. Delegate to a haiku subagent (Task tool, model: \"haiku\"). Only synthesize or review the result yourself if needed."

  fi
  # No match = Sonnet handles directly (code writing, bug fixes, tests, etc.)
fi

# --- Cost-Based Routing Cap ---
# If BLOCK_OPUS_ON_COST=true and session cost >= COST_STOP, downgrade opus/pipeline
# to sonnet. Prevents continued expensive model dispatch in a costly session.
if [ "$BLOCK_OPUS_ON_COST" = "true" ] && [ "$COST_LEVEL" = "stop" ]; then
  if echo "$ROUTE" | grep -qE "(MULTI-STEP PIPELINE|Deep reasoning)"; then
    ROUTE=""  # Sonnet direct — opus blocked
    OPUS_BLOCKED_MSG="MODEL ROUTER: Opus/pipeline blocked — session cost ${COST_FMT} exceeds limit. Handling directly as Sonnet. Start a new session to re-enable opus routing."
  fi
fi

# --- Write routing state for model-enforcer.sh ---
ROUTE_FILE="$STATE_DIR/${SESSION_ID}.route"
if echo "$ROUTE" | grep -q "MULTI-STEP PIPELINE"; then
  echo "pipeline" > "$ROUTE_FILE"
elif echo "$ROUTE" | grep -q "Deep reasoning"; then
  echo "opus" > "$ROUTE_FILE"
elif echo "$ROUTE" | grep -q "Lightweight"; then
  echo "haiku" > "$ROUTE_FILE"
else
  echo "sonnet" > "$ROUTE_FILE"
fi

# --- Combine Output ---
if [ -n "${OPUS_BLOCKED_MSG:-}" ]; then
  OUTPUT="${OUTPUT:+$OUTPUT
}$OPUS_BLOCKED_MSG"
elif [ -n "$ROUTE" ]; then
  OUTPUT="${OUTPUT:+$OUTPUT
}$ROUTE"
fi

if [ -n "$OUTPUT" ]; then
  echo "$OUTPUT"
fi

exit 0
