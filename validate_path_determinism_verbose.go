package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/inth3shadows/runecho/internal/ir"
)

// Validates byte-identical IR generation and shows JSON structure
func main() {
	fmt.Println("=== PATH DETERMINISM VALIDATION (VERBOSE) ===\n")

	config := ir.GeneratorConfig{}
	gen := ir.NewGenerator(config)

	// Get absolute path for variant 3
	absPath, err := filepath.Abs("testdata/sample-project")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get absolute path: %v\n", err)
		os.Exit(1)
	}

	// Define 4 path variants
	variants := []struct {
		name string
		path string
	}{
		{"Variant 1: relative (.)", "testdata/sample-project"},
		{"Variant 2: relative with slash", "testdata/sample-project/"},
		{"Variant 3: absolute", absPath},
		{"Variant 4: normalized", "testdata/../testdata/sample-project"},
	}

	outputs := make([][]byte, len(variants))
	var firstIR *ir.IR

	// Generate IR for each variant
	for i, v := range variants {
		fmt.Printf("Generating %s\n", v.name)
		fmt.Printf("  Path: %s\n", v.path)

		result, err := gen.Generate(v.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ Generation failed: %v\n", err)
			os.Exit(1)
		}

		if i == 0 {
			firstIR = result
		}

		jsonBytes, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ Marshal failed: %v\n", err)
			os.Exit(1)
		}

		outputs[i] = jsonBytes
		fmt.Printf("  ✓ Generated %d bytes\n\n", len(jsonBytes))
	}

	// Show first variant's JSON structure
	fmt.Println("=== Variant 1 JSON Structure ===")
	fmt.Println(string(outputs[0]))
	fmt.Println()

	// Show file keys from first IR
	fmt.Println("=== File Keys (should be relative with forward slashes) ===")
	for key := range firstIR.Files {
		fmt.Printf("  - %s\n", key)
	}
	fmt.Println()

	// Compare all outputs byte-for-byte
	fmt.Println("=== Byte-by-Byte Comparison ===")

	reference := outputs[0]
	allMatch := true

	for i := 1; i < len(outputs); i++ {
		if !bytes.Equal(reference, outputs[i]) {
			allMatch = false
			fmt.Printf("\n✗ FAIL: Variant %d differs from Variant 1\n", i+1)
			fmt.Printf("  Length: %d vs %d bytes\n", len(reference), len(outputs[i]))

			// Find first byte difference
			minLen := len(reference)
			if len(outputs[i]) < minLen {
				minLen = len(outputs[i])
			}

			for j := 0; j < minLen; j++ {
				if reference[j] != outputs[i][j] {
					fmt.Printf("  First diff at byte %d: 0x%02x vs 0x%02x\n", j, reference[j], outputs[i][j])

					// Show context
					start := j - 30
					if start < 0 {
						start = 0
					}
					end := j + 30
					if end > minLen {
						end = minLen
					}

					fmt.Printf("\n  Context V1: ...%s...\n", string(reference[start:end]))
					fmt.Printf("  Context V%d: ...%s...\n", i+1, string(outputs[i][start:end]))
					break
				}
			}
		} else {
			fmt.Printf("✓ Variant %d matches Variant 1 (byte-identical)\n", i+1)
		}
	}

	if !allMatch {
		fmt.Println("\n=== FAIL: PATH DETERMINISM VIOLATED ===")
		os.Exit(1)
	}

	fmt.Println("\n=== PASS: PATH DETERMINISM VALIDATED ===")
	fmt.Printf("  All 4 variants: byte-identical\n")
	fmt.Printf("  Size: %d bytes\n", len(reference))
	fmt.Printf("  Files: %d\n", len(firstIR.Files))
}
