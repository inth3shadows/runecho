#!/usr/bin/env bash
# One-shot RunEcho agent-efficiency audit:
#   build throwaway binaries -> ensure corpus repo -> enroll under an isolated
#   RUNECHO_HOME -> run with/without A/B -> done (nothing to restore).
#
# Usage: audit.sh <repo-name> <repo-url> "<question>" [headless|tmux|all]
#   <repo-name>  dir name under the corpus dir + enrolled RunEcho repo name
#   <repo-url>   git URL (cloned --depth 1 when the repo dir is missing)
#   [mode]       headless (default) | tmux | all
# Env: CORPUS          corpus dir (default: /tmp/runecho-agent-eval-corpus)
#      AGENT_EVAL_BUILD throwaway binary dir (default: /tmp/runecho-agent-eval-bin)
set -uo pipefail

NAME="${1:?usage: audit.sh <repo-name> <repo-url> \"<question>\" [mode]}"
URL="${2:?repo-url required}"
Q="${3:?question required}"
MODE="${4:-headless}"

HARNESS="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$HARNESS/../.." && pwd)"     # runecho repo root
CORPUS="${CORPUS:-/tmp/runecho-agent-eval-corpus}"
BUILD="${AGENT_EVAL_BUILD:-/tmp/runecho-agent-eval-bin}"
REPO="$CORPUS/$NAME"

echo "==================== RunEcho agent-eval ===================="
echo "repo=$NAME  mode=$MODE  corpus=$CORPUS"
echo

# 1. Build throwaway runecho-mcp + runecho-ir (never touches ~/.local/bin, so
#    the user's live daily-driver install/other sessions are undisturbed).
GRAMMAR_TAGS="grammar_subset grammar_subset_python grammar_subset_javascript grammar_subset_typescript grammar_subset_tsx"
RUNECHO_VERSION="$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
LDFLAGS="-X github.com/inth3shadows/runecho/internal/version.Version=$RUNECHO_VERSION"
mkdir -p "$BUILD"
echo "→ [1/4] building throwaway runecho-mcp + runecho-ir ($RUNECHO_VERSION)"
( cd "$REPO_ROOT" && go build -tags "$GRAMMAR_TAGS" -ldflags "$LDFLAGS" -o "$BUILD/runecho-mcp" ./cmd/runecho-mcp ) || { echo "build runecho-mcp failed"; exit 1; }
( cd "$REPO_ROOT" && go build -tags "$GRAMMAR_TAGS" -ldflags "$LDFLAGS" -o "$BUILD/runecho-ir"  ./cmd/runecho-ir  ) || { echo "build runecho-ir failed"; exit 1; }
echo "  built: $BUILD/runecho-mcp, $BUILD/runecho-ir"

# 2. Ensure the corpus repo exists (clone shallow if missing, reuse if present).
mkdir -p "$CORPUS"
if [ -d "$REPO/.git" ]; then
  echo "→ [2/4] reusing existing checkout: $REPO"
else
  echo "→ [2/4] cloning $URL"
  git clone --depth 1 "$URL" "$REPO" || { echo "git clone failed"; exit 1; }
fi

# 3. Fresh isolated registry + enroll (enrollment IS the index step here --
#    central SQLite, not a per-repo dir). Never touches ~/.runecho/history.db,
#    so the user's real RunEcho state is untouched by this run.
RUNECHO_HOME_DIR="$(mktemp -d /tmp/runecho-agent-eval-home.XXXXXX)"
echo "→ [3/4] enrolling under isolated registry $RUNECHO_HOME_DIR"
# Flags must precede the positional path -- Go's flag package stops parsing
# at the first non-flag argument, so trailing flags after $REPO are silently
# ignored (this bit the first draft: --name and --no-hooks both no-opped).
RUNECHO_HOME="$RUNECHO_HOME_DIR" "$BUILD/runecho-ir" repo add --name "$NAME" --no-hooks "$REPO" || { echo "repo add failed"; exit 1; }
if [ ! -f "$REPO/.ai/ir.json" ]; then
  echo "enrollment/reindex did not produce $REPO/.ai/ir.json"
  exit 1
fi

# 4. Run the with/without A/B.
echo "→ [4/4] running A/B harness (mode=$MODE)"
RUNECHO_MCP_BIN="$BUILD/runecho-mcp" RUNECHO_HOME_DIR="$RUNECHO_HOME_DIR" RUNECHO_BUILD_DIR="$BUILD" \
  bash "$HARNESS/run-all.sh" "$REPO" "$Q" "$MODE"

echo
echo "registry left at $RUNECHO_HOME_DIR (nothing to restore/clean up)"
echo "==================== audit complete ===================="
