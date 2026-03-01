# RunEcho

**Status:** Hooks Active ✅

RunEcho is a session governance and model routing layer for Claude Code. It enforces cost-optimal model selection and session discipline via Claude Code hooks.

---

## What It Does

- **Session Governor**: Tracks turn count per session. Warns at turn 15, 25, and 35. Tells Claude to wrap up before context degrades and cache costs compound.
- **Model Router**: Analyzes each prompt and injects routing guidance — haiku for cheap tasks, opus for architecture, full pipeline (haiku→opus→sonnet) for multi-step work.
- **Model Enforcer**: PreToolUse hook that denies subagents using the wrong model. If the router said haiku, Claude can't spawn an opus subagent.

Together these enforce the cost model: **Haiku = eyes, Sonnet = hands, Opus = brain.**

---

## Install

```bash
bash install.sh
```

This symlinks `hooks/session-governor.sh` and `hooks/model-enforcer.sh` into `~/.claude/hooks/`.

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

## Verify

```bash
# Hooks installed
ls -la ~/.claude/hooks/

# Governor fires (test haiku routing)
echo '{"session_id":"test","prompt":"summarize the code"}' | bash hooks/session-governor.sh

# Enforcer reads state
cat ~/.claude/hooks/.governor-state/test.route
```

---

## Repo Structure

```
.ai/
├── hooks/
│   ├── session-governor.sh   # UserPromptSubmit hook
│   └── model-enforcer.sh     # PreToolUse hook
├── install.sh                # Symlinks hooks to ~/.claude/hooks/
├── internal/                 # Go IR generator (future codebase indexer)
└── README.md
```

The Go code (`internal/`) is the foundation for a future codebase indexer — deterministic IR generation and content-addressed hashing for source files. Not used by the hooks.

---

## Roadmap

RunEcho is stage A of a three-stage arc:

**A — Session Discipline (now)**
- Session governor (turn limits, warnings)
- Model router (keyword-based routing)
- Model enforcer (PreToolUse gate)
- Decision persistence via CLAUDE.md rules

**B — Structural Intelligence (next)**
- Codebase indexer using the Go IR generator
- IR-diff: detect what changed between sessions, inject only the delta
- Anti-hallucination via structural grounding (Claude reads IR, not raw files)
- Context compression: summarize session state into IR snapshot

**C — Multi-Agent Orchestrator (future)**
- Claude-native agent framework built on the IR + governance layer
- Fingerprint-gated task routing: tasks matched to agents by codebase context
- Cost-aware orchestration: enforce haiku/sonnet/opus budgets at the pipeline level
- Session handoff: compress + resume across sessions without context loss

Each stage is a separate planning session.

---

## License

TBD
