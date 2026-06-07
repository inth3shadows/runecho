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

## User-Facing Surfaces

RunEcho has three distinct surfaces. They share the same store and the same core
IR format, but they solve different problems:

| Surface | Primary user | Job |
|---|---|---|
| `runecho-ir` | Human operator | Enrol repos, capture snapshots, inspect drift/churn, validate claims, manage the store |
| `runecho-mcp` | AI agent / MCP host | Ask read-only structure, diff, hash, status, and health questions |
| `runecho-guard` | Human + AI agent | Stop or question edits that reference symbols outside known repo truth |

The intended operating model is: use `runecho-ir` to maintain the baseline, let
the MCP server answer live questions, and keep the guard close to edit time.

## Repo Identity and Source Roots

Each enrolled repo has three related identity values:

- `path`: the enrolled working-tree path
- `source_root`: the directory RunEcho should actually walk when building IR
- `common_dir`: the canonical git-common-dir used to recognize worktrees of the
  same repo quickly and consistently

Most repos use the same directory for `path` and `source_root`. `--source-root`
exists for nonstandard layouts, especially bare-repo or worktree setups where
the place you enrol is not the place you want parsed.

`repo reindex` already honors `EffectiveSourceRoot()`. Some compare-style CLI
commands still resolve live code from the caller's root path, so for unusual
layouts you should run them from the enrolled source tree until surface parity
is fully finished.

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

Residual false positives are intrinsic to shallow static analysis
(dynamically-assigned callables, locals): measured ~0% for Go, ~0.5% for JS,
~5% for Python across the 40-case guard test corpus — which is why hook mode
asks instead of denying.

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
| `RUNECHO_GUARD_STRICT` | — | Set to `1` for fail-closed behaviour: pre-commit exits 1 on degraded states (store unreachable, no snapshot, schema mismatch, oversized diff); hook mode emits an advisory instead of silently deferring. Unenrolled repos are always skipped silently regardless of this flag. |

### Decision log

Every guard decision (both modes) appends one JSON line to
`$RUNECHO_HOME/decisions.jsonl`:

```json
{"v":1,"ts":"2026-06-06T22:53:49Z","mode":"hook","repo":"runecho-master","file":"…","lang":"go","decision":"ask","reason":"violations","symbols":["FakeFn"]}
```

`decision` is `ask` or `defer`; `reason` classifies why (`clean`, `violations`,
`stale-ir`, `no-repo`, `store-degraded`, `schema-newer`, `unknown-lang`,
`bad-path`, `empty-input`, `parse-fail`). The write happens after the decision
is emitted and all logging errors are discarded — the log can never alter a
decision or slow the hook. It exists to measure the guard's real-world
ask/defer behaviour (and to feed future learned-allow analysis); delete the
file freely if you don't want the history.

## Deployment

This is a local developer tool, not a service.

```bash
bash install.sh                              # build all three binaries → $RUNECHO_BIN_DIR
claude mcp add runecho -- ~/.local/bin/runecho-mcp   # register with Claude Code
# Codex: add [mcp_servers.runecho] command = "/home/YOUR_USER/.local/bin/runecho-mcp" to ~/.codex/config.toml
#   (absolute path — TOML does not expand ~)
bash install.sh --print-hook-config          # print the Claude Code PreToolUse snippet
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

## Parser Capability Matrix

One parser per language family; all are shallow (line-regex, not AST). The table
is intentionally honest: gaps here are tracked issues, not silently accepted.

| Language | Extensions | Definitions captured | Exports semantics | Nested declarations | Depth |
|---|---|---|---|---|---|
| **Go** | `.go` | Top-level `func` and exported methods (→ Functions), `type` (→ Classes), exported `var`/`const` (→ Exports) | Exported `var`/`const` names only — exported funcs/types land in Functions/Classes, not Exports | Top-level only (no leading whitespace) | Shallow / line-regex |
| **JS/TS/JSX/TSX** | `.js`, `.ts`, `.jsx`, `.tsx`, `.gs` | `function` declarations, `const/let/var = function/arrow`, `class` declarations | `export { ... }`, `export const/let/var/function/class`, `export default <ident>` | Best-effort: named functions inside callback arguments are missed (leading `(` is not whitespace, so the regex anchor fails); local function expressions inside factory bodies are over-captured as top-level | Shallow / line-regex |
| **Python** | `.py` | `def` functions, `class` declarations | Names in `__all__` when present; empty otherwise (no fallback) | Top-level only (`def`/`class` at column 0) | Shallow / line-regex |

**Known gaps in the JS/TS/JSX parser** (evidence for the AST go/no-go decision, issue #15):

- `export default function Foo()` and `export default class Foo` — the
  `exportDefaultRegex` captures the keyword (`function` / `class`) as the
  export name instead of the identifier. The function/class itself is correctly
  captured in Functions/Classes via its own regex. Over-capture in Exports.
- `const Name: Type = (...) => …` — TypeScript-annotated arrow components are
  not captured in Functions. The `: Type` annotation between the variable name
  and `=` breaks `arrowFuncRegex`. Under-capture.
- `export * from './mod'` — star re-exports are not enumerable by regex; the
  re-exported symbol set is silently dropped. Under-capture in Exports.
- `export * as ns from './mod'` — namespace re-export not captured. Under-capture.
- `export { local as alias }` — the pre-alias local name is recorded, not the
  exported alias. Intentional (we index local definitions), but callers querying
  by published API name will not find it.
- Named callback functions (`setTimeout(function tick() {…})`) are **not**
  promoted to top-level — the `(?:^|\s)` anchor before `function` correctly
  excludes them. This is intentional under-capture (callbacks are not public
  definitions) but it means factory-internal named function expressions are also
  missed.

## Known Limitations

- **Languages:** Go, JS/TS/JSX/TSX, and Python only. Parsers are deliberately
  shallow — top-level symbols, not nested scopes or full semantic resolution.
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
- **Hashes are byte-level.** Line-ending differences (`LF` vs `CRLF`) change
  file hashes and therefore root hashes. Cross-machine determinism depends on
  consistent checkouts.
- **Some degraded guard states are intentionally fail-open.** Missing store,
  unenrolled repo, missing snapshots, and similar conditions degrade to silence
  or warnings rather than blocking work.
- **`--source-root` support is not fully uniform yet.** Reindex already
  respects it; some snapshot/compare flows still assume the caller's root path
  for live IR generation.
