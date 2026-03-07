# RunEcho

**F1–F7 complete · Enhancements 1–6 complete · 9 binaries · next: F8 — local result cache**

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
- **Model Enforcer**: PreToolUse hook that denies subagents using the wrong model. If the router said haiku, Claude can't spawn an opus subagent. Agent tool calls (which carry `subagent_type` but no `model` param) are audited rather than enforced — they're logged and allowed through.
- **IR Injector**: On session turn 1, reads `.ai/ir.json` and injects a compact codebase summary — file list + all symbols. Claude starts every session knowing what exists without reading files to orient itself.
- **Stop Checkpoint**: After every Claude response, writes `.ai/checkpoint.json` with turn count, IR hash, and last message. Provides state for failure recovery.
- **Session End**: On session termination, `ai-session-end` runs a seven-stage pipeline: (1) scope-drift detection — compares git-changed files vs. the active task's declared scope, emits `SCOPE_DRIFT` fault if files fall outside it, (2) IR re-index + snapshot + churn cache update, (3) pipeline envelope write if route=pipeline, (4) task verify — emits `VERIFY_FAIL` fault on failure, (5) IR structural diff summary, (6) `ai-session` parses the Claude Code JSONL log for ground-truth facts with `ai-document` update in background, (7) falls back to minimal checkpoint template if JSONL unavailable. `session-end.sh` is now a 4-line exec wrapper identical to `session-governor.sh`.
- **Session Synthesizer** (`ai-session`): Reads the Claude Code JSONL session log, extracts ground-truth facts (files edited/created, commands run, token counts, cost, duration), and calls haiku to summarize the session narrative. Produces `.ai/handoff.md` with factual accuracy — no speculation.
- **Document Generator** (`ai-document`): Auto-generates and updates project documentation using haiku. Which docs are generated is configured via `.ai/document.yaml` (per-project) or `~/.config/runecho/document.yaml` (global); defaults to all three (README.md, TECHNICAL.md, USAGE.md). Change-gated by IR diff — skips entirely if no structural changes and all configured docs already exist.
- **Destructive Bash Guard**: PreToolUse[Bash] hook. Hard-denies catastrophic commands (`rm -rf /`, `mkfs`, fork-bombs). Approval-gates dangerous-but-recoverable patterns: `rm -rf`, `git reset --hard`, `DROP TABLE`, pipe-to-shell installs.
- **Scope Guard**: PreToolUse[Edit|Write] hook. Always blocks writes to settings files, hook files, `.env`, and `*.key`. Optional scope-lock via `.ai/scope-lock.json` — when present, restricts all writes to declared paths only.
- **Constraint Reinjector**: SessionStart hook (matcher: `compact`). Re-injects active constraints after context compaction so BPB rules and routing directives survive a `/compact`.
- **Pre-Compact Snapshot**: PreCompact hook. Captures a session state snapshot immediately before compaction so the reinjector has accurate, current data to work from.
- **Provenance Export** (`ai-provenance`): Assembles a full execution record for any task by joining `.ai/progress.jsonl`, `.ai/faults.jsonl`, `.ai/results.jsonl`, and `.ai/tasks.json`. `export <task-id>` produces a session timeline with turns, cost, IR hashes, drift flags, fault signals, and verify outcomes. `list` shows all tasks with recorded sessions and total cost.

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

Builds nine binaries, symlinks all hooks into `~/.claude/hooks/`, and automatically merges the RunEcho hook configuration into `~/.claude/settings.json`. Idempotent — safe to re-run after updates. Uses `#!/usr/bin/env bash` and `rm -f` + `ln -s` for portable symlink creation on macOS/Linux. Requires Go in PATH.

| Binary | Purpose |
|---|---|
| `ai-ir` | Indexes codebase → `.ai/ir.json`; manages SQLite snapshot history |
| `ai-session` | Parses Claude Code JSONL log → ground-truth session handoff |
| `ai-document` | Auto-generates/updates README.md, TECHNICAL.md, USAGE.md via haiku |
| `ai-task` | Persistent task ledger for cross-session work tracking (`.ai/tasks.json`) |
| `ai-context` | Compiles turn-1 context block (contract + IR + diff + handoff + tasks + review) within a token budget |
| `ai-governor` | Session governor + model router (replaces `session-governor.sh` logic) |
| `ai-pipeline` | Declarative pipeline definitions — `render` (injection text) + `envelope` (execution records) |
| `ai-session-end` | Session-end orchestration pipeline (replaces `session-end.sh` logic) |
| `ai-provenance` | Session provenance export — task timeline, faults, verify outcomes, cost |

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
      },
      {
        "matcher": "",
        "hooks": [{ "type": "command", "command": "bash ~/.claude/hooks/contract-sync.sh", "timeout": 3 }]
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

**SessionEnd pipeline** (`ai-session-end` binary, `session-end.sh` is a 4-line exec wrapper):
1. **Scope-drift detection** — compare `git diff --name-only HEAD` vs. active task's `scope` glob; emit `SCOPE_DRIFT` fault to `faults.jsonl` for out-of-scope files
2. **IR snapshot** — re-index codebase, `ai-ir snapshot --label=session-end`, update `churn-cache.txt`
3. **Pipeline envelope** — if route was `pipeline`, write execution record to `.ai/executions.jsonl` (idempotent)
4. **Task verify** — run active task's `verify` command; emit `VERIFY_FAIL` fault on failure (exit 1); exit 2 = no verify cmd
5. **IR verify summary** — `ai-ir verify` captures structural diff since last snapshot
6. **`ai-session`** (primary) — parse Claude Code JSONL log → ground-truth handoff; fire `ai-document` in background; append `progress.jsonl` record; validate handoff schema
7. **Checkpoint fallback** — if `ai-session` fails or JSONL unavailable, synthesize minimal handoff from `checkpoint.json`

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
ai-task drift-check [--session=<id>] [root]  # intersect IR snapshot diff with task scopes; emit DRIFT_AFFECTED faults
ai-task replan <id> [root]                   # print task scope + IR diff + DRIFT_AFFECTED faults for review
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
│   ├── provenance/main.go      # ai-provenance — task provenance export (sessions, faults, verify, cost)
│   ├── session/main.go         # ai-session — parses JSONL log → ground-truth handoff
│   ├── session-end/main.go     # ai-session-end — session-end orchestration pipeline
│   └── task/main.go            # ai-task — persistent task ledger with dependency graph
├── hooks/
│   ├── session-governor.sh        # UserPromptSubmit — turn count + model routing + fault emission
│   ├── model-enforcer.sh          # PreToolUse[Task] — denies wrong-model subagents
│   ├── ir-injector.sh             # UserPromptSubmit — index + snapshot + ai-context injection
│   ├── contract-sync.sh           # UserPromptSubmit — syncs CONTRACT.yaml task to tasks.json
│   ├── fault-emitter.sh           # Sourced by hooks — emit_fault() writes to .ai/faults.jsonl
│   ├── stop-checkpoint.sh         # Stop — writes .ai/checkpoint.json; emits IR_DRIFT + HALLUCINATION faults
│   ├── session-end.sh             # SessionEnd — 4-line exec wrapper for ai-session-end
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
│   ├── provenance/             # task provenance assembler: Export, List, FormatText
│   ├── schema/                 # canonical Go types for all .ai/ JSONL files (FaultEntry, ProgressEntry, VerifyEntry, Envelope, ClassifierEntry)
│   ├── session/                # session fact extraction, JSONL parser, summarizer, writer; progress + fault writers; drift check
│   ├── sessionend/             # session-end orchestration (all 7 stages); replaces session-end.sh logic
│   ├── snapshot/               # SQLite snapshot store, structural diff engine
│   └── task/                   # Task, TaskDB, Load, Save, MaxID, SortByID — shared task types
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

## Roadmap

Priority order reflects load-bearing dependencies, not feature preference. Each milestone either unblocks the next or delivers direct cost/speed/quality value on its own.

**North star:** speed, quality, cost reduction. Every milestone is evaluated against those three.

---

### F1 — Extract `internal/task` Package ✅

**Completed.** Moved `Task`, `TaskDB`, `Load`, `Save`, `MaxID`, `SortByID` out of `cmd/task/main.go` into `internal/task/`. Zero behavior change. Unblocked F2, F5, and F7 by giving all packages a shared import path for task types.

---

### F2 — Contract → Task Auto-Wire ✅

**Completed.** `ai-task sync [--quiet]` creates a task from `.ai/CONTRACT.yaml` if no matching task exists (idempotent by title). Callable from hooks or manually.

---

### F3 — Token Cost Compression + Context Relevance Scoring ✅

**Completed.** `internal/context/ir.go` rewritten with IDF-weighted scoring, import propagation (call graph edges), test-symbol filtering, and compact directory-grouped flat dump. Measured reduction: flat dump ~64%, prompt mode ~20%. Zero schema changes — all improvements in the scoring and display layer only.

| Deliverable | Outcome |
|---|---|
| IDF-weighted scorer | Terms rare across the IR corpus score higher; common path segments (e.g. `internal`) score lower. Eliminates false-positive file matches. |
| Import propagation | Files imported by high-scoring files receive +2 bonus. Surfaces implementation packages when cmd/* callers match the query. |
| Test-symbol filter | `Test*`/`Benchmark*`/`Example*` stripped from display (retained in IR for `validate-claims`). Removes ~50 test function names from flat dump. |
| Compact flat dump | Post-compact no-prompt mode: directory-grouped summary (17 dirs) vs. former flat path list + all-symbols blob. ~64% size reduction. |
| maxShown 15 → 10 | Tighter prompt-mode output; noise-only entries (no displayable symbols, zero keyword score) suppressed. |

---

### F4 — Migrate `session-end.sh` → Go (`ai-session-end`) ✅

**Completed.** `internal/sessionend/end.go` implements all 7 stages. `session-end.sh` is a 4-line exec wrapper. `install.sh` builds 8 binaries. All session-end logic is typed, testable Go.

---

### F5 — Drift-Aware Task Advisory ✅

**Completed.** `internal/task/drift.go` intersects IR snapshot diffs with task scopes. `internal/session/drift_check.go` runs at session end via `ai-task drift-check`. `DRIFT_AFFECTED` faults are emitted to `faults.jsonl`. `ContractProvider` injects a `DRIFT ADVISORY` block into SESSION CONTRACT at turn 1 when faults exist. `ai-task replan <id>` prints scope + IR diff for human review.

---

### F6 — Schema Stabilization ✅

**Completed.** `internal/schema/` contains canonical types for all five `.ai/` data files: `FaultEntry` + `DriftFaultEntry` (`faults.jsonl`), `ProgressEntry` + `ScopeDrift` (`progress.jsonl`), `VerifyEntry` (`results.jsonl`), `Envelope` + `StageResult` (`executions.jsonl`), `ClassifierEntry` (`classifier.jsonl`). All consumers (`internal/session`, `internal/pipeline`, `internal/governor`, `internal/sessionend`) migrated. No duplicate struct definitions.

---

### F7 — Session Provenance Export ✅

**Completed.** `internal/provenance/provenance.go` is a pure consumer of all five `.ai/` data files. `cmd/provenance/main.go` exposes two subcommands: `export <task-id>` and `list`. `install.sh` builds 9 binaries.

| Subcommand | Description |
|---|---|
| `ai-provenance export <task-id> [--json]` | Task definition + session timeline (turns, cost, IR hashes, drift, faults, verify outcome). Text default; `--json` for machine-readable output. |
| `ai-provenance list [--json]` | All tasks that have at least one recorded session, with session count and total cost. |

*Inspired by: SLSA provenance (supply chain attestation), Jupyter execution records*

---

### F8 — Local Result Cache

**Goal:** Hash `(ir_snapshot_id + prompt_hash + model)` → reuse result for identical analysis tasks. Avoid repeated model calls when inputs haven't changed.

**Why after F7:** Only pays off once orchestration exists and identical analysis tasks run repeatedly. Building a cache before you have repeated callers is premature optimization.

| Deliverable | Description |
|---|---|
| sqlite cache table | Key `(ir_hash, prompt_hash, model)` → `result TEXT, created_at`. One table in existing sqlite db — no new dep. |
| Cache read/write in `ai-context` | Before calling the model, check cache. On miss, call and write result. TTL: invalidate on IR hash change. |
| `ai-context --no-cache` | Escape hatch to bypass for debugging. |

**Done when:** Identical `ai-context compile` calls on unchanged IR return cached result; `ai-ir diff` output changing invalidates the cache.

---

### Deferred

**Fast-Loop CLI (`just`)** — a `justfile` in the repo root with a `run` recipe (`ai-context compile && ai-pipeline exec && ai-task verify`) gives a single `just run` entry point. Not a Go binary. `winget install Casey.Just`. Add after F3 when the context/pipeline commands are stable.

**MCP Tool Server** ✅ — `internal/mcp/` + `cmd/mcp-server/`. 7 tools: `runecho_task_list`, `runecho_task_next`, `runecho_task_update`, `runecho_session_status`, `runecho_fault_list`, `runecho_provenance_export`, `runecho_context_compile`. Registered in `~/.claude/settings.json` via `install.sh`.

**Orchestration Prototype** *(Stage C entry)* — `ai-orchestrate <task-id>` decomposes a task into subtasks with model assignments and file scopes. Requires MCP or equivalent stable tool interface. Deferred.

**Supervised Subtask Execution** — `ai-orchestrate run` spawns Claude Code sessions per subtask with mandatory verify gates. Requires Orchestration Prototype. Deferred.

*Inspired by: Temporal (durable execution), Dagger (typed pipeline objects), Taskfile (DAG task deps)*

---

## Enhancement Backlog

Candidates identified through codebase review and architecture analysis. Prioritized by impact-to-effort ratio. Effort: S = hours, M = 1–3 days, L = 1 week+.

| Priority | Name | Effort | Value driver |
|---|---|---|---|
| 1 ✅ | Hook Reliability Gate | S | Fixes active pain — silent hook failures |
| 2 ✅ | Hook Latency Telemetry | S | Makes hook performance visible; pairs with #1 |
| 3 ✅ | Classifier Route Caching | S | Saves 0.5–1s per turn; LRU cache for haiku routes |
| 4 ✅ | Claude Code Skills Integration | S | Zero-code DX win — slash commands for every binary |
| 5 ✅ | Context Window Pressure Detection | S | Prevents silent context failure mid-task |
| 6 ✅ | Fault-Driven Test Generation | S | Closes verify loop — injects failure context for next session |
| 7 ✅ | MCP Tool Server | M | Highest leverage — structured LLM-native interface |
| 8 | Cost Attribution per Hook/Task | M | Unblocks F8 targeting; makes costs actionable |
| 9 | Classifier Feedback Loop | M | Self-improving routing accuracy |
| 10 | Multi-Session Trend Analysis | M | Early warning on efficiency degradation |
| 11 | Drift-Aware IR Caching | M | O(changed files) re-indexing; 5–10x faster on large repos |
| 12 | Hook Test Suite + Simulation Harness | M | Regression safety net for the weakest layer |
| 13 | Symbol Stability Index | S | Low effort; surfaces architectural debt proactively |
| 14 | Git Pre-Commit IR Validation | S | Structural integrity enforcement at commit time |
| 15 | Language Parser Extensions (Rust + Java) | M | Extends IR coverage; write-once, high polish |
| 16 | Symbol Provenance Lineage | M | Tracks which task introduced each symbol; `ai-ir symbol-lineage` |
| 17 | Prompt Quality Anomaly Detector | M | Detects circular exploration before turns are wasted |
| 18 | Cross-Project IR Federation | M | Monorepo symbol search across package boundaries |
| 19 | Symbol-Level IR Diff + Impact Analysis | L | Next-level drift detection; feeds context scoring |
| 20 | Semantic Handoff Search + Replay | L | Searchable knowledge base from ground-truth handoffs |

---

### 1. Hook Reliability Gate — S

Add a `HOOK_FAILURE` fault signal + timeout/recovery wrapper so sessions continue when hooks fail. Today a 5s timeout stalls the entire session; hooks fail silently on jq parse errors or missing binaries. Recovery queues faults for next turn and logs which binary failed.

Touches: `internal/schema/faults.go` (new signal), `internal/governor/fault.go`, all shell hooks (retry wrapper).

---

### 2. Hook Latency Telemetry — S

Record each hook's execution time, exit code, and output size in `.ai/hooks.jsonl`. Emit `HOOK_SLOW` and `HOOK_FAILED` fault signals when hooks exceed thresholds. Makes the invisible hook chain observable — currently there's no way to know which hook is adding latency to turn 1.

Touches: `hooks/fault-emitter.sh` (add `emit_hook_latency()`), all hooks (wrap with timing), new `HookEntry` type in `internal/schema/`.

---

### 3. Classifier Route Caching — S

LRU cache for haiku classification results, keyed by prompt fingerprint. Cache hits skip the 0.5–1s API call and return the route instantly. Most sessions see 15–20 structurally similar prompts. Track hit rate in `classifier-log.jsonl` via a `cache_hit` field.

Touches: `internal/governor/classifier.go` (add `PromptFingerprint()`, cache get/set), `internal/governor/state.go` (persist cache in state dir), `internal/schema/classifier.go` (extend with `cache_hit`).

---

### 4. Claude Code Skills Integration — S

Ship RunEcho as a set of Claude Code skills — YAML files in `~/.claude/skills/` that users invoke as `/ai-review`, `/ai-cost`, `/ai-scope`, `/ai-drift`, `/ai-classify`. Each skill wraps an existing binary call with natural-language framing. No code changes to existing binaries; install.sh symlinks them.

Touches: new `skills/` directory with YAML files, `install.sh` (add symlink step).

---

### 5. Context Window Pressure Detection — S

Measure remaining context capacity by tracking cumulative token usage and projected future turns. Emit `WINDOW_PRESSURE` fault when free space falls below threshold (10% default). Early warning allows preemptive `/compact` before quality degrades silently mid-task.

Touches: `internal/governor/cost.go`, `internal/governor/fault.go`, `internal/schema/faults.go`.

---

### 6. Fault-Driven Test Generation — S

When `VERIFY_FAIL` is emitted at session end, capture the verify command's stderr and git diff summary, then inject a `TEST_FAILURE_ADVISORY` block into the next session's context. Haiku can generate minimal failing test cases or reproduction steps without re-diagnosing from scratch.

Touches: `internal/sessionend/stages.go` (capture verify stderr), `internal/context/` (new `VerifyFailureProvider`), `internal/schema/results.go` (extend VerifyEntry with `Stderr`, `Stdout`).

---

### 7. MCP Tool Server — M ✅

MCP stdio server (`ai-mcp-server`) exposes 7 RunEcho tools to Claude Code natively — structured JSON I/O, no shell parsing, no subagent spawning. JSON-RPC 2.0 over stdin/stdout; ~250 lines of stdlib, no external deps.

**Tools:** `runecho_task_list`, `runecho_task_next`, `runecho_task_update`, `runecho_session_status`, `runecho_fault_list`, `runecho_provenance_export`, `runecho_context_compile`.

**Files added:** `internal/mcp/` (protocol, registry, server, 4 tool files, tests), `cmd/mcp-server/main.go`. `install.sh` builds the binary and registers `mcpServers.runecho` in `~/.claude/settings.json`.

---

### 8. Cost Attribution per Hook/Task — M

Tag session costs by hook and task so `ai-provenance export` shows breakdowns: "$0.30 on IR indexing, $0.40 on doc gen." Currently cost is a session-level lump sum. Identifies outliers and targets optimization work.

Touches: `internal/schema/progress.go` (add `CostBreakdown map[string]float64`), `internal/session/writer.go`, `internal/governor/cost.go`, `cmd/provenance/main.go`.

---

### 9. Classifier Feedback Loop — M

Post-session, read verify outcomes and handoff quality to score whether the routing classification was correct. Accumulate per-route accuracy in `classifier-log.jsonl`. Adjust confidence thresholds over time; emit `CLASSIFIER_DRIFT` fault when accuracy degrades.

Touches: `internal/governor/classifier.go`, `internal/schema/classifier.go`, `internal/session/review.go`.

---

### 10. Multi-Session Trend Analysis — M

Aggregate `progress.jsonl` across 10+ sessions to compute moving averages (cost/session, turns/task, file churn/session). Emit `TREND_ALERT` fault when a metric exceeds ±20% of its 5-session average. Detects efficiency degradation before it compounds.

Touches: `internal/session/review.go`, `internal/governor/fault.go`, `internal/schema/progress.go`.

---

### 11. Drift-Aware IR Caching — M

Cache parsed IR results per file, keyed by content hash. Incremental re-indexing skips unchanged files — O(changed) instead of O(all). On large repos, turn-1 `ai-ir` runs drop from seconds to milliseconds. Invalidate on `git diff` detecting changes to a file.

Touches: `internal/ir/generator.go` (add `FileHashCache`, `incrementalParse()`), `internal/ir/types.go` (extend IR with hash metadata), `internal/snapshot/` (git hash as cache invalidation signal).

---

### 12. Hook Test Suite + Simulation Harness — M

Zero hook tests exist. Add a Go test driver that simulates Claude Code hook inputs (mock `.ai/` structure, fixture sessions) and validates fault output, turn counter increment, and route persistence. Catches regressions in 2 min vs. 20-min manual session cycles.

Touches: new `hooks/test/` fixtures, `cmd/hooks-test/main.go`, refactor hook logic into `internal/hooks/` thin wrappers.

---

### 13. Symbol Stability Index — S

Track each symbol's churn rate across IR snapshot history. Flag high-churn symbols (refactored 5+ times) and inject a `CHURN ADVISORY` block into SESSION CONTRACT when the active task's scope touches them. Surfaces architectural debt proactively.

Touches: `internal/snapshot/churn.go`, `internal/schema/progress.go`, `internal/context/review_provider.go`.

---

### 14. Git Pre-Commit IR Validation — S

Install a `.git/hooks/pre-commit` that runs `ai-ir verify` and blocks commits when structural diff exceeds configured thresholds — new exports without task records, symbol deletion without scope tracking. Transforms RunEcho from session observer to structural integrity enforcer.

Touches: `install.sh` (add git hook symlink), new `scripts/pre-commit`.

---

### 15. Language Parser Extensions (Rust + Java) — M

Add AST-based parsers for `.rs` and `.java` to extend IR coverage beyond JS/TS/Go/Python. Parsers are write-once; no schema changes. Register via the existing `Parser` interface in `NewGenerator()`.

Touches: `internal/parser/rust.go`, `internal/parser/java.go`, `internal/ir/generator.go`.

---

### 16. Symbol Provenance Lineage — M

Track which task and session introduced each symbol (function, type, constant). Link symbol definitions to their task ID and verify outcome. New `ai-ir symbol-lineage <symbol>` subcommand enables "who introduced this?" analysis — saves 10+ minutes of git archaeology per multi-session debugging incident.

Touches: `internal/ir/types.go` (extend Symbol with `IntroducedBy`, `VerifyStatus`), `internal/snapshot/diff.go` (compute provenance), `cmd/ir/main.go` (new subcommand).

---

### 17. Prompt Quality Anomaly Detector — M

Analyze `classifier-log.jsonl` and session progress to detect anomalous prompt patterns — circular exploration, rapid successive similar queries, very short/long prompts with low semantic diversity. Emit `PROMPT_ANOMALY` fault to surface debugging loops before they waste 5–10 turns.

Touches: new `internal/anomaly/detector.go`, `internal/governor/fault.go`, `internal/schema/classifier.go`.

---

### 18. Cross-Project IR Federation — M

Index linked projects (monorepo or workspace) into a single federated IR with namespace prefixes, enabling symbol search across package boundaries. Surfaces symbols from all linked projects in a single injection — eliminates "missing symbol" issues in multi-repo work.

Touches: `internal/ir/generator.go`, `internal/ir/storage.go`, `internal/context/ir.go`, `.ai/ir.json` schema.

---

### 19. Symbol-Level IR Diff + Impact Analysis — L

`ai-ir diff` shows which files changed. This adds which files *reference* changed symbols — import-graph propagation for scope validation and context relevance. `DRIFT_AFFECTED` faults become symbol-aware, not just file-aware. Context relevance scoring in `internal/context/ir.go` gets a boost for files that reference changed symbols.

Touches: `internal/snapshot/snapshot.go`, `internal/ir/`, `internal/task/drift.go`, `internal/context/ir.go`.

---

### 20. Semantic Handoff Search + Replay — L

Index all `.ai/handoff.md` files across sessions into SQLite FTS (TF-IDF). Allow Claude to query similar past sessions (`/ai-review search "auth refactor"`) to surface what worked before. Reframes handoffs from one-shot summaries to a searchable knowledge base — the data already exists and is ground-truth.

Touches: new `internal/search/semantic.go`, `internal/snapshot/db.go` (add FTS table), `cmd/session/main.go`.

---

*Inspired by: Temporal (durable execution), Dagger (typed pipeline objects), Taskfile (DAG task deps), SLSA provenance (supply chain attestation), Jupyter execution records*

---

## License

MIT — see [LICENSE](LICENSE)
