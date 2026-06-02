package ir

import (
	"errors"
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
	parsers      []parser.Parser
	ignoredPaths map[string]bool
	fileCap      int // 0 = unlimited; walk stops after this many files
}

// GeneratorConfig configures IR generation behavior.
type GeneratorConfig struct {
	IgnoredPaths []string // Directory names to ignore
	FileCap      int      // Max files to index; 0 = unlimited. Walk stops after this many files are processed.
}

// errFileCap is the sentinel returned by a walk callback when the file cap is
// reached. It stops the walk early and is not propagated as a real error.
var errFileCap = errors.New("file cap reached")

// capReached reports whether indexedCount has hit the configured file cap.
// indexedCount is the number of files already added to the IR — files that fail
// to parse never reach the IR, so they do not consume cap budget.
func (g *Generator) capReached(indexedCount int) bool {
	return g.fileCap > 0 && indexedCount >= g.fileCap
}

// NewGenerator creates a new IR generator.
func NewGenerator(config GeneratorConfig) *Generator {
	paths := config.IgnoredPaths
	if len(paths) == 0 {
		paths = DefaultIgnoredPaths
	}
	ignored := make(map[string]bool, len(paths))
	for _, p := range paths {
		ignored[p] = true
	}
	return &Generator{
		parsers:      []parser.Parser{parser.NewJSParser(), parser.NewGoParser(), parser.NewPythonParser()},
		ignoredPaths: ignored,
		fileCap:      config.FileCap,
	}
}

// walkerFunc is called for each supported source file found during a walk.
// absRoot is the walk root; normalizedPath is the relative, normalized path.
// Returning an error from walkerFunc is propagated and stops the walk.
type walkerFunc func(absPath, normalizedPath string) error

// walkSourceFiles walks absRoot, calling fn for each supported source file.
// It skips ignored directories, symlinked directories, and unsupported extensions.
func (g *Generator) walkSourceFiles(absRoot string, fn walkerFunc) error {
	return filepath.Walk(absRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to access %s: %v\n", path, err)
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if g.ignoredPaths[filepath.Base(path)] {
				return filepath.SkipDir
			}
			return nil
		}
		if !g.supportsExtension(filepath.Ext(path)) {
			return nil
		}
		relPath, err := filepath.Rel(absRoot, path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to compute relative path for %s: %v\n", path, err)
			return nil
		}
		return fn(path, normalizePath(relPath))
	})
}

// Generate creates IR for all supported files in the given root directory.
// When FileCap > 0, the walk stops after that many files are processed.
func (g *Generator) Generate(rootPath string) (*IR, int, error) {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to resolve absolute path: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	result := &IR{Version: 1, Files: make(map[string]FileIR)}
	var parseErrors int

	if err := g.walkSourceFiles(absRoot, func(absPath, normPath string) error {
		if g.capReached(len(result.Files)) {
			return errFileCap
		}
		fileIR, err := g.parseFile(absPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse %s: %v\n", absPath, err)
			parseErrors++
			return nil
		}
		result.Files[normPath] = fileIR
		return nil
	}); err != nil && !errors.Is(err, errFileCap) {
		return nil, 0, fmt.Errorf("failed to walk directory: %w", err)
	}

	result.RootHash = ComputeRootHash(result.Files)
	return result, parseErrors, nil
}

// Update incrementally updates IR based on file hashes.
// Only re-parses files whose hash has changed. When FileCap > 0, the walk stops
// after that many files are processed (consistent with Generate).
func (g *Generator) Update(existingIR *IR, rootPath string) (*IR, int, error) {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to resolve absolute path: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	updated := &IR{Version: 1, Files: make(map[string]FileIR)}
	var parseErrors int

	if err := g.walkSourceFiles(absRoot, func(absPath, normPath string) error {
		if g.capReached(len(updated.Files)) {
			return errFileCap
		}
		currentHash, err := HashFile(absPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to hash %s: %v\n", absPath, err)
			return nil
		}
		if existing, ok := existingIR.Files[normPath]; ok && existing.Hash == currentHash {
			updated.Files[normPath] = existing
			return nil
		}
		fileIR, err := g.parseFile(absPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse %s: %v\n", absPath, err)
			parseErrors++
			return nil
		}
		updated.Files[normPath] = fileIR
		return nil
	}); err != nil && !errors.Is(err, errFileCap) {
		return nil, 0, fmt.Errorf("failed to walk directory: %w", err)
	}

	updated.RootHash = ComputeRootHash(updated.Files)
	return updated, parseErrors, nil
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

// supportsExtension returns true if any registered parser handles this extension.
func (g *Generator) supportsExtension(ext string) bool {
	for _, p := range g.parsers {
		if p.SupportsExtension(ext) {
			return true
		}
	}
	return false
}

// parserFor returns the first parser that supports the given extension, or nil.
func (g *Generator) parserFor(ext string) parser.Parser {
	for _, p := range g.parsers {
		if p.SupportsExtension(ext) {
			return p
		}
	}
	return nil
}

// maxParseBytes is the per-file size limit for source parsing. Files larger
// than this are skipped with a warning — oversized files are usually generated
// artifacts, not hand-authored source. var so tests can lower the cap.
var maxParseBytes int64 = 10 * 1024 * 1024

// parseFile parses a single file and returns its IR.
func (g *Generator) parseFile(path string) (FileIR, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileIR{}, fmt.Errorf("failed to stat file: %w", err)
	}
	if info.Size() > maxParseBytes {
		return FileIR{}, fmt.Errorf("skipping oversized file (%d bytes)", info.Size())
	}

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

	// Dispatch to the right parser by extension
	ext := filepath.Ext(path)
	p := g.parserFor(ext)
	if p == nil {
		return FileIR{}, fmt.Errorf("no parser for extension %s", ext)
	}

	// Parse structure
	structure, err := p.Parse(string(content))
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

