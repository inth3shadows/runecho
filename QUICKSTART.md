# RunEcho Phase 0 - Quick Start

## Prerequisites

- Go 1.21 or higher

## Running Tests

```bash
# Test parser determinism (100x stability)
go test ./internal/parser -v -run Determinism

# Test storage determinism (100x stability)
go test ./internal/ir -v -run Storage.*Determinism

# Test generator determinism (100x stability)
go test ./internal/ir -v -run Generator.*Determinism

# Run all tests
go test ./internal/parser -v
go test ./internal/ir -v
```

## Running Demo

```bash
# Generate IR from sample project
go run phase0_demo.go
```

Expected output:
```
=== RunEcho v1 - Phase 0 Demo ===

Generating IR from testdata/sample-project...
✓ Generated IR with 2 files
✓ Version: 1
✓ Generated at: 2024-01-15T10:30:00Z

Parsed Structure:
-----------------

File: testdata/sample-project/example.ts
  Hash: abc123def456...
  Imports: [react]
  Functions: [calculateSum, fetchUserData, greet]
  Classes: [UserService]
  Exports: [UserService, calculateSum, greet]

File: testdata/sample-project/utils.js
  Hash: xyz789...
  Imports: [lodash]
  Functions: [debounce, throttle]
  Exports: [debounce, throttle]

Saving IR to testdata/sample-project-ir.json...
✓ IR saved successfully

Determinism Check:
------------------
✓ Byte-identical JSON output (determinism verified)

=== Phase 0 Demo Complete ===
```

## Using the API

### Basic IR Generation

```go
package main

import (
    "github.com/inth3shadows/runecho/internal/ir"
)

func main() {
    // Create generator
    config := ir.GeneratorConfig{}
    gen := ir.NewGenerator(config)

    // Generate IR
    irData, _ := gen.Generate("./src")

    // Save to file
    irData.Save(".ai/ir.json")
}
```

### Incremental Update

```go
// Load existing IR
existingIR, _ := ir.Load(".ai/ir.json")

// Update (only re-parses changed files)
updatedIR, _ := gen.Update(existingIR, "./src")

// Save
updatedIR.Save(".ai/ir.json")
```

### Custom Configuration

```go
config := ir.GeneratorConfig{
    IgnoredPaths: []string{
        "node_modules",
        "dist",
        "build",
        ".git",
    },
    TimestampFunc: ir.DefaultTimestampFunc,
}

gen := ir.NewGenerator(config)
```

## Verifying Determinism

The most critical property of Phase 0 is deterministic output. Verify it:

```go
package main

import (
    "encoding/json"
    "github.com/inth3shadows/runecho/internal/ir"
)

func main() {
    config := ir.GeneratorConfig{
        TimestampFunc: func() string {
            return "2024-01-01T00:00:00Z" // Fixed timestamp
        },
    }
    gen := ir.NewGenerator(config)

    // Generate twice
    ir1, _ := gen.Generate("./src")
    ir2, _ := gen.Generate("./src")

    // Marshal to JSON
    json1, _ := json.Marshal(ir1)
    json2, _ := json.Marshal(ir2)

    // Compare
    if string(json1) == string(json2) {
        println("✓ Deterministic")
    } else {
        println("✗ Non-deterministic")
    }
}
```

## IR JSON Structure

```json
{
  "version": 1,
  "generated_at": "2024-01-15T10:30:00Z",
  "files": {
    "src/example.ts": {
      "hash": "a1b2c3d4e5f6...",
      "imports": ["react", "lodash"],
      "functions": ["foo", "bar", "baz"],
      "classes": ["UserService"],
      "exports": ["foo", "UserService"]
    }
  }
}
```

**Notes:**
- `files` keys are sorted alphabetically
- All arrays (imports, functions, classes, exports) are sorted
- Hashes are lowercase hex (64 characters)
- Timestamp is ISO8601 UTC format
- Paths use forward slashes (even on Windows)

## Troubleshooting

### Parse Warnings

If you see warnings like:
```
Warning: failed to parse src/complex.tsx: ...
```

This is expected for complex syntax (JSX, decorators, etc.). The parser will:
- Log the warning
- Continue processing other files
- Include partial IR for the problematic file

### File Access Errors

If you see:
```
Warning: failed to access src/locked.js: permission denied
```

The generator will:
- Log the warning
- Skip the file
- Continue processing

### Empty IR

If IR has no files:
- Check that directory contains .js/.ts/.gs files
- Verify ignored paths aren't excluding your files
- Check file permissions

## What Phase 0 Does NOT Do

Phase 0 is IR foundation only. It does NOT:
- Provide CLI commands (Phase 2)
- Validate config (Phase 1)
- Enforce policies (Phase 3)
- Call Claude API (Phase 4)
- Have user-facing interface (Phase 2)

Phase 0 is a **library** for IR generation. Phases 1-5 build the full tool.
