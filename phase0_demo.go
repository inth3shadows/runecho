package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/inth3shadows/runecho/internal/ir"
)

// Phase 0 demonstration - Generate IR from testdata/sample-project
func main() {
	fmt.Println("=== RunEcho v1 - Phase 0 Demo ===\n")

	// Configure generator
	config := ir.GeneratorConfig{
		IgnoredPaths: []string{"node_modules", "dist", ".git"},
	}

	generator := ir.NewGenerator(config)

	// Generate IR
	fmt.Println("Generating IR from testdata/sample-project...")
	irData, err := generator.Generate("testdata/sample-project")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Generated IR with %d files\n", len(irData.Files))
	fmt.Printf("✓ Version: %d\n\n", irData.Version)

	// Display parsed structure
	fmt.Println("Parsed Structure:")
	fmt.Println("-----------------")

	for path, fileIR := range irData.Files {
		fmt.Printf("\nFile: %s\n", path)
		fmt.Printf("  Hash: %s\n", fileIR.Hash[:16]+"...")

		if len(fileIR.Imports) > 0 {
			fmt.Printf("  Imports: %v\n", fileIR.Imports)
		}

		if len(fileIR.Functions) > 0 {
			fmt.Printf("  Functions: %v\n", fileIR.Functions)
		}

		if len(fileIR.Classes) > 0 {
			fmt.Printf("  Classes: %v\n", fileIR.Classes)
		}

		if len(fileIR.Exports) > 0 {
			fmt.Printf("  Exports: %v\n", fileIR.Exports)
		}
	}

	// Save to file
	outputPath := "testdata/sample-project-ir.json"
	fmt.Printf("\nSaving IR to %s...\n", outputPath)
	if err := irData.Save(outputPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ IR saved successfully")

	// Demonstrate determinism - generate again and compare
	fmt.Println("\nDeterminism Check:")
	fmt.Println("------------------")

	// Generate again without any changes
	generator2 := ir.NewGenerator(config)
	irData2, err := generator2.Generate("testdata/sample-project")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Marshal both to JSON and compare
	json1, _ := json.Marshal(irData)
	json2, _ := json.Marshal(irData2)

	if string(json1) == string(json2) {
		fmt.Println("✓ Byte-identical JSON output (determinism verified)")
	} else {
		fmt.Println("✗ JSON outputs differ (determinism failed)")
		os.Exit(1)
	}

	fmt.Println("\n=== Phase 0 Demo Complete ===")
}
