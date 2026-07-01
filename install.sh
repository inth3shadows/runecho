#!/usr/bin/env bash
# RunEcho installer — builds the three truth-oracle binaries.
#
#   runecho-ir     low-level CLI: index, snapshot, diff, log, churn, verify,
#                  repo add|list|rm|reindex, backup
#   runecho-mcp    stdio MCP oracle server (structure/diff/hash/status/health)
#   runecho-guard  git pre-commit hook — blocks commits with unresolved symbols
#
# Usage:
#   bash install.sh            # build all three binaries to $BIN_DIR
#   bash install.sh --hook     # also install the GIT pre-commit hook in the cwd repo
#   bash install.sh --hook --force      # overwrite an existing pre-commit hook
#   bash install.sh --print-hook-config # print the Claude Code PreToolUse snippet
#   bash install.sh --hook-pre-push     # install the tag-monotonicity pre-push hook
#                                        # (this repo's own release safety net, #51/#63)
#
# Two distinct integrations share the runecho-guard binary:
#   --hook               installs the git pre-commit variant (fires at `git commit`)
#   --print-hook-config  emits the Claude Code PreToolUse settings.json snippet
#                        (--hook-mode; fires on every Edit/Write/MultiEdit). This
#                        is the primary, edit-time integration the docs describe.
#
# --hook-pre-push is unrelated to runecho-guard: it installs THIS repo's own
# githooks/pre-push (rejects a non-monotonic vX.Y.Z tag push) into the repo you
# invoke it from. Relevant to runecho maintainers/forks cutting tags, not to
# consumers of the oracle.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# Remember where the user invoked us: --hook targets the CALLER's repo, but the
# Go build below must run from the runecho source tree.
INVOKE_DIR="$(pwd)"
cd "$SCRIPT_DIR"

# Parse flags
INSTALL_HOOK=0
FORCE_HOOK=0
PRINT_HOOK_CONFIG=0
INSTALL_PRE_PUSH=0
for arg in "$@"; do
  case "$arg" in
    --hook)  INSTALL_HOOK=1 ;;
    --force) FORCE_HOOK=1 ;;
    --print-hook-config) PRINT_HOOK_CONFIG=1 ;;
    --hook-pre-push) INSTALL_PRE_PUSH=1 ;;
    *) echo "install.sh: unknown argument: $arg" >&2; exit 1 ;;
  esac
done

# Install target: ~/.local/bin (XDG default; on PATH for most setups).
BIN_DIR="${RUNECHO_BIN_DIR:-$HOME/.local/bin}"
mkdir -p "$BIN_DIR"

# Windows (Git Bash) needs an explicit .exe for native process spawners.
EXE=""
case "${OSTYPE:-}" in
  msys|cygwin) EXE=".exe" ;;
esac
[ -n "${WINDIR:-}" ] && EXE=".exe"

# --print-hook-config: emit the Claude Code PreToolUse snippet and exit. This is
# config-only (no build needed), so it short-circuits before the Go toolchain
# check — usable even on a box without Go to copy the snippet into settings.json.
# The matcher (Edit|Write|MultiEdit) and --hook-mode invocation MUST match what
# cmd/runecho-guard/main.go reads and what TECHNICAL.md documents.
if [ "$PRINT_HOOK_CONFIG" -eq 1 ]; then
  cat <<CFG
Add this to your Claude Code settings.json (~/.claude/settings.json) to vet every
assistant edit at write time via the PreToolUse hook:

  {
    "hooks": {
      "PreToolUse": [
        {
          "matcher": "Edit|Write|MultiEdit",
          "hooks": [
            { "type": "command", "command": "$BIN_DIR/runecho-guard$EXE --hook-mode" }
          ]
        }
      ]
    }
  }

The guard reads the tool-call JSON on stdin and answers via permissionDecision:
unresolved symbols → "ask"; a clean check defers to the normal permission flow.
It never auto-approves and never exits nonzero. Disable per-session with
RUNECHO_GUARD_SKIP=1.
CFG
  exit 0
fi

command -v go >/dev/null 2>&1 || { echo "install.sh: ERROR: Go toolchain not found (need Go 1.24+)." >&2; exit 1; }

# The Python and JS/TS symbol parsers use a pure-Go (CGO-free) tree-sitter
# runtime. Its grammar package can embed all ~206 grammars (~20MB); these build
# tags embed ONLY the languages runecho parses via AST: Python and
# JavaScript/TypeScript/TSX (~200 KiB total, ~1.3% of the binary). Go uses the
# stdlib go/ast, no grammar needed. Without these tags the JS/TS parser degrades
# to regex (names only, no per-symbol spans). runecho-guard does not import the
# parser, so the tags are a harmless no-op there. Build stays CGO-free; do not
# set CGO_ENABLED=1.
GRAMMAR_TAGS="grammar_subset grammar_subset_python grammar_subset_javascript grammar_subset_typescript grammar_subset_tsx"

# Stamp the version both binaries report (internal/version.Version) from the
# latest git tag. Outside a git checkout (tarball install, no tags) git describe
# fails and we fall back to "dev" — honest about being an unstamped build rather
# than asserting a release number.
RUNECHO_VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
VERSION_LDFLAGS="-X github.com/inth3shadows/runecho/internal/version.Version=$RUNECHO_VERSION"

for cmd in runecho-ir runecho-mcp runecho-guard; do
  echo "Building $cmd..."
  go build -tags "$GRAMMAR_TAGS" -ldflags "$VERSION_LDFLAGS" -o "$BIN_DIR/$cmd$EXE" "./cmd/$cmd"
  echo "  Built: $BIN_DIR/$cmd$EXE"
done

echo ""
echo "RunEcho install complete. Central store lives at ~/.runecho/history.db."

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo ""; echo "NOTE: $BIN_DIR is not on your PATH. Add it:"; echo "  export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac

# --hook: install pre-commit hook in the repo the user invoked us FROM (not the
# runecho source tree we cd'd into for the build) — resolve via the invoke dir.
if [ "$INSTALL_HOOK" -eq 1 ]; then
  echo ""
  HOOK_DIR="$(git -C "$INVOKE_DIR" rev-parse --absolute-git-dir 2>/dev/null)" || {
    echo "install.sh: ERROR: --hook requires a git repository in the directory you ran it from." >&2
    exit 1
  }
  HOOK_DIR="$HOOK_DIR/hooks"
  HOOK_FILE="$HOOK_DIR/pre-commit"
  mkdir -p "$HOOK_DIR"

  if [ -f "$HOOK_FILE" ] && [ "$FORCE_HOOK" -eq 0 ]; then
    # Allow overwrite only if this is already a runecho-guard hook
    if ! grep -q "runecho-guard" "$HOOK_FILE" 2>/dev/null; then
      echo "install.sh: ERROR: $HOOK_FILE already exists and is not a runecho-guard hook." >&2
      echo "  Use --force to overwrite, or inspect and integrate manually." >&2
      exit 1
    fi
  fi

  cat > "$HOOK_FILE" <<HOOK
#!/usr/bin/env bash
exec "$BIN_DIR/runecho-guard$EXE" "\$@"
HOOK
  chmod +x "$HOOK_FILE"
  echo "Git pre-commit hook installed: $HOOK_FILE"
  echo "  NOTE: this is the GIT-COMMIT-TIME variant — it vets the staged diff at"
  echo "  'git commit'. For edit-time vetting inside Claude Code (the primary"
  echo "  integration), wire the PreToolUse hook: bash install.sh --print-hook-config"
  echo "  Bypass any commit with: RUNECHO_GUARD_SKIP=1 git commit ..."
fi

# --hook-pre-push: install THIS repo's own release-safety hook into the repo the
# user invoked us from. Hooks live in the git-common-dir (shared across all
# worktrees of a repo), never per-worktree, so this uses --git-common-dir rather
# than --hook's --absolute-git-dir.
if [ "$INSTALL_PRE_PUSH" -eq 1 ]; then
  echo ""
  COMMON_DIR="$(git -C "$INVOKE_DIR" rev-parse --git-common-dir 2>/dev/null)" || {
    echo "install.sh: ERROR: --hook-pre-push requires a git repository in the directory you ran it from." >&2
    exit 1
  }
  case "$COMMON_DIR" in
    /*) ;;
    *) COMMON_DIR="$INVOKE_DIR/$COMMON_DIR" ;;
  esac
  PP_HOOK_DIR="$COMMON_DIR/hooks"
  PP_HOOK_FILE="$PP_HOOK_DIR/pre-push"
  mkdir -p "$PP_HOOK_DIR"

  if [ -f "$PP_HOOK_FILE" ] && [ "$FORCE_HOOK" -eq 0 ]; then
    if ! grep -q "issue #51" "$PP_HOOK_FILE" 2>/dev/null; then
      echo "install.sh: ERROR: $PP_HOOK_FILE already exists and is not this repo's pre-push hook." >&2
      echo "  Use --force to overwrite, or inspect and integrate manually." >&2
      exit 1
    fi
  fi

  cp "$SCRIPT_DIR/githooks/pre-push" "$PP_HOOK_FILE"
  chmod +x "$PP_HOOK_FILE"
  echo "Pre-push tag-monotonicity hook installed: $PP_HOOK_FILE"
  echo "  Rejects a vX.Y.Z tag push that isn't semver-greater than the highest"
  echo "  existing tag (see issue #51). Only relevant if you cut releases here."
fi

cat <<EOF

Quick start:
  runecho-ir repo add /path/to/repo     # enroll a repo
  runecho-ir repo reindex <name>        # build IR + snapshot
  runecho-ir repo list                  # see enrolled repos

Install the GIT pre-commit guard in a repo (commit-time):
  bash install.sh --hook                # run from the target repo's root
  # (installs into the repo you invoke it from; use the full path to install.sh)

Wire the Claude Code PreToolUse guard (edit-time — the primary integration):
  bash install.sh --print-hook-config   # prints the settings.json snippet

(Maintainers/forks only) Guard release tags against non-monotonic pushes:
  bash install.sh --hook-pre-push       # run from the target repo's root

Register the oracle MCP server (manual — edits your agent config):
  # Claude Code:
  claude mcp add runecho -- "$BIN_DIR/runecho-mcp$EXE"
  # Codex (~/.codex/config.toml):
  #   [mcp_servers.runecho]
  #   command = "$BIN_DIR/runecho-mcp$EXE"
EOF
