# RunEcho

**Status:** Stages A–C Complete ✅ · M1 ✅ · M2 ✅ · M3 ✅ · M4 ✅ · V2 spike ✅ · next: M5

RunEcho is a session governance, model routing, and structural grounding layer for Claude Code. It enforces cost-optimal model selection, session discipline, and injects codebase structure at session start so Claude operates with accurate structural awareness.

**Fully automated.** After install, RunEcho requires no CLI commands in normal operation. Open Claude Code and work. The hook chain handles everything silently: model routing, IR injection, checkpointing, session synthesis, doc updates. The only user actions are the initial install and profile switching between work/personal accounts.

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

**Session Contract** — A machine-readable scope boundary and success criterion for a work unit. Stored as `.ai/CONTRACT.yaml` (YAML). Fields: `title`, `scope` (list of glob patterns), `verify` (shell command), `assumptions`, `non_goals`, `success`. Authored by `ai-task contract` (tool path) or written directly by Claude (Claude path). The `ContractProvider` reads `CONTRACT.yaml` when present and falls back to the active task's `scope`/`verify` fields.

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
- **Session End**: On session termination, runs a five-stage pipeline: (1) scope-drift detection — compares git-changed files vs. the active task's declared scope, emits `SCOPE_DRIFT` fault if files fall outside it, (2) `ai-session` parses the Claude Code JSONL log for ground-truth facts, (3) falls back to minimal checkpoint template if JSONL unavailable, (4) calls `ai-session review` silently — injects a SESSION REVIEW block into the next turn-1 if actionable patterns exist, (5) calls `ai-document` to update project docs if structural changes occurred.
- **Session Synthesizer** (`ai-session`): Reads the Claude Code JSONL session log, extracts ground-truth facts (files edited/created, commands run, token counts, cost, duration), and calls haiku to summarize the session narrative. Produces `.ai/handoff.md` with factual accuracy — no speculation.
- **Document Generator** (`ai-document`): Auto-generates and updates project documentation (README.md, TECHNICAL.md, USAGE.md) using haiku. Change-gated by IR diff — skips entirely if no structural changes and docs already exist. Work mode generates all three docs; personal/unknown mode generates README only.
- **Destructive Bash Guard**: PreToolUse[Bash] hook. Hard-denies catastrophic commands (`rm -rf /`, `mkfs`, fork-bombs). Approval-gates dangerous-but-recoverable patterns: `rm -rf`, `git reset --hard`, `DROP TABLE`, pipe-to-shell installs.
- **Scope Guard**: PreToolUse[Edit|Write] hook. Always blocks writes to settings files, hook files, `.env`, and `*.key`. Optional scope-lock via `.ai/scope-lock.json` — when present, restricts all writes to declared paths only.
- **Constraint Reinjector**: SessionStart hook (matcher: `compact`). Re-injects active constraints after context compaction so BPB rules and routing directives survive a `/compact`.
- **Pre-Compact Snapshot**: PreCompact hook. Captures a session state snapshot immediately before compaction so the reinjector has accurate, current data to work from.

Together these enforce the cost model: **Haiku = eyes, Sonnet = hands, Opus = brain.**

---

## Dependencies

**Required:**
- Go 1.24+ — build-time only; not needed at runtime after `install.sh`
- Python 3 — used by `install.sh` to merge hook config into `~/.claude/settings.json`
- [Claude Code](https://claude.ai/code) — the CLI RunEcho hooks into
- **Claude Code Pro (or higher)** — hooks require a paid Claude Code plan; free tier does not support the hooks API
- `RUNECHO_CLASSIFIER_KEY` — Anthropic API key used by the model router classifier and `ai-document`. Set once in your PowerShell profile (see [Classifier Setup](#classifier-setup))

**Optional:**
- ShellCheck — hook validation during install (`winget install koalaman.shellcheck`)

**Not a dependency:** `ccusage` and similar external cost-tracking tools are not required and are not integrated.

---

## Install

```bash
bash install.sh
```

Builds five binaries, symlinks all hooks into `~/.claude/hooks/`, and automatically merges the RunEcho hook configuration into `~/.claude/settings.json`. Idempotent — safe to re-run after updates. Requires Go in PATH.

| Binary | Purpose |
|---|---|
| `ai-ir` | Indexes codebase → `.ai/ir.json`; manages SQLite snapshot history |
| `ai-session` | Parses Claude Code JSONL log → ground-truth session handoff |
| `ai-document` | Auto-generates/updates README.md, TECHNICAL.md, USAGE.md via haiku |
| `ai-task` | Persistent task ledger for cross-session work tracking (`.ai/tasks.json`) |
| `ai-context` | Compiles turn-1 context block (contract + IR + diff + handoff + tasks + review) within a token budget |

---

## Settings Reference

`install.sh` automatically merges these hooks into `~/.claude/settings.json`. This section is for reference — manual configuration is not required.

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

The model router and `ai-document` both require `RUNECHO_CLASSIFIER_KEY` — a dedicated Anthropic API key. Set it once in your PowerShell profile:

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

On every new Claude Code session in a project with supported source files (currently JS/TS/Go/Python; extensible via the `Parser` interface), turn 1 will include:

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
3. **Scope-drift detection** (M3) — compare `git diff --name-only HEAD` vs. active task's `scope` glob; emit `SCOPE_DRIFT` fault to `faults.jsonl` for out-of-scope files
4. **`ai-session`** (primary) — parse Claude Code JSONL log → ground-truth handoff with real facts: files edited, commands run, token counts, cost, duration
5. **Checkpoint fallback** — if `ai-session` fails or JSONL unavailable, synthesize minimal handoff from `checkpoint.json`
6. **`ai-document`** — if IR diff exists, update project docs via haiku (non-fatal, runs in background)

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

**Claude drives this during sessions.** When you describe work in conversation, Claude calls `ai-task add` directly. When work completes, Claude calls `ai-task update`. The `ir-injector.sh` hook calls `ai-task next` and `ai-task contract` automatically on session turn 1. No manual CLI invocation needed in normal operation.

The CLI is available for inspection, override, and bootstrapping:

```bash
ai-task add "<title>" [--blocked-by=<id>] [--scope=<glob>] [--verify=<cmd>]  # append task, status=pending
ai-task update <id> <status>                 # status ∈ {pending, in-progress, done}
ai-task list [--status=<s>]                  # tabular; no filter = all non-done
ai-task next                                 # first unblocked non-done task; exit 1 if none
ai-task contract [--task-id=<id>] [root]     # write .ai/CONTRACT.yaml from active (or specified) task
```

**Blocking model:** `--blocked-by=<id>` links tasks. `next` skips tasks whose blocker isn't done yet.

**Storage:** atomic write via temp-file rename → no partial writes on crash.

**Fields:** `--scope=<glob>` locks the task to specific file paths (e.g. `internal/auth/*`; comma-separated or newline-separated for multiple). `--verify=<cmd>` sets a shell command run at session end to validate completion. Both fields are injected into the SESSION CONTRACT block on turn 1 and used for scope-drift detection in `session-end.sh`.

**CONTRACT.yaml:** `ai-task contract` generates `.ai/CONTRACT.yaml` from the active task. Claude can also write this file directly — the schema is well-defined (see `internal/contract/types.go`). Supports list-valued `scope`, `assumptions`, `non_goals`, and `success` fields beyond what task flags expose. The `ContractProvider` reads it on every turn 1; absence falls back silently to task-derived output.

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
│   ├── context/main.go         # ai-context — compiles session context block (IR + handoff + tasks + diff)
│   ├── document/main.go        # ai-document — auto-generates project docs via haiku
│   ├── ir/main.go              # ai-ir — indexes codebase, manages snapshot history
│   ├── session/main.go         # ai-session — parses JSONL log → ground-truth handoff
│   └── task/main.go            # ai-task — persistent task ledger with dependency graph
├── hooks/
│   ├── session-governor.sh        # UserPromptSubmit — turn count + model routing + fault emission
│   ├── model-enforcer.sh          # PreToolUse[Task] — denies wrong-model subagents
│   ├── ir-injector.sh             # UserPromptSubmit — index + snapshot + ai-context injection (20 lines)
│   ├── fault-emitter.sh           # Sourced by hooks — emit_fault() writes to .ai/faults.jsonl
│   ├── stop-checkpoint.sh         # Stop — writes .ai/checkpoint.json; emits IR_DRIFT + HALLUCINATION faults
│   ├── session-end.sh             # SessionEnd — JSONL handoff → checkpoint fallback → doc update
│   ├── destructive-bash-guard.sh  # PreToolUse[Bash] — hard deny + approval gate for destructive ops
│   ├── scope-guard.sh             # PreToolUse[Edit|Write] — protects settings/keys; optional scope-lock
│   ├── constraint-reinjector.sh   # SessionStart[compact] — re-injects constraints after /compact
│   └── pre-compact-snapshot.sh    # PreCompact — captures state before compaction
├── install.sh                  # Builds all binaries, symlinks hooks, auto-configures ~/.claude/settings.json
├── internal/
│   ├── contract/               # Contract type, YAML parser, validator, FromTask adapter
│   ├── context/                # context compiler: Provider interface + contract/ir/gitdiff/handoff/tasks/churn/review providers
│   ├── document/               # doc generation: types, generator, reader, writer
│   ├── ir/                     # IR types, generator, hasher, storage
│   ├── parser/                 # language parsers (JS/TS/Go/Python); extensible via Parser interface
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

**M3 — Session Contracts ✅**
- `ai-task` `--scope=<glob>` and `--verify=<cmd>` fields per task
- `SESSION CONTRACT` block injected at turn 1 via `ContractProvider` (scope, title, verify command)
- Scope-drift detection in `session-end.sh`: git diff vs. task scope → `SCOPE_DRIFT` fault in `faults.jsonl`
- `DefaultProviders` order: contract, ir, gitdiff, handoff, tasks, review

**M4 — Session Review ✅**
- `ai-session review [--trace] [--n=5] [--force]` subcommand: reads `progress.jsonl` + `faults.jsonl` + `tasks.json`
- Reports stuck tasks (3+ sessions, not done), scope drift frequency, cost per session
- `ReviewProvider` injects `SESSION REVIEW` block at turn 1 only when actionable — silent otherwise
- `--trace` flag groups entries by task across sessions for full lifecycle view

**V2 Spike — Contract Package ✅**
- `internal/contract/` package: `Contract` type (list-valued `scope`, `assumptions`, `non_goals`, `success`), YAML parser, validator, `FromTask` adapter
- `ai-task contract [--task-id=<id>] [root]` subcommand: writes `.ai/CONTRACT.yaml` atomically from the active task
- `ContractProvider` reads `CONTRACT.yaml` when present; falls back to task-derived; logs validation warnings to stderr
- Richer SESSION CONTRACT block: multi-line scope, assumptions, non-goals, success criterion
- Added `gopkg.in/yaml.v3` dependency (validates fit before M5 pipeline definitions)

---

## Roadmap

The order reflects dependencies and value. Each milestone is independently useful — the project doesn't need all six to be materially better.

---

### Milestone 1 — Fault Signal Taxonomy
**Goal:** Formalize the hook chain's ad-hoc warning emissions into a typed, structured signal layer. Decouples fault detection from fault consumption so any downstream (session review, Langfuse, handoff, progress.jsonl) reads from a stable schema.

| Deliverable | Description |
|---|---|
| Signal schema | Named signal identifiers: `IR_DRIFT`, `HALLUCINATION`, `TURN_FATIGUE`, `COST_FATIGUE`, `OPUS_BLOCKED`. Each carries `session_id`, `ts`, `value` (numeric), and `context` (string). |
| `.ai/faults.jsonl` | Append-only fault log per workspace. One JSON line per signal emission. Idempotent by `(session_id, signal, ts)`. |
| Emission utility | Shared shell function (or Go helper called from hooks) that writes structured JSON to `faults.jsonl`. Replaces inline `echo` + `.verify-warnings` ad-hoc text. |
| Signal migration | Migrate all 5 existing fault signals in `stop-checkpoint.sh` and `session-governor.sh` to use the new emission path. Remove `.verify-warnings` plain-text format. |

**Done when:** All 5 existing signals emit structured JSON to `faults.jsonl`; `.verify-warnings` is retired; `ai-ir validate-claims` and `ai-ir verify` emit structured output consumed by the emission utility.

---

### Milestone 2 — Context Compiler
**Goal:** Replace bash+jq context assembly in `ir-injector.sh` with a single Go binary that composes, scores, and budget-constrains session context.

| Deliverable | Description |
|---|---|
| `ai-context` binary | Accepts `--budget=<tokens>` and `--providers=ir,handoff,tasks,churn,git-diff`. Outputs one markdown block. Relevance scoring moves from bash to Go. |
| `ir-injector.sh` simplification | Reduced to a single binary call + echo. All logic in compiled, testable Go. |
| Context provider interface | `internal/context/provider.go` — adding a new provider is a Go function, not a bash stanza. |
| Python parser | `internal/parser/python.go` — implements `Parser` interface for `.py` files. Extracts imports, top-level functions, classes, `__all__` exports. Validates the provider interface is general enough for a third language. |

**Done when:** `ir-injector.sh` is under 20 lines; all context assembly has unit tests; token budget is respected; Python files appear in IR output.

---

### ~~Milestone 3 — Session Contracts~~ ✅
**Goal:** Every session starts with a machine-verifiable scope contract and ends with a pass/fail evaluation against it.

| Deliverable | Description |
|---|---|
| `ai-task` scope + verify fields | `--scope="internal/auth/*"` + `--verify="go test ./internal/auth/..."` per task. |
| Turn-1 contract injection | `SESSION CONTRACT` block in turn 1: task title, success criteria, file scope. Derived from the active task. |
| Scope drift detection | `session-end.sh` runs `git diff --name-only` vs. task scope. Files outside scope → warning in `progress.jsonl`. |

**Done when:** A session that modifies files outside its task's declared scope produces a visible scope-drift warning carried into the next session's handoff injection.

---

### ~~Milestone 4 — Session Review~~ ✅
**Goal:** Surface patterns across sessions — stuck tasks, scope drift, cost trends — before starting work.

| Deliverable | Description |
|---|---|
| `ai-session review` | Reads `progress.jsonl` + `tasks.json`. Reports stuck tasks (3+ sessions, still not done), cost per task, scope drift frequency. |
| Trace mode | `--trace` groups entries by task across sessions, showing full lifecycle. |
| Actionable injection | Injects a `SESSION REVIEW` block on turn 1 only when review surfaces something worth acting on — never noise. |

**Done when:** `ai-session review` on a project with 5+ sessions produces an accurate report. Stuck-task detection is correct.

*Inspired by: OpenTelemetry/Honeycomb (traces, not flat logs)*

> **Optional add-on after M4:** Langfuse integration — wire `faults.jsonl` signals as Langfuse Scores, map `session_id` to Langfuse session traces. Deferred until M1 structured signals exist. Treated as an optional consumer of the fault layer, not a replacement for `ai-session review`.

---

### Milestone 5 — Pipeline Definitions
**Goal:** Replace hardcoded pipeline text in `session-governor.sh` with declarative YAML definitions.

| Deliverable | Description |
|---|---|
| `.ai/pipelines/*.yaml` format | Stages with model, token budget, and input/output contract. Example: `explore (haiku) → reason (opus) → implement (sonnet)`. |
| Governor reads definitions | Stage-specific injection text, not monolithic `MULTI-STEP PIPELINE` block. |
| `ai-pipeline` binary | `ai-pipeline run <name>` validates definition and emits the injection text. Templates only — no orchestration. |

**Done when:** A custom pipeline YAML produces different governor injection than the default. Adding a pipeline is a YAML edit, not a bash edit.

*Prerequisite satisfied:* `gopkg.in/yaml.v3` added and validated in V2 spike.

*Inspired by: Dagger (pipelines as typed, composable objects)*

---

### Milestone 6 — MCP Tool Server
**Goal:** Expose RunEcho capabilities as MCP tools so Claude invokes them directly instead of relying on text injection.

| Deliverable | Description |
|---|---|
| `ai-mcp` binary | stdio MCP server (Go) exposing `task/list`, `task/update`, `ir/diff`, `session/review`, `context/compile`. |
| MCP config in `settings.json` | Claude Code registers `ai-mcp` as a tool server. |
| Hook consolidation | Bash injection removed for capabilities now available as tools. Governance hooks (governor, enforcer, guards) remain — they must intercept, not serve. |

**Done when:** Claude calls `task/update` as a tool call instead of Bash. The tools appear in Claude's tool list.

*Inspired by: Claude Code's own hooks system extended to MCP; Continue.dev context providers*

---

### Milestone 7 — Orchestration Prototype *(Stage C entry)*
**Goal:** A single command that decomposes a task into subtasks, assigns models, and produces a multi-session execution plan.

| Deliverable | Description |
|---|---|
| `ai-orchestrate <task-id>` | Reads task + pipeline definition + IR. Produces a plan: subtask list with dependencies, model assignments, file scopes. |
| Subtasks as `ai-task` entries | Each subtask gets `parent_id`, `scope`, and `verify`. Traceable back to the orchestrating task. |
| Human-in-the-loop execution | Orchestrator produces the plan; the developer executes each subtask as a separate Claude Code session. No autonomous spawning yet. |

**Done when:** `ai-orchestrate 5` produces a concrete, executable multi-session plan. Executing each subtask in separate sessions produces `progress.jsonl` entries tracing back to the parent task.

*Inspired by: Temporal/Inngest (durable execution with explicit checkpoints); full Stage C automation follows after this proves the contracts work)*

---

### Milestone 8 — Outcome Verification Loop
**Goal:** Close the contract cycle by automatically running each task's `verify` command at session end, recording pass/fail in `progress.jsonl`, and feeding results into the next session's handoff.

| Deliverable | Description |
|---|---|
| `session-end.sh` verify runner | After scope-drift detection (M3), runs `task.verify` command if present. Captures exit code, stdout (truncated to 2 KB), and duration. Writes structured result to `progress.jsonl` with `type: "verify"`. |
| Verify result in handoff injection | `HANDOFF` block includes last verify outcome: `PASS`, `FAIL + summary`, or `SKIPPED (no verify command)`. Next session sees whether the previous session's work actually held. |
| `ai-task verify <task-id>` | Standalone command to run a task's verify step on demand. Returns structured JSON: `{task_id, status, exit_code, output, duration_ms}`. |
| Fault signal: `VERIFY_FAIL` | Failed verification emits `VERIFY_FAIL` to `faults.jsonl` (M1 schema). Consecutive failures across sessions on the same task escalate to `TASK_STUCK` signal. |

**Done when:** A session completing a task with `--verify="go test ./pkg/auth/..."` automatically runs it, records the result in `progress.jsonl`, and the next session's handoff injection shows `VERIFY: PASS` or `VERIFY: FAIL — <first failure line>`. A task failing verification 3 sessions in a row emits `TASK_STUCK`.

*Inspired by: Bazel's test result caching + GitHub Actions job summaries (structured outcomes, not just exit codes)*

---

### Milestone 9 — Drift-Aware Re-Planning
**Goal:** When the IR snapshot changes significantly between sessions, detect which in-progress tasks are affected by the structural drift and surface actionable re-planning signals before work begins.

| Deliverable | Description |
|---|---|
| Task impact analysis | Intersects `task.scope` globs with IR diff's changed file set. Tasks with overlap are flagged `DRIFT_AFFECTED` in `faults.jsonl`. |
| Turn-1 drift injection | When drift-affected tasks exist, the `SESSION CONTRACT` block (M3) includes a `DRIFT ADVISORY` section listing affected tasks, what changed, and whether `verify` commands still reference valid paths. |
| `ai-task replan <task-id>` | Prints original task scope alongside the IR diff. Suggests scope adjustments (new files in scope, removed files that were in scope). Human confirms or edits. |

**Done when:** After a refactor session moves `internal/auth/` to `pkg/auth/`, the next session working on an auth-scoped task sees a drift advisory naming the moved files. Tasks unaffected by the drift show no advisory.

*Inspired by: Renovate/Dependabot (automated impact detection) + Pants/Buck2 (target-level invalidation from dependency graphs)*

---

### Milestone 10 — Supervised Subtask Execution
**Goal:** Extend `ai-orchestrate` (M7) from plan-only to plan-and-execute, spawning Claude Code sessions for each subtask with mandatory gates between stages.

| Deliverable | Description |
|---|---|
| `ai-orchestrate run <task-id>` | Executes the orchestration plan sequentially. Each subtask spawns a headless `claude` session with scope locked to the subtask's file set. Captures `progress.jsonl` output. |
| Gate enforcement | Between subtasks: runs `ai-task verify` (M8). `FAIL` → halts pipeline, emits `GATE_FAIL` fault. `PASS` → proceeds. Human override via `--continue-on-fail` (explicit opt-in). |
| Scope lock | Each spawned session receives `ALLOWED_PATHS` from `task.scope`. Enforcer hook rejects writes outside scope. |
| Execution manifest | `.ai/orchestrations/<task-id>.jsonl` — one entry per subtask with `{subtask_id, model, start_ts, end_ts, verify_result, cost, files_touched}`. |
| `--dry-run` | Prints execution plan with estimated cost without spawning. Default mode — `--execute` required to actually run. |

**Done when:** `ai-orchestrate run 5 --execute` runs each subtask in a scoped session, halts on verify failure, and produces an orchestration manifest. Re-running after failure resumes from the failed subtask (idempotent).

*Inspired by: Temporal (durable execution with compensation) + Buildkite (pipeline stages with manual gates)*

---

### Milestone 11 — Session Provenance Export
**Goal:** Produce a self-contained, machine-readable provenance record for any completed task — the full chain of evidence from planning through verification — suitable for audit, onboarding, and post-mortems.

| Deliverable | Description |
|---|---|
| `ai-provenance export <task-id>` | Assembles a single JSON document: task definition, session timeline, IR snapshots at session boundaries, model routing decisions, fault signals, verify outcomes, scope drift events, total cost. |
| Provenance schema | Versioned JSON schema (`provenance.v1.schema.json`). Fields: `task`, `sessions[]` (each with `model_turns[]`, `files_touched[]`, `faults[]`, `verify`), `ir_snapshots[]`, `cost_summary`, `outcome`. |
| `--format=markdown` | Renders as structured markdown: Decision Log, Session Timeline, Outcome, Cost Breakdown. Suitable for PR descriptions or post-mortems. |
| Provenance diff | `ai-provenance diff <task-a> <task-b>` — compares two task records: cost, session count, fault frequency, model distribution. |

**Done when:** `ai-provenance export 5 --format=markdown` on a completed multi-session task produces a document a new team member can read to understand what was done, what the AI chose at each branch point, what failed, and what the final verification result was — without access to the original chat transcripts.

*Inspired by: SLSA provenance (supply chain attestation) + Jupyter execution records (reproducible decision trails)*

---

## License

TBD
