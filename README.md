# RunEcho

**Status:** Stage B Active ✅

RunEcho is a session governance, model routing, and structural grounding layer for Claude Code. It enforces cost-optimal model selection, session discipline, and injects codebase structure at session start so Claude operates with accurate structural awareness.

---

## What It Does

- **Session Governor**: Tracks turn count per session. Warns at turn 15, 25, and 35. Tells Claude to wrap up before context degrades and cache costs compound.
- **Model Router**: Analyzes each prompt and injects routing guidance — haiku for cheap tasks, opus for architecture, full pipeline (haiku→opus→sonnet) for multi-step work.
- **Model Enforcer**: PreToolUse hook that denies subagents using the wrong model. If the router said haiku, Claude can't spawn an opus subagent.
- **IR Injector**: On session turn 1, reads `.ai/ir.json` and injects a compact codebase summary — file list + all symbols. Claude starts every session knowing what exists without reading files to orient itself.

Together these enforce the cost model: **Haiku = eyes, Sonnet = hands, Opus = brain.**

---

## Install

```bash
bash install.sh
```

This builds the `ai-ir` binary and symlinks all hooks into `~/.claude/hooks/`. Requires Go in PATH.

---

## Settings

Add to `~/.claude/settings.json`:

```json
{
  "model": "sonnet",
  "hooks": {
    "UserPromptSubmit": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "bash ~/.claude/hooks/session-governor.sh",
            "timeout": 5
          }
        ]
      },
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "bash ~/.claude/hooks/ir-injector.sh",
            "timeout": 5
          }
        ]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "Task",
        "hooks": [
          {
            "type": "command",
            "command": "bash ~/.claude/hooks/model-enforcer.sh",
            "timeout": 5
          }
        ]
      }
    ]
  }
}
```

**Order matters:** `session-governor.sh` must appear before `ir-injector.sh` in the array. The governor writes the turn count; the injector reads it.

---

## Model Routing Logic

| Signal in prompt | Route |
|---|---|
| plan, implement feature, build new, end-to-end | Pipeline: haiku explore → opus design → sonnet implement |
| architect, security review, trade-off, root cause | Opus subagent for reasoning |
| summarize, search, find, explain code, grep | Haiku subagent |
| bug fix, refactor, write tests, direct code | Sonnet handles directly (no delegation) |

---

## Session Warnings

| Turn | Message |
|---|---|
| 15 | Consider wrapping up or /compact |
| 25 | Finish current task, suggest new session |
| 35 | Session degraded — wrap up now |

---

## Stage B: IR Injection

Index a project (run from the project root, or pass a path):

```bash
ai-ir              # indexes current directory → .ai/ir.json
ai-ir /path/to/project
```

On the next Claude Code session in that directory, turn 1 will include:

```
IR CONTEXT [root_hash: abc123456789...]:
3 files — src/auth.ts, src/user.ts, utils/helpers.js
Symbols (12): AuthService, UserService, fetchUser, validateToken, ...
```

Subsequent turns: silent (no injection). Projects without `.ai/ir.json`: silent. Requires `jq`.

Re-run `ai-ir` any time the codebase changes. It performs incremental updates (only re-parses changed files).

---

## Verify

```bash
# Hooks installed
ls -la ~/.claude/hooks/

# Governor fires (test haiku routing)
echo '{"session_id":"test","prompt":"summarize the code"}' | bash hooks/session-governor.sh

# Enforcer reads state
cat ~/.claude/hooks/.governor-state/test.route

# IR injector — turn 1 with ir.json present
mkdir -p ~/.claude/hooks/.governor-state
echo "1" > ~/.claude/hooks/.governor-state/test-session
echo '{"session_id":"test-session","prompt":"fix a bug"}' | bash hooks/ir-injector.sh

# IR injector — not turn 1 (should produce no output)
echo "5" > ~/.claude/hooks/.governor-state/test-session
echo '{"session_id":"test-session","prompt":"fix a bug"}' | bash hooks/ir-injector.sh
```

---

## Repo Structure

```
.ai/
├── cmd/
│   └── ir/
│       └── main.go           # ai-ir CLI binary (generates .ai/ir.json)
├── hooks/
│   ├── session-governor.sh   # UserPromptSubmit hook — turn count + model routing
│   ├── model-enforcer.sh     # PreToolUse hook — denies wrong-model subagents
│   └── ir-injector.sh        # UserPromptSubmit hook — injects IR summary on turn 1
├── install.sh                # Builds ai-ir, symlinks hooks to ~/.claude/hooks/
├── internal/                 # Go IR generator and parser
│   ├── ir/                   # IR types, generator, hasher, storage
│   └── parser/               # JS/TS parser
└── README.md
```

---

## Roadmap

RunEcho is a three-stage arc:

**A — Session Discipline ✅ done**
- Session governor (turn limits, warnings)
- Model router (keyword-based routing)
- Model enforcer (PreToolUse gate)
- Decision persistence via CLAUDE.md rules

**B — Structural Intelligence ✅ done (Stage B)**
- `ai-ir` CLI: generates `.ai/ir.json` (file list + symbols) for any JS/TS project
- IR injector hook: injects compact codebase summary on session turn 1
- Incremental updates: only re-parses files whose hash changed
- Remaining (Stage C): IR-diff, session handoff, context compression

**C — Multi-Agent Orchestrator (future)**
- Claude-native agent framework built on the IR + governance layer
- Fingerprint-gated task routing: tasks matched to agents by codebase context
- Cost-aware orchestration: enforce haiku/sonnet/opus budgets at the pipeline level
- Session handoff: compress + resume across sessions without context loss

Each stage is a separate planning session.

---

## License

TBD
