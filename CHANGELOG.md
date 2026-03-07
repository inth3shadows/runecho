# Changelog

All notable changes. Naming convention: **F** = feature milestone, **M** = infrastructure milestone.
Commits follow conventional format: `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`.

Each entry: F/M label · commit SHA · binary (if new) · what changed and why.

---

## [Unreleased]

### F10 — Local Result Cache
**Prerequisites:** complete (F10-pre, commit `98ab949`)

**Cache key:** `(ir_hash, prompt_hash, model, task_id)` — `task_id` required to prevent cross-task collisions

**Deliverables:**
- SQLite cache table in `.ai/history.db` — no new dependency
- Cache read/write in `ai-context compile`; TTL invalidation on IR hash change
- `ai-context --no-cache` escape hatch

---

### F10-pre — VerifyEntry + Task schema prerequisites
**Commit:** `98ab949` | **No new binary**

Three schema changes required before F10 cache invalidation can work correctly:

| Change | Location | Why |
|---|---|---|
| `OutputHash string` | `internal/schema/results.go` | SHA-256 of full combined stdout+stderr before truncation; enables cache invalidation |
| `OutputPath string` | `internal/schema/results.go` | Relative path to full output sidecar; escapes 500-char truncation cap |
| `BlockedBy []string` | `internal/task/task.go` | Upgrades flat dep string to multi-dep slice; backward-compat `UnmarshalJSON` handles old format |

---

## [F9] — MCP Tool Server

**Commit:** `110eb64` | **Binary:** `ai-mcp-server` | **Total binaries:** 10

> **Commit label note:** This was committed as "F7 — MCP Tool Server" but the commit body correctly identifies it as "Enhancement #7." Canonical label is F9 per the unified F-series. Git commits cannot be rewritten; CHANGELOG is the source of truth.

`internal/mcp/` — JSON-RPC 2.0 stdio server exposes RunEcho state as Claude-native MCP tools. No external dependencies (~250 lines stdlib). Registered as `mcpServers.runecho` in `~/.claude/settings.json` by `install.sh`.

| Tool | Description |
|---|---|
| `task_list` | List all tasks with status |
| `task_next` | First unblocked non-done task |
| `task_update` | Update task status |
| `session_status` | Current session state |
| `fault_list` | Recent fault signals |
| `provenance_export` | Task provenance record |
| `context_compile` | Compiled session context block |

---

## [F8] — Infrastructure Hardening

**Commit:** `14c95ab` | **Total binaries:** 9 (no new binary)

> **Commit label note:** This was committed as "enhancement backlog items 1–6." Canonical label is F8 per the unified F-series.

Six cross-cutting improvements shipped as a single batch:

| Item | Description |
|---|---|
| Hook Reliability Gate | `HOOK_FAILURE` fault signal + `run_with_fault_guard()` in `fault-emitter.sh`; failures surface next turn via `pending-faults` |
| Hook Latency Telemetry | `HookEntry` schema (`hooks.jsonl`); `HOOK_SLOW`/`HOOK_FAILED` signals; timing on all 6 hooks |
| Classifier Route Caching | 64-entry LRU (30-min TTL) keyed on `PromptFingerprint` (SHA-256 of first 200 chars); `CacheHit` in classifier log |
| Skills Integration | 5 skills (`ai-review`, `ai-cost`, `ai-scope`, `ai-drift`, `ai-classify`) in `skills/*/SKILL.md`; `install.sh` symlinks to `~/.claude/skills/` |
| Context Window Pressure | `TokensUsed`/`WindowPressure` in `cost.go`; `WINDOW_PRESSURE` fault at 90% of 200k token limit |
| Fault-Driven Test Generation | `Stdout`/`Stderr` on `VerifyEntry`; `VerifyFailureProvider` injects `TEST_FAILURE_ADVISORY` into next session context on verify failure |

---

## [F7] — Session Provenance Export

**Commit:** `96c6103` | **Binary:** `ai-provenance` | **Total binaries:** 9

`internal/provenance/provenance.go` — pure consumer of all five `.ai/` JSONL data files. No new schema types. Assembles a complete execution record per task by joining progress, faults, results, hooks, and executions.

| Subcommand | Description |
|---|---|
| `ai-provenance export <task-id> [--json]` | Session timeline: turns, cost, IR hashes, drift flags, fault signals, verify outcomes |
| `ai-provenance list [--json]` | All tasks with recorded sessions; session count and total cost |

---

## [F6] — Schema Stabilization

**Commit:** `c14736f` | **Package:** `internal/schema/`

Canonical Go types for all five `.ai/` JSONL data files. Eliminated duplicate struct definitions. All consumers migrated.

| File | Types |
|---|---|
| `faults.jsonl` | `FaultEntry`, `DriftFaultEntry` |
| `progress.jsonl` | `ProgressEntry`, `ScopeDrift` |
| `results.jsonl` | `VerifyEntry` |
| `executions.jsonl` | `Envelope`, `StageResult` |
| `classifier.jsonl` | `ClassifierEntry` |

---

## [F5] — Drift-Aware Task Advisory

**Commit:** `f7ed698` | **Package:** `internal/task/drift.go`, `internal/session/drift_check.go`

IR snapshot diffs intersected with task scopes at session end. `DRIFT_AFFECTED` faults emitted to `.ai/faults.jsonl`. `ContractProvider` injects a `DRIFT ADVISORY` block into the SESSION CONTRACT at turn 1 when faults exist.

| Command | Description |
|---|---|
| `ai-task drift-check [--session=id]` | Intersect IR diff with task scopes; emit faults |
| `ai-task replan <id>` | Print task scope + IR diff + `DRIFT_AFFECTED` faults for review |

---

## [F4] — Migrate session-end.sh to Go (ai-session-end)

**Commit:** `f7ed698` | **Binary:** `ai-session-end` | **Total binaries:** 8

`internal/sessionend/end.go` — 7-stage session-end pipeline in typed, testable Go. `session-end.sh` reduced to a 4-line exec wrapper.

**Stages:** scope-drift detection → IR snapshot → pipeline envelope → task verify → IR verify summary → session synthesis → checkpoint fallback

> F4 and F5 shipped in the same commit.

---

## [F3] — Token Cost Compression + Context Relevance Scoring

**Commit:** `8f7f557` | **Package:** `internal/context/ir.go`

IDF-weighted scoring, import propagation, test-symbol filtering, compact flat dump. Zero schema changes.

| Change | Impact |
|---|---|
| IDF-weighted scorer | Rare terms score higher; common path segments score lower |
| Import propagation | +2 bonus for files imported by high-scoring callers |
| Test-symbol filter | `Test*`/`Benchmark*`/`Example*` stripped from display |
| Compact flat dump | ~64% size reduction in no-prompt mode |

---

## [F2] — Contract to Task Auto-Wire

**Commit:** `54398ad` | **Subcommand:** `ai-task sync [--quiet]`

Idempotent task creation from `.ai/CONTRACT.yaml`. Creates a task if no matching title exists. Callable from hooks or manually.

---

## [F1] — Extract internal/task Package

**Commit:** `8eb17ee` | **Package:** `internal/task/`

Extracted `Task`, `TaskDB`, `Load`, `Save`, `MaxID`, `SortByID` from `cmd/task/main.go`. Zero behavior change. Unblocked F2, F5, F7 shared type imports.

---

## [M1–M8] — Infrastructure Milestones

Core infrastructure established before F-series work.

| Label | Description |
|---|---|
| M1 | Session governor hook — turn counter, cost tracking, model routing directives |
| M2 | IR indexer and injector — codebase structure injected at session turn 1 |
| M3 | Model router — `RegexRoute` (deterministic) + optional haiku classifier |
| M4 | Handoff synthesis — `ai-session` parses JSONL log for ground-truth facts |
| M5 | Document generator — `ai-document` generates README/TECHNICAL/USAGE via haiku |
| M8 | Verify loop — task `verify` field + `VERIFY_FAIL` fault emission at session end |
