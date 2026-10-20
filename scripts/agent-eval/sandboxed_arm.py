#!/usr/bin/env python3
"""Run ONE headless with/without arm inside modelbench's sandbox (tier C).

SANDBOX.md (modelbench bench/review/) tier C: an efficiency A/B has no answer
key, so what matters is arm parity (no host hooks, CLAUDE.md or skills leaking
into one arm) and host safety (no bypassPermissions on the host). Here the arm
runs in bubblewrap with a fresh config dir, so RunEcho is the only variable:

  with     the runecho MCP server (binary ro at its host path, RUNECHO_HOME a
           throwaway copy of the enrolled registry) and runecho-ir on PATH
  without  an empty MCP config

Both arms get a throwaway copy of the repo, bound at /work/repo (cwd) and at
its original path (the registry records that path), so neither can leave files
for the other or touch the corpus. Enrol on the host first (audit.sh does);
this script never builds or enrols.

Credential: CLAUDE_CODE_OAUTH_TOKEN, the dedicated benchmark token, from
secret-broker (never the host's own login):
  sb-run --cred claude_bench_oath_token=CLAUDE_CODE_OAUTH_TOKEN -- \\
      python3 sandboxed_arm.py --repo R --label headless-with --question Q --out DIR \\
      --mcp-bin BIN --home-dir HOME --build-dir BUILD

modelbench is imported by path (its SANDBOX.md import contract); override the
location with MODELBENCH_REVIEW.
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile

REVIEW = os.environ.get("MODELBENCH_REVIEW") or os.path.expanduser(
    "~/personal_projects/modelbench/main/bench/review")


def load_sandbox():
    if not os.path.isfile(os.path.join(REVIEW, "sandbox.py")):
        sys.exit(f"sandboxed_arm.py: modelbench sandbox.py not found at {REVIEW} (set MODELBENCH_REVIEW)")
    sys.path.insert(0, REVIEW)
    import sandbox  # noqa: E402
    return sandbox


def mcp_config(with_runecho, mcp_bin, home_dir):
    if not with_runecho:
        return {"mcpServers": {}}
    # RUNECHO_HOME travels in the MCP config's own env block: the server has no
    # --path scoping flag, and Claude Code launches it with exactly this env.
    return {"mcpServers": {"runecho": {"command": mcp_bin, "args": [],
                                       "env": {"RUNECHO_HOME": home_dir}}}}


def build_cmd(S, question, model, effort, budget, with_runecho, home_dir, build_dir):
    claude = [S.CLAUDE_IN, "-p", question, "--output-format", "stream-json", "--verbose",
              "--no-session-persistence", "--permission-mode", "bypassPermissions",
              "--model", model, "--effort", effort, "--max-budget-usd", str(budget),
              "--strict-mcp-config", "--mcp-config", f"{S.CONFIG_DIR}/mcp.json",
              "--disallowedTools", ",".join(S.DISALLOWED_TOOLS)]
    if not with_runecho:
        return claude
    # The WITH arm also gets RUNECHO_HOME/PATH in the outer claude process, so a
    # self-discovering `Bash: runecho-ir repo list` hits the same registry.
    path = f"{build_dir}:{S.sandbox_env()['PATH']}"
    return ["env", f"RUNECHO_HOME={home_dir}", f"PATH={path}", *claude]


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--repo", required=True, help="enrolled corpus repo (host path)")
    ap.add_argument("--label", required=True, help="e.g. headless-with / headless-without")
    ap.add_argument("--question", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--mcp-bin")
    ap.add_argument("--home-dir", help="RUNECHO_HOME registry dir (host path)")
    ap.add_argument("--build-dir", help="dir holding runecho-ir")
    ap.add_argument("--model", default=os.environ.get("MODEL", "sonnet"))
    ap.add_argument("--effort", default=os.environ.get("EFFORT", "high"))
    ap.add_argument("--budget", type=float, default=4)
    ap.add_argument("--timeout", type=int, default=1800)
    a = ap.parse_args(argv)
    with_runecho = bool(a.mcp_bin)
    if with_runecho and not (a.home_dir and a.build_dir):
        ap.error("--mcp-bin needs --home-dir and --build-dir")
    token = os.environ.pop("CLAUDE_CODE_OAUTH_TOKEN", None)
    if not token:
        sys.exit("sandboxed_arm.py: CLAUDE_CODE_OAUTH_TOKEN not set: run via secret-broker (see the docstring)")

    S = load_sandbox()
    repo = os.path.realpath(a.repo)
    os.makedirs(a.out, exist_ok=True)
    work = tempfile.mkdtemp(prefix="re-arm-", dir="/tmp")
    try:
        copy = os.path.join(work, "repo")
        shutil.copytree(repo, copy, symlinks=True)
        cfg = os.path.join(work, "mcp.json")
        with open(cfg, "w") as fh:
            json.dump(mcp_config(with_runecho, a.mcp_bin, a.home_dir), fh)
        extra_ro, extra_rw = [], [(copy, repo)]
        if with_runecho:
            home_copy = os.path.join(work, "home")
            shutil.copytree(a.home_dir, home_copy, symlinks=True)
            extra_ro += [(a.mcp_bin, a.mcp_bin), (a.build_dir, a.build_dir)]
            extra_rw.append((home_copy, a.home_dir))
        cmd = build_cmd(S, a.question, a.model, a.effort, a.budget, with_runecho, a.home_dir, a.build_dir)
        print(f"############ HEADLESS [{a.label}] (sandboxed) ############", flush=True)
        with S.sandbox(copy, claude=S.claude_binary(), config_files={"mcp.json": cfg},
                       extra_ro=extra_ro, extra_rw=extra_rw,
                       secret_env={"CLAUDE_CODE_OAUTH_TOKEN": token}) as run:
            try:
                r = run(cmd, timeout=a.timeout)
                rc, out, err = r.returncode, r.stdout or "", r.stderr or ""
            except subprocess.TimeoutExpired as e:
                dec = lambda b: b.decode("utf-8", "replace") if isinstance(b, bytes) else (b or "")  # noqa: E731
                rc, out, err = "timeout", dec(e.stdout), dec(e.stderr)
        out, _ = S.scrub(out, {token})
        err, _ = S.scrub(err, {token})
        jsonl = os.path.join(a.out, f"run-{a.label}.jsonl")
        with open(jsonl, "w") as fh:
            fh.write(out)
        with open(os.path.join(a.out, f"run-{a.label}.err"), "w") as fh:
            fh.write(err)
        print(f"exit {rc} -> {jsonl} ({out.count(chr(10))} lines)", flush=True)
        return 0 if rc == 0 else 1
    finally:
        shutil.rmtree(work, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
