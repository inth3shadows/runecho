# RunEcho

**Status:** Stage C Active ✅

RunEcho is a session governance, model routing, and structural grounding layer for Claude Code. It enforces cost-optimal model selection, session discipline, and injects codebase structure at session start so Claude operates with accurate structural awareness.

---

## What It Does

- **Session Governor**: Tracks turn count per session. Warns at turn 15, 25, and 35. Tells Claude to wrap up before context degrades and cache costs compound.
- **Model Router**: Classifies each prompt via a haiku LLM call and injects routing guidance — haiku for cheap tasks, opus for architecture, full pipeline (haiku→opus→sonnet) for multi-step work. Falls back to regex if classifier is unavailable.
- **Model Enforcer**: PreToolUse hook that denies subagents using the wrong model. If the router said haiku, Claude can't spawn an opus subagent.
- **IR Injector**: On session turn 1, reads `.ai/ir.json` and injects a compact codebase summary — file list + all symbols. Claude starts every session knowing what exists without reading files to orient itself.
- **Stop Checkpoint**: After every Claude response, writes `.ai/checkpoint.json` with turn count, IR hash, and last message. Provides state for failure recovery.
- **Session End**: On session termination, synthesizes `.ai/handoff.md` from the checkpoint if one wasn't already written. No-ops if Claude already wrote a proper handoff.

Together these enforce the cost model: **Haiku = eyes, Sonnet = hands, Opus = brain.**

---

## Install

```bash
bash install.sh
```

Builds the `ai-ir` binary and symlinks all hooks into `~/.claude/hooks/`. Requires Go in PATH.

---

## Settings

Full `~/.claude/settings.json` hook configuration:

```json
{
  "model": "sonnet",
  "hooks": {
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

Claude Code supports one auth method at a time — OAuth (claude.ai) or API key. If you need both a corporate LiteLLM proxy and a personal Claude Pro subscription on the same machine, a naive `claude /logout` before each work session breaks things: `/logout` resets `hasCompletedOnboarding: false` in `~/.claude.json`, causing the login selector to appear on every subsequent launch even with `ANTHROPIC_API_KEY` correctly set.

**The fix:** a PowerShell switcher that atomically swaps `~/.claude/credentials.json` (the OAuth token file) and sets env vars. No manual login/logout steps.

```powershell
claude-profile work      # stashes OAuth token, sets ANTHROPIC_API_KEY + ANTHROPIC_BASE_URL
claude-profile personal  # restores OAuth token, clears env vars
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

---

## Session Warnings

| Turn | Message |
|---|---|
| 15 | Consider wrapping up or /compact |
| 25 | Finish current task, suggest new session |
| 35 | Session degraded — wrap up now, write handoff |

---

## IR Injection

**No manual indexing needed.** `ir-injector.sh` runs `ai-ir` automatically on session turn 1.

On every new Claude Code session in a JS/TS/Go project, turn 1 will include:

```
IR CONTEXT [root_hash: abc123456789...]:
3 files — src/auth.ts, src/user.ts, utils/helpers.js
Symbols (12): AuthService, UserService, fetchUser, validateToken, ...
```

Subsequent turns: silent. Unsupported projects: silent.

To manually force a re-index:

```bash
ai-ir              # current directory
ai-ir /path/to/project
```

---

## Session Handoff

Closes the cross-session continuity gap.

**Normal path:** at turn 35, the governor instructs Claude to write `.ai/handoff.md`. On the next session's turn 1, the IR injector injects it after the IR block.

**Failure recovery path:** if the session terminates before Claude writes a handoff (crash, kill, early exit), `session-end.sh` synthesizes a minimal `.ai/handoff.md` from `checkpoint.json`. The checkpoint is written after every response by `stop-checkpoint.sh`, so state is never older than one turn.

**Triggers (Claude-written handoff):**
- Auto: governor fires at turn 35 with `ACTION REQUIRED: Write session handoff now.`
- Manual: type `write handoff` — routes to haiku

**Files:**
- `.ai/handoff.md` — current handoff (written by Claude or synthesized from checkpoint)
- `.ai/checkpoint.json` — updated after every response; used by session-end.sh for recovery

**Canonical format (Claude-written):**

```markdown
# Session Handoff
**Date:** 2026-03-01T14:30:00-06:00
**IR snapshot:** a1b2c3d4e5f6
**Session length:** ~35 turns

## Accomplished
## Decisions
## In Progress
## Blocked
## Next Steps
## Notes
```

---

## Verify

```bash
# Hooks installed
ls -la ~/.claude/hooks/

# Governor + classifier fallback (no key = regex fires)
RUNECHO_CLASSIFIER_KEY="" \
  echo '{"session_id":"test","prompt":"architect the system"}' | bash hooks/session-governor.sh
# Expected: "Deep reasoning task" (opus via regex)

# Stop checkpoint write
STATE_DIR="$HOME/.claude/hooks/.governor-state"
mkdir -p "$STATE_DIR" && echo "5" > "$STATE_DIR/test-ck"
echo '{"session_id":"test-ck","cwd":"'$PWD'","last_assistant_message":"done"}' | bash hooks/stop-checkpoint.sh
cat .ai/checkpoint.json

# SessionEnd synthesize (no existing handoff)
rm -f .ai/handoff.md
echo '{"session_id":"test-ck","cwd":"'$PWD'","reason":"other"}' | bash hooks/session-end.sh
cat .ai/handoff.md

# Cleanup
rm -f .ai/handoff.md .ai/checkpoint.json "$STATE_DIR/test-ck"
```

---

## Repo Structure

```
.ai/
├── cmd/ir/main.go              # ai-ir CLI binary (generates .ai/ir.json)
├── hooks/
│   ├── session-governor.sh     # UserPromptSubmit — turn count + model routing (classifier + regex)
│   ├── model-enforcer.sh       # PreToolUse — denies wrong-model subagents
│   ├── ir-injector.sh          # UserPromptSubmit — injects IR summary on turn 1
│   ├── stop-checkpoint.sh      # Stop — writes .ai/checkpoint.json after each response
│   └── session-end.sh          # SessionEnd — synthesizes handoff from checkpoint on termination
├── install.sh                  # Builds ai-ir, symlinks hooks to ~/.claude/hooks/
├── internal/
│   ├── ir/                     # IR types, generator, hasher, storage
│   └── parser/                 # JS/TS + Go parsers
└── README.md
```

---

## Roadmap

**A — Session Discipline ✅**
- Session governor (turn limits, warnings)
- Regex model router
- Model enforcer (PreToolUse gate)

**B — Structural Intelligence ✅**
- `ai-ir` CLI: generates `.ai/ir.json` for JS/TS/Go projects
- IR injector: auto-index + inject codebase summary on turn 1
- Incremental updates, routing audit

**C — Intent-Aware Routing + Failure Recovery ✅**
- LLM classifier (haiku, 2s timeout) replaces regex as primary router
- Regex fallback — zero regression on key absence or API failure
- Stop checkpoint: turn-level state persistence after every response
- SessionEnd recovery: synthesizes handoff from checkpoint on abnormal termination

**D — Multi-Agent Orchestrator (future)**
- Claude-native agent framework built on the IR + governance layer
- Fingerprint-gated task routing: tasks matched to agents by codebase context
- Cost-aware orchestration: enforce haiku/sonnet/opus budgets at pipeline level

---

## License

TBD
