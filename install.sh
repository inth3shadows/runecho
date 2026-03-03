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

echo "Building ai-session..."
go build -o "$BIN_DIR/ai-session" ./cmd/session
echo "  Built: $BIN_DIR/ai-session"

echo "Building ai-document..."
go build -o "$BIN_DIR/ai-document" ./cmd/document
echo "  Built: $BIN_DIR/ai-document"

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

ln -sf "$SCRIPT_DIR/hooks/destructive-bash-guard.sh" "$HOOK_DIR/destructive-bash-guard.sh"
echo "  $HOOK_DIR/destructive-bash-guard.sh -> $SCRIPT_DIR/hooks/destructive-bash-guard.sh"

ln -sf "$SCRIPT_DIR/hooks/scope-guard.sh" "$HOOK_DIR/scope-guard.sh"
echo "  $HOOK_DIR/scope-guard.sh -> $SCRIPT_DIR/hooks/scope-guard.sh"

ln -sf "$SCRIPT_DIR/hooks/constraint-reinjector.sh" "$HOOK_DIR/constraint-reinjector.sh"
echo "  $HOOK_DIR/constraint-reinjector.sh -> $SCRIPT_DIR/hooks/constraint-reinjector.sh"

ln -sf "$SCRIPT_DIR/hooks/pre-compact-snapshot.sh" "$HOOK_DIR/pre-compact-snapshot.sh"
echo "  $HOOK_DIR/pre-compact-snapshot.sh -> $SCRIPT_DIR/hooks/pre-compact-snapshot.sh"

echo ""
echo "RunEcho hooks installed:"
ls -la "$HOOK_DIR/session-governor.sh"
ls -la "$HOOK_DIR/model-enforcer.sh"
ls -la "$HOOK_DIR/ir-injector.sh"
ls -la "$HOOK_DIR/stop-checkpoint.sh"
ls -la "$HOOK_DIR/session-end.sh"
ls -la "$HOOK_DIR/destructive-bash-guard.sh"
ls -la "$HOOK_DIR/scope-guard.sh"
ls -la "$HOOK_DIR/constraint-reinjector.sh"
ls -la "$HOOK_DIR/pre-compact-snapshot.sh"
ls -la "$BIN_DIR/ai-ir"*
ls -la "$BIN_DIR/ai-session"
ls -la "$BIN_DIR/ai-document"

# Validate hooks with ShellCheck if available
if command -v shellcheck &>/dev/null; then
  echo ""
  echo "Running ShellCheck on hooks..."
  shellcheck "$SCRIPT_DIR"/hooks/*.sh && echo "  All hooks passed." || echo "  ShellCheck warnings found — review before deploying."
else
  echo ""
  echo "ShellCheck not found — skipping hook validation (install: winget install koalaman.shellcheck)"
fi

echo ""
echo "Add hooks to ~/.claude/settings.json — see README for full config."
echo ""
echo "Required settings.json hook events:"
echo "  SessionStart (compact): constraint-reinjector.sh"
echo "  UserPromptSubmit: session-governor.sh, ir-injector.sh"
echo "  PreToolUse (Task): model-enforcer.sh"
echo "  PreToolUse (Bash): destructive-bash-guard.sh"
echo "  PreToolUse (Edit|Write): scope-guard.sh"
echo "  PreCompact: pre-compact-snapshot.sh"
echo "  Stop: stop-checkpoint.sh"
echo "  SessionEnd: session-end.sh"
echo ""
echo "Set RUNECHO_CLASSIFIER_KEY in your PowerShell profile for LLM routing:"
echo '  $env:RUNECHO_CLASSIFIER_KEY = "sk-ant-api03-..."'
echo ""
echo "Then index a project:"
echo "  cd /path/to/project && ai-ir"
