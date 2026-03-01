package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/inth3shadows/runecho/internal/ir"
)

// Usage: ai-ir [root-path]
// Generates .ai/ir.json for the project at root-path (default: current directory).
// If .ai/ir.json already exists, performs incremental update (only re-parses changed files).
func main() {
	rootPath := "."
	if len(os.Args) > 1 {
		rootPath = os.Args[1]
	}

	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to resolve path %q: %v\n", rootPath, err)
		os.Exit(1)
	}

	irPath := filepath.Join(absRoot, ".ai", "ir.json")

	config := ir.GeneratorConfig{
		IgnoredPaths: []string{"node_modules", "dist", ".git", ".cursor", ".vscode"},
	}
	generator := ir.NewGenerator(config)

	var result *ir.IR

	if _, err := os.Stat(irPath); err == nil {
		// Incremental update
		existing, loadErr := ir.Load(irPath)
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load existing IR, regenerating: %v\n", loadErr)
			result, err = generator.Generate(absRoot)
		} else {
			result, err = generator.Update(existing, absRoot)
		}
	} else {
		// Fresh generation
		if err := os.MkdirAll(filepath.Dir(irPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create .ai directory: %v\n", err)
			os.Exit(1)
		}
		result, err = generator.Generate(absRoot)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := result.Save(irPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to save IR: %v\n", err)
		os.Exit(1)
	}

	shortHash := result.RootHash
	if len(shortHash) > 12 {
		shortHash = shortHash[:12]
	}
	fmt.Printf("Indexed %d files — root_hash: %s...\n", len(result.Files), shortHash)
}
