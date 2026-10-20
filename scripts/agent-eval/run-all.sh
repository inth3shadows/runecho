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

# Headless arm: claude -p with stream-json -> exact tool sequence + tokens/cost,
# run INSIDE modelbench's sandbox (sandboxed_arm.py; SANDBOX.md tier C). A fresh
# config dir means the host's hooks, CLAUDE.md and skills cannot reach either
# arm (the host `codegraph prompt-hook` once leaked structural analysis into the
# "without" arm and invalidated a run; CODEGRAPH_NO_PROMPT_HOOK is no longer
# needed here), and bypassPermissions is bounded by the sandbox, not the host.
# The WITH arm gets the runecho MCP + runecho-ir; the WITHOUT arm an empty MCP.
# Needs CLAUDE_CODE_OAUTH_TOKEN (dedicated benchmark token) via secret-broker.
headless() {
  local label="$1"; shift
  echo "############################## HEADLESS [$label] ##############################"
  python3 "$HARNESS/sandboxed_arm.py" --repo "$REPO" --label "$label" --question "$Q" \
      --out "$OUT" --model "${MODEL:-sonnet}" --effort "${EFFORT:-high}" "$@"
  tail -2 "$OUT/run-$label.err" 2>/dev/null
  node "$HARNESS/parse-run.mjs" "$OUT/run-$label.jsonl" 2>&1 || true
  echo
}

if [ "$MODE" = headless ] || [ "$MODE" = all ]; then
  [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] || { echo "headless arms run sandboxed and need CLAUDE_CODE_OAUTH_TOKEN (run via secret-broker; see sandboxed_arm.py)"; exit 1; }
  headless "headless-with"    --mcp-bin "$RUNECHO_MCP_BIN" --home-dir "$RUNECHO_HOME_DIR" --build-dir "$RUNECHO_BUILD_DIR"
  headless "headless-without"
fi

if [ "$MODE" = tmux ] || [ "$MODE" = all ]; then
  # HOST-ONLY and not arm-parity-safe: itrun.sh drives the real TUI with the
  # host ~/.claude (that is its point: users' interactive behaviour). The host
  # `codegraph prompt-hook` fires regardless of --strict-mcp-config, so
  # CODEGRAPH_NO_PROMPT_HOOK=1 is applied to both arms unconditionally.
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
