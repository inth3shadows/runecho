#!/usr/bin/env bash
# RunEcho pre-commit hook — auto-close tasks whose verify passes on commit
# Installed by install.sh → .git/hooks/pre-commit

set -uo pipefail

TASKS_FILE=".ai/tasks.json"

# git hooks may not inherit ~/bin — ensure it's in PATH
export PATH="$HOME/bin:$PATH"

STAGED=$(git diff --cached --name-only 2>/dev/null || true)
[[ -z "$STAGED" ]] && exit 0
[[ ! -f "$TASKS_FILE" ]] && exit 0

command -v ai-task >/dev/null 2>&1 || { echo "[pre-commit] ai-task not found — skipping task auto-close" >&2; exit 0; }

# Detect Python — on Windows, 'py' (launcher) works where 'python3' is a Store stub
PYTHON=$(command -v py 2>/dev/null || command -v python 2>/dev/null || command -v python3 2>/dev/null || true)
if [ -z "$PYTHON" ] || ! "$PYTHON" -c "import sys; sys.exit(0)" 2>/dev/null; then
  echo "[pre-commit] python not found or non-functional — skipping task auto-close" >&2
  exit 0
fi

"$PYTHON" - <<'PYEOF'
import json, subprocess, sys, os, fnmatch

# Ensure ~/bin is in PATH so ai-task is found in subprocesses (Git Bash home)
_home = os.environ.get("HOME", os.path.expanduser("~"))
# On Windows via Git Bash, HOME may be /c/Users/... — resolve to real Windows path
import pathlib
_home_path = pathlib.Path(_home).expanduser()
os.environ["PATH"] = str(_home_path / "bin") + os.pathsep + os.environ.get("PATH", "")

tasks_file = ".ai/tasks.json"
staged = subprocess.check_output(["git", "diff", "--cached", "--name-only"]).decode().splitlines()

with open(tasks_file, encoding="utf-8") as f:
    data = json.load(f)

tasks = data if isinstance(data, list) else data.get("tasks", [])
closed = []

def matches_scope(staged_files, scope):
    """Return True if any staged file matches any pattern in the comma-separated scope."""
    if not scope:
        return True  # no scope = always eligible
    patterns = [p.strip() for p in scope.split(",")]
    for f in staged_files:
        for pat in patterns:
            if fnmatch.fnmatch(f, pat):
                return True
            # Handle dir/** patterns: check prefix
            base = pat.rstrip("/*").rstrip("/")
            if base and f.startswith(base + "/"):
                return True
            # Exact match
            if f == pat:
                return True
    return False

import re

def safe_tid(tid):
    """Task IDs must be alphanumeric + hyphen only — reject anything else."""
    return bool(re.match(r'^[a-zA-Z0-9\-]+$', str(tid)))

def safe_verify(verify):
    """Verify commands must not contain shell metacharacters that enable injection."""
    # Allow: word chars, spaces, hyphens, forward slashes, dots, underscores, equals, quotes
    # Deny: semicolons, pipes, backticks, $(...), &&, ||, redirects, newlines
    bad = re.search(r'[;&|`$\n<>]|\$\(|&&|\|\|', verify)
    return bad is None

for task in tasks:
    if task.get("status") not in ("pending", "in-progress"):
        continue
    verify = task.get("verify", "").strip()
    if not verify:
        continue
    scope = task.get("scope", "").strip()
    tid = task["id"]

    # Security: validate task ID and verify command before executing
    if not safe_tid(tid):
        print(f"[pre-commit] SKIP #{tid} — unsafe task ID rejected", file=sys.stderr)
        continue
    if not safe_verify(verify):
        print(f"[pre-commit] SKIP #{tid} — verify command contains unsafe chars, refusing shell execution", file=sys.stderr)
        continue

    if not matches_scope(staged, scope):
        continue

    # verify is validated above (no shell metacharacters) — shell=True is safe here
    # and required for PATH resolution on Windows (go, shellcheck, etc.)
    # cwd=None inherits the process working directory (avoids POSIX path issues on Windows)
    result = subprocess.run(
        verify, shell=True, capture_output=True, text=True
    )
    if result.returncode == 0:
        # tid is validated alphanumeric-only above — safe to interpolate here
        subprocess.run(f"ai-task update {tid} done", shell=True, check=False, capture_output=True)
        closed.append(tid)
        title = task.get("title", tid)[:60]
        print(f"[pre-commit] ✓ #{tid} closed — {title}", file=sys.stderr)
    else:
        title = task.get("title", tid)[:50]
        print(f"[pre-commit] #{tid} verify failed — not closed ({title})", file=sys.stderr)

if closed:
    # git is always in PATH from the hook environment
    subprocess.run("git add .ai/tasks.json", shell=True, check=False)
PYEOF
