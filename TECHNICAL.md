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
                 └──────┬───────────────┬──────┘
                        │               │
           runecho-ir (CLI)      runecho-mcp (stdio MCP)
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
| `cmd/runecho-ir/main.go` | CLI entrypoint and subcommand dispatch | `ir`, `snapshot` |
| `cmd/runecho-mcp/main.go` | Opens the store, registers the oracle, serves stdio | `mcp`, `snapshot` |

## The MCP Oracle Tools

All tools are read-only and resolve a repo by its enrolled **name**. The server
speaks newline-delimited JSON-RPC 2.0 (`initialize`, `tools/list`, `tools/call`).

| Tool | Args | Returns |
|---|---|---|
| `structure` | `repo` | Files + symbols of the live IR, with counts |
| `diff` | `repo`, optional `a`+`b` (snapshot ids) or `since` (label) | Structural drift; default is latest snapshot vs live |
| `hash` | `repo` | Deterministic root hash + file count |
| `status` | `repo` | last-indexed, staleness, parse errors, snapshot count, latest stored hash, file cap |
| `health` | — | Schema version, live integrity check, repo count, db path |

A `diff` with explicit `a`/`b` rejects snapshot ids that belong to a different
repo — diffs never cross repo boundaries.

## Storage Schema

SQLite at `~/.runecho/history.db` (override dir with `RUNECHO_HOME`). Schema
version is tracked in `PRAGMA user_version`; migrations run in order inside
transactions on `Open`, so an interrupted upgrade can never leave a torn schema.

- `repos(id, name UNIQUE, path UNIQUE, file_cap, enrolled_at, last_indexed, parse_errors)`
- `snapshots(id, repo_id → repos, session_id, label, timestamp, root, root_hash)`
- `files(id, snapshot_id → snapshots, path, content_hash)`
- `symbols(id, file_id → files, name, kind)`

WAL is enabled; the connection pool is capped to a single connection, so writes
and reads are serialized — there are no torn reads (verified by a `-race`
concurrency test). `Open` runs `PRAGMA quick_check` and refuses a corrupt or
newer-than-supported database.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `RUNECHO_HOME` | `~/.runecho` | Directory for `history.db` and backups (isolation / testing seam) |
| `RUNECHO_BIN_DIR` | `~/.local/bin` | Install target used by `install.sh` |
| `RUNECHO_GUARD_SKIP` | — | Reserved for a future commit-guard bypass |

## Deployment

This is a local developer tool, not a service.

```bash
bash install.sh                              # build both binaries → $RUNECHO_BIN_DIR
claude mcp add runecho -- ~/.local/bin/runecho-mcp   # register with Claude Code
# Codex: add [mcp_servers.runecho] command = "~/.local/bin/runecho-mcp" to ~/.codex/config.toml
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
- **File cap is advisory.** `repo add --cap N` is recorded and a warning is logged
  when a repo exceeds it, but indexing is not yet truncated (enforcement needs a
  generator change). Coverage is reported honestly; it is never silently capped.
- **Single-connection store.** Correct and torn-read-free, but reads do not run
  concurrently with writes. Fine at single-operator scale; not built for many
  concurrent indexers.
- **`coverage %`** is not computed (the generator does not yet count skipped vs
  supported files); `status` reports indexed file count and parse errors instead.
