package parser

import (
	"regexp"
	"sort"
	"strings"
)

// GoParser implements shallow parsing for .go files.
// Uses regex patterns — not semantically correct, but deterministic.
// Only exported symbols (capitalized) are extracted for functions and types,
// since unexported symbols rarely matter for IR-level codebase orientation.
type GoParser struct{}

var (
	// Single-line import: import "path" or import alias "path"
	goImportSingleRegex = regexp.MustCompile(`^\s*import\s+(?:\w+\s+)?"([^"]+)"`)

	// Inside an import block: "path" or alias "path"
	goImportBlockItemRegex = regexp.MustCompile(`"([^"]+)"`)

	// Top-level exported function: func FuncName(
	goFuncRegex = regexp.MustCompile(`^func\s+([A-Z]\w*)\s*[\(\[]`)

	// Exported method: func (recv Type) MethodName(
	goMethodRegex = regexp.MustCompile(`^func\s+\([^)]+\)\s+([A-Z]\w*)\s*[\(\[]`)

	// Exported type declaration: type TypeName struct|interface|...
	goTypeRegex = regexp.MustCompile(`^type\s+([A-Z]\w*)\s+`)
)

// NewGoParser creates a new Go parser.
func NewGoParser() *GoParser {
	return &GoParser{}
}

// SupportsExtension returns true for .go files.
func (p *GoParser) SupportsExtension(ext string) bool {
	return ext == ".go"
}

// Parse extracts top-level exported structure from Go source.
// Shallow pass — does not handle multiline constructs, build tags, or cgo.
func (p *GoParser) Parse(source string) (FileStructure, error) {
	fs := FileStructure{
		Imports:   []string{},
		Functions: []string{},
		Classes:   []string{}, // used for Go types (struct, interface, etc.)
		Exports:   []string{},
	}

	lines := strings.Split(source, "\n")
	inImportBlock := false

	for _, line := range lines {
		// Strip inline comments before matching
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Import block: import (
		if trimmed == "import (" || strings.HasPrefix(trimmed, "import (\"") {
			inImportBlock = true
			// If there's a path on the same line as "import (", capture it below
		}

		if inImportBlock {
			if trimmed == ")" {
				inImportBlock = false
				continue
			}
			if m := goImportBlockItemRegex.FindStringSubmatch(trimmed); m != nil {
				fs.Imports = append(fs.Imports, m[1])
			}
			continue
		}

		// Single-line import
		if m := goImportSingleRegex.FindStringSubmatch(line); m != nil {
			fs.Imports = append(fs.Imports, m[1])
			continue
		}

		// Method (check before bare func — both start with "func ")
		if m := goMethodRegex.FindStringSubmatch(line); m != nil {
			fs.Functions = append(fs.Functions, m[1])
			continue
		}

		// Top-level function
		if m := goFuncRegex.FindStringSubmatch(line); m != nil {
			fs.Functions = append(fs.Functions, m[1])
			continue
		}

		// Type declaration
		if m := goTypeRegex.FindStringSubmatch(line); m != nil {
			fs.Classes = append(fs.Classes, m[1])
			continue
		}
	}

	sort.Strings(fs.Imports)
	sort.Strings(fs.Functions)
	sort.Strings(fs.Classes)

	fs.Imports = deduplicate(fs.Imports)
	fs.Functions = deduplicate(fs.Functions)
	fs.Classes = deduplicate(fs.Classes)

	return fs, nil
}
