#!/usr/bin/env bash
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

for cmd in ir session document task context governor pipeline session-end provenance; do
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
  pre-compact-snapshot.sh \
  contract-sync.sh; do
  rm -f "$HOOK_DIR/$hook" && ln -s "$SCRIPT_DIR/hooks/$hook" "$HOOK_DIR/$hook"
  echo "  $hook"
done

# Symlink skills → ~/.claude/skills/
SKILL_DIR="$HOME/.claude/skills"
mkdir -p "$SKILL_DIR"
echo ""
echo "Symlinking skills → $SKILL_DIR/"
for skill_src in "$SCRIPT_DIR"/skills/*/; do
  skill_name="$(basename "$skill_src")"
  rm -rf "$SKILL_DIR/$skill_name" && ln -s "$skill_src" "$SKILL_DIR/$skill_name"
  echo "  $skill_name"
done

# Configure ~/.claude/settings.json — merge RunEcho hooks (idempotent)
echo ""
echo "Configuring $SETTINGS_FILE..."

PYTHON=$(command -v python3 2>/dev/null || command -v python 2>/dev/null || true)
if [ -z "$PYTHON" ]; then
  echo "install.sh: ERROR: Python 3 not found. Install Python 3 and re-run." >&2
  exit 1
fi
# Verify python isn't a Windows Store stub (exits 9 rather than running Python)
if ! "$PYTHON" -c "import sys; sys.exit(0)" 2>/dev/null; then
  echo "install.sh: ERROR: Python at '$PYTHON' is not functional (Windows Store stub?). Install Python 3 from python.org." >&2
  exit 1
fi
"$PYTHON" - <<'PYEOF'
import json, os, sys

settings_file = os.path.expanduser("~/.claude/settings.json")

runecho_hooks = {
    "SessionStart": [
        {"matcher": "compact", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/constraint-reinjector.sh", "timeout": 3}]}
    ],
    "UserPromptSubmit": [
        {"matcher": "", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/session-governor.sh", "timeout": 5}]},
        {"matcher": "", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/ir-injector.sh", "timeout": 5}]},
        {"matcher": "", "hooks": [{"type": "command", "command": "bash ~/.claude/hooks/contract-sync.sh", "timeout": 3}]}
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
  case "$(uname -s)" in
    Darwin) echo "ShellCheck not found — skipping hook validation (install: brew install shellcheck)" ;;
    Linux)  echo "ShellCheck not found — skipping hook validation (install: apt install shellcheck / dnf install ShellCheck)" ;;
    *)      echo "ShellCheck not found — skipping hook validation (install: winget install koalaman.shellcheck)" ;;
  esac
fi

echo ""
echo "RunEcho install complete."
echo ""
echo "Optional: set RUNECHO_CLASSIFIER_KEY for LLM routing:"
case "$(uname -s)" in
  Darwin|Linux) echo '  export RUNECHO_CLASSIFIER_KEY="sk-ant-api03-..."  # add to ~/.bashrc or ~/.zshrc' ;;
  *)            echo '  $env:RUNECHO_CLASSIFIER_KEY = "sk-ant-api03-..."  # add to PowerShell profile' ;;
esac
echo ""
echo "Index a project:"
echo "  cd /path/to/project && ai-ir"
