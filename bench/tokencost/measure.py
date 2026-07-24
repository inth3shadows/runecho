#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = ["tiktoken==0.13.0"]
# ///
"""Measure the context-token cost of each RunEcho surface (#201).

Every tool in the code-intelligence category is sold on saving context, and an
independent benchmark found several of them *increase* it. RunEcho's README
claims "~0 tokens of your context window." This measures that claim instead of
asserting it.

WHAT IT MEASURES, AND WHAT IT DOES NOT

It counts the tokens each surface **emits into the model's context per
invocation**. It does NOT run an agent over a task set and diff total session
usage — that needs real model runs, is expensive and noisy, and its result is
mostly determined by the per-invocation numbers measured here anyway.

That scoping is the honest part. A per-invocation number is deterministic and
reproducible; a whole-session number depends on which tools the agent chose to
call, which is a property of the agent, not of RunEcho.

The guard and the MCP server are reported SEPARATELY and must stay that way. The
guard is a PreToolUse hook — the agent does not choose to call it and it prints
nothing on a clean check, so its cost is structurally zero. The MCP server is a
set of tools the agent calls, and it is not free: its schemas are injected at
session start whether or not any tool is used. Conflating the two would be
exactly the overclaim this benchmark exists to prevent.

Usage:  ./measure.py <enrolled-repo-name>
        RUNECHO_BIN_DIR=/path/to/bin ./measure.py <enrolled-repo-name>
"""

import json
import os
import subprocess
import sys

import tiktoken

# Honour RUNECHO_BIN_DIR the way install.sh does, so this can measure a build
# under test rather than only whatever happens to be installed. Measuring the
# wrong binary is how three of this project's published numbers became fossils.
BIN = os.path.expanduser(os.environ.get("RUNECHO_BIN_DIR") or "~/.local/bin")
ENC = tiktoken.get_encoding("cl100k_base")


def toks(s: str) -> int:
    return len(ENC.encode(s))


def guard_costs(repo_path: str) -> list[tuple[str, str]]:
    """Guard stdout for a clean edit and for one that trips a violation.

    stdout IS the cost: hook mode answers on stdout and Claude Code feeds that
    to the model. stderr is operator diagnostics and never reaches the context,
    so it is deliberately not counted.

    The violating symbol is Capitalized on purpose. RunEcho's Go parser indexes
    exported names only, so the guard skips unexported call names by design —
    a lowercase probe reads as "clean" and would silently measure nothing.
    """
    target = os.path.join(repo_path, "internal", "guard", "probe.go")
    cases = {
        "guard: clean edit": "package guard\n\nfunc probe() {\n\tSuggest(a, b)\n}\n",
        "guard: ask (blocks)": "package guard\n\nfunc probe() {\n\tSuggestNearestSymbo(1)\n}\n",
    }
    out = []
    for label, content in cases.items():
        payload = json.dumps({"tool_name": "Write",
                              "tool_input": {"file_path": target, "content": content}})
        p = subprocess.run([os.path.join(BIN, "runecho-guard"), "--hook-mode"],
                           input=payload, capture_output=True, text=True)
        out.append((label, p.stdout))
    return out


def mcp_costs(repo: str) -> list[tuple[str, str]]:
    """Every oracle tool's response, over one real stdio session."""
    calls = [
        ("mcp: tools/list (always-on)", None, {}),
        ("mcp: health", "health", {}),
        ("mcp: hash", "hash", {"repo": repo}),
        ("mcp: status", "status", {"repo": repo}),
        ("mcp: diff", "diff", {"repo": repo}),
        ("mcp: locate (one symbol)", "locate", {"repo": repo, "symbol": "Suggest"}),
        ("mcp: structure detail=tree", "structure", {"repo": repo, "detail": "tree"}),
        ("mcp: structure paths-scoped", "structure",
         {"repo": repo, "detail": "symbols", "paths": ["internal/guard/**"]}),
        ("mcp: structure (DEFAULT)", "structure", {"repo": repo, "detail": "symbols"}),
        # The pre-#224 default shape. Kept measured, not just described, so the
        # claim that per-symbol hashes dominate the payload stays falsifiable.
        ("mcp: structure detail=hashes", "structure", {"repo": repo, "detail": "hashes"}),
    ]
    lines = [json.dumps({"jsonrpc": "2.0", "id": 0, "method": "initialize",
                         "params": {"protocolVersion": "2024-11-05", "capabilities": {},
                                    "clientInfo": {"name": "tokencost", "version": "1"}}})]
    for i, (_, tool, args) in enumerate(calls, start=1):
        if tool is None:
            lines.append(json.dumps({"jsonrpc": "2.0", "id": i, "method": "tools/list",
                                     "params": {}}))
        else:
            lines.append(json.dumps({"jsonrpc": "2.0", "id": i, "method": "tools/call",
                                     "params": {"name": tool, "arguments": args}}))
    p = subprocess.run([os.path.join(BIN, "runecho-mcp")],
                       input="\n".join(lines) + "\n", capture_output=True, text=True)
    frames = [l for l in p.stdout.splitlines() if l.strip()]
    if len(frames) < len(calls) + 1:
        sys.exit(f"tokencost: expected {len(calls)+1} frames, got {len(frames)} "
                 f"(is the repo enrolled and indexed?)\n{p.stderr[:400]}")
    out = []
    for (label, _, _), raw in zip(calls, frames[1:]):  # frames[0] is initialize
        d = json.loads(raw)
        r = d.get("result", d)
        # A tools/call result wraps its payload in content[0].text; tools/list
        # does not. Measure what actually lands in context either way.
        text = r["content"][0]["text"] if "content" in r else json.dumps(r)
        out.append((label, text))
    return out


def main() -> int:
    if len(sys.argv) != 2:
        sys.exit(__doc__.strip().splitlines()[-1])
    repo = sys.argv[1]
    p = subprocess.run([os.path.join(BIN, "runecho-ir"), "repo", "list"],
                       capture_output=True, text=True)
    path = next((l.split()[-1] for l in p.stdout.splitlines()
                 if l.split() and l.split()[0] == repo), None)
    if not path:
        sys.exit(f"tokencost: {repo!r} is not enrolled (runecho-ir repo list)")

    rows = guard_costs(path) + mcp_costs(repo)
    print(f"{'surface':<32}{'bytes':>10}{'tokens':>10}")
    print("-" * 52)
    for label, payload in rows:
        print(f"{label:<32}{len(payload):>10,}{toks(payload):>10,}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
