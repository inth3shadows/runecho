#!/usr/bin/env bash
# RunEcho installer — builds the two truth-oracle binaries.
#
#   runecho-ir   low-level CLI: index, snapshot, diff, log, churn, verify,
#                repo add|list|rm|reindex, backup
#   runecho-mcp  stdio MCP oracle server (structure/diff/hash/status/health)
#
# Run from the repo root:  bash install.sh
#
# This script does NOT modify ~/.claude.json or any agent config. Registering
# the MCP server is a manual, reversible step printed at the end.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

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

for cmd in runecho-ir runecho-mcp; do
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

cat <<EOF

Quick start:
  runecho-ir repo add /path/to/repo     # enroll a repo
  runecho-ir repo reindex <name>        # build IR + snapshot
  runecho-ir repo list                  # see enrolled repos

Register the oracle MCP server (manual — edits your agent config):
  # Claude Code:
  claude mcp add runecho -- "$BIN_DIR/runecho-mcp$EXE"
  # Codex (~/.codex/config.toml):
  #   [mcp_servers.runecho]
  #   command = "$BIN_DIR/runecho-mcp$EXE"
EOF
