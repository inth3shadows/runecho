# Technical Reference: RunEcho

## Architecture

RunEcho is a small Go program built around one idea: a **deterministic structural
fact table** for source code, plus durable history of it, queryable by humans
(CLI) and by AI agents (MCP).

```
                 ┌─────────────┐
  source files ─▶│  parser     │  per-language: imports/functions/classes/exports
                 └──────┬──────┘
                        ▼
                 ┌─────────────┐
                 │  ir         │  IR{Version, RootHash, Files{path: {Hash, ...}}}
                 │  (hashed)   │  deterministic SHA-256 root hash
                 └──────┬──────┘
                        ▼
                 ┌─────────────────────────────┐
                 │  snapshot (central store)   │  ~/.runecho/history.db (SQLite/WAL)
                 │  repos · snapshots · files  │  versioned schema, integrity-checked
                 │  · symbols                  │
                 └──────┬───────────────┬───────────────┬──────┘
                        │               │               │
           runecho-ir (CLI)   runecho-mcp (stdio MCP)   runecho-guard
                                                        (pre-commit / PreToolUse hook)
```

Key design decisions:

- **Determinism over fuzziness.** No embeddings, no similarity. The IR is a flat,
  sorted, hashable fact table. "Same code → byte-identical IR" is a tested
  guarantee, which is what lets an agent trust the answer.
- **One central store, not per-repo DBs.** A single `~/.runecho/history.db` holds
  every enrolled repo's history. One integrity boundary, one backup, atomic
  cross-repo queries.
- **Stable repo identity.** Snapshots carry a `repo_id` foreign key to a `repos`
  table, so a repo keeps its identity (and history) even if its path moves. Reads
  scope by `repo_id`, never by the volatile root-path string.
- **The oracle never answers from a cache.** `runecho-mcp` and the CLI's
  `snapshot`/`diff`/`verify` build a *fresh* IR on every call. `.ai/ir.json` is
  only an incremental working artifact maintained by `runecho-ir index`.

## File Descriptions

| Path | Role | Depends on |
|---|---|---|
| `internal/parser/{go,js,python}.go` | Extract top-level structure per language via the `Parser` interface | — (leaf) |
| `internal/ir/generator.go` | Walk a tree, parse files, build IR; `Generate` (full) and `Update` (incremental, hash-gated) | `parser` |
| `internal/ir/hasher.go` | `HashFile`, `HashBytes`, `ComputeRootHash` (sorted `path:hash` pairs → SHA-256) | — |
| `internal/ir/storage.go` | Canonical JSON marshal (sorted) + `Save`/`Load` of `.ai/ir.json` | — |
| `internal/snapshot/db.go` | `Open` (pragmas, `quick_check`, migrations), versioned `migrate`, `Health`, `BackupTo` | `ir` |
| `internal/snapshot/registry.go` | `repos` table CRUD: `EnrollRepo`, `GetRepoBy*`, `ListRepos`, `TouchRepo`, `PurgeRepo` | — |
| `internal/snapshot/snapshot.go` | `SaveSnapshot`, `List`, `GetByID`, `GetLatestByLabel` (all repo-scoped) | `ir` |
| `internal/snapshot/diff.go` | `Diff`, `DiffLive`, formatters | — |
| `internal/snapshot/churn.go` | `Churn` over the last N snapshots | — |
| `internal/mcp/server.go` | Minimal stdio JSON-RPC 2.0 MCP server | — |
| `internal/mcp/tools_oracle.go` | The five oracle tools, wired to `ir` + `snapshot` | `ir`, `snapshot` |
| `internal/guard/diff.go` | Parse `git diff --cached --unified=0` into added lines | — |
| `internal/guard/extract.go` | Per-language definition/reference/import extraction + builtin sets | — |
| `internal/guard/validate.go` | Two-pass validation: collect new defs, then flag unresolved refs | — |
| `internal/guard/suggest.go` | Deterministic "did you mean" via Levenshtein (distance ≤ 2) | — |
| `internal/claims/claims.go` | Extract code-symbol references from prose for `validate-claims` | — |
| `internal/gitutil/gitutil.go` | Canonical git-common-dir resolution — the V4 repo-lookup key | — |
| `internal/store/dir.go` | Single source of truth for `$RUNECHO_HOME` / `~/.runecho` | — |
| `cmd/runecho-ir/main.go` | CLI entrypoint and subcommand dispatch | `ir`, `snapshot` |
| `cmd/runecho-mcp/main.go` | Opens the store, registers the oracle, serves stdio | `mcp`, `snapshot` |
| `cmd/runecho-guard/main.go` | Guard entrypoint: pre-commit mode + `--hook-mode`, 3-tier repo resolution | `guard`, `snapshot`, `gitutil` |

## The MCP Oracle Tools

All tools are read-only and resolve a repo by its enrolled **name**. The server
speaks newline-delimited JSON-RPC 2.0 (`initialize`, `tools/list`, `tools/call`).

| Tool | Args | Returns |
|---|---|---|
| `structure` | `repo` | Files + symbols of the live IR, with counts; per-file `refs` answer "who calls X" |
| `diff` | `repo`, optional `a`+`b` (snapshot ids) or `since` (label) + `session` | Structural drift; default is latest snapshot vs live |
| `hash` | `repo` | Deterministic root hash + file count |
| `status` | `repo` | last-indexed, staleness, parse errors, coverage %, snapshot count, latest stored hash, file cap |
| `health` | — | Schema version, live integrity check, repo count, db path |

A `diff` with explicit `a`/`b` rejects snapshot ids that belong to a different
repo — diffs never cross repo boundaries.

## The Guard (`runecho-guard`)

The guard validates *new* code against the enrolled repo's indexed symbols and
flags bare function calls that resolve to nothing — the signature shape of a
hallucinated API. Two modes share the same validation core:

- **Pre-commit mode** (default; installed by `install.sh --hook`). Reads
  `git diff --cached --unified=0`, validates added lines, and exits 1 with a
  `file:line: symbol (did you mean "X"?)` report if violations are found.
- **Hook mode** (`--hook-mode`). A Claude Code `PreToolUse` hook for
  `Edit|Write|MultiEdit`. Reads the tool-call JSON on stdin, validates the new
  content, and answers via the `hookSpecificOutput` contract: unresolved symbols
  → `permissionDecision: "ask"` with the violation list as the reason. The guard
  never auto-approves — on a clean check it emits nothing and defers to the
  normal permission flow.

Validation is a two-pass static check: first collect every definition the change
itself introduces (plus the on-disk file's own definitions and imports, so local
helpers don't false-positive), then flag bare calls — `Foo(`, never `pkg.Foo(` —
that appear in neither the IR, the new definitions, the per-language builtin
sets, nor `.runechoguardignore` (one literal symbol per line, `#` comments, at
the repo root).

**Fail-open by design.** Not installed, repo not enrolled, no snapshot, DB
error, or a hung git subprocess (3s cap) all degrade to silence — the guard
blocks hallucinations; it must never block work. Repo resolution is three-tier:
git-common-dir key (O(1), schema V4) → enrolled-path lookup → worktree-list
scan, backfilling `common_dir` on a hit so the next fire takes the fast path.

Residual false positives are intrinsic to regex-level static analysis
(dynamically-assigned callables, locals): measured ~0% for Go, ~0.5% for JS,
~5% for Python — which is why hook mode asks instead of denying.

## Storage Schema

SQLite at `~/.runecho/history.db` (override dir with `RUNECHO_HOME`). Schema
version is tracked in `PRAGMA user_version`; migrations run in order inside
transactions on `Open`, so an interrupted upgrade can never leave a torn schema.

- `repos(id, name UNIQUE, path UNIQUE, source_root, common_dir, file_cap, enrolled_at, last_indexed, parse_errors, supported_seen)`
  — `common_dir` is the git-common-dir, a stable identity shared by every
  worktree of a repo; the guard keys lookup on it so bare-repo worktrees resolve
  in O(1) instead of scanning `git worktree list`.
- `snapshots(id, repo_id → repos, session_id, label, timestamp, root, root_hash)`
- `files(id, snapshot_id → snapshots, path, content_hash)`
- `symbols(id, file_id → files, name, kind)`
- `refs(id, file_id → files, name UNIQUE per file)` — bare call sites per snapshot file (IR v2).
  Kept separate from `symbols` on purpose: refs are derived *usage* facts, not
  declared structure, so they never widen the guard's known-symbol set or add
  noise to structural diffs. Extraction is shared with the guard
  (`guard.ExtractRefs`), so index-time facts and edit-time validation can
  never disagree about what counts as a call.

WAL is enabled; the connection pool is capped to a single connection, so writes
and reads are serialized — there are no torn reads (verified by a `-race`
concurrency test). `Open` runs `PRAGMA quick_check` and refuses a corrupt or
newer-than-supported database.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `RUNECHO_HOME` | `~/.runecho` | Directory for `history.db` and backups (isolation / testing seam) |
| `RUNECHO_BIN_DIR` | `~/.local/bin` | Install target used by `install.sh` |
| `RUNECHO_GUARD_SKIP` | — | Set to `1` to bypass the guard entirely (both modes), e.g. `RUNECHO_GUARD_SKIP=1 git commit …` |
| `RUNECHO_GUARD_MAX_AGE` | `24h` | IR staleness threshold (Go duration). Past it, pre-commit warns and hook mode attaches an advisory instead of judging against stale facts |

## Deployment

This is a local developer tool, not a service.

```bash
bash install.sh                              # build all three binaries → $RUNECHO_BIN_DIR
claude mcp add runecho -- ~/.local/bin/runecho-mcp   # register with Claude Code
# Codex: add [mcp_servers.runecho] command = "~/.local/bin/runecho-mcp" to ~/.codex/config.toml
bash install.sh --hook                       # from a target repo's root: install the pre-commit guard
```

Rollback: `claude mcp remove runecho`, delete the Codex block, and remove the
binaries from `$RUNECHO_BIN_DIR`. The store at `~/.runecho/` is untouched by
uninstall and can be deleted separately.

## Maintenance Commands

```bash
go build ./... && go vet ./... && go test ./...   # full verification
go test -race ./internal/snapshot/                # concurrency safety
go test -run=x -fuzz=FuzzJSParser ./internal/parser   # parser fuzzing
runecho-ir backup [dest.db]                       # atomic VACUUM INTO backup
runecho-ir repo list                              # enrolled repos + index state
```

## Known Limitations

- **Languages:** Go, JS/TS, Python only. Parsers are regex/AST-shallow — top-level
  symbols, not nested scopes.
- **File cap is enforced.** `repo add --cap N` stops indexing after N files (the
  walk continues counting supported files, so the coverage denominator stays
  honest). The root hash reflects only the capped file set — truncation changes
  the hash compared to an uncapped run of the same repo. Coverage % — indexed
  files over supported files seen by the last walk — is reported by `status`
  and `repo list`.
- **Single-connection store.** Correct and torn-read-free, but reads do not run
  concurrently with writes. Fine at single-operator scale; not built for many
  concurrent indexers.
- **The guard checks bare calls only.** Qualified calls (`pkg.Foo()`,
  `obj.method()`) are assumed external and skipped; dynamically-assigned
  callables can't be resolved statically. It catches the common hallucination
  shape, not every possible one.
