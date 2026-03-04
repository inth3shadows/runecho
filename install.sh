#!/bin/bash
# RunEcho installer — builds binaries, symlinks hooks, and configures ~/.claude/settings.json
# Run once from the repo root: bash install.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HOOK_DIR="$HOME/.claude/hooks"
SETTINGS_FILE="$HOME/.claude/settings.json"

mkdir -p "$HOOK_DIR"

# Build binaries → ~/bin
BIN_DIR="$HOME/bin"
mkdir -p "$BIN_DIR"
cd "$SCRIPT_DIR"

for cmd in ir session document task context; do
  echo "Building ai-$cmd..."
  go build -o "$BIN_DIR/ai-$cmd" "./cmd/$cmd"
  echo "  Built: $BIN_DIR/ai-$cmd"
done

# Symlink hooks (including fault-emitter — sourced by governor, stop-checkpoint, session-end)
echo ""
echo "Symlinking hooks → $HOOK_DIR/"
for hook in \
  fault-emitter.sh \
  session-governor.sh \
  model-enforcer.sh \
  ir-injector.sh \
  stop-checkpoint.sh \
  session-end.sh \
  destructive-bash-guard.sh \
  scope-guard.sh \
  constraint-reinjector.sh \
  pre-compact-snapshot.sh; do
  ln -sf "$SCRIPT_DIR/hooks/$hook" "$HOOK_DIR/$hook"
  echo "  $hook"
done

# Configure ~/.claude/settings.json — merge RunEcho hooks (idempotent)
echo ""
echo "Configuring $SETTINGS_FILE..."

python3 - <<'PYEOF'
import json, os, sys

settings_file = os.path.expanduser("~/.claude/settings.json")

runecho_hooks = {
    "SessionStart": [
        {"matcher": "compact", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/constraint-reinjector.sh", "timeout": 3}]}
    ],
    "UserPromptSubmit": [
        {"matcher": "", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/session-governor.sh", "timeout": 5}]},
        {"matcher": "", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/ir-injector.sh", "timeout": 5}]}
    ],
    "PreToolUse": [
        {"matcher": "Task",      "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/model-enforcer.sh", "timeout": 5}]},
        {"matcher": "Bash",      "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/destructive-bash-guard.sh", "timeout": 3}]},
        {"matcher": "Edit|Write","hooks": [{"type": "command", "command": "bash ~/.claude/hooks/scope-guard.sh", "timeout": 3}]}
    ],
    "PreCompact": [
        {"matcher": "", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/pre-compact-snapshot.sh", "timeout": 3}]}
    ],
    "Stop": [
        {"matcher": "", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/stop-checkpoint.sh", "timeout": 5}]}
    ],
    "SessionEnd": [
        {"matcher": "", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/session-end.sh", "timeout": 5}]}
    ]
}

# Load or create settings
if os.path.exists(settings_file):
    with open(settings_file) as f:
        settings = json.load(f)
else:
    settings = {}

# Extract runecho hook commands for dedup check
def hook_commands(entries):
    cmds = set()
    for entry in entries:
        for h in entry.get("hooks", []):
            cmds.add(h.get("command", ""))
    return cmds

existing_hooks = settings.get("hooks", {})
added = []
skipped = []

for event, entries in runecho_hooks.items():
    existing = existing_hooks.get(event, [])
    existing_cmds = hook_commands(existing)
    for entry in entries:
        entry_cmds = hook_commands([entry])
        if entry_cmds & existing_cmds:
            skipped.append(next(iter(entry_cmds)))
        else:
            existing.append(entry)
            added.append(next(iter(entry_cmds)))
    existing_hooks[event] = existing

settings["hooks"] = existing_hooks

with open(settings_file, "w") as f:
    json.dump(settings, f, indent=2)

if added:
    for cmd in added:
        print(f"  Added: {cmd}")
if skipped:
    for cmd in skipped:
        print(f"  Already present: {cmd}")
print(f"  Settings written: {settings_file}")
PYEOF

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
echo "RunEcho install complete."
echo ""
echo "Optional: set RUNECHO_CLASSIFIER_KEY in your PowerShell profile for LLM routing:"
echo '  $env:RUNECHO_CLASSIFIER_KEY = "sk-ant-api03-..."'
echo ""
echo "Index a project:"
echo "  cd /path/to/project && ai-ir"
