# RunEcho v1.2

**Status:** Phase 0 Complete ✅

RunEcho is a deterministic intermediate representation (IR) and execution fingerprinting primitive for AI model interactions. It provides structural containment and reproducible codebase snapshots via content-addressed hashing.

---

## What It Does

- **Deterministic IR Generation**: Parses source files into stable, reproducible JSON representations
- **Execution Fingerprinting**: RootHash provides cryptographic identity for entire codebase state
- **Incremental Updates**: Hash-based change detection avoids redundant parsing
- **Cross-Platform Determinism**: Unicode NFC normalization ensures identical output across Windows/Linux/macOS

RunEcho is **not** a static analyzer, compiler, or multi-agent framework. It is a **structural containment layer** that prevents architectural rediscovery and context explosion in AI-assisted development.

---

## Core Principles

1. Deterministic
2. Minimal
3. Incremental
4. Local-first
5. Model-agnostic (Claude-first implementation)
6. No background processes
7. No hidden state

---

## Phase 0 Capabilities

### Parser
- Shallow regex-based parsing (no AST)
- Extracts top-level: functions, classes, imports, exports
- Supports: `.js`, `.ts`, `.gs`
- Deterministic symbol ordering (sorted, deduplicated)

### Hasher
- SHA256 file hashing
- Lowercase hex output
- Byte-identical results on repeated runs

### Generator
- Directory tree traversal with ignored paths
- Incremental updates via hash comparison
- Path normalization (Windows → Unix paths)
- Unicode NFC normalization for cross-platform determinism

### Storage
- Deterministic JSON marshalling
- Sorted file paths and symbol arrays
- RootHash: content-addressed fingerprint of entire IR
- Save/Load to `.ai/ir.json`

### Testing
- 100x stability tests verify byte-identical output
- Parser, hasher, generator, storage all determinism-verified

See [PHASE0_COMPLETE.md](./PHASE0_COMPLETE.md) for implementation details.

---

## IR Format (v1.2)

```json
{
  "version": 1,
  "root_hash": "a1b2c3d4e5f6...",
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

**Key Fields:**
- `version`: IR schema version (always 1 for v1.x)
- `root_hash`: SHA256 of concatenated `path:hash` pairs (sorted, newline-delimited)
- `files`: Map of normalized paths to file IR

**Changes from v1.0:**
- Added `root_hash` for execution fingerprinting
- Removed `generated_at` (non-deterministic timestamp)
- Path normalization now includes Unicode NFC (macOS/Linux consistency)

---

## Ignored Paths

- `node_modules/`
- `dist/`
- `.git/`
- `.cursor/`
- `.vscode/`

---

## Requirements

- **Go 1.24+**
- **Dependencies:**
  - `golang.org/x/text` (Unicode normalization)

---

## Testing

```bash
# Run all tests
go test ./internal/parser -v
go test ./internal/ir -v

# Run determinism tests specifically
go test ./internal/parser -v -run Determinism
go test ./internal/ir -v -run Determinism
```

---

## Demo

```bash
go run phase0_demo.go
```

This will:
1. Generate IR from `testdata/sample-project`
2. Display parsed structure
3. Save IR to JSON
4. Verify byte-identical regeneration (determinism check)

---

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

---

## Determinism Guarantees

1. **Parse Stability**: 100x identical `FileStructure` output
2. **JSON Marshalling**: 100x byte-identical JSON
3. **Full Pipeline**: 100x byte-identical IR generation
4. **Hash Stability**: 100x identical SHA256 output
5. **Path Normalization**: Windows/Unix/macOS produce identical paths
6. **Unicode Normalization**: macOS NFD and Linux NFC produce identical output
7. **RootHash Stability**: Identical codebase state → identical root_hash

---

## Upcoming Phases

- **Phase 1**: Config system + schema validation
- **Phase 2**: CLI skeleton (`ai init`, `ai ir`, `ai exec`)
- **Phase 3**: Policy engine (warn/strict modes)
- **Phase 4**: Engine interface + Claude adapter
- **Phase 5**: Integration tests + verification

---

## License

TBD
