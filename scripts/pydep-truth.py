#!/usr/bin/env python3
"""Capture ground truth for the depindex false-positive harness.

Imports each package in a real virtualenv and records what the LIVE interpreter
reports via dir(module). internal/depindex/realenv_test.go then asserts that
every one of those names is present in the resolver's Resolved export set — a
name the interpreter has but the resolver lacks is a false positive waiting to
happen.

This exists because the resolver reads SOURCE while Python resolves at RUNTIME.
Only the interpreter can settle what a module actually exposes, so the fixtures
in testdata/ can never replace this check; they can only pin what it finds.

Usage:
    python3 scripts/pydep-truth.py /path/to/.venv /tmp/truth.json [pkg ...]

With no package list, a default set of widely-used distributions is probed and
any that are not installed are skipped.
"""

import json
import subprocess
import sys

DEFAULT_PACKAGES = [
    "polars", "numpy", "pandas", "requests", "httpx", "pydantic", "sqlalchemy",
    "flask", "click", "rich", "attrs", "jinja2", "yaml", "urllib3", "werkzeug",
]

# Runs INSIDE the target interpreter: the whole point is to observe the module
# objects that interpreter builds, which cannot be done from this process.
PROBE = r"""
import importlib, json, sys
out = {}
for name in json.loads(sys.argv[1]):
    try:
        mod = importlib.import_module(name)
    except Exception:
        continue
    out[name] = sorted(n for n in dir(mod) if not n.startswith("__"))
json.dump(out, sys.stdout)
"""


def main(argv):
    if len(argv) < 3:
        print(__doc__, file=sys.stderr)
        return 2
    venv, out_path = argv[1], argv[2]
    packages = argv[3:] or DEFAULT_PACKAGES

    python = f"{venv}/bin/python"
    proc = subprocess.run(
        [python, "-c", PROBE, json.dumps(packages)],
        capture_output=True, text=True,
    )
    if proc.returncode != 0:
        print(f"probe failed: {proc.stderr}", file=sys.stderr)
        return 1

    truth = json.loads(proc.stdout)
    with open(out_path, "w") as fh:
        json.dump(truth, fh, indent=2, sort_keys=True)

    missing = sorted(set(packages) - set(truth))
    print(f"captured {len(truth)} module(s) -> {out_path}")
    if missing:
        print(f"not installed, skipped: {', '.join(missing)}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
