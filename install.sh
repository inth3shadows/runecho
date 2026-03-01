#!/bin/bash
# RunEcho installer — builds binaries and symlinks hooks to ~/.claude/hooks/
# Run once from the repo root: bash install.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HOOK_DIR="$HOME/.claude/hooks"

mkdir -p "$HOOK_DIR"

# Build ai-ir binary → ~/bin (already on PATH on this machine)
BIN_DIR="$HOME/bin"
mkdir -p "$BIN_DIR"
echo "Building ai-ir..."
cd "$SCRIPT_DIR"
go build -o "$BIN_DIR/ai-ir" ./cmd/ir
echo "  Built: $BIN_DIR/ai-ir"

# Symlink hooks
ln -sf "$SCRIPT_DIR/hooks/session-governor.sh" "$HOOK_DIR/session-governor.sh"
echo "  $HOOK_DIR/session-governor.sh -> $SCRIPT_DIR/hooks/session-governor.sh"

ln -sf "$SCRIPT_DIR/hooks/model-enforcer.sh" "$HOOK_DIR/model-enforcer.sh"
echo "  $HOOK_DIR/model-enforcer.sh -> $SCRIPT_DIR/hooks/model-enforcer.sh"

ln -sf "$SCRIPT_DIR/hooks/ir-injector.sh" "$HOOK_DIR/ir-injector.sh"
echo "  $HOOK_DIR/ir-injector.sh -> $SCRIPT_DIR/hooks/ir-injector.sh"

ln -sf "$SCRIPT_DIR/hooks/stop-checkpoint.sh" "$HOOK_DIR/stop-checkpoint.sh"
echo "  $HOOK_DIR/stop-checkpoint.sh -> $SCRIPT_DIR/hooks/stop-checkpoint.sh"

ln -sf "$SCRIPT_DIR/hooks/session-end.sh" "$HOOK_DIR/session-end.sh"
echo "  $HOOK_DIR/session-end.sh -> $SCRIPT_DIR/hooks/session-end.sh"

echo ""
echo "RunEcho hooks installed:"
ls -la "$HOOK_DIR/session-governor.sh"
ls -la "$HOOK_DIR/model-enforcer.sh"
ls -la "$HOOK_DIR/ir-injector.sh"
ls -la "$HOOK_DIR/stop-checkpoint.sh"
ls -la "$HOOK_DIR/session-end.sh"
ls -la "$BIN_DIR/ai-ir"*

echo ""
echo "Add hooks to ~/.claude/settings.json — see README for full config."
echo ""
echo "Required settings.json hook events:"
echo "  UserPromptSubmit: session-governor.sh, ir-injector.sh"
echo "  PreToolUse (Task): model-enforcer.sh"
echo "  Stop: stop-checkpoint.sh"
echo "  SessionEnd: session-end.sh"
echo ""
echo "Set RUNECHO_CLASSIFIER_KEY in your PowerShell profile for LLM routing:"
echo '  $env:RUNECHO_CLASSIFIER_KEY = "sk-ant-api03-..."'
echo ""
echo "Then index a project:"
echo "  cd /path/to/project && ai-ir"
