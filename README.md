# RunEcho

RunEcho is a deterministic **code-truth oracle for AI coding agents**. It gives an
assistant (Claude Code, Codex, or any MCP client) a ground-truth view of what
symbols actually exist in a repo and what *structurally* changed between two
points in time — so the agent can ground its claims instead of guessing.

It is **model-free and vendor-neutral**: no LLM, no API keys, no network. Same
code in always produces the same answer out. The whole pitch is determinism.

## How It Works

RunEcho parses your source into a compact **Intermediate Representation (IR)** —
per file: its content hash plus the functions, classes, exports, and imports it
declares. The IR has a deterministic root hash, so "did the structure change?"
becomes a cheap hash comparison, and "what changed?" becomes a structural diff.

Snapshots of that IR are stored in a single central history database. Each
enrolled repo has a stable identity, so the oracle can answer questions about any
of your repos and compute drift between any two snapshots.

Three binaries:

- **`runecho-ir`** — a CLI to enrol repos, index them, take snapshots, and inspect
  diffs and churn from the terminal.
- **`runecho-mcp`** — a stdio MCP server that exposes read-only oracle tools
  (`structure`, `diff`, `hash`, `status`, `health`) to an AI agent.
- **`runecho-guard`** — a guard that checks new code against the indexed IR and
  flags references to symbols that don't exist (likely hallucinations). Runs as a
  git pre-commit hook, or as a Claude Code `PreToolUse` hook that vets every
  Edit/Write/MultiEdit before it lands.

```
source ──▶ parser ──▶ IR (hashed) ──▶ snapshot ──▶ ~/.runecho/history.db
                                          │
                  AI agent ──(MCP)──▶ runecho-mcp ──▶ structure / diff / hash / ...
                                          │
        git commit / agent edit ──▶ runecho-guard ──▶ "symbol X doesn't exist — block/ask"
```

## Prerequisites

- **Go 1.24+** to build (no other runtime; the binaries are self-contained).
- A POSIX or Windows shell. Storage lives under `~/.runecho/` by default.
- No external services, no API keys.

Languages parsed today: **Go, JavaScript/TypeScript, Python**.

## Quick Start

1. Build and install the binaries into `~/.local/bin`:
   ```bash
   bash install.sh
   ```
2. Enrol a repo and capture its current structure:
   ```bash
   runecho-ir repo add /path/to/your/repo
   runecho-ir repo reindex <name>     # name is shown by `repo add`
   ```
3. See what's enrolled and ask for drift since the last snapshot:
   ```bash
   runecho-ir repo list
   runecho-ir diff --since=reindex /path/to/your/repo
   ```
4. (Optional) Register the oracle with your AI agent so it can query directly.
   The installer prints the exact command, e.g.:
   ```bash
   claude mcp add runecho -- ~/.local/bin/runecho-mcp
   ```
5. (Optional) Install the commit guard in a repo you've enrolled:
   ```bash
   bash install.sh --hook        # run from the target repo's root
   ```
   It blocks commits that call functions which exist nowhere in the indexed code
   (with a "did you mean …?" suggestion when there's a close match). Bypass any
   single commit with `RUNECHO_GUARD_SKIP=1 git commit …`.

## Project Structure

| Path | Purpose |
|---|---|
| `cmd/runecho-ir/` | The CLI: index, snapshot, diff, log, churn, verify, repo, backup |
| `cmd/runecho-mcp/` | The stdio MCP oracle server |
| `cmd/runecho-guard/` | The guard: pre-commit mode + Claude Code hook mode |
| `internal/parser/` | Per-language structure extraction (Go/JS/TS/Python) |
| `internal/ir/` | IR build, deterministic hashing, JSON storage |
| `internal/snapshot/` | Central store: migrations, registry, diff, churn, backup |
| `internal/mcp/` | Minimal MCP plumbing + the oracle tools |
| `internal/guard/` | Diff parsing, symbol extraction, validation, did-you-mean |
| `internal/claims/` | Symbol-reference extraction from prose (`validate-claims`) |
| `internal/gitutil/` | Canonical git-common-dir resolution (worktree identity) |
| `install.sh` | Builds all three binaries; `--hook` installs the pre-commit guard |

## Related Documentation

- [Technical Reference](TECHNICAL.md) — architecture, storage schema, the IR, the MCP tools, maintenance
- [Usage Guide](USAGE.md) — day-to-day operations: enrolling repos, reading drift, troubleshooting

## License

Apache 2.0 — see [LICENSE](LICENSE).
