# RunEcho Usage Guide

RunEcho is fully automated in normal operation. After `install.sh`, open Claude Code and work — the hook chain handles model routing, IR injection, checkpointing, and session synthesis silently. No CLI commands are required day-to-day.

This guide covers manual CLI usage, verification, and advanced workflows.

---

## Prerequisites

- Go 1.24+ (build-time only)
- Python 3 (install only — merges hook config into `~/.claude/settings.json`)
- Claude Code Pro or higher (hooks API requires paid plan)
- `RUNECHO_CLASSIFIER_KEY` — Anthropic API key for model routing and doc generation

See [README.md](README.md) for classifier setup and profile switching instructions.

---

## Install

```bash
bash install.sh
```

Builds all 10 binaries to `~/bin/`, symlinks hooks into `~/.claude/hooks/`, and merges hook configuration + `mcpServers.runecho` into `~/.claude/settings.json`. Safe to re-run after updates.

---

## Binaries

### ai-ir

Indexes a codebase into `.ai/ir.json` and manages SQLite snapshot history.

```bash
ai-ir [root]                                         # index/update .ai/ir.json
ai-ir snapshot [--label=name] [root]                 # save snapshot to SQLite
ai-ir diff [--since=label | id-a id-b] [root]        # structural delta between snapshots
ai-ir verify [--session=id] [root]                   # format diff summary for handoff
ai-ir log [--n=10] [root]                            # list snapshots with timestamps
ai-ir churn [--n=20] [--min-changes=2] [--compact] [root]  # symbol/file churn rate
```

### ai-session

Parses a Claude Code JSONL session log and writes a ground-truth `.ai/handoff.md`.

```bash
ai-session --session=<session-id> [root]
ai-session validate [root]                           # validate handoff front-matter schema
```

### ai-document

Auto-generates and updates project documentation using haiku. Change-gated by IR diff.

```bash
ai-document [root]                                   # generate/update docs (skips if no changes)
ai-document --ir-diff="<diff>" [root]                # use IR diff string
ai-document --docs=README.md,TECHNICAL.md [root]     # override configured doc list
ai-document --dry-run [root]                         # preview without writing
ai-document --force [root]                           # bypass change-gate, regenerate all
```

### ai-task

Persistent task ledger with dependency graph. Claude drives this during sessions; CLI is for inspection and override.

```bash
ai-task add "<title>" [--blocked-by=<id>] [--scope=<glob>] [--verify=<cmd>]
ai-task update <id> <status>                         # status: pending | in-progress | done
ai-task list [--status=<s>]                          # tabular; no filter = all non-done
ai-task next                                         # first unblocked non-done task
ai-task contract [--task-id=<id>] [root]             # write .ai/CONTRACT.yaml from active task
ai-task drift-check [--session=<id>] [root]          # intersect IR diff with task scopes
ai-task replan <id> [root]                           # print scope + IR diff + faults for review
ai-task sync [--quiet]                               # create task from CONTRACT.yaml if missing
```

### ai-context

Compiles the turn-1 context block (contract + IR + diff + handoff + tasks + review) within a token budget.

```bash
ai-context [root]
```

### ai-governor

Session governor and model router. Normally invoked by `session-governor.sh`; can be called directly for testing.

```bash
ai-governor [root]
```

### ai-pipeline

Declarative pipeline definitions.

```bash
ai-pipeline render [--name=<pipeline>] [root]        # print injection text for a pipeline
ai-pipeline envelope [--session=<id>] [root]         # write execution record to executions.jsonl
```

### ai-session-end

Session-end orchestration pipeline (7 stages). Normally invoked by `session-end.sh`; can be called directly.

```bash
ai-session-end [root]
```

### ai-provenance

Exports full execution records for any task.

```bash
ai-provenance export <task-id> [--json]              # task timeline: turns, cost, IR hashes, faults, verify
ai-provenance list [--json]                          # all tasks with recorded sessions and total cost
```

---

### ai-mcp-server

MCP stdio server — exposes all RunEcho capabilities as Claude-native tools. Registered automatically in `~/.claude/settings.json` by `install.sh`; Claude Code starts it on demand.

```bash
# Manual smoke test (two JSON lines out; second contains result.tools with 7 entries)
printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}\n{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}\n' \
  | ai-mcp-server --workspace /path/to/project
```

Available tools:

| Tool | Description |
|---|---|
| `runecho_task_list` | List tasks, optionally filtered by status |
| `runecho_task_next` | Next unblocked todo task (lowest ID) |
| `runecho_task_update` | Mark a task done / in-progress / blocked |
| `runecho_session_status` | Fault count + most recent progress entry |
| `runecho_fault_list` | Faults filtered by session ID or signal |
| `runecho_provenance_export` | Full task timeline (sessions, cost, faults, verify) |
| `runecho_context_compile` | Fresh IR+task context block (same as `ai-context`) |

All tools accept an optional `workspace` param to override the server's default project root.

---

## Common Workflows

### Verify install

```bash
# Hooks installed
ls -la ~/.claude/hooks/

# Binaries built
ls -la ~/bin/ai-ir ~/bin/ai-session ~/bin/ai-document ~/bin/ai-task

# Governor classifier fallback (no key = regex fires)
RUNECHO_CLASSIFIER_KEY="" \
  echo '{"session_id":"test","prompt":"architect the system"}' | bash hooks/session-governor.sh
# Expected output includes: "Deep reasoning task" (opus via regex)
```

### Inspect current IR

```bash
ai-ir .
ai-ir log .
ai-ir diff --since=session-end .
```

### Review session provenance

```bash
ai-provenance list
ai-provenance export <task-id>
```

### Manually trigger session end

```bash
echo '{"session_id":"<id>","cwd":"'$PWD'","reason":"other"}' | bash hooks/session-end.sh
cat .ai/handoff.md
```

### Force-regenerate documentation

```bash
ai-document --force .
```

### Inspect task state

```bash
ai-task list
ai-task next
```

---

## Environment Variables

| Variable | Required | Description |
|---|---|---|
| `RUNECHO_CLASSIFIER_KEY` | Yes | Anthropic API key for model router classifier and `ai-document` |

Set once in your PowerShell profile — see [README.md § Classifier Setup](README.md#classifier-setup).

**Fallback:** if unset, the classifier returns empty and the regex router fires. No routing regression.

---

## Data Files (`.ai/`)

| File | Written by | Purpose |
|---|---|---|
| `ir.json` | `ai-ir` | Current codebase IR snapshot |
| `handoff.md` | `ai-session`, session-end fallback | Cross-session continuity |
| `checkpoint.json` | `stop-checkpoint.sh` | Turn-level recovery state |
| `tasks.json` | `ai-task` | Persistent task ledger |
| `CONTRACT.yaml` | `ai-task contract`, Claude | Active session scope boundary |
| `faults.jsonl` | `fault-emitter.sh` | Typed fault signals (IR_DRIFT, SCOPE_DRIFT, etc.) |
| `progress.jsonl` | `ai-session` | Per-session progress records |
| `results.jsonl` | `ai-session-end` | Task verify outcomes |
| `executions.jsonl` | `ai-pipeline envelope` | Pipeline execution records |
