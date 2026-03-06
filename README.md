# RunEcho

**Status:** Stages A–C Complete ✅ · M1–M5 ✅ · M8 (verify loop) ✅ · next: F1 — extract `internal/task` package

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
- **Document Generator** (`ai-document`): Auto-generates and updates project documentation using haiku. Which docs are generated is configured via `.ai/document.yaml` (per-project) or `~/.config/runecho/document.yaml` (global); defaults to all three (README.md, TECHNICAL.md, USAGE.md). Change-gated by IR diff — skips entirely if no structural changes and all configured docs already exist.
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

Builds six binaries, symlinks all hooks into `~/.claude/hooks/`, and automatically merges the RunEcho hook configuration into `~/.claude/settings.json`. Idempotent — safe to re-run after updates. Uses `#!/usr/bin/env bash` and `rm -f` + `ln -s` for portable symlink creation on macOS/Linux. Requires Go in PATH.

| Binary | Purpose |
|---|---|
| `ai-ir` | Indexes codebase → `.ai/ir.json`; manages SQLite snapshot history |
| `ai-session` | Parses Claude Code JSONL log → ground-truth session handoff |
| `ai-document` | Auto-generates/updates README.md, TECHNICAL.md, USAGE.md via haiku |
| `ai-task` | Persistent task ledger for cross-session work tracking (`.ai/tasks.json`) |
| `ai-context` | Compiles turn-1 context block (contract + IR + diff + handoff + tasks + review) within a token budget |
| `ai-governor` | Session governor + model router (replaces `session-governor.sh` logic) |
| `ai-pipeline` | Declarative pipeline definitions — `render` (injection text) + `envelope` (execution records) |

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
4. **`ai-pipeline envelope`** (M5) — if route was `pipeline`, write an execution record to `.ai/executions.jsonl` (idempotent)
5. **`ai-session`** (primary) — parse Claude Code JSONL log → ground-truth handoff with real facts: files edited, commands run, token counts, cost, duration
6. **Checkpoint fallback** — if `ai-session` fails or JSONL unavailable, synthesize minimal handoff from `checkpoint.json`
7. **`ai-document`** — if IR diff exists, update project docs via haiku (non-fatal, runs in background)

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
ai-document [root]                              # read config, skip if no changes
ai-document --ir-diff="<diff>" [root]           # use IR diff to update existing docs
ai-document --docs=README.md,TECHNICAL.md [root] # override configured doc list
ai-document --dry-run [root]                    # preview without writing
ai-document --force [root]                      # bypass change-gate, regenerate all
```

**Doc config hierarchy:**
1. `{root}/.ai/document.yaml` — per-project override
2. `~/.config/runecho/document.yaml` — global user default
3. Fallback: all three docs (README.md, TECHNICAL.md, USAGE.md)

Format:
```yaml
docs:
  - README.md
  - TECHNICAL.md
  - USAGE.md
```

**Change-gate:** if all configured docs exist and `--ir-diff` is empty, generation is skipped. Pass `--force` to override. The gate prevents unnecessary API calls when nothing structural changed.

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
│   ├── governor/main.go        # ai-governor — session governor + model router
│   ├── ir/main.go              # ai-ir — indexes codebase, manages snapshot history
│   ├── pipeline/main.go        # ai-pipeline — declarative pipeline render + envelope subcommands
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
│   ├── governor/               # session governor logic, model router, fault emission
│   ├── ir/                     # IR types, generator, hasher, storage
│   ├── parser/                 # language parsers (JS/TS/Go/Python); extensible via Parser interface
│   ├── pipeline/               # Pipeline/Stage/Envelope types; Load, RenderText, AppendEnvelope, FaultsForSession
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

**M4.5-A — POSIX Audit + install.sh Symlink Fix ✅**
- All hook shebangs updated to `#!/usr/bin/env bash`
- `install.sh`: portable symlink creation (`rm -f` + `ln -s`); `#!/usr/bin/env bash`
- `scope-guard.sh`: removed unused variable (SC2034); fixed unquoted pattern expansion (SC2295)
- All hooks pass `shellcheck` with zero warnings
- `ai-document`: removed hardcoded work/personal path detection; doc list now driven by `.ai/document.yaml` config hierarchy

**M4.5-B — session-governor.sh → Go Binary ✅**
- `ai-governor` binary (`cmd/governor/`, `internal/governor/`): turn counter, weighted routing, session cost from JSONL, LLM classifier, regex fallback, fault emission — all in typed, testable Go
- `session-governor.sh` reduced to a 4-line exec wrapper (`exec ai-governor`)
- No more `date +%s%3N` (macOS), `awk`, `jq`, or `curl` in the governor path
- `install.sh` builds `ai-governor` alongside the other 5 binaries

**M5 — Pipeline Definitions + Execution Envelopes ✅**
- `internal/pipeline/` package: `Pipeline`, `Stage`, `Envelope`, `StageResult` types; `Load`/`LoadNamed` with `default.yaml` fallback to `DefaultPipeline()`; `RenderText`; `AppendEnvelope` (idempotent by `session_id`); `FaultsForSession`
- `ai-pipeline render [--pipeline=default] [root]` — prints MODEL ROUTER injection text from YAML definition; output matches hardcoded `RoutePipeline` text for `default.yaml`
- `ai-pipeline envelope --session=<id> ...` — writes execution record to `.ai/executions.jsonl`; reads `ir_hash_start` from `checkpoint.json` and `ir_hash_end` from `ir.json` if not supplied; idempotent
- Governor: `getRouteText(cwd, route)` loads pipeline YAML for `RoutePipeline` and renders dynamically; falls back to hardcoded text on load failure (never blocks session)
- `session-end.sh`: calls `ai-pipeline envelope` after snapshot when `route == pipeline`
- Pipeline definitions: `.ai/pipelines/*.yaml` — adding a pipeline is a YAML edit, not a bash edit
- `install.sh` builds `ai-pipeline` alongside the other 6 binaries

**M8 — Outcome Verification Loop ✅** *(completed out of order; F-series numbering below)*
- `internal/session/verify.go`: `VerifyEntry` type, `AppendVerify`/`ReadVerify` — append-only `.ai/results.jsonl`, idempotent by `(task_id, session_id)`
- `ai-task verify <id> [--session=<sid>] [root]` — runs task's `verify` command, writes structured result to `results.jsonl`; exit 0/1/2
- `session-end.sh`: runs verify on active task at session end; emits `VERIFY_FAIL` fault signal on failure
- `fault-emitter.sh`: added `SCOPE_DRIFT` and `VERIFY_FAIL` to signal taxonomy
- `internal/session/review.go`: joins `progress.jsonl` entries with `results.jsonl` to surface verify outcome per session

---

## Roadmap

Priority order reflects load-bearing dependencies, not feature preference. Each milestone either unblocks the next or delivers direct cost/speed/quality value on its own.

**North star:** speed, quality, cost reduction. Every milestone is evaluated against those three.

---

### Architectural Notes (2026-03-06)

**Key structural gap identified:** `Task` type is defined as a local struct inside `cmd/task/main.go`. No other package can import it. This means `internal/session`, `internal/context`, and `internal/governor` cannot join task data with sessions, IR, or faults. Drift detection, cross-session review enrichment, and provenance all require this join. **F1 fixes this before anything else.**

**MCP deferred.** The value of an MCP tool server is exposing a stable API. Building it before the data model (types, schema) is stable means versioning pain. Requeued after F6 (schema stabilization).

**session-end.sh complexity ceiling.** At 251 lines it already handles verify, scope-drift, handoff generation, fault emission, pipeline envelope, and doc update. Before F5 and F6 add more consumers of session-end data, migrate to a Go binary (F4). The pattern is proven — `session-governor.sh` → `ai-governor` was the same move.

---

### F1 — Extract `internal/task` Package ← **START HERE**

**Goal:** Move `Task`/`TaskDB` types out of `cmd/task/main.go` and into `internal/task/types.go` so every binary can import them.

**Why foundational:** Without this, `internal/session/review.go` can't join progress entries with tasks, the context compiler can't score tasks by IR relevance, drift detection can't read task scopes, and provenance can't assemble task-centric records. Everything downstream is blocked or must duplicate the type.

| Deliverable | Description |
|---|---|
| `internal/task/types.go` | `Task`, `TaskDB`, `TaskStatus` types extracted from `cmd/task/main.go` |
| `cmd/task/main.go` updated | Imports `internal/task`; zero behavior change |
| Load/save helpers | `LoadTasks(root string)`, `SaveTasks(root string, db TaskDB)` moved to `internal/task/store.go` |

**Done when:** `go build ./...` passes; `internal/session` and `internal/context` can import `internal/task` without circular deps.

**Effort:** ~1 hour. Zero behavior change.

---

### F2 — Contract → Task Auto-Wire

**Goal:** When a session starts with `CONTRACT.yaml` present, ensure a corresponding task entry exists in `tasks.json` automatically. Eliminates the manual `ai-task add` step that is never done consistently.

**Why now:** The verify loop (M8), drift advisory (F5), and session review (M4) all depend on tasks being populated. Currently `ai-task list` returns empty on every project. The system cannot make routing or drift decisions on empty data.

| Deliverable | Description |
|---|---|
| Auto-create logic | `ir-injector.sh` (or a new turn-1 hook): if `CONTRACT.yaml` exists and no matching task exists, call `ai-task add` with contract title + scope + verify |
| Idempotency | Match on title; skip if task already exists. Never create duplicates. |
| `ai-task sync-contract [root]` | Explicit subcommand for the same logic; callable manually or from hooks |

**Done when:** Opening Claude Code in a project with `CONTRACT.yaml` automatically results in a task entry. `ai-task next` returns the contract's task without any manual steps.

---

### F3 — Token Cost Compression

**Goal:** IR currently dumps full symbol lists. Compress to signatures, types, and call relationships so turn-1 token cost drops materially while model focus improves.

**Why now:** Direct cost/speed lever. This changes the shape of IR output — do it before F5 (drift advisory) and F7 (provenance) build consumers against the IR format.

| Deliverable | Description |
|---|---|
| IR summarization mode | `ai-ir` new flag `--summary` or `ai-context` compression pass: emit `func Name(args) ReturnType` signatures instead of full symbol metadata |
| Call graph extraction | Where parsers support it (Go, JS), emit top-level call relationships as edges: `{caller, callee}`. Enables the model to reason about impact without reading files. |
| Token budget enforcement | `ai-context --budget=<n>` already exists; wire compressed IR as the default provider output |
| Benchmarks | Before/after token counts for a representative project. Target: ≥40% reduction in IR block size. |

**Done when:** Turn-1 IR block for this repo is ≥40% smaller than current output. `ai-session review` confirms no increase in file-read tool calls (proxy for model disorientation).

---

### F4 — Migrate `session-end.sh` → Go (`ai-session-end`)

**Goal:** Replace the 251-line bash orchestrator with a typed, testable Go binary. Same behavior, better foundation for F5/F6 which need programmatic access to session-end logic.

**Why now:** Every new feature (drift advisory, schema joins, provenance hooks) wants to run at session end. Adding them to bash means more fragile string handling and untestable logic. The `ai-governor` migration proved the pattern works.

| Deliverable | Description |
|---|---|
| `cmd/session-end/main.go` | Reads session-end event JSON from stdin; orchestrates: scope-drift → verify → handoff → progress → fault emission → pipeline envelope → doc update |
| `session-end.sh` → 4-line wrapper | `exec ai-session-end` — identical to how `session-governor.sh` works now |
| `internal/session/end.go` | Core logic package, fully unit-testable |
| Parity test | Run old and new side-by-side on a real session; compare `progress.jsonl` and `handoff.md` output |

**Done when:** `session-end.sh` is under 10 lines; all session-end logic has unit tests; parity verified against bash version.

---

### F5 — Drift-Aware Task Advisory

**Goal:** When IR structural changes intersect active task scopes between sessions, surface a `DRIFT_AFFECTED` advisory at turn 1. The model starts the session knowing which tasks need re-evaluation — without the developer having to notice the drift manually.

**Requires:** F1 (task types importable), F3 (IR shape stable post-compression).

| Deliverable | Description |
|---|---|
| Task impact analysis | Intersect `task.scope` globs with IR diff changed-file set. Tasks with overlap → `DRIFT_AFFECTED` fault in `faults.jsonl` |
| Turn-1 advisory injection | `SESSION CONTRACT` block gains a `DRIFT ADVISORY` section when affected tasks exist: lists tasks, changed files, whether `verify` paths are still valid |
| `ai-task replan <id>` | Prints task scope alongside the IR diff. Human confirms or adjusts. No automatic mutation. |

**Done when:** After moving `internal/session/` to `internal/session/v2/`, the next session on a session-scoped task sees a drift advisory naming the moved package. Unaffected tasks show no advisory.

*Inspired by: Renovate/Dependabot (automated impact detection), Pants/Buck2 (target-level invalidation)*

---

### F6 — Schema Stabilization

**Goal:** Canonical Go types for all five JSONL data files in a shared package. Currently `ProgressEntry` and `VerifyEntry` live in `internal/session`; `Task`/`TaskDB` live in `cmd/task` (fixed by F1); `FaultEntry` and `Envelope` are not in any shared package. Provenance (F7) needs to join all five.

| Deliverable | Description |
|---|---|
| `internal/schema/` package | Canonical types: `ProgressEntry`, `VerifyEntry`, `FaultEntry`, `Envelope`, `Task` — one package, all consumers import it |
| Migration | `internal/session`, `internal/pipeline`, `internal/governor` updated to use `internal/schema` types |
| Schema versioning | Each type carries a `SchemaVersion string` field. Readers skip lines with unknown versions rather than failing. |

**Done when:** `go build ./...` passes with all types in `internal/schema`; no duplicated struct definitions across packages.

---

### F7 — Session Provenance Export

**Goal:** `ai-provenance export <task-id>` produces a complete, machine-readable execution record for any task — full chain of evidence from planning through verification. Pure consumer of F1–F6; build last.

| Deliverable | Description |
|---|---|
| `ai-provenance export <task-id>` | Assembles single JSON document: task definition, session timeline, IR snapshots at boundaries, routing decisions, fault signals, verify outcomes, scope drift events, total cost |
| `--format=markdown` | Structured markdown: Decision Log, Session Timeline, Outcome, Cost Breakdown. Suitable for PR descriptions or post-mortems. |
| `ai-provenance diff <task-a> <task-b>` | Compare two task records: cost, session count, fault frequency, model distribution |

**Done when:** `ai-provenance export <id> --format=markdown` on a completed multi-session task produces a document readable without access to the original chat transcript.

*Inspired by: SLSA provenance (supply chain attestation), Jupyter execution records*

---

### Deferred

**MCP Tool Server** — expose RunEcho capabilities as MCP tools (task/list, ir/diff, session/review, context/compile). Deferred until F6 schema stabilization. Building an API before the data model is stable means versioning pain. High value but not foundational.

**Orchestration Prototype** *(Stage C entry)* — `ai-orchestrate <task-id>` decomposes a task into subtasks with model assignments and file scopes. Requires MCP or equivalent stable tool interface. Deferred.

**Supervised Subtask Execution** — `ai-orchestrate run` spawns Claude Code sessions per subtask with mandatory verify gates. Requires Orchestration Prototype. Deferred.

*Inspired by: Temporal (durable execution), Dagger (typed pipeline objects), Taskfile (DAG task deps)*

---

## License

TBD
