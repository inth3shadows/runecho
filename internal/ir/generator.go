package ir

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inth3shadows/runecho/internal/parser"
	"golang.org/x/text/unicode/norm"
)

// Generator creates and updates IR from source files.
type Generator struct {
	parser       parser.Parser
	ignoredPaths map[string]bool
}

// GeneratorConfig configures IR generation behavior.
type GeneratorConfig struct {
	IgnoredPaths []string // Directory names to ignore
}

// NewGenerator creates a new IR generator.
func NewGenerator(config GeneratorConfig) *Generator {
	// Build ignored paths map
	ignored := make(map[string]bool)
	for _, path := range config.IgnoredPaths {
		ignored[path] = true
	}

	// Set default ignored paths if none provided
	if len(ignored) == 0 {
		ignored["node_modules"] = true
		ignored["dist"] = true
		ignored[".git"] = true
		ignored[".cursor"] = true
		ignored[".vscode"] = true
	}

	return &Generator{
		parser:       parser.NewJSParser(),
		ignoredPaths: ignored,
	}
}

// Generate creates IR for all supported files in the given root directory.
func (g *Generator) Generate(rootPath string) (*IR, error) {
	// Convert to absolute and clean path for determinism
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve absolute path: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	ir := &IR{
		Version: 1,
		Files:   make(map[string]FileIR),
	}

	// Walk directory tree
	err = filepath.Walk(absRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Log but continue on access errors
			fmt.Fprintf(os.Stderr, "Warning: failed to access %s: %v\n", path, err)
			return nil
		}

		// Skip symlinks
		if info.Mode()&os.ModeSymlink != 0 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip directories in ignored list
		if info.IsDir() {
			dirName := filepath.Base(path)
			if g.ignoredPaths[dirName] {
				return filepath.SkipDir
			}
			return nil
		}

		// Check if file extension is supported
		ext := filepath.Ext(path)
		if !g.parser.SupportsExtension(ext) {
			return nil
		}

		// Parse file
		fileIR, err := g.parseFile(path)
		if err != nil {
			// Log warning but continue
			fmt.Fprintf(os.Stderr, "Warning: failed to parse %s: %v\n", path, err)
			return nil
		}

		// Compute relative path from root and normalize
		relPath, err := filepath.Rel(absRoot, path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to compute relative path for %s: %v\n", path, err)
			return nil
		}
		normalizedPath := normalizePath(relPath)

		ir.Files[normalizedPath] = fileIR
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	// Compute root hash
	ir.RootHash = ComputeRootHash(ir.Files)

	return ir, nil
}

// Update incrementally updates IR based on file hashes.
// Only re-parses files whose hash has changed.
func (g *Generator) Update(existingIR *IR, rootPath string) (*IR, error) {
	// Convert to absolute and clean path for determinism
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve absolute path: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	updatedIR := &IR{
		Version: 1,
		Files:   make(map[string]FileIR),
	}

	// Track which files we've seen
	seenFiles := make(map[string]bool)

	// Walk directory tree
	err = filepath.Walk(absRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to access %s: %v\n", path, err)
			return nil
		}

		// Skip symlinks
		if info.Mode()&os.ModeSymlink != 0 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			dirName := filepath.Base(path)
			if g.ignoredPaths[dirName] {
				return filepath.SkipDir
			}
			return nil
		}

		ext := filepath.Ext(path)
		if !g.parser.SupportsExtension(ext) {
			return nil
		}

		// Compute relative path from root and normalize
		relPath, err := filepath.Rel(absRoot, path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to compute relative path for %s: %v\n", path, err)
			return nil
		}
		normalizedPath := normalizePath(relPath)
		seenFiles[normalizedPath] = true

		// Compute current hash
		currentHash, err := HashFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to hash %s: %v\n", path, err)
			return nil
		}

		// Check if file exists in existing IR with same hash
		if existingFile, exists := existingIR.Files[normalizedPath]; exists {
			if existingFile.Hash == currentHash {
				// Hash unchanged, reuse existing IR
				updatedIR.Files[normalizedPath] = existingFile
				return nil
			}
		}

		// Hash changed or new file - reparse
		fileIR, err := g.parseFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse %s: %v\n", path, err)
			return nil
		}

		updatedIR.Files[normalizedPath] = fileIR
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	// Compute root hash
	updatedIR.RootHash = ComputeRootHash(updatedIR.Files)

	return updatedIR, nil
}

// normalizePath applies all path normalization rules:
// 1. Convert to forward slashes (filepath.ToSlash)
// 2. Strip leading "./" if present
// 3. Apply Unicode NFC normalization
// This ensures cross-platform determinism (Windows/Linux/macOS).
func normalizePath(relPath string) string {
	// Convert to forward slashes
	normalized := filepath.ToSlash(relPath)

	// Strip leading "./" if present
	normalized = strings.TrimPrefix(normalized, "./")

	// Apply Unicode NFC normalization
	// This ensures macOS NFD filenames and Linux NFC filenames produce identical output
	normalized = norm.NFC.String(normalized)

	return normalized
}

// parseFile parses a single file and returns its IR.
func (g *Generator) parseFile(path string) (FileIR, error) {
	// Read file
	content, err := os.ReadFile(path)
	if err != nil {
		return FileIR{}, fmt.Errorf("failed to read file: %w", err)
	}

	// Compute hash
	hash, err := HashFile(path)
	if err != nil {
		return FileIR{}, fmt.Errorf("failed to hash file: %w", err)
	}

	// Parse structure
	structure, err := g.parser.Parse(string(content))
	if err != nil {
		return FileIR{}, fmt.Errorf("failed to parse file: %w", err)
	}

	// Ensure all slices are sorted (parser should do this, but enforce here)
	sort.Strings(structure.Imports)
	sort.Strings(structure.Functions)
	sort.Strings(structure.Classes)
	sort.Strings(structure.Exports)

	return FileIR{
		Hash:      hash,
		Imports:   structure.Imports,
		Functions: structure.Functions,
		Classes:   structure.Classes,
		Exports:   structure.Exports,
	}, nil
}

