# Phase 0 Determinism Patch

## Changes Applied

### 1. Removed `generated_at` Field

**storage.go**
- Removed `GeneratedAt` field from `IR` struct
- Removed `GenerateTimestamp()` function
- Removed `TimestampFunc` type
- Removed `DefaultTimestampFunc` variable
- Added `DefaultIRPath` constant: `".ai/ir.json"`
- Modified `Save()` to use `DefaultIRPath` when empty string provided
- Updated `MarshalJSON()` to exclude `generated_at`
- Updated `UnmarshalJSON()` to exclude `generated_at`

**generator.go**
- Removed `timestampFunc` field from `Generator` struct
- Removed `TimestampFunc` field from `GeneratorConfig` struct
- Removed timestamp handling from `NewGenerator()`
- Removed timestamp assignment from `Generate()`
- Removed timestamp assignment from `Update()`

### 2. Updated Tests

**storage_test.go**
- Removed all `GeneratedAt` field references
- Removed `TestGenerateTimestamp_Format()`
- Added `TestIR_Save_DefaultPath()` to verify `DefaultIRPath` behavior
- Updated all test IR structs to omit `GeneratedAt`

**generator_test.go**
- Removed all `TimestampFunc` configurations
- Removed timestamp assertions from tests
- Simplified test setup (no fixed timestamp injection)

**phase0_demo.go**
- Removed timestamp output
- Removed fixed timestamp in determinism check
- Simplified determinism verification

### 3. Added Verification

**verify_determinism.go**
- New standalone verification script
- Tests byte-identical regeneration
- Tests Save/Load cycle preservation
- No special handling or injection required

## Result

### Before Patch
```json
{
  "version": 1,
  "generated_at": "2024-01-15T10:30:00Z",
  "files": { ... }
}
```
- NOT byte-identical across runs (timestamp varies)
- Required test injection for determinism verification

### After Patch
```json
{
  "version": 1,
  "files": { ... }
}
```
- BYTE-IDENTICAL across runs (no varying fields)
- No special handling required

## Verification

Run verification script:
```bash
go run verify_determinism.go
```

Expected output:
```
=== Determinism Verification ===

Generating IR (first time)...
Generating IR (second time)...
✓ PASS: Byte-identical JSON output
  Size: XXXX bytes
  Files: X

Testing Save/Load cycle...
✓ PASS: Save/Load cycle preserves byte-identity

=== All Determinism Checks Passed ===
```

## Compliance Update

**ARCHITECTURE_LOCK_v1.md Requirements:**

| Requirement | Before | After |
|------------|--------|-------|
| Running IR generation twice produces identical file | FAIL (timestamp varies) | PASS (byte-identical) |
| Deleting IR and regenerating produces identical structure | PARTIAL (structure yes, timestamp no) | PASS (fully identical) |
| IR stored at `.ai/ir.json` | FAIL (no default path) | PASS (DefaultIRPath const) |

## Files Modified

- `internal/ir/storage.go` (simplified, -27 lines)
- `internal/ir/generator.go` (simplified, -10 lines)
- `internal/ir/storage_test.go` (updated, +35 lines test coverage)
- `internal/ir/generator_test.go` (simplified, -15 lines)
- `phase0_demo.go` (simplified, -5 lines)

## Files Added

- `verify_determinism.go` (new, 80 lines)
- `PHASE0_DETERMINISM_PATCH.md` (this file)

## Confirmation

✅ Regenerating IR twice produces **byte-identical JSON** without special handling
✅ No structural hash field added
✅ No new metadata fields added
✅ DefaultIRPath constant defined and used
✅ All tests updated and passing
