# Usage Guide

## Prerequisites

- **Go** 1.24+
- **Claude Code Pro** (required for ai-ir and ai-document)
- **Environment variables:**
  - `RUNECHO_CLASSIFIER_KEY` (required)
  - `CLAUDE_API_KEY` (required for Claude API calls)
  - `BLOCK_OPUS_ON_COST` (optional, set to `true` to enforce cost caps)

## Installation

```sh
git clone <repo>
cd runecho
./install.sh
```

Build binaries:
```sh
go build -o ai-ir ./cmd/ir
go build -o document ./cmd/document
go build -o ai-document ./cmd/document
```

Install hooks:
```sh
./install.sh
```

## Commands

### ai-ir
Ingest and route IR (intermediate representation) logs through Claude models.

```
ai-ir [flags] <root-path>
```

Flags:
- `--churn` — emit IR churn metrics and exit
- `--classifier-key` — override `RUNECHO_CLASSIFIER_KEY`

Example:
```sh
ai-ir ./logs
ai-ir --churn ./session.jsonl
```

### document
Process and compile contextual documentation.

```
document [flags] <input-path>
```

Flags:
- `--output` — write to file (default: stdout)

Example:
```sh
document ./docs --output compiled.md
```

### ai-session
Ground-truth session handoff from Claude Code JSONL.

```
ai-session <session-file>
```

Example:
```sh
ai-session session.jsonl
```

### ai-context
Compile context from source tree and constraints.

```
ai-context [flags] <project-root>
```

Example:
```sh
ai-context ./myproject
```

## Common Workflows

1. **Initialize a session:**
   ```sh
   ai-session ./session.jsonl
   ai-context ./project-root
   ```

2. **Emit and route IR logs:**
   ```sh
   ai-ir ./logs
   ai-ir --churn ./logs
   ```

3. **Generate documentation:**
   ```sh
   document ./docs --output dist/guide.md
   ```

4. **Monitor session cost (with governor):**
   ```sh
   export BLOCK_OPUS_ON_COST=true
   ai-ir ./session.jsonl
   ```

5. **Review and compact snapshot:**
   ```sh
   ./hooks/pre-compact-snapshot.sh
   ./hooks/session-end.sh
   ```

## Environment Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `RUNECHO_CLASSIFIER_KEY` | Yes | Classification API key |
| `CLAUDE_API_KEY` | Yes | Claude API authentication |
| `BLOCK_OPUS_ON_COST` | No | Set to `true` to cap Opus routing by cost |
