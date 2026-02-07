# RunEcho v1 --- Architecture Lock

## Status

LOCKED --- Do Not Expand Without Version Bump

------------------------------------------------------------------------

## Primary Goal

Deterministic scoping boundary for AI execution to prevent architectural
rediscovery and uncontrolled context expansion.

This tool is not a static analyzer. This tool is not a compiler. This
tool is not a multi-agent framework. This tool is a governance +
structural containment layer.

------------------------------------------------------------------------

## Language Support (v1)

### Supported Extensions

-   `.ts`
-   `.js`
-   `.gs`

### Parsing Strategy

-   Native Go implementation
-   Shallow deterministic parsing
-   No AST engine
-   No Tree-sitter
-   No type inference
-   No semantic resolution

### Extracted Structure

Per file:

-   Top-level function declarations
-   Top-level exported functions
-   Class declarations
-   Import statements (ESM + require)

No nested scope graph. No call graph. No decorator analysis. No type
graph.

------------------------------------------------------------------------

## Explicitly Ignored Paths

-   `node_modules/`
-   `dist/`
-   `.git/`
-   `.cursor/`
-   `.vscode/`

------------------------------------------------------------------------

## IR Format

Stored at:

    .ai/ir.json

Structure:

``` json
{
  "version": 1,
  "generated_at": "ISO8601",
  "files": {
    "src/example.ts": {
      "hash": "sha256",
      "imports": [],
      "functions": [],
      "classes": [],
      "exports": []
    }
  }
}
```

Rules:

-   Deterministic ordering
-   Stable JSON formatting
-   Idempotent regeneration
-   No random fields

------------------------------------------------------------------------

## Update Strategy

-   Incremental per-file hash
-   Re-parse only changed files
-   Never full rebuild unless explicitly requested
-   Not Git-based
-   File-system state authoritative

------------------------------------------------------------------------

## Execution Model

1.  Verify IR exists
2.  Verify file hashes match
3.  Regenerate changed entries
4.  Evaluate policies
5.  Execute engine

------------------------------------------------------------------------

## Policy Engine (v1)

Supported checks:

-   Max file count per execution
-   IR freshness
-   Unscoped execution

Modes:

-   warn (default)
-   strict (blocks unless `--override`)

No enterprise features. No telemetry. No remote policy loading.

------------------------------------------------------------------------

## Engine Support

-   Claude adapter only in v1
-   Engine interface abstracted
-   Model-agnostic design preserved

------------------------------------------------------------------------

## Determinism Requirements

The following must always hold:

-   Running IR generation twice produces identical file
-   Deleting IR and regenerating produces identical structure
-   Parsing same file twice produces identical node structure
-   Scope resolution is stable across runs
-   All file paths MUST be Unicode NFC normalized
    -   macOS NFD filenames and Linux NFC filenames produce byte-identical IR
    -   Normalization applied after path computation and before hashing

If any fail → bug.

------------------------------------------------------------------------

## Versioning Rule

Any of the following require v2:

-   Call graph support
-   Tree-sitter integration
-   Python support
-   Database backend
-   Enterprise policy enforcement
-   Multi-engine routing

No silent scope expansion allowed.
