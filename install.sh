#!/bin/bash
# RunEcho installer — builds binaries and symlinks hooks to ~/.claude/hooks/
# Run once from the repo root: bash install.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HOOK_DIR="$HOME/.claude/hooks"

mkdir -p "$HOOK_DIR"

# Build ai-ir binary
echo "Building ai-ir..."
cd "$SCRIPT_DIR"
go build -o "$HOOK_DIR/ai-ir" ./cmd/ir
echo "  Built: $HOOK_DIR/ai-ir"

# Symlink hooks
ln -sf "$SCRIPT_DIR/hooks/session-governor.sh" "$HOOK_DIR/session-governor.sh"
echo "  $HOOK_DIR/session-governor.sh -> $SCRIPT_DIR/hooks/session-governor.sh"

ln -sf "$SCRIPT_DIR/hooks/model-enforcer.sh" "$HOOK_DIR/model-enforcer.sh"
echo "  $HOOK_DIR/model-enforcer.sh -> $SCRIPT_DIR/hooks/model-enforcer.sh"

ln -sf "$SCRIPT_DIR/hooks/ir-injector.sh" "$HOOK_DIR/ir-injector.sh"
echo "  $HOOK_DIR/ir-injector.sh -> $SCRIPT_DIR/hooks/ir-injector.sh"

echo ""
echo "RunEcho hooks installed:"
ls -la "$HOOK_DIR/session-governor.sh"
ls -la "$HOOK_DIR/model-enforcer.sh"
ls -la "$HOOK_DIR/ir-injector.sh"
ls -la "$HOOK_DIR/ai-ir"

echo ""
echo "Add ir-injector.sh to ~/.claude/settings.json UserPromptSubmit hooks."
echo "Wire it AFTER session-governor.sh (governor must write turn count first)."
echo ""
echo "Add this entry to the UserPromptSubmit hooks array:"
echo ""
cat <<'EOF'
    {
      "matcher": "",
      "hooks": [
        {
          "type": "command",
          "command": "bash ~/.claude/hooks/ir-injector.sh",
          "timeout": 5
        }
      ]
    }
EOF
echo ""
echo "Then index a project:"
echo "  cd /path/to/project && ai-ir"
