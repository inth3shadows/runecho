# Technical Architecture

## Overview

runecho is a Go-based CLI system for AI-assisted code documentation and session management. It integrates with Claude Code through structured context compilation, contract-based validation, and cost-aware session governance to enforce constraints and automate documentation workflows.

## Architecture

```
┌─────────────────┐
│  CLI Commands   │
│ (task/ir/doc)   │
└────────┬────────┘
         │
    ┌────▼─────────────────────┐
    │  Context Compiler        │
    │  (resolve + validate)     │
    └────┬──────────────────────┘
         │
    ┌────▼──────────────────────────────┐
    │  Contract Engine                   │
    │  (task ledger + session contracts) │
    └────┬───────────────────────────────┘
         │
    ┌────▼─────────────────────────────┐
    │  Hooks + Checkpoint System        │
    │  (constraint + cost injection)    │
    └────┬──────────────────────────────┘
         │
    ┌────▼────────────────────┐
    │  Session Governor       │
    │  (cost routing + caps)   │
    └────┬───────────────────┘
         │
    ┌────▼─────────────────┐
    │  Output (JSONL/Docs) │
    │  Snapshots + Ledger  │
    └──────────────────────┘
```

## Components

- **cmd/{context,document,ir,session,task}** — CLI entry points for each subcommand; parse flags and delegate to internal packages.
- **internal/context** — Resolves and validates project context; compiles Claude Code input from source files and constraints.
- **internal/contract** — Task ledger, session contracts, and persona registry; enforces compliance rules.
- **internal/document** — Generates and manages documentation artifacts; writes JSONL output.
- **internal/ir** — Intermediate representation parser; processes Claude Code responses.
- **internal/session** — Session state tracking, cost aggregation, and JSONL ground-truth from Claude Code.
- **internal/snapshot** — Checkpoint creation; persists session state before destructive operations.
- **internal/parser** — Parses constraint files and session metadata.
- **hooks/** — Bash scripts wired into CI/pre-commit; inject guards (scope, constraint reinjection, destructive ops, cost blocking).

## Data Flow

1. User runs `runecho task submit [--profile=P]` or equivalent command.
2. Context compiler loads source files, contracts, and environment (RUNECHO_CLASSIFIER_KEY required).
3. Contract engine validates task against ledger and session constraints.
4. Snapshot created at `internal/snapshot/{checkpoint-id}.json` if destructive.
5. Hook system (pre-compact-snapshot.sh, constraint-reinjector.sh) injects guards into Claude Code context.
6. Session governor checks cumulative cost against BLOCK_OPUS_ON_COST threshold; routes to GPT-4 if limit exceeded.
7. Claude Code processes request; returns JSONL response.
8. internal/ir parses response; internal/session records cost and updates ledger.
9. Documentation written to docs/ or output format specified in profile.
10. session-end.sh hook executes cleanup and archival.

## Configuration

| File/Var | Purpose |
|----------|---------|
| `.claude/settings.local.json` | Local profile overrides (cost thresholds, model routing). |
| `RUNECHO_CLASSIFIER_KEY` | Claude Code API key (required). |
| `BLOCK_OPUS_ON_COST` | Boolean flag; if true, governor caps Opus routing by session cost. |
| `powershell/claude-profile.ps1` | Windows profile setup for session env vars. |
| `cmd/context/main.go` | Default context resolution paths. |
| `hooks/*.sh` | Pre/post-operation constraints and checkpoints. |

## Dependencies

| Module | Version | Purpose |
|--------|---------|---------|
| Go standard library | 1.21+ | Core CLI, JSON marshaling, file I/O. |
| (see go.mod) | — | No external vendor packages evident; self-contained CLI. |
