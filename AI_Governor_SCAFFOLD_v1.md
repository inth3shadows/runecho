# AI Governor v1 --- Scaffold

## Purpose

AI Governor is a deterministic scoping and governance layer placed in
front of AI model execution.

It prevents:

-   Architectural rediscovery
-   Large-context drift
-   Unbounded file ingestion
-   Cost explosion
-   Structural ambiguity

It does not attempt semantic reasoning. It does not replace an IDE. It
does not replace a compiler.

It enforces containment.

------------------------------------------------------------------------

## Core Principles

1.  Deterministic\
2.  Minimal\
3.  Incremental\
4.  Local-first\
5.  Model-agnostic (Claude-first implementation)\
6.  No background processes\
7.  No hidden state

------------------------------------------------------------------------

## Project Structure

    /cmd/ai/
        main.go

    /internal/
        /config/
            loader.go
            resolver.go
        /parser/
            parser.go
            js.go
        /ir/
            generator.go
            hasher.go
            storage.go
        /policy/
            evaluator.go
            rules.go
        /engine/
            engine.go
            claude.go
        /scope/
            scope.go

    /.ai/
        ir.json
        config.json
        config.schema.json
        ARCHITECTURE_LOCK_v1.md
        SCAFFOLD.md

------------------------------------------------------------------------

## Supported File Types (v1)

-   `.ts`
-   `.js`
-   `.gs`

Shallow parsing only.

------------------------------------------------------------------------

## IR Strategy

Stored at:

    .ai/ir.json

Generated incrementally via file hashing.

Per file:

-   sha256 hash\
-   imports\
-   functions\
-   classes\
-   exports

No call graph.\
No nested function mapping.\
No AST engine.

------------------------------------------------------------------------

## Execution Flow

1.  Load config (CLI \> Project \> Global \> Default)\
2.  Validate config against schema\
3.  Verify IR freshness\
4.  Incrementally update IR if needed\
5.  Evaluate policy rules\
6.  If violation:
    -   warn OR block\
7.  Execute engine\
8.  Exit

No daemon.\
No watchers.\
No background syncing.

------------------------------------------------------------------------

## Policy Philosophy

Default mode: warn\
Strict mode: block unless `--override`

Policy checks (v1):

-   IR freshness\
-   Unscoped execution\
-   File count \> maxFilesPerExecution

No enterprise enforcement.\
No telemetry.\
No cost tracking.\
No remote governance.

------------------------------------------------------------------------

## Determinism Contract

The system must satisfy:

-   IR regeneration is idempotent\
-   Same file always yields same node structure\
-   JSON ordering is stable\
-   Scope resolution is stable

Any violation is a bug.

------------------------------------------------------------------------

## Explicit Non-Goals (v1)

-   Tree-sitter integration\
-   Python support\
-   Call graph extraction\
-   Multi-engine routing\
-   Database backend (JSON only)\
-   Git-aware diffing\
-   CI integration\
-   IDE plugin\
-   Token estimator\
-   Remote policy server\
-   Enterprise features

Scope expansion requires version bump.

------------------------------------------------------------------------

## Version Policy

v1 is frozen to:

-   JS-family shallow parsing\
-   Deterministic JSON IR\
-   Claude execution adapter\
-   Local governance only

Any structural expansion = v2.

------------------------------------------------------------------------

## Design Constraint

If adding a feature increases:

-   Runtime complexity\
-   Install friction\
-   Parsing surface area\
-   External dependency depth\
-   Hidden state

It does not belong in v1.
