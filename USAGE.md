# Usage Guide: RunEcho

## What This Does

RunEcho keeps an accurate, up-to-date map of the code in your repositories and
remembers what that map looked like at past points in time. With it, an AI coding
assistant can check "does this function really exist?" and "what actually changed
since I last looked?" against facts instead of memory.

You interact with it two ways: the `runecho-ir` command in your terminal, and
(once registered) automatically through your AI assistant.

## How to Use It

### Enrol a repository

Tell RunEcho about a repo once. It picks a name from the folder path (you can
override it).

```
runecho-ir repo add /path/to/repo
runecho-ir repo add /path/to/repo --name myproject
```

### Capture its current structure

"Reindexing" reads the repo fresh and records a new snapshot of its structure.
Do this whenever you want a new reference point.

```
runecho-ir repo reindex myproject
```

### See what's tracked

```
runecho-ir repo list
```

Each row shows when the repo was last indexed, how many parse errors were seen,
and where it lives on disk.

### See what changed

From inside a repo, compare the live code to the last snapshot:

```
runecho-ir diff --since=reindex
```

Empty output means nothing structural changed. Otherwise you get a per-file list
of added and removed functions, classes, exports, and imports.

### Other everyday commands

```
runecho-ir log              # recent snapshots for this repo
runecho-ir churn            # which files/symbols change most often
runecho-ir backup           # save a safe copy of the history database
```

### Let your AI assistant use it

After registering RunEcho with your assistant (the installer prints the command),
the assistant can ask the oracle directly for a repo's structure or drift — no
action needed from you. It just gets more accurate.

## What to Do When Something Breaks

- **"repo … is not enrolled"** — run `runecho-ir repo add <path>` first, then
  `repo reindex <name>`.
- **`diff` says nothing changed but you know it did** — you probably need a fresh
  reference point. Run `runecho-ir repo reindex <name>` and compare again.
- **The assistant can't reach the oracle** — confirm it's registered: for Claude
  Code run `claude mcp list` and look for `runecho` marked Connected. Re-register
  with the command the installer printed if it's missing.
- **A repo shows unexpected file counts** — RunEcho only understands Go,
  JavaScript/TypeScript, and Python; files in other languages are not counted.
- **You want to start a repo's history over** — `runecho-ir repo rm <name>`
  removes it and its history, then `repo add` + `repo reindex` gives a clean start.

For anything not covered here, see the [Technical Reference](TECHNICAL.md).

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
