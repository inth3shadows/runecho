package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ai-governor/internal/ir"
)

// Validates byte-identical IR generation across 4 path variants
func main() {
	fmt.Println("=== PATH DETERMINISM VALIDATION ===\n")

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

	// Generate IR for each variant
	for i, v := range variants {
		fmt.Printf("Generating %s\n", v.name)
		fmt.Printf("  Path: %s\n", v.path)

		result, err := gen.Generate(v.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ Generation failed: %v\n", err)
			os.Exit(1)
		}

		jsonBytes, err := json.Marshal(result)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ Marshal failed: %v\n", err)
			os.Exit(1)
		}

		outputs[i] = jsonBytes
		fmt.Printf("  ✓ Generated %d bytes\n\n", len(jsonBytes))
	}

	// Compare all outputs byte-for-byte
	fmt.Println("Comparing outputs...")

	reference := outputs[0]
	allMatch := true

	for i := 1; i < len(outputs); i++ {
		if !bytes.Equal(reference, outputs[i]) {
			allMatch = false
			fmt.Printf("\n✗ FAIL: Variant %d differs from Variant 1\n", i+1)
			fmt.Printf("\n  Variant 1 (%s): %d bytes\n", variants[0].name, len(reference))
			fmt.Printf("  Variant %d (%s): %d bytes\n", i+1, variants[i].name, len(outputs[i]))

			// Find first byte difference
			minLen := len(reference)
			if len(outputs[i]) < minLen {
				minLen = len(outputs[i])
			}

			diffFound := false
			for j := 0; j < minLen; j++ {
				if reference[j] != outputs[i][j] {
					fmt.Printf("\n  First byte difference at position %d:\n", j)
					fmt.Printf("    Variant 1: 0x%02x ('%c')\n", reference[j], reference[j])
					fmt.Printf("    Variant %d: 0x%02x ('%c')\n", i+1, outputs[i][j], outputs[i][j])

					// Show context (20 bytes around diff)
					start := j - 20
					if start < 0 {
						start = 0
					}
					end := j + 20
					if end > minLen {
						end = minLen
					}

					fmt.Printf("\n  Context (Variant 1):\n    %s\n", string(reference[start:end]))
					fmt.Printf("  Context (Variant %d):\n    %s\n", i+1, string(outputs[i][start:end]))
					diffFound = true
					break
				}
			}

			if !diffFound && len(reference) != len(outputs[i]) {
				fmt.Printf("\n  No byte differences found in common prefix, but lengths differ\n")
			}

			// Show full JSON for comparison
			fmt.Printf("\n  Variant 1 JSON:\n%s\n", string(reference))
			fmt.Printf("\n  Variant %d JSON:\n%s\n", i+1, string(outputs[i]))
		}
	}

	if !allMatch {
		fmt.Println("\n=== FAIL: Outputs are NOT byte-identical ===")
		os.Exit(1)
	}

	fmt.Println("✓ All 4 variants produce byte-identical output")
	fmt.Printf("  Size: %d bytes\n", len(reference))

	// Parse to show file count
	var result ir.IR
	if err := json.Unmarshal(reference, &result); err == nil {
		fmt.Printf("  Files: %d\n", len(result.Files))
	}

	fmt.Println("\n=== PASS: PATH DETERMINISM VALIDATED ===")
}
