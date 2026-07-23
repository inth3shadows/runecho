# RunEcho

[![CI](https://github.com/inth3shadows/runecho/actions/workflows/ci.yml/badge.svg)](https://github.com/inth3shadows/runecho/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/inth3shadows/runecho)](https://github.com/inth3shadows/runecho/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![MCP-compatible](https://img.shields.io/badge/MCP-compatible-blueviolet.svg)](https://modelcontextprotocol.io)
[![No LLM](https://img.shields.io/badge/No%20LLM-100%25%20local-brightgreen.svg)](#prerequisites)

[![Windows](https://img.shields.io/badge/Windows-supported-blue.svg)](#quick-start)
[![macOS](https://img.shields.io/badge/macOS-supported-blue.svg)](#quick-start)
[![Linux](https://img.shields.io/badge/Linux-supported-blue.svg)](#quick-start)

RunEcho stops an agent from writing a call to a function your repo doesn't have —
**before the write lands**, not after the build fails. It runs as a `PreToolUse`
hook inside the agent loop: every `Edit`/`Write` is checked against the symbols
your code actually declares, and a reference to one that doesn't exist stops the
write and asks you first. ~12 ms, no build, no language server.

It is **model-free and vendor-neutral**: no LLM, no API keys, no network, no
build, no language server. **The same code produces the same answer.**

**The guard costs zero context tokens** — measured, not asserted: a clean check
writes nothing at all, and only an edit it actually stops costs anything (~100
tokens). It is a `PreToolUse` hook, so the agent never spends context deciding
whether to call it. The oracle MCP server is a separate surface and is *not*
free — its tool schemas cost ~919 tokens at session start, and `structure`
unscoped is expensive enough to be worth scoping. Every number, including the
unflattering ones, is in [bench/TOKEN-COST.md](bench/TOKEN-COST.md).

**The scope, stated up front.** RunEcho reads *unqualified* references — bare
calls, constant references, and type annotations. Measured against its own
corpus of real model hallucinations, that catches **4 of 9** (N=15 hand-verified
cases mined from live session transcripts, each backed by a compiler or runtime
error as independent ground truth). The other 5 are qualified positions —
`df.groupby(…)`, `tree.Root()` — which need receiver-type resolution and are out
of scope by design. The numbers, including the misses, are in
[bench/FINDINGS.md](bench/FINDINGS.md).

That is the honest shape of the thing: **one cheap layer against AI coding
mistakes — not the whole answer.** A narrow, fast, certain check that runs before
the write, not a system that makes your agent correct. Run it the way you run a
type checker — one layer that removes one class of mistake completely, alongside
the tests and review that catch the rest.

## Why RunEcho Exists

Coding agents are useful, but they routinely make three kinds of mistakes:

- they refer to functions or types that do not exist
- they describe structural changes inaccurately
- they keep reasoning from stale repo state after the code has moved on

RunEcho exists to give those agents a **local source of truth** they can query
before they speak, edit, or commit.

Use it when you want:

- a deterministic answer to "does this symbol actually exist?"
- a structural diff instead of a vague summary of what changed
- a guard that catches invented helper calls before they land in your repo

If your main problem is broad semantic search or general codebase exploration,
RunEcho is not trying to be that. Its job is narrower: **verify repo facts and
reduce hallucinated code changes**.

## How It Works

RunEcho parses your source into a compact **Intermediate Representation (IR)**:
per file: its content hash plus the functions, classes, exports, and imports it
declares. The IR has a deterministic root hash, so "did the structure change?"
becomes a cheap hash comparison, and "what changed?" becomes a structural diff.

Snapshots of that IR are stored in a single central history database. Each
enrolled repo has a stable identity, so the oracle can answer questions about any
of your repos and compute drift between any two snapshots.

Three binaries make up the surface area:

- **`runecho-ir`** — a CLI to enrol repos, index them, take snapshots, and inspect
  diffs and churn from the terminal.
- **`runecho-mcp`** — a stdio MCP server that exposes read-only oracle tools
  (`structure`, `diff`, `hash`, `status`, `health`, `locate`) to an AI agent.
  `locate` answers "where is symbol X" deterministically (name → file:line), so
  an agent finds definitions without grepping or guessing.
- **`runecho-guard`** — a guard that checks new code against the indexed IR and
  flags references to symbols that don't exist (likely hallucinations). Runs as a
  git pre-commit hook, or as a Claude Code `PreToolUse` hook that vets every
  `Edit`/`Write`/`MultiEdit` before it lands.

```
source ──▶ parser ──▶ IR (hashed) ──▶ snapshot ──▶ ~/.runecho/history.db
                                          │
                  AI agent ──(MCP)──▶ runecho-mcp ──▶ structure / diff / hash / ...
                                          │
        git commit / agent edit ──▶ runecho-guard ──▶ "symbol X doesn't exist — block/ask"
```

## Prerequisites

- **Nothing** to run a tagged release — the [prebuilt binaries](#quick-start) are
  self-contained (no runtime, no API keys).
- **Go 1.25+** only if you build from source (`bash install.sh`).
- A POSIX or Windows shell. Storage lives under `~/.runecho/` by default.
- No external services, no API keys.

Languages parsed today: **Go, JavaScript, TypeScript, JSX, TSX, Google Apps
Script (`.gs`), Python, shell (`.sh`/`.bash`), Rust (`.rs`), and Ruby (`.rb`)**.
Extraction is intentionally shallow and deterministic: top-level structure, not
full semantic analysis.

## Quick Start

1. Get the binaries. Either **download a prebuilt release** (no Go needed) — pick
   your OS/arch from the [latest release](https://github.com/inth3shadows/runecho/releases/latest):
   ```bash
   # example: macOS arm64 — adjust the asset name for your platform.
   # NOTE: the tag in the URL path is v-prefixed; the asset filename is not.
   TAG=v0.16.0; NUM=0.16.0
   curl -sSL "https://github.com/inth3shadows/runecho/releases/download/${TAG}/runecho_${NUM}_darwin_arm64.tar.gz" | tar -xz
   install -m755 runecho-ir runecho-mcp runecho-guard ~/.local/bin/
   ```
   …or **build from source** (needs Go 1.25+), which also installs the guard hooks:
   ```bash
   bash install.sh
   ```
2. Enrol a repo and capture its current structure:
   ```bash
   runecho-ir repo add /path/to/your/repo
   runecho-ir repo reindex <name>     # name is shown by `repo add`
   ```
   If the directory you want to enrol is not the directory you want parsed, set
   a separate source root:
   ```bash
   runecho-ir repo add /path/to/worktree --source-root=/path/to/source
   ```
3. See what's enrolled and ask for drift since the last snapshot:
   ```bash
   runecho-ir repo list
   runecho-ir diff --since=reindex /path/to/your/repo
   ```
4. Register the oracle with your AI agent so it can query directly:
   ```bash
   claude mcp add runecho -- ~/.local/bin/runecho-mcp
   ```
   For Codex, add this to `~/.codex/config.toml`:
   ```toml
   [mcp_servers.runecho]
   command = "/home/YOUR_USER/.local/bin/runecho-mcp"  # absolute path; TOML does not expand ~
   ```
5. Install the edit-time guard in Claude Code — the primary integration if you
   want RunEcho to vet assistant edits before they are written:
   ```
   /plugin marketplace add inth3shadows/runecho
   /plugin install runecho-guard@runecho
   ```
   The plugin wires the `PreToolUse` hook; it does **not** ship the binary, so
   step 1 still has to have happened. If the binary is missing the hook defers
   silently rather than erroring on every edit. Uninstall with
   `/plugin uninstall runecho-guard@runecho`.

   Without plugin support, print the equivalent `~/.claude/settings.json`
   snippet and merge it by hand:
   ```bash
   bash install.sh --print-hook-config
   ```
6. (Optional) Install the commit-time guard in a repo you've enrolled:
   ```bash
   bash install.sh --hook        # run from the target repo's root
   ```
   It blocks commits that call functions which exist nowhere in the indexed code
   (with a "did you mean …?" suggestion when there's a close match). Bypass any
   single commit with `RUNECHO_GUARD_SKIP=1 git commit …`.
7. (Maintainers/forks only) If you cut release tags from this repo, install the
   tag-monotonicity safety net:
   ```bash
   bash install.sh --hook-pre-push
   ```
   Rejects a `vX.Y.Z` tag push that isn't semver-greater than the highest
   existing tag — see [issue #51](https://github.com/inth3shadows/runecho/issues/51).

## Current Boundaries

RunEcho is strongest when you want **deterministic structure and guardrails**,
not general-purpose code intelligence.

- It tracks top-level symbols and imports/exports, not full type information.
- Parsers are AST-based but intentionally shallow — they extract *definitions*
  (functions, classes, methods), not semantics: no type inference, call graph,
  or cross-file binding. Go uses the stdlib `go/ast`; Python, JS/TS, Rust, and
  Ruby use a pure-Go tree-sitter runtime; shell uses a masking scan. Imports and
  exports for the tree-sitter languages are still regex. Each language has known
  gaps — see the [Parser Capability Matrix](TECHNICAL.md#parser-capability-matrix)
  for the per-language honest accounting.
- **Indexing covers more languages than the guard checks.** Shell, Rust, and
  Ruby feed the index (`structure`, `locate`, `diff`) but are not validated at
  edit time — the guard's reference checks exist for Go, JS/TS, and Python only.
- The guard validates **unqualified** references: bare **calls** (`foo(...)`),
  bare **type annotations** (`x: SomeType`), and SCREAMING_SNAKE **constant**
  references. It does **not** flag **qualified** references (`obj.method(...)`,
  `pkg.Thing`, `x.attr`) — those would need receiver-type resolution, semantic
  analysis RunEcho deliberately avoids. This boundary is measured, not asserted:
  see [`bench/FINDINGS.md`](bench/FINDINGS.md), where a corpus of real
  transcript-observed hallucinations places the guard's catch-rate by reference
  position (the qualified positions are the deliberate gap).
- Snapshots, diffs, and hash queries are local and deterministic. There is no
  semantic search, embedding index, or hosted control plane here.
- The guard runs unattended on every commit/edit with no sandboxing — see
  [SECURITY.md](SECURITY.md) for the threat model, what's stored, and how to
  report a vulnerability.

## Project Structure

| Path | Purpose |
|---|---|
| `cmd/runecho-ir/` | The CLI: index, snapshot, diff, map, log, churn, verify, truth-trail, validate-claims, contract, guard-stats, fpreport, repo, backup, install |
| `cmd/runecho-mcp/` | The stdio MCP oracle server |
| `cmd/runecho-guard/` | The guard: pre-commit mode + Claude Code hook mode, plus the opt-in checks |
| `internal/parser/` | Per-language structure extraction (Go/JS/TS/JSX/TSX/.gs/Python/shell/Rust/Ruby) |
| `internal/ir/` | IR build, deterministic hashing, JSON storage |
| `internal/snapshot/` | Central store: migrations, registry, diff, churn, contracts, backup |
| `internal/mcp/` | Minimal MCP plumbing + the oracle tools |
| `internal/guard/` | Diff parsing, symbol extraction, validation, did-you-mean |
| `internal/contract/` | Edit-scope contract format and parsing |
| `internal/depindex/` | Memoized export sets for Go dependencies (qualified-call checks) |
| `internal/guardstats/` | `guard-stats` and `fpreport` analysis over `decisions.jsonl` |
| `internal/claims/` | Symbol-reference extraction from prose (`validate-claims`, `truth-trail --text`) |
| `internal/gitutil/` | Canonical git-common-dir resolution (worktree identity) |
| `install.sh` | Builds all three binaries; `--hook` installs the pre-commit guard |

## Related Documentation

- [Technical Reference](TECHNICAL.md) — architecture, storage schema, the IR, the MCP tools, maintenance
- [Usage Guide](USAGE.md) — day-to-day operations: enrolling repos, integrations, reading drift, troubleshooting
- [Token cost](bench/TOKEN-COST.md) — measured context cost of every surface, including where RunEcho is expensive
- [Changelog](CHANGELOG.md) — notable changes per release; versioning policy

## License

MIT — see [LICENSE](LICENSE).
