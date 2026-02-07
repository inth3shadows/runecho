# AI Governor v1 --- Config Reference

This document describes the configuration options defined in
`.ai/config.schema.json`.

------------------------------------------------------------------------

## Config Resolution Order

1.  CLI flags
2.  Project `.ai/config.json`
3.  Global config
4.  Built-in defaults

------------------------------------------------------------------------

## Fields

### mode

Type: string\
Allowed: `warn`, `strict`\
Default: `warn`

Controls policy enforcement behavior.

-   `warn` → Logs violations but allows execution.
-   `strict` → Blocks execution unless `--override` is passed.

------------------------------------------------------------------------

### maxFilesPerExecution

Type: integer\
Default: 5\
Minimum: 1

Maximum number of files allowed in a scoped execution request.

Prevents uncontrolled context expansion.

------------------------------------------------------------------------

### ignoredPaths

Type: array of strings

Directories excluded from IR generation.

Default includes:

-   node_modules
-   dist
-   .git
-   .cursor
-   .vscode

------------------------------------------------------------------------

### engine

Type: string\
Allowed: `claude`

Execution backend. v1 supports Claude only.

------------------------------------------------------------------------

### allowOverride

Type: boolean\
Default: true

If true, `--override` downgrades strict policy violations to warnings.

------------------------------------------------------------------------

## Philosophy

-   Local-first
-   Deterministic
-   No hidden behavior
-   No remote governance
-   No telemetry
-   No enterprise enforcement

Expanding config surface requires version bump.
