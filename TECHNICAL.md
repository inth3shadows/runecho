# Technical Architecture

## Overview

RunEcho is a Go-based session governance and model routing layer for Claude Code. It enforces cost-optimal model selection, injects codebase structure into session context, and produces durable execution records. Seven binaries, ten hooks, and one shared state directory compose a fully automated pipeline that fires on every Claude Code session without user intervention.

## Binary Inventory

| Binary | Package | Role |
|--------|---------|------|
| `ai-ir` | `cmd/ir` | Indexes source files → `.ai/ir.json`; manages SQLite snapshot history; structural diff |
| `ai-session` | `cmd/session` | Parses Claude Code JSONL log → ground-truth handoff (files, commands, cost, tokens) |
| `ai-document` | `cmd/document` | Generates/updates README.md, TECHNICAL.md, USAGE.md via haiku; change-gated |
| `ai-task` | `cmd/task` | Persistent task ledger (`.ai/tasks.json`); dependency graph; CONTRACT.yaml generation |
| `ai-context` | `cmd/context` | Compiles turn-1 context block within a token budget; Provider interface |
| `ai-governor` | `cmd/governor` | Session governor + model router; called from `session-governor.sh` |
| `ai-pipeline` | `cmd/pipeline` | Declarative pipeline render + execution envelope recording |

## Hook Chain

All hooks are bash scripts in `hooks/`, symlinked to `~/.claude/hooks/`.

| Hook | Event | Timeout | Role |
|------|-------|---------|------|
| `session-governor.sh` | UserPromptSubmit | 5s | Exec wrapper → `ai-governor`; injects turn/cost/routing text |
| `ir-injector.sh` | UserPromptSubmit | 5s | Runs `ai-ir`; calls `ai-context` for full turn-1 context block |
| `model-enforcer.sh` | PreToolUse[Task] | 5s | Denies Task tool calls using wrong model vs. route decision |
| `destructive-bash-guard.sh` | PreToolUse[Bash] | 3s | Hard-denies `rm -rf /`, fork-bombs, `mkfs`; approval-gates `rm -rf`, DDL drops |
| `scope-guard.sh` | PreToolUse[Edit\|Write] | 3s | Always blocks settings/hooks/env/key files; enforces `.ai/scope-lock.json` when present |
| `stop-checkpoint.sh` | Stop | 5s | Writes `.ai/checkpoint.json`; queues `IR_DRIFT` + `HALLUCINATION` pending-fault signals |
| `session-end.sh` | SessionEnd | 5s | Re-index → snapshot → scope-drift → envelope → ai-session → ai-document pipeline |
| `constraint-reinjector.sh` | SessionStart[compact] | 3s | Re-injects routing directives + active constraints after `/compact` |
| `pre-compact-snapshot.sh` | PreCompact | 3s | Captures state snapshot immediately before compaction |
| `fault-emitter.sh` | (sourced) | — | Shared `emit_fault()` function; appends structured JSON to `.ai/faults.jsonl` |

## Internal Packages

### `internal/governor`
Session governor logic extracted to Go for testability. Key responsibilities:
- **Turn counter** — per-session increment in `.governor-state/{session_id}.turns`
- **Session cost** — reads `~/.claude/projects/.../\*.jsonl` for cumulative USD cost
- **LLM classifier** — haiku call to classify prompt intent; writes log to `.governor-state/classifier-log.jsonl`
- **Regex fallback** — keyword matching when classifier key absent
- **Fault emission** — `EmitFault()` writes structured JSON to `.ai/faults.jsonl` (mirrors `fault-emitter.sh`)
- **Route persistence** — writes route decision to `.governor-state/{session_id}.route`
- **Pipeline integration** — calls `pipeline.Load(cwd)` + `pipeline.RenderText(p)` for `RoutePipeline`; falls back to hardcoded text on error

### `internal/pipeline`
Declarative pipeline system added in M5.

**Types:** `Pipeline` (name, description, stages), `Stage` (id, model, token_budget, scope, verify, description), `Envelope` (session execution record), `StageResult` (per-stage outcome).

**Loader:** `Load(root)` reads `.ai/pipelines/default.yaml`; falls back to `DefaultPipeline()` if missing. `LoadNamed(root, name)` for named pipelines. Calls `Validate()` on load.

**Renderer:** `RenderText(p)` produces MODEL ROUTER injection text from stage definitions. Model labels: haiku → "haiku subagents", opus → "opus subagent", sonnet → "you, Sonnet".

**Envelope:** `AppendEnvelope(root, env)` appends to `.ai/executions.jsonl`; idempotent by `session_id`. `FaultsForSession(root, sessionID)` reads `faults.jsonl` and returns deduplicated signal names for the session.

**Validate:** enforces non-empty name, at least one stage, each stage has id and valid model (haiku/sonnet/opus).

### `internal/ir`
AST-based codebase indexer. Produces `.ai/ir.json`: file list + all exported symbols (functions, types, interfaces, constants, variables). Extensible via `Parser` interface (`internal/parser/`). Currently supports JS/TS, Go, Python. IR hash is SHA-256 of full content — deterministic.

### `internal/snapshot`
SQLite-backed snapshot store. One record per `ai-ir snapshot` call. `diff` computes structural delta between two snapshots: added/removed/modified files and symbols. Used by `ai-ir verify` to produce a diff summary for `session-end.sh`.

### `internal/context`
Context compiler with `Provider` interface. `DefaultProviders` order: contract → ir → gitdiff → handoff → tasks → churn → review. Each provider returns a `Block` (header + content + token estimate). Compiler applies budget constraint; providers exceeding budget are truncated or dropped.

### `internal/contract`
`Contract` type with list-valued fields (`scope`, `assumptions`, `non_goals`, `success`). YAML serialization via `gopkg.in/yaml.v3`. `FromTask()` builds a minimal contract from task fields. `Validate()` checks required fields. `ContractProvider` reads `.ai/CONTRACT.yaml`; falls back to task-derived output.

### `internal/session`
Reads Claude Code JSONL logs from `~/.claude/projects/.../*.jsonl`. Extracts ground-truth facts: files edited/created (Edit/Write tool calls), commands run (Bash calls), token counts and cost (usage fields), duration. Calls haiku to summarize session narrative. Writes `.ai/handoff.md` with front-matter (YAML between `---` delimiters).

### `internal/document`
Generates project docs using haiku. `DocStatus` tracks whether each doc needs create or update. Token budget for update mode scales with existing doc size (bytes/4 × 1.3, floor 800, cap 8000). Change-gated: skips if all configured docs exist and IR diff is empty.

## State Layout

```
~/.claude/hooks/.governor-state/
  {session_id}.turns          # turn counter (plain integer)
  {session_id}.route          # last route decision ("haiku"|"sonnet"|"opus"|"pipeline")
  {session_id}.pending-faults # queued IR_DRIFT/HALLUCINATION signals (JSONL)
  classifier-log.jsonl        # LLM classifier call log

{project}/.ai/
  ir.json                     # current codebase IR (file list + symbols + root_hash)
  history.db                  # SQLite snapshot store
  faults.jsonl                # append-only fault signal log
  progress.jsonl              # append-only session progress records
  executions.jsonl            # append-only pipeline execution envelopes (M5)
  handoff.md                  # last session handoff (front-matter + narrative)
  checkpoint.json             # last-response state (turn, ir_hash, last_message)
  tasks.json                  # task ledger
  CONTRACT.yaml               # active session contract (scope, success criteria)
  churn-cache.txt             # top-N churn files (written at session-end for turn-1 injection)
  pipelines/
    default.yaml              # pipeline definition (optional; falls back to DefaultPipeline())
  agents/
    explorer.yaml             # haiku persona
    implementer.yaml          # sonnet persona
    architect.yaml            # opus persona
```

## Fault Signal Schema

All faults written to `.ai/faults.jsonl` by `EmitFault()` (Go) or `emit_fault()` (bash):

```json
{"signal":"IR_DRIFT","value":3,"context":"3 files changed","workspace":"/path","session_id":"abc","ts":"2026-01-01T00:00:00Z"}
```

| Signal | Emitted by | Meaning |
|--------|-----------|---------|
| `IR_DRIFT` | `stop-checkpoint.sh` | Files changed since session-start snapshot |
| `HALLUCINATION` | `stop-checkpoint.sh` | Claude referenced a symbol not in IR |
| `TURN_FATIGUE` | `ai-governor` | Turn threshold crossed (15/25/35) |
| `COST_FATIGUE` | `ai-governor` | Cost threshold crossed ($1/$3/$8) |
| `OPUS_BLOCKED` | `ai-governor` | Opus/pipeline downgraded due to cost cap |
| `SCOPE_DRIFT` | `session-end.sh` | Files changed outside task's declared scope |

## Pipeline Execution Flow

```
UserPromptSubmit
  ├── session-governor.sh → ai-governor
  │     ├── classifyOrRoute() → Route
  │     ├── if RoutePipeline: pipeline.Load(cwd) → pipeline.RenderText()
  │     └── assembleOutput() → injection text → Claude context
  └── ir-injector.sh → ai-context → IR + contract + handoff + tasks block

[Claude session runs]

Stop (each turn)
  └── stop-checkpoint.sh → checkpoint.json + pending-faults

SessionEnd
  └── session-end.sh
        ├── ai-ir (re-index + snapshot + churn-cache)
        ├── scope-drift detection → faults.jsonl
        ├── ai-pipeline envelope (if route=pipeline) → executions.jsonl
        ├── ai-session → handoff.md + progress.jsonl
        └── ai-document → README.md / TECHNICAL.md / USAGE.md
```

## Cost Model

| Model | Role | Trigger |
|-------|------|---------|
| Haiku | Eyes: search, read, summarize, classify | Cheap tasks; also used for LLM classification |
| Sonnet | Hands: code writing, bug fixes, direct edits | Default; handles all non-delegated work |
| Opus | Brain: architecture, trade-offs, root cause | Complex reasoning; blocked above $8/session |
| Pipeline | All three in sequence | Multi-step implementation (haiku explore → opus design → sonnet implement) |

## Key Design Decisions

**Why inject routing text into Claude's context rather than intercept API calls?** Claude Code routes requests through the API itself; there is no interception point at the application layer. Injecting routing directives into the LLM's context causes Claude to route itself — the model follows its own instructions. This works because Claude Code executes Task tool calls according to the `model` parameter, and the governor's injected text tells Claude which model to use.

**Why Go binaries rather than bash scripts?** Bash scripts are hard to test, have edge cases on different shells and OS versions, and accumulate complexity silently. Go gives typed data structures, unit tests, and cross-platform builds. Hooks remain bash (10–30 lines each) as exec wrappers — they handle the Claude Code protocol (stdin/stdout/exit codes) and delegate all logic to Go.

**Why append-only JSONL for fault signals, progress, and envelopes?** JSONL files are human-readable, grep-able, and trivially appendable without parsing the entire file. Idempotency guards (session_id substring search before write) prevent duplicates without requiring a database.

**Why `DefaultPipeline()` as a fallback rather than requiring the YAML file?** The governor must never block a session. If `.ai/pipelines/default.yaml` is absent or malformed, `Load()` returns a valid in-memory pipeline identical to the hardcoded `routeText[RoutePipeline]`. The fallback chain is: YAML file → DefaultPipeline() → hardcoded string (governor error path only).
