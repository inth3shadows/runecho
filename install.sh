#!/bin/bash
# RunEcho installer — symlinks hooks to ~/.claude/hooks/
# Run once from the repo root: bash install.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HOOK_DIR="$HOME/.claude/hooks"

mkdir -p "$HOOK_DIR"

# Symlink hooks
ln -sf "$SCRIPT_DIR/hooks/session-governor.sh" "$HOOK_DIR/session-governor.sh"
ln -sf "$SCRIPT_DIR/hooks/model-enforcer.sh" "$HOOK_DIR/model-enforcer.sh"

# Verify
echo "RunEcho hooks installed:"
ls -la "$HOOK_DIR/session-governor.sh"
ls -la "$HOOK_DIR/model-enforcer.sh"
echo ""
echo "Ensure ~/.claude/settings.json has hooks configured."
echo "See README.md for settings.json example."
