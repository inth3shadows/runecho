# RunEcho v1

**Status:** Phase 0 Complete ✅

RunEcho is a deterministic scoping and governance layer for AI model execution. It prevents architectural rediscovery, context explosion, and unbounded file ingestion.

## Architecture

This tool is **not** a static analyzer, compiler, or multi-agent framework. It is a **governance + structural containment layer**.

### Core Principles

1. Deterministic
2. Minimal
3. Incremental
4. Local-first
5. Model-agnostic (Claude-first implementation)
6. No background processes
7. No hidden state

## Current Status: Phase 0

Phase 0 implements the IR (Intermediate Representation) foundation:

- ✅ Shallow JS/TS/GS parser (regex-based, no AST)
- ✅ SHA256 file hasher
- ✅ Deterministic IR generator
- ✅ Incremental updates via hash comparison
- ✅ Deterministic JSON marshalling
- ✅ Comprehensive determinism test suite (100x stability verified)

### What Phase 0 Delivers

- **Parser**: Extracts top-level functions, classes, imports, exports
- **Hasher**: SHA256 file hashing with lowercase hex output
- **Generator**: Walks directory tree, skips ignored paths, generates IR
- **Storage**: Deterministic JSON with sorted keys and stable ordering
- **Tests**: 100x stability tests prove byte-identical output

See [PHASE0_COMPLETE.md](./PHASE0_COMPLETE.md) for detailed documentation.

## Supported Languages (v1)

- `.js` - JavaScript
- `.ts` - TypeScript
- `.gs` - Google Apps Script

Shallow parsing only. No semantic analysis.

## Ignored Paths

- `node_modules/`
- `dist/`
- `.git/`
- `.cursor/`
- `.vscode/`

## Testing

```bash
# Run all tests
go test ./internal/parser -v
go test ./internal/ir -v

# Run determinism tests specifically
go test ./internal/parser -v -run Determinism
go test ./internal/ir -v -run Determinism
```

## Demo

```bash
# Run Phase 0 demo
go run phase0_demo.go
```

This will:
1. Generate IR from `testdata/sample-project`
2. Display parsed structure
3. Save IR to JSON
4. Verify byte-identical regeneration (determinism check)

## IR Format

```json
{
  "version": 1,
  "generated_at": "2024-01-15T10:30:00Z",
  "files": {
    "src/example.ts": {
      "hash": "abc123...",
      "imports": ["react", "lodash"],
      "functions": ["foo", "bar"],
      "classes": ["UserService"],
      "exports": ["foo", "UserService"]
    }
  }
}
```

## Architecture Documents

- [AI_Governor_ARCHITECTURE_LOCK_v1.md](./AI_Governor_ARCHITECTURE_LOCK_v1.md) - Locked architecture spec
- [AI_Governor_SCAFFOLD_v1.md](./AI_Governor_SCAFFOLD_v1.md) - Project structure
- [AI_Governor_CONFIG_REFERENCE_v1.md](./AI_Governor_CONFIG_REFERENCE_v1.md) - Config spec
- [PHASE0_COMPLETE.md](./PHASE0_COMPLETE.md) - Phase 0 delivery report

## Upcoming Phases

- **Phase 1**: Config system + schema validation
- **Phase 2**: CLI skeleton (`ai init`, `ai ir`, `ai exec`)
- **Phase 3**: Policy engine (warn/strict modes)
- **Phase 4**: Engine interface + Claude adapter
- **Phase 5**: Integration tests + verification

## v1 Constraints

This is v1. The following are **explicitly forbidden**:

- Tree-sitter or AST libraries
- Call graph analysis
- Python/Go/Rust support
- Database backend (JSON only)
- Background daemons
- Multi-engine routing
- Telemetry or remote governance
- Enterprise features

Any expansion requires v2 and architecture review.

## License

TBD

## Requirements

- Go 1.21+
- No external dependencies (stdlib only for Phase 0)
