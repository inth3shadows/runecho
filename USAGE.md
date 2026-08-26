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

Then install the edit-time guard. The plugin is the supported path — it wires the
`PreToolUse` hook for you and can be uninstalled cleanly:

```
/plugin marketplace add inth3shadows/runecho
/plugin install runecho-guard@runecho
```

The plugin does not ship the binary, only the wiring, so `install.sh` (or a
release tarball) still has to have run. A missing binary makes the hook defer
silently rather than error on every edit.

If plugins are unavailable, print the equivalent snippet and merge it by hand:

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
list of added, removed, and modified (`~`) functions, classes, exports, and imports.

#### What the diff did NOT look at

"Nothing structural changed" is only as strong as what was indexed. RunEcho does
not parse every language, so a function deleted from a `.java`, `.c`, or `.php`
file used to produce a clean diff at exit 0 with empty stderr — indistinguishable
from a genuinely unchanged repo.

`diff --since` and `verify` now name what they declined:

```
IR DIFF  8f2a1c04... → b31d9e77...

No structural changes.

NOT EXAMINED — a symbol removed here would be invisible to this diff (1 path):
  Widget.java  [unsupported_language]
```

`--json` carries two arrays, and **the split is the contract**:

| Key | Contains | How to match | What to do |
|---|---|---|---|
| `skipped` | Everything it **could not read** | `p == e \|\| strings.HasPrefix(p, e+"/")` | Fail closed |
| `ignored_paths` | What **you configured away** | Same | Informational |

Reasons in `skipped`:

| Reason | Meaning |
|---|---|
| `unsupported_language` | Source code in a language no parser handles |
| `parse_error` | Supported extension, but the file could not be read or parsed |
| `oversized` | Larger than the per-file parse limit |
| `cap_reached` | The repo's file cap stopped indexing before this file |
| `symlink` | A symlinked source file; the walk does not follow symlinks |
| `unreadable_file` | A single file the walk could not stat, while its siblings were fine |
| `symlink_dir` | A symlinked directory; its subtree is unexamined under this path |
| `unreadable_dir` | A directory the walk could not read; its whole subtree went unvisited |

`ignored_paths` carries exactly one reason: `ignored_dir`. Directory entries in
either array name the directory, never its contents — the walk never descends,
and enumerating it would defeat the pruning.

Why two arrays and not one: `.git` is in the default ignore list, so a merged
list is non-empty in *every* repo. A consumer prefix-matching it would block an
edit to `testdata/README.md` — a documentation-only false block, which is exactly
what the language table exists to prevent, arriving through the other reason
code.

The split is **"did you ask for this?"**, not "file or directory". An ignore rule
is a decision you made. A permission error or an unfollowable link is a limit of
the tool — and a directory it could not read took its whole subtree with it,
recording none of the files inside, because it never saw them. That is a blind
spot, so it is in `skipped`.

Four rules for machine consumers:

* **Non-code is never in `skipped`.** A `README.md` or a `.png` is not a blind
  spot in a symbol index, so a gate can fail closed on a non-empty `skipped`
  without blocking documentation changes.
* **An absent `skipped` key means "not measured", not "nothing skipped".** Only
  `diff --since` and `verify` walk the filesystem; the two-snapshot form
  (`diff <id-a> <id-b>`) omits both keys entirely rather than claim a clean bill
  of health it never checked.
* **`skipped_truncated: true` means the list hit its cap**, so absence from it no
  longer implies "indexed". Treat it as "unknown".
* **`root_unreadable: true` means NOTHING was examined.** The repo root itself
  could not be read, so `skipped` is empty, `skipped_truncated` is false, and no
  file was indexed — every other signal reads clean. It is the one condition a
  gate cannot detect from the other two, so check it first.

The text output shows `skipped` only. `ignored_paths` is your own configuration
echoed back, and with `.git` always in it, printing it would put a block on every
diff of every repo — a note that fires every time stops being read. It is always
in `--json`.

### Locate symbols (repo map)

A deterministic "where is X" map of every indexed symbol — no LLM, no guessing.
Useful to hand an agent at the start of a task so it can find code without
grepping:

```bash
runecho-ir map                      # symbol → file:line  (sorted, with a short body hash)
runecho-ir map --by-file            # group symbols under their file
runecho-ir map --since=reindex      # only symbols added or modified since a snapshot
runecho-ir map --kind=class --dir=src/core
runecho-ir map --json               # machine shape (parity with diff --json)
```

Each row is `name  kind  file:line  hash`. The 4-char hash is the symbol's body
hash — the same value `diff` uses to detect in-place changes, so a consumer can
tell whether a symbol moved or actually changed. A `?` line means that file
hasn't been re-indexed since the symbol-line data was added (run `repo reindex`).
Lines come from Python's AST; regex-parsed languages show `?` until they gain
span support.

For agents, prefer the MCP `locate` tool over loading the whole map — it answers
a single "where is X" query without spending context on the full table:

```jsonc
// MCP tool call
{"name": "locate", "arguments": {"repo": "myproject", "symbol": "fetch"}}
// → matches Reader.fetch → src/reader.py:42 (exact, prefix, or last-segment match)
```

An unfiltered or broad query on a large repo is capped per call; the response
carries `next_offset` when more matches remain — pass it back as `offset` to
page through the rest, or narrow the query instead. Each call re-reads the
live repo (no cached snapshot between pages), so on a repo actively being
edited mid-session, results across pages aren't pinned to one point in
time — prefer narrowing the query over paging deep into a large, changing repo.

To prime a session without dumping the map, `runecho-ir map --header` prints a
<200-token summary (file/symbol counts, busiest directories, and a pointer to
`locate`) — suitable for a Claude Code SessionStart hook.

### Capture a new baseline snapshot

```bash
runecho-ir repo reindex myproject
```

Run this after meaningful work if you want later diffs to compare against the
new state instead of the old one.

> **Auto-fresh index:** the Claude Code PostToolUse hook
> (`runecho-guard --outcome-mode`) incrementally re-indexes just the file you
> touched into a rolling `auto` snapshot on each edit. The guard's next check
> sees symbols you added moments ago, so you no longer get a false "unknown
> symbol" prompt for a function defined earlier in the same session — and manual
> `repo reindex` becomes optional rather than a chore. Manual snapshots
> (`reindex`, `session-start`, …) are never touched by the auto-refresh.
>
> **Using the plugin?** It wires this hook for you — do not add a `PostToolUse`
> entry to `settings.json` as well.
>
> **Hand-rolled your `settings.json`?** Check the `PostToolUse` entry is present.
> **Earlier releases shipped no config that included it**, so an install from
> that era runs the guard without it, and the symptom is quiet: stale-IR false
> positives keep appearing, and `runecho-ir fpreport` reports every ask as
> unrated, because the approval rate is computed by joining asks to outcome
> records this hook is the only source of. Re-run
> `install.sh --print-hook-config` for the current snippet.
>
> **First edit in a repo can pause.** If the repo has no `.ai/ir.json` yet, this
> hook builds the whole index once before returning, which on a large tree can
> take several seconds (it gives up at 30). It is silent while it works. Every
> edit after that is incremental. Run `runecho-ir repo reindex <name>` once after
> enrolling to pay that cost up front instead.
>
> If you end up with both wired anyway it is handled, not fatal: Claude Code runs
> every matching hook, so the recorder fires twice, and the second fire sees the
> outcome the first one wrote and no-ops. That dedupe is what keeps a
> double-wired install from reporting an *inflated* approval rate instead of an
> empty one. It serializes the check with an advisory lock, which is best-effort
> by design — it never blocks an edit, so on a platform without file locking, or
> if the lock cannot be taken, a rare duplicate is still possible. Prefer wiring
> the hook once.

### Inspect recent history

```bash
runecho-ir log
runecho-ir churn
runecho-ir churn --json           # machine shape (parity with diff --json)
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

### Measure how often the guard is wrong

Every guard decision is logged to `~/.runecho/decisions.jsonl`. Three commands
read it back:

```bash
runecho-ir guard-stats            # ask/defer VOLUME: how loud is the guard, where
runecho-ir fpaudit                # was the guard RIGHT: judged against git history
runecho-ir fpreport               # ask approval rate: see the warning below first
```

**Start with `fpaudit`.** It asks two dated questions of git about every flagged
symbol — was it defined when the guard complained, is it defined now — and splits
the answer three ways:

| verdict | meaning | what to fix |
|---|---|---|
| `fp` | already defined at ask time | a resolver bug — the guard's cheap path missed it |
| `premature` | defined only afterwards | nothing in the resolver. The guard was **right**; it fired while the agent was writing a caller before its callee. Move the check later |
| `stands` | never came to resolve | the guard caught a real unbacked reference |

```bash
runecho-ir fpaudit --days 30             # trailing-30-day audit
runecho-ir fpaudit --gv v0.29.1 --json   # one build, machine-readable per-symbol verdicts
```

It is read-only — `rev-list`, `rev-parse` and `grep` against commits. Nothing is
checked out and nothing is written. Its oracle is regex-over-source, chosen to be
*independent* of the guard's own extractors rather than better than them, and it
reports what it could not answer rather than dropping it.

> **`fpreport`'s approval rate is not a false-positive rate.** It measures how
> often the agent proceeded anyway — which is only informative if approvals vary.
> On the author's 30-day log, joined to the Claude Code transcripts that record
> what actually happened, **308 ask-gated edits produced 308 approvals and 0
> denials**. A constant cannot rank anything, so the per-check and per-language
> spreads `fpreport` prints describe the join's failure modes, not the guard's
> precision. Read it for volume and for the shape of the log; read `fpaudit` for
> whether the guard was right.

`fpreport` joins each ask to its outcome — a symbol-exact match, not a time-window
guess — and reports the fraction the agent approved anyway, broken down by check
and language, with the most-approved symbols and the loudest repos:

```bash
runecho-ir fpreport --days 30            # trailing-30-day report
runecho-ir fpreport --gv v0.12.2         # only decisions written by one guard build
runecho-ir fpreport --json               # machine-readable
runecho-ir fpreport --max-rate 0.15      # exit non-zero if approval rate > 15%
```

**Read the guard-version breakdown before you read anything else.** Each decision
records the guard binary that wrote it, and a window wide enough to span two
installs pools their behaviour into one number that describes neither. That is not
hypothetical: measured on a real log, a 30-day window reported a **70%** approval
rate while the trailing 2 days reported **19%**, because the installed binary had
been six releases stale. The report says so out loud when it happens:

```
!! MIXED GUARD VERSIONS — the rate above pools 2 different builds.
```

Use `--gv <version>` to scope the report to a single build (`--gv unknown` selects
records written before version stamping existed). For the same reason, `--max-rate`
**refuses to evaluate** a mixed-version window and says so on stderr rather than
passing or failing on a pooled average.

**Not every ask can be rated, and the report says which.** The join is
symbol-exact, so an ask that carries no symbols has nothing to pair an outcome
with. The edit-scope contract check is the clear case: it fires on a *path*, not
on identifiers, so almost none of its asks are rateable. Those asks are counted
as **unrated** and appear in no rate on the page:

```
contract        3 ask   0 approved    0%  ! +13 unrated (rate covers 18%)
```

Read that as "this 0% describes 3 of the check's 16 asks" — and the 3 are the
ones that happened to co-fire with a symbol-bearing check, which is the opposite
of a random sample. A row marked `!` has under half its asks rated. A row with
*nothing* rateable prints no percentage at all:

```
contract        0 ask   no rate (no ask here carries a join key)  ! +13 unrated
```

The `ask` column is always the **rateable** count, on every row — the same
quantity the headline and `--max-rate` use. In `--json`, every bucket carries
`unrated`, `total` and `coverage` beside `rate`, and `rate` is `null` (not `0`)
wherever the text refuses to print one, so a dashboard plots a gap rather than a
claim. `asks` and `rate` keep their old meaning, so **`--max-rate` gates on
exactly what it always did** — including its mixed-version refusal, which counts
builds that contributed a rateable ask, not builds that merely appear.

The approval rate is an **upper bound** on the false-positive rate: an approved
ask is one the guard raised and the user waved through, but some of those
approvals are the user fixing the flagged symbol rather than dismissing a wrong
alarm. `--max-rate` makes it a CI or cron check — a rising rate means the guard
is interrupting more legitimate work. Its exit codes are chosen so CI can act on
each distinctly:

| exit | meaning | CI action |
|---|---|---|
| `0` | passed, or no gate requested | continue |
| `1` | no decision log yet (fresh checkout) | skip — do **not** fail |
| `2` | gate tripped, or a bad flag | fail the build |

The gate only evaluates with **≥20 asks** (below that a single approval swings the
rate too far); when it skips for that reason it says so on stderr rather than
passing silently. With `--json`, the result is also in a `gate` object, so a
`| jq` pipeline can read it without relying on the exit status.

Note the number only means anything once the guard actually prompts: if its
`PreToolUse` output is being discarded (e.g. a `1>/dev/null` in the hook command),
every edit lands regardless and the "approvals" are an artifact, not judgment.

## What to Do When Something Breaks

Not sure which of these applies? `runecho-ir doctor` checks binaries-on-PATH,
Claude Code hook wiring, git hooks, enrollment/staleness, store health, and
active `RUNECHO_GUARD_*` flags in one pass and names which is wrong — start
there before working through the list below.

- **`runecho-ir: command not found`** — add `~/.local/bin` to your `PATH`, or
  set `RUNECHO_BIN_DIR` before running `bash install.sh`.
- **`install.sh: ERROR: Go toolchain not found`** — install Go 1.25+ first, then
  rerun `bash install.sh`.
- **"repo … is not enrolled"** — run `runecho-ir repo add <path>` first, then
  `repo reindex <name>`.
- **`diff` says nothing changed but you know it did** — you probably need a fresh
  reference point. Run `runecho-ir repo reindex <name>` and compare again.
- **The assistant can't reach the oracle** — confirm it's registered: for Claude
  Code run `claude mcp list` and look for `runecho` marked Connected. Re-register
  with the command the installer printed if it's missing.
- **A repo shows unexpected file counts** — RunEcho only understands Go,
  JavaScript/TypeScript/JSX/TSX, Python, and shell (`.sh`/`.bash`); files in other
  languages are not counted.
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
