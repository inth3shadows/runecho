# Usage Guide: RunEcho

## What This Does

RunEcho keeps an accurate, up-to-date map of the code in your repositories and
remembers what that map looked like at past points in time. With it, an AI coding
assistant can check "does this function really exist?" and "what actually changed
since I last looked?" against facts instead of memory.

You interact with it three ways: the `runecho-ir` command in your terminal,
automatically through your AI assistant (once registered), and — if you install
the guard — automatically at commit time or whenever the assistant edits a file.

## Install RunEcho

RunEcho builds three local binaries:

- `runecho-ir` — terminal CLI
- `runecho-mcp` — MCP server for assistants
- `runecho-guard` — edit-time / commit-time guard

From the repo root:

```bash
bash install.sh
```

By default that installs the binaries to:

```text
~/.local/bin
```

If that directory is not already on your `PATH`, add it:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

To make that permanent, add the same line to your shell profile (`~/.bashrc`,
`~/.zshrc`, etc.).

Quick sanity check:

```bash
runecho-ir repo list
```

If nothing is enrolled yet, that is fine. You should see:

```text
No repos enrolled. Add one: runecho-ir repo add <path>
```

## First-Time Setup For a Repo

The fastest path from zero to useful is:

```bash
cd /path/to/your/repo
repo_name="$(basename "$PWD")"
runecho-ir repo add "$PWD" --name "$repo_name"
runecho-ir repo reindex "$repo_name"
runecho-ir repo list
```

That does three things:

1. enrols the repo in RunEcho's central store
2. captures the first structural snapshot
3. confirms the repo is now tracked

If the directory you want to enrol is not the directory you want parsed, use a
separate source root:

```bash
runecho-ir repo add /path/to/worktree --name myproject --source-root=/path/to/source
runecho-ir repo reindex myproject
```

## Connect It To Your Assistant

### Claude Code

Register the MCP server:

```bash
claude mcp add runecho -- ~/.local/bin/runecho-mcp
```

Then install the edit-time guard configuration:

```bash
bash install.sh --print-hook-config
```

That prints the exact `PreToolUse` snippet to paste into:

```text
~/.claude/settings.json
```

After both steps, Claude Code can:

- query RunEcho for live structure, diff, hash, and status
- ask for confirmation when an edit references symbols that do not exist

### Codex

If you want a manual edit, add this block to:

```text
~/.codex/config.toml
```

```toml
[mcp_servers.runecho]
command = "/home/YOUR_USER/.local/bin/runecho-mcp"
```

Replace `YOUR_USER` with your actual username, or point to wherever you set
`RUNECHO_BIN_DIR`.

If you want to append the default configuration automatically:

```bash
printf '\n[mcp_servers.runecho]\ncommand = "%s"\n' "${RUNECHO_BIN_DIR:-$HOME/.local/bin}/runecho-mcp" >> ~/.codex/config.toml
```

## Daily Workflow

### See what is tracked

```bash
runecho-ir repo list
```

Each row shows the repo name, last indexed time, parse errors, file cap, code
coverage for supported languages, and its enrolled path.

### See what changed

From inside a repo, compare the live code to the last snapshot:

```bash
runecho-ir diff --since=reindex
```

`reindex` is the label that `repo reindex` writes automatically — you can also use
any label you created with `snapshot --label=<name>`, such as `session-start`.
Empty diff output means nothing structural changed. Otherwise you get a per-file
list of added and removed functions, classes, exports, and imports.

### Capture a new baseline snapshot

```bash
runecho-ir repo reindex myproject
```

Run this after meaningful work if you want later diffs to compare against the
new state instead of the old one.

### Inspect recent history

```bash
runecho-ir log
runecho-ir churn
```

Use `log` to see recent snapshots. Use `churn` to see which files and symbols
have changed most often across recent snapshots.

### Validate claims in notes or PR text

```bash
runecho-ir validate-claims --text notes.md
```

Use this when you have prose that mentions functions, classes, or other symbols
and you want to check those references against the current IR.

### Capture a session-start snapshot

Before starting a long coding session, bookmark the current structure:

```bash
runecho-ir snapshot --label=session-start
```

This gives you a reference point that `verify` and `truth-trail` compare against
during and after the session.

### Check for structural drift from the session start

```bash
runecho-ir verify
```

Shows what functions, classes, exports, or imports changed since your
`session-start` snapshot. An empty diff body (just the header line) means the
structure is unchanged.

### Get a full change receipt before committing

```bash
runecho-ir truth-trail --since=session-start
```

Fuses four signals into one report: structural diff, callers of removed
symbols, file churn (how hot each changed file is), and — optionally — a prose
check for stale symbol references:

```bash
runecho-ir truth-trail --since=session-start --text=my-notes.md
```

The `--text` variant exits with a non-zero code if the prose mentions symbols
that no longer exist in the current IR.

### Back up the history database

```bash
runecho-ir backup
```

That writes an atomic backup of the central SQLite store.

## Install the Commit-Time Guard

If you want protection at `git commit` time as well, run this from the target
repo's root:

```bash
bash install.sh --hook        # run from the target repo's root
```

From then on, a commit that calls a nonexistent function is blocked with a
`file:line` report and, when there's a near match, a "did you mean …?" hint.
Common situations:

- **It flagged something real** (a dynamic or generated symbol) — add that name
  on its own line to `.runechoguardignore` at the repo root, or refresh the map
  with `runecho-ir repo reindex <name>`.
- **You need this one commit through right now** —
  `RUNECHO_GUARD_SKIP=1 git commit …`.
- **It warns the index is stale** — run `runecho-ir repo reindex <name>`; the
  guard won't judge against facts older than a day by default.
- **You want hard guarantees** — set `RUNECHO_GUARD_STRICT=1` (e.g. in
  `.envrc` or your shell profile). In pre-commit mode, degraded states that
  would normally warn-and-pass (store unreachable, no snapshot yet, schema
  mismatch, oversized diff truncation) instead exit 1 so the commit is blocked.
  In hook mode, those same degraded states emit an advisory note instead of
  silently deferring. Repos that have never been enrolled are still skipped
  silently — strict only tightens the behaviour for enrolled repos where the
  guard cannot reach the store or a snapshot.

The same validation core also powers the Claude Code edit-time hook. See
[TECHNICAL.md](TECHNICAL.md#the-guard-runecho-guard) for the exact hook
behavior.

## What to Do When Something Breaks

- **`runecho-ir: command not found`** — add `~/.local/bin` to your `PATH`, or
  set `RUNECHO_BIN_DIR` before running `bash install.sh`.
- **`install.sh: ERROR: Go toolchain not found`** — install Go 1.24+ first, then
  rerun `bash install.sh`.
- **"repo … is not enrolled"** — run `runecho-ir repo add <path>` first, then
  `repo reindex <name>`.
- **`diff` says nothing changed but you know it did** — you probably need a fresh
  reference point. Run `runecho-ir repo reindex <name>` and compare again.
- **The assistant can't reach the oracle** — confirm it's registered: for Claude
  Code run `claude mcp list` and look for `runecho` marked Connected. Re-register
  with the command the installer printed if it's missing.
- **A repo shows unexpected file counts** — RunEcho only understands Go,
  JavaScript/TypeScript/JSX/TSX, and Python; files in other languages are not counted.
- **You want to start a repo's history over** — `runecho-ir repo rm <name>`
  removes it and its history, then `repo add` + `repo reindex` gives a clean start.
- **A commit is blocked and you disagree with the guard** — ignore-list the
  symbol, reindex, or bypass once with `RUNECHO_GUARD_SKIP=1`.
- **You use worktrees or a bare-repo layout** — enrol with `--source-root` so
  RunEcho knows which directory to parse.

For anything not covered here, see the [Technical Reference](TECHNICAL.md).

## Exit Codes (for scripting)

Every `runecho-ir` subcommand returns one of three exit codes:

| Code | Meaning | Examples |
|------|---------|---------|
| `0` | Success — clean run, no notable findings | Diff with no drift; verify matches; truth-trail with no stale claims |
| `1` | No-data / soft condition | Repo not enrolled; no matching snapshot; **stale or invented symbol references found** by `truth-trail --text` or `validate-claims` |
| `2` | Hard error | Bad arguments; I/O failure; database error |

Important: exit `1` from `validate-claims` or `truth-trail --text` means the check
**ran and found a problem** — it is not a harmless no-op. Do not use `cmd || true`
around those commands or you will silently swallow real hallucination findings.

For pure no-data cases (not enrolled, no snapshot), `1` means "skip gracefully."
To treat both gracefully: `code=$?; [ $code -le 1 ] && proceed`.

## FAQ

**Does this send my code anywhere?**
No. RunEcho runs entirely on your machine. There is no network call, no API key,
and no model involved.

**Where is everything stored?**
In a single database at `~/.runecho/history.db`. Back it up any time with
`runecho-ir backup`.

**Will it slow down my assistant?**
Queries build a fresh structural map of the repo, which is fast for normal
projects and always reflects the current code rather than a stale cache.

**Do I have to reindex constantly?**
Only when you want a new reference point to compare against. The assistant's live
structure/hash queries are always current regardless of when you last reindexed.

**What kinds of mistakes does the guard actually catch?**
Mostly the common ones: invented helper functions, misspelled local calls, and
stale references to symbols that no longer exist. It is not a full static
analyzer or type checker.
