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
#   bash install.sh --hook     # also install pre-commit hook in the current repo
#   bash install.sh --hook --force  # overwrite an existing pre-commit hook

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# Parse flags
INSTALL_HOOK=0
FORCE_HOOK=0
for arg in "$@"; do
  case "$arg" in
    --hook)  INSTALL_HOOK=1 ;;
    --force) FORCE_HOOK=1 ;;
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

command -v go >/dev/null 2>&1 || { echo "install.sh: ERROR: Go toolchain not found (need Go 1.24+)." >&2; exit 1; }

for cmd in runecho-ir runecho-mcp runecho-guard; do
  echo "Building $cmd..."
  go build -o "$BIN_DIR/$cmd$EXE" "./cmd/$cmd"
  echo "  Built: $BIN_DIR/$cmd$EXE"
done

echo ""
echo "RunEcho install complete. Central store lives at ~/.runecho/history.db."

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo ""; echo "NOTE: $BIN_DIR is not on your PATH. Add it:"; echo "  export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac

# --hook: install pre-commit hook in the current repo
if [ "$INSTALL_HOOK" -eq 1 ]; then
  echo ""
  HOOK_DIR="$(git rev-parse --git-dir 2>/dev/null)" || {
    echo "install.sh: ERROR: --hook requires a git repository in the current directory." >&2
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
  echo "Pre-commit hook installed: $HOOK_FILE"
  echo "  Bypass any commit with: RUNECHO_GUARD_SKIP=1 git commit ..."
fi

cat <<EOF

Quick start:
  runecho-ir repo add /path/to/repo     # enroll a repo
  runecho-ir repo reindex <name>        # build IR + snapshot
  runecho-ir repo list                  # see enrolled repos

Install the pre-commit guard in a repo:
  bash install.sh --hook                # from the runecho repo root, targeting cwd
  # or manually: cp .git/hooks/pre-commit <target-repo>/.git/hooks/

Register the oracle MCP server (manual — edits your agent config):
  # Claude Code:
  claude mcp add runecho -- "$BIN_DIR/runecho-mcp$EXE"
  # Codex (~/.codex/config.toml):
  #   [mcp_servers.runecho]
  #   command = "$BIN_DIR/runecho-mcp$EXE"
EOF
