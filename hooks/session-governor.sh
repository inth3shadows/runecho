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
COUNT=$((COUNT + 1))
echo "$COUNT" > "$STATE_FILE"

find "$STATE_DIR" -type f -mtime +1 -delete 2>/dev/null || true

OUTPUT=""

# --- Session Length Warnings ---
WARN_AT=15
STRONG_WARN_AT=25
STOP_AT=35

if [ "$COUNT" -ge "$STOP_AT" ]; then
  OUTPUT="SESSION GOVERNOR [turn $COUNT/$STOP_AT]: Session is very long — context is degrading and cache reads are compounding. Wrap up current task, summarize what was done, and tell the user to start a new session."
elif [ "$COUNT" -ge "$STRONG_WARN_AT" ]; then
  OUTPUT="SESSION GOVERNOR [turn $COUNT]: Session is long. Finish current task, suggest /compact or new session."
elif [ "$COUNT" -ge "$WARN_AT" ]; then
  OUTPUT="SESSION GOVERNOR [turn $COUNT]: Consider wrapping up soon or /compact."
fi

# --- Model Router ---
# Detect task type and inject routing guidance.
# The base model (Sonnet) handles direct coding work.
# Subagents are spawned via the Task tool with model parameter.
#
# ORDER MATTERS: opus check runs before pipeline.
# "review the plan" and "is this the right direction" are opus tasks, not pipeline.
# Pipeline only fires when there is clear implementation intent with no analysis signal.

ROUTE=""

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
elif echo " $PROMPT_LOWER " | grep -qE '( summariz| summary | tl;?dr | recap | search | find | explore | grep | look for | check if | where is | what files | show me | scan | browse | format | boilerplate | template | document | explain .* code| what does .* do| how does .* work| describe | generate .* docs| write .*(readme|comment|doc)| add .* comment| rename | move .* file| diff | compare .* file| git status| git log | git history )'; then
  ROUTE="MODEL ROUTER: Lightweight task. Delegate to a haiku subagent (Task tool, model: \"haiku\"). Only synthesize or review the result yourself if needed."

fi
# No match = Sonnet handles directly (code writing, bug fixes, tests, etc.)

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
if [ -n "$ROUTE" ]; then
  if [ -n "$OUTPUT" ]; then
    OUTPUT="$OUTPUT
$ROUTE"
  else
    OUTPUT="$ROUTE"
  fi
fi

if [ -n "$OUTPUT" ]; then
  echo "$OUTPUT"
fi

exit 0
