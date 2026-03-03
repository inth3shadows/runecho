# RunEcho

**Status:** Stages A–C Complete ✅ · Milestone 1 (Context Compiler) next

RunEcho is a session governance, model routing, and structural grounding layer for Claude Code. It enforces cost-optimal model selection, session discipline, and injects codebase structure at session start so Claude operates with accurate structural awareness.

---

## Glossary

**Session Governor** — A Claude Code hook (`UserPromptSubmit`) that fires on every user message. Tracks turn count and cumulative session cost, injects warnings when thresholds are crossed, and enforces model routing by injecting routing directives Claude must follow.

**Model Routing** — Classifying a prompt by intent (read, reason, code, multi-step) and directing the LLM to use the appropriate model for each subtask. RunEcho injects this guidance *into the LLM's context* so Claude routes itself — distinct from API-level routers that intercept requests before they reach the model. Haiku classifies; regex is the fallback.

**Cost Model** — The routing principle underlying all model selection: Haiku = eyes (reads, searches, summaries), Sonnet = hands (writing code, direct edits), Opus = brain (architecture, trade-offs, root cause). Cost cap at $8/session downgrades opus/pipeline routes to prevent runaway spend.

**Codebase IR (Intermediate Representation)** — A compact, structured index of a project: file list + all exported symbols (functions, types, interfaces, constants). Not a vector embedding. A flat, deterministic fact table computed from AST parsing of source files and stored as `.ai/ir.json`. Currently supports JS/TS/Go; extensible via the `Parser` interface.

**IR Injection** — Feeding the codebase IR into the LLM's context on session turn 1, before any user task. Claude starts every session knowing what files and symbols exist without reading files to orient itself. Subsequent turns are silent.

**IR Snapshot** — A point-in-time capture of the codebase IR stored in SQLite. Snapshots enable structural diff between sessions — `ai-ir diff` shows exactly which files and symbols were added, removed, or modified.

**IR Hash** — A deterministic SHA-256 hash of the full IR content. Included in handoff files and checkpoints. If the hash in a handoff doesn't match the current `ir.json`, it signals structural drift — the session was summarized against a stale codebase view.

**Session Handoff** — A structured markdown file (`.ai/handoff.md`) written at session end: files changed, commands run, decisions made, next steps. Produced by `ai-session` from ground-truth JSONL facts, not Claude's memory. Bridges the gap between Claude Code sessions so context isn't rebuilt from scratch.

**GUPP (Guided Upstream Priority Protocol)** — The block injected at session turn 1 by `ir-injector.sh` after the IR context. Contains: last session's `next_session_intent` from the handoff front-matter, and the output of `ai-task next`. Tells Claude what it should work on before the user types anything.

**Handoff Front-Matter** — YAML metadata at the top of `.ai/handoff.md`: `session_id`, `ir_hash`, `status`, `tasks_touched`, `files_changed`, `next_session_intent`. Machine-readable; consumed by `constraint-reinjector.sh` and `ai-session validate`.

**Persona Registry** — YAML files in `.ai/agents/` that define model assignments for agent roles (explorer → haiku, implementer → sonnet, architect → opus). `model-enforcer.sh` reads these to validate subagent model choices at PreToolUse time.

**Scope Lock** — An opt-in write restriction for high-stakes sessions. When `.ai/scope-lock.json` is present, `scope-guard.sh` restricts all file writes to the declared paths only. Settings files, hook files, `.env`, and `*.key` are always blocked regardless.

**Context Compaction** — Claude Code's `/compact` mechanism that summarizes and truncates conversation history to free context window space. RunEcho handles this via two hooks: `pre-compact-snapshot.sh` captures state immediately before, and `constraint-reinjector.sh` re-injects routing directives and active constraints after.

**Checkpoint** — A turn-level state snapshot written to `.ai/checkpoint.json` after every Claude response. Contains turn count, IR hash, and last message. Used as fallback recovery state if `ai-session` can't parse the JSONL log.

**Pipeline Route** — A multi-step agent chain for complex tasks: haiku explores the codebase → opus designs the solution → sonnet implements it. Triggered by prompts like "implement feature" or "build from scratch." Blocked when session cost exceeds $8.

---

## What It Does

- **Session Governor**: Tracks turn count and session cost. Thresholds trigger on whichever hits first — turns (15/25/35) or cost ($1/$3/$8). At the hard threshold (turn 35 or $8), opus/pipeline routing is blocked and Claude must write `.ai/handoff.md` immediately including the current IR snapshot hash. Prevents context degradation and compounding cache costs.
- **Model Router**: Classifies each prompt via a haiku LLM call and injects routing guidance — haiku for cheap tasks, opus for architecture, full pipeline (haiku→opus→sonnet) for multi-step work. Falls back to regex if classifier is unavailable.
- **Model Enforcer**: PreToolUse hook that denies subagents using the wrong model. If the router said haiku, Claude can't spawn an opus subagent.
- **IR Injector**: On session turn 1, reads `.ai/ir.json` and injects a compact codebase summary — file list + all symbols. Claude starts every session knowing what exists without reading files to orient itself.
- **Stop Checkpoint**: After every Claude response, writes `.ai/checkpoint.json` with turn count, IR hash, and last message. Provides state for failure recovery.
- **Session End**: On session termination, runs a three-tier handoff pipeline: (1) `ai-session` parses the Claude Code JSONL log for ground-truth facts, (2) falls back to minimal checkpoint template if JSONL unavailable, (3) calls `ai-document` to update project docs if structural changes occurred.
- **Session Synthesizer** (`ai-session`): Reads the Claude Code JSONL session log, extracts ground-truth facts (files edited/created, commands run, token counts, cost, duration), and calls haiku to summarize the session narrative. Produces `.ai/handoff.md` with factual accuracy — no speculation.
- **Document Generator** (`ai-document`): Auto-generates and updates project documentation (README.md, TECHNICAL.md, USAGE.md) using haiku. Change-gated by IR diff — skips entirely if no structural changes and docs already exist. Work mode generates all three docs; personal/unknown mode generates README only.
- **Destructive Bash Guard**: PreToolUse[Bash] hook. Hard-denies catastrophic commands (`rm -rf /`, `mkfs`, fork-bombs). Approval-gates dangerous-but-recoverable patterns: `rm -rf`, `git reset --hard`, `DROP TABLE`, pipe-to-shell installs.
- **Scope Guard**: PreToolUse[Edit|Write] hook. Always blocks writes to settings files, hook files, `.env`, and `*.key`. Optional scope-lock via `.ai/scope-lock.json` — when present, restricts all writes to declared paths only.
- **Constraint Reinjector**: SessionStart hook (matcher: `compact`). Re-injects active constraints after context compaction so BPB rules and routing directives survive a `/compact`.
- **Pre-Compact Snapshot**: PreCompact hook. Captures a session state snapshot immediately before compaction so the reinjector has accurate, current data to work from.

Together these enforce the cost model: **Haiku = eyes, Sonnet = hands, Opus = brain.**

---

## Install

```bash
bash install.sh
```

Builds four binaries and symlinks all hooks into `~/.claude/hooks/`. Requires Go in PATH.

| Binary | Purpose |
|---|---|
| `ai-ir` | Indexes codebase → `.ai/ir.json`; manages SQLite snapshot history |
| `ai-session` | Parses Claude Code JSONL log → ground-truth session handoff |
| `ai-document` | Auto-generates/updates README.md, TECHNICAL.md, USAGE.md via haiku |
| `ai-task` | Persistent task ledger for cross-session work tracking (`.ai/tasks.json`) |

---

## Settings

Full `~/.claude/settings.json` hook configuration:

```json
{
  "model": "sonnet",
  "hooks": {
    "SessionStart": [
      {
        "matcher": "compact",
        "hooks": [{ "type": "command", "command": "bash ~/.claude/hooks/constraint-reinjector.sh", "timeout": 3 }]
      }
    ],
    "UserPromptSubmit": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "bash ~/.claude/hooks/session-governor.sh", "timeout": 5 }]
      },
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "bash ~/.claude/hooks/ir-injector.sh", "timeout": 5 }]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "Task",
        "hooks": [{ "type": "command", "command": "bash ~/.claude/hooks/model-enforcer.sh", "timeout": 5 }]
      },
      {
        "matcher": "Bash",
        "hooks": [{ "type": "command", "command": "bash ~/.claude/hooks/destructive-bash-guard.sh", "timeout": 3 }]
      },
      {
        "matcher": "Edit|Write",
        "hooks": [{ "type": "command", "command": "bash ~/.claude/hooks/scope-guard.sh", "timeout": 3 }]
      }
    ],
    "PreCompact": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "bash ~/.claude/hooks/pre-compact-snapshot.sh", "timeout": 3 }]
      }
    ],
    "Stop": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "bash ~/.claude/hooks/stop-checkpoint.sh", "timeout": 5 }]
      }
    ],
    "SessionEnd": [
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "bash ~/.claude/hooks/session-end.sh", "timeout": 5 }]
      }
    ]
  }
}
```

**Order matters:** `session-governor.sh` must appear before `ir-injector.sh`. The governor writes the turn count; the injector reads it.

---

## Profile Switching (Work + Personal)

Run Claude Code against a corporate LiteLLM proxy and Claude Pro OAuth simultaneously in separate terminals — no conflicts, no login/logout steps.

**How:** `CLAUDE_CONFIG_DIR` points each profile at an isolated config directory. Work gets `~/.claude-work/` (API key, no OAuth token); personal uses `~/.claude/` (OAuth token, no API key). They never share a `credentials.json`.

```powershell
claude-profile work      # sets ANTHROPIC_API_KEY + ANTHROPIC_BASE_URL + CLAUDE_CONFIG_DIR=~/.claude-work
claude-profile personal  # clears all three env vars (falls back to ~/.claude)
```

Full setup, mechanics, edge cases, and security notes: **[docs/profile-switching.md](docs/profile-switching.md)**

---

## Classifier Setup

The model router uses a haiku LLM call to classify prompt intent — more accurate than regex on nuanced prompts.

**Requires** `RUNECHO_CLASSIFIER_KEY` — a dedicated Anthropic API key. Set it once in your PowerShell profile:

```powershell
# Store key (run once):
"sk-ant-api03-..." | ConvertTo-SecureString -AsPlainText -Force | ConvertFrom-SecureString | Set-Content "$HOME\.claude\runecho-classifier.key"
```

The profile loader (`Microsoft.PowerShell_profile.ps1`) reads this file at startup and sets `$env:RUNECHO_CLASSIFIER_KEY` automatically — active for both `work` and `personal` claude-profiles.

The classifier always calls `api.anthropic.com` directly. It ignores `ANTHROPIC_BASE_URL` (LiteLLM) even when the work profile is active.

**Cost:** haiku @ ~$0.001 per 100 classifications. 1000/day ≈ $0.01. Use a dedicated key with a low spend cap.

**Fallback:** if `RUNECHO_CLASSIFIER_KEY` is unset, the classifier returns empty and the regex router fires. No routing regression.

**Classifier log:** `~/.claude/hooks/.governor-state/classifier-log.jsonl` — one entry per call with prompt, route, and latency.

---

## Model Routing Logic

Classifier routes first. Regex fires as fallback.

| Intent | Route |
|---|---|
| implement feature, build new, from scratch, scaffold | Pipeline: haiku explore → opus design → sonnet implement |
| architect, review, trade-off, root cause, right direction, feasibility, alignment | Opus subagent for reasoning |
| summarize, search, find, explain code, grep, document | Haiku subagent |
| bug fix, refactor, write tests, direct code edit | Sonnet handles directly |

Opus check runs before pipeline — "review the plan" routes to opus, not pipeline.

**Cost cap:** when session cost reaches $8 (`COST_STOP`), opus and pipeline routes are downgraded to sonnet direct. Start a new session to restore opus routing.

---

## Session Warnings

Triggers on whichever threshold hits first — turn count or session cost.

| Threshold | Turn | Cost | Message |
|---|---|---|---|
| Warn | 15 | $1.00 | "Cost rising. Consider wrapping up soon or /compact." |
| Strong | 25 | $3.00 | "Session is expensive. Finish current task, suggest /compact or new session." |
| Stop | 35 | $8.00 | "Session limit reached. ACTION REQUIRED: write `.ai/handoff.md` now with IR snapshot hash." Opus/pipeline routing blocked. |

---

## IR Injection

**No manual indexing needed.** `ir-injector.sh` runs `ai-ir` automatically on session turn 1.

On every new Claude Code session in a project with supported source files (currently JS/TS/Go; extensible via the `Parser` interface), turn 1 will include:

```
IR CONTEXT [root_hash: abc123456789...]:
3 files — src/auth.ts, src/user.ts, utils/helpers.js
Symbols (12): AuthService, UserService, fetchUser, validateToken, ...
```

Subsequent turns: silent. Unsupported projects: silent.

### ai-ir Subcommands

```bash
ai-ir [root]                                        # index/update .ai/ir.json
ai-ir snapshot [--label=name] [root]                # save IR snapshot to SQLite history
ai-ir diff [--since=label | id-a id-b] [root]       # structural delta between snapshots
ai-ir verify [--session=id] [root]                  # format diff summary for handoff
ai-ir log [--n=10] [root]                           # list snapshots with timestamps
ai-ir churn [--n=20] [--min-changes=2] [--compact] [root]  # symbol/file churn rate across sessions
```

`session-end.sh` calls `snapshot` → `verify` → passes the diff to `ai-document`. Use `diff --since=session-end` to see what changed in the last session.

---

## Session Handoff

Closes the cross-session continuity gap.

**Normal path:** at turn 35, the governor instructs Claude to write `.ai/handoff.md`. On the next session's turn 1, the IR injector injects it after the IR block.

**SessionEnd pipeline** (`session-end.sh`):
1. `ai-ir snapshot` — capture final IR state to SQLite
2. `ai-ir verify` — compute structural diff summary since last snapshot
3. **`ai-session`** (primary) — parse Claude Code JSONL log → ground-truth handoff with real facts: files edited, commands run, token counts, cost, duration
4. **Checkpoint fallback** — if `ai-session` fails or JSONL unavailable, synthesize minimal handoff from `checkpoint.json`
5. **`ai-document`** — if IR diff exists, update project docs via haiku (non-fatal, runs in background)

**Triggers (Claude-written handoff):**
- Auto: governor fires at turn 35 with `ACTION REQUIRED: Write session handoff now.`
- Manual: type `write handoff` — routes to haiku

**Files:**
- `.ai/handoff.md` — current handoff (from ai-session, Claude, or checkpoint fallback)
- `.ai/checkpoint.json` — updated after every response; used as fallback recovery state

**ai-session output format:**

```markdown
# Session Handoff
**Date:** 2026-03-02 | **Turns:** 35 | **Duration:** 11m18s | **Cost:** ~$1.82
**Model:** claude-sonnet-4-6 | **Tokens:** 12k in / 40k out / 4019k cache

## Files Changed
- path/to/file.go (created)

## Commands Run
- go build ./...

## Accomplished
## Decisions
## In Progress
## Next Steps
## Notes
```

---

## Task Ledger

`ai-task` maintains a persistent, dependency-aware task list at `.ai/tasks.json`. Designed for cross-session work tracking — tasks survive session boundaries and are injected into turn 1 via the GUPP (Guided Upstream Priority Protocol) block in `ir-injector.sh`.

```bash
ai-task add "<title>" [--blocked-by=<id>]   # append task, status=pending
ai-task update <id> <status>                 # status ∈ {pending, in-progress, done}
ai-task list [--status=<s>]                  # tabular; no filter = all non-done
ai-task next                                 # first unblocked non-done task; exit 1 if none
```

**Blocking model:** `--blocked-by=<id>` links tasks. `next` skips tasks whose blocker isn't done yet.

**Storage:** atomic write via temp-file rename → no partial writes on crash.

**Integration:** `ir-injector.sh` calls `ai-task next` on session turn 1 and prepends the result to the HANDOFF DIRECTIVE block — Claude starts each session aware of the highest-priority unblocked task.

---

## Auto-Generated Documentation

`ai-document` generates and updates project docs using haiku, gated by structural changes.

```bash
ai-document [root]                         # auto-detect mode, skip if no changes
ai-document --ir-diff="<diff>" [root]      # use IR diff to update existing docs
ai-document --mode=work|personal [root]    # override mode detection
ai-document --dry-run [root]               # preview without writing
ai-document --force [root]                 # bypass change-gate, regenerate all
```

**Docs generated:**

| Mode | Docs |
|---|---|
| work | README.md, TECHNICAL.md, USAGE.md (parallel, ~2000 token budget each) |
| personal / unknown | README.md only (sequential, ~800 token budget) |

**Change-gate:** if all managed docs exist and `--ir-diff` is empty, generation is skipped. Pass `--force` to override. The gate prevents unnecessary API calls when nothing structural changed.

**Integration:** `session-end.sh` calls `ai-document --ir-diff="$VERIFY_SUMMARY"` after writing the handoff. Runs in the background, non-fatal.

**Requires:** `RUNECHO_CLASSIFIER_KEY` — same key used by the model router classifier.

---

## Verify

```bash
# Hooks installed
ls -la ~/.claude/hooks/

# Binaries built
ls -la ~/bin/ai-ir ~/bin/ai-session ~/bin/ai-document

# Governor + classifier fallback (no key = regex fires)
RUNECHO_CLASSIFIER_KEY="" \
  echo '{"session_id":"test","prompt":"architect the system"}' | bash hooks/session-governor.sh
# Expected: "Deep reasoning task" (opus via regex)

# Stop checkpoint write
STATE_DIR="$HOME/.claude/hooks/.governor-state"
mkdir -p "$STATE_DIR" && echo "5" > "$STATE_DIR/test-ck"
echo '{"session_id":"test-ck","cwd":"'$PWD'","last_assistant_message":"done"}' | bash hooks/stop-checkpoint.sh
cat .ai/checkpoint.json

# ai-session: factual extraction (no API key needed for factual-only mode)
ai-session --session="$SESSION_ID" .
cat .ai/handoff.md

# ai-document: dry-run (requires RUNECHO_CLASSIFIER_KEY)
ai-document --dry-run .

# ai-ir snapshot + diff
ai-ir snapshot --label=test .
ai-ir log .
ai-ir diff --since=test .

# SessionEnd full pipeline
rm -f .ai/handoff.md
echo '{"session_id":"test-ck","cwd":"'$PWD'","reason":"other"}' | bash hooks/session-end.sh
cat .ai/handoff.md

# Cleanup
rm -f .ai/handoff.md .ai/checkpoint.json "$STATE_DIR/test-ck"
```

---

## Repo Structure

```
.
├── cmd/
│   ├── document/main.go        # ai-document — auto-generates project docs via haiku
│   ├── ir/main.go              # ai-ir — indexes codebase, manages snapshot history
│   ├── session/main.go         # ai-session — parses JSONL log → ground-truth handoff
│   └── task/main.go            # ai-task — persistent task ledger with dependency graph
├── hooks/
│   ├── session-governor.sh        # UserPromptSubmit — turn count + model routing
│   ├── model-enforcer.sh          # PreToolUse[Task] — denies wrong-model subagents
│   ├── ir-injector.sh             # UserPromptSubmit — injects IR + handoff + next task on turn 1
│   ├── stop-checkpoint.sh         # Stop — writes .ai/checkpoint.json after each response
│   ├── session-end.sh             # SessionEnd — JSONL handoff → checkpoint fallback → doc update
│   ├── destructive-bash-guard.sh  # PreToolUse[Bash] — hard deny + approval gate for destructive ops
│   ├── scope-guard.sh             # PreToolUse[Edit|Write] — protects settings/keys; optional scope-lock
│   ├── constraint-reinjector.sh   # SessionStart[compact] — re-injects constraints after /compact
│   └── pre-compact-snapshot.sh    # PreCompact — captures state before compaction
├── install.sh                  # Builds all binaries, symlinks hooks to ~/.claude/hooks/
├── internal/
│   ├── document/               # doc generation: types, generator, reader, writer
│   ├── ir/                     # IR types, generator, hasher, storage
│   ├── parser/                 # language parsers (JS/TS, Go); extensible via Parser interface
│   ├── session/                # session fact extraction, JSONL parser, summarizer, writer
│   └── snapshot/               # SQLite snapshot store, structural diff engine
├── .ai/agents/
│   ├── explorer.yaml           # haiku persona — file reads, search, summarization
│   ├── implementer.yaml        # sonnet persona — code writing, bug fixes, refactoring
│   └── architect.yaml          # opus persona — design decisions, trade-off analysis
├── docs/
│   └── profile-switching.md    # work/personal profile setup
├── powershell/
│   └── claude-profile.ps1      # work/personal profile switcher (copy into $PROFILE)
└── README.md
```

---

## Completed Stages

**A — Session Discipline ✅**
- Session governor (turn limits + cost warnings)
- Regex model router + model enforcer PreToolUse gate
- Destructive bash guard, scope guard

**B — Structural Intelligence ✅**
- `ai-ir` CLI: generates `.ai/ir.json` for JS/TS/Go projects (extensible via `Parser` interface)
- IR injector: auto-index + inject codebase summary on session turn 1
- IR snapshots in SQLite, structural diff between sessions, symbol/file churn analysis

**C — Intent-Aware Routing + Failure Recovery ✅**
- LLM classifier (haiku) replaces regex as primary router; regex fallback on key absence
- Stop checkpoint: turn-level state persistence after every response
- `ai-session`: ground-truth handoff from Claude Code JSONL log (files, commands, tokens, cost, duration)
- `ai-ir snapshot/diff/verify/churn`: SQLite snapshot store + structural diff
- `ai-document`: change-gated doc generation via haiku; SessionEnd pipeline
- `ai-task`: persistent task ledger with dependency graph; GUPP injection on turn 1
- Persona registry: model assignments in YAML, enforced at PreToolUse time

---

## Roadmap

The order reflects dependencies and value. Each milestone is independently useful — the project doesn't need all six to be materially better.

---

### Milestone 1 — Context Compiler
**Goal:** Replace bash+jq context assembly in `ir-injector.sh` with a single Go binary that composes, scores, and budget-constrains session context.

| Deliverable | Description |
|---|---|
| `ai-context` binary | Accepts `--budget=<tokens>` and `--providers=ir,handoff,tasks,churn,git-diff`. Outputs one markdown block. Relevance scoring moves from bash to Go. |
| `ir-injector.sh` simplification | Reduced to a single binary call + echo. All logic in compiled, testable Go. |
| Context provider interface | `internal/context/provider.go` — adding a new provider is a Go function, not a bash stanza. |

**Done when:** `ir-injector.sh` is under 20 lines; all context assembly has unit tests; token budget is respected (verified by output length).

---

### Milestone 2 — Session Contracts
**Goal:** Every session starts with a machine-verifiable scope contract and ends with a pass/fail evaluation against it.

| Deliverable | Description |
|---|---|
| `ai-task` scope + verify fields | `--scope="internal/auth/*"` + `--verify="go test ./internal/auth/..."` per task. |
| Turn-1 contract injection | `SESSION CONTRACT` block in turn 1: task title, success criteria, file scope. Derived from the active task. |
| Scope drift detection | `session-end.sh` runs `git diff --name-only` vs. task scope. Files outside scope → warning in `progress.jsonl`. |

**Done when:** A session that modifies files outside its task's declared scope produces a visible scope-drift warning carried into the next session's handoff injection.

---

### Milestone 3 — Session Review
**Goal:** Surface patterns across sessions — stuck tasks, scope drift, cost trends — before starting work.

| Deliverable | Description |
|---|---|
| `ai-session review` | Reads `progress.jsonl` + `tasks.json`. Reports stuck tasks (3+ sessions, still not done), cost per task, scope drift frequency. |
| Trace mode | `--trace` groups entries by task across sessions, showing full lifecycle. |
| Actionable injection | Injects a `SESSION REVIEW` block on turn 1 only when review surfaces something worth acting on — never noise. |

**Done when:** `ai-session review` on a project with 5+ sessions produces an accurate report. Stuck-task detection is correct.

*Inspired by: OpenTelemetry/Honeycomb (traces, not flat logs)*

---

### Milestone 4 — Pipeline Definitions
**Goal:** Replace hardcoded pipeline text in `session-governor.sh` with declarative YAML definitions.

| Deliverable | Description |
|---|---|
| `.ai/pipelines/*.yaml` format | Stages with model, token budget, and input/output contract. Example: `explore (haiku) → reason (opus) → implement (sonnet)`. |
| Governor reads definitions | Stage-specific injection text, not monolithic `MULTI-STEP PIPELINE` block. |
| `ai-pipeline` binary | `ai-pipeline run <name>` validates definition and emits the injection text. Templates only — no orchestration. |

**Done when:** A custom pipeline YAML produces different governor injection than the default. Adding a pipeline is a YAML edit, not a bash edit.

*Inspired by: Dagger (pipelines as typed, composable objects)*

---

### Milestone 5 — MCP Tool Server
**Goal:** Expose RunEcho capabilities as MCP tools so Claude invokes them directly instead of relying on text injection.

| Deliverable | Description |
|---|---|
| `ai-mcp` binary | stdio MCP server (Go) exposing `task/list`, `task/update`, `ir/diff`, `session/review`, `context/compile`. |
| MCP config in `settings.json` | Claude Code registers `ai-mcp` as a tool server. |
| Hook consolidation | Bash injection removed for capabilities now available as tools. Governance hooks (governor, enforcer, guards) remain — they must intercept, not serve. |

**Done when:** Claude calls `task/update` as a tool call instead of Bash. The tools appear in Claude's tool list.

*Inspired by: Claude Code's own hooks system extended to MCP; Continue.dev context providers*

---

### Milestone 6 — Orchestration Prototype *(Stage C entry)*
**Goal:** A single command that decomposes a task into subtasks, assigns models, and produces a multi-session execution plan.

| Deliverable | Description |
|---|---|
| `ai-orchestrate <task-id>` | Reads task + pipeline definition + IR. Produces a plan: subtask list with dependencies, model assignments, file scopes. |
| Subtasks as `ai-task` entries | Each subtask gets `parent_id`, `scope`, and `verify`. Traceable back to the orchestrating task. |
| Human-in-the-loop execution | Orchestrator produces the plan; the developer executes each subtask as a separate Claude Code session. No autonomous spawning yet. |

**Done when:** `ai-orchestrate 5` produces a concrete, executable multi-session plan. Executing each subtask in separate sessions produces `progress.jsonl` entries tracing back to the parent task.

*Inspired by: Temporal/Inngest (durable execution with explicit checkpoints); full Stage C automation follows after this proves the contracts work)*

---

## License

TBD
