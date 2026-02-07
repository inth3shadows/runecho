package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/inth3shadows/runecho/internal/ir"
)

// Verification: Regenerating IR twice produces byte-identical JSON
func main() {
	fmt.Println("=== Determinism Verification ===\n")

	config := ir.GeneratorConfig{}
	gen := ir.NewGenerator(config)

	// First generation
	fmt.Println("Generating IR (first time)...")
	ir1, err := gen.Generate("testdata/sample-project")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	json1, err := json.Marshal(ir1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Marshal error: %v\n", err)
		os.Exit(1)
	}

	// Second generation
	fmt.Println("Generating IR (second time)...")
	ir2, err := gen.Generate("testdata/sample-project")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	json2, err := json.Marshal(ir2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Marshal error: %v\n", err)
		os.Exit(1)
	}

	// Compare byte-by-byte
	if string(json1) != string(json2) {
		fmt.Println("✗ FAIL: JSON outputs differ")
		fmt.Printf("\nFirst output (%d bytes):\n%s\n", len(json1), string(json1))
		fmt.Printf("\nSecond output (%d bytes):\n%s\n", len(json2), string(json2))
		os.Exit(1)
	}

	fmt.Println("✓ PASS: Byte-identical JSON output")
	fmt.Printf("  Size: %d bytes\n", len(json1))
	fmt.Printf("  Files: %d\n", len(ir1.Files))

	// Test with Save/Load cycle
	fmt.Println("\nTesting Save/Load cycle...")
	tmpFile := "testdata/verify-ir.json"
	defer os.Remove(tmpFile)

	if err := ir1.Save(tmpFile); err != nil {
		fmt.Fprintf(os.Stderr, "Save error: %v\n", err)
		os.Exit(1)
	}

	loaded, err := ir.Load(tmpFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Load error: %v\n", err)
		os.Exit(1)
	}

	json3, _ := json.Marshal(loaded)
	if string(json1) != string(json3) {
		fmt.Println("✗ FAIL: Save/Load cycle not deterministic")
		os.Exit(1)
	}

	fmt.Println("✓ PASS: Save/Load cycle preserves byte-identity")

	fmt.Println("\n=== All Determinism Checks Passed ===")
}
