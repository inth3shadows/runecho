#!/usr/bin/env bash
# With/without A/B (and optional interactive) eval for RunEcho on an enrolled
# repo. RunEcho is the ONLY variable: both arms launch claude with
# --strict-mcp-config -- with = runecho-only MCP (pointed at $RUNECHO_MCP_BIN,
# env-scoped to $RUNECHO_HOME_DIR), without = empty MCP. Built-in Read/Grep/Bash
# stay available in both arms; runecho-ir is on PATH for the with arm too, so a
# self-discovering agent can `runecho-ir repo list` to learn the enrolled name
# (RunEcho, unlike CodeGraph, has no --path flag to scope the server to one repo).
#
# Usage: run-all.sh <repo-path> "<question>" [headless|tmux|all]
# Env:   RUNECHO_MCP_BIN    runecho-mcp binary (required)
#        RUNECHO_HOME_DIR   isolated registry dir (required)
#        RUNECHO_BUILD_DIR  dir containing runecho-ir, put on PATH for the with arm
#        AGENT_EVAL_OUT     output dir (default: /tmp/agent-eval)
#        MODEL / EFFORT     claude model/effort (default: sonnet / high)
set -uo pipefail

REPO="${1:?usage: run-all.sh <repo-path> \"<question>\" [headless|tmux|all]}"
Q="${2:?question required}"
MODE="${3:-headless}"
RUNECHO_MCP_BIN="${RUNECHO_MCP_BIN:?RUNECHO_MCP_BIN required}"
RUNECHO_HOME_DIR="${RUNECHO_HOME_DIR:?RUNECHO_HOME_DIR required}"
RUNECHO_BUILD_DIR="${RUNECHO_BUILD_DIR:?RUNECHO_BUILD_DIR required}"
OUT="${AGENT_EVAL_OUT:-/tmp/agent-eval}"
HARNESS="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$OUT"

case "$MODE" in headless|tmux|all) ;; *) echo "mode must be headless|tmux|all (got '$MODE')"; exit 1;; esac

# MCP config files (path form avoids inline-JSON quoting through tmux). The
# runecho server has no --path scoping flag, so RUNECHO_HOME travels in the
# MCP config's own env block -- Claude Code launches the server subprocess
# with exactly this env, regardless of the parent claude process's env.
cat > "$OUT/mcp-runecho.json" <<JSON
{"mcpServers":{"runecho":{"command":"$RUNECHO_MCP_BIN","args":[],"env":{"RUNECHO_HOME":"$RUNECHO_HOME_DIR"}}}}
JSON
echo '{"mcpServers":{}}' > "$OUT/mcp-empty.json"

echo "###### runecho-mcp: $RUNECHO_MCP_BIN"
echo "###### repo:        $REPO"
echo "###### question:    $Q"
echo

# Headless arm: claude -p with stream-json -> exact tool sequence + tokens/cost.
# The WITH arm also gets RUNECHO_HOME/PATH in the OUTER claude process's env
# (not just the MCP subprocess's) so a self-discovering `Bash: runecho-ir
# repo list` call hits the same isolated registry and finds the binary.
# CODEGRAPH_NO_PROMPT_HOOK=1 is applied to BOTH arms unconditionally: this
# machine's global ~/.claude/settings.json runs `codegraph prompt-hook` on
# every prompt regardless of --strict-mcp-config (which only restricts MCP
# servers, not Claude Code's own hooks) -- without this, the "without" arm
# silently gets CodeGraph's structural analysis injected for free, which
# invalidated an earlier run (it answered a symbol-lookup question in 2s with
# zero tool calls, citing "verified via CodeGraph's live re-parse").
headless() {
  local label="$1" cfg="$2" withenv="$3"
  echo "############################## HEADLESS [$label] ##############################"
  ( cd "$REPO" && env CODEGRAPH_NO_PROMPT_HOOK=1 $withenv claude -p "$Q" \
      --output-format stream-json --verbose \
      --permission-mode bypassPermissions \
      --model "${MODEL:-sonnet}" --effort "${EFFORT:-high}" \
      --max-budget-usd 4 \
      --strict-mcp-config --mcp-config "$cfg" \
      > "$OUT/run-$label.jsonl" 2>"$OUT/run-$label.err" )
  echo "exit $? -> $OUT/run-$label.jsonl ($(wc -l < "$OUT/run-$label.jsonl" | tr -d ' ') lines)"
  tail -2 "$OUT/run-$label.err" 2>/dev/null
  node "$HARNESS/parse-run.mjs" "$OUT/run-$label.jsonl" 2>&1 || true
  echo
}

if [ "$MODE" = headless ] || [ "$MODE" = all ]; then
  headless "headless-with"    "$OUT/mcp-runecho.json" "RUNECHO_HOME=$RUNECHO_HOME_DIR PATH=$RUNECHO_BUILD_DIR:$PATH"
  headless "headless-without" "$OUT/mcp-empty.json"    ""
fi

if [ "$MODE" = tmux ] || [ "$MODE" = all ]; then
  # Same CODEGRAPH_NO_PROMPT_HOOK=1 rationale as the headless arm above --
  # applied to both arms unconditionally.
  echo "############################## INTERACTIVE [with] ##############################"
  CLAUDE_ENV_PREFIX="env CODEGRAPH_NO_PROMPT_HOOK=1 RUNECHO_HOME=$RUNECHO_HOME_DIR PATH=$RUNECHO_BUILD_DIR:$PATH" \
    CLAUDE_EXTRA_ARGS="--model ${MODEL:-sonnet} --effort ${EFFORT:-high} --strict-mcp-config --mcp-config $OUT/mcp-runecho.json" \
    bash "$HARNESS/itrun.sh" "$REPO" "int-with" "$Q" 2>&1 || echo "[itrun WITH failed]"
  echo
  echo "############################## INTERACTIVE [without] ##############################"
  CLAUDE_ENV_PREFIX="env CODEGRAPH_NO_PROMPT_HOOK=1" \
    CLAUDE_EXTRA_ARGS="--model ${MODEL:-sonnet} --effort ${EFFORT:-high} --strict-mcp-config --mcp-config $OUT/mcp-empty.json" \
    bash "$HARNESS/itrun.sh" "$REPO" "int-without" "$Q" 2>&1 || echo "[itrun WITHOUT failed]"
  echo
fi
echo "############################## RUN-ALL COMPLETE ##############################"
