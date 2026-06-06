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

	// Single-line exported var/const, optionally typed, with a value or type:
	//   var X = ...        const X = ...
	//   var X int = ...    const X int = ...
	//   var X int          (declaration without initializer)
	// The trailing (\s|=|$) ensures we match a full identifier (not a prefix) and
	// that a name is followed by a type, an assignment, or end-of-line.
	goVarConstSingleRegex = regexp.MustCompile(`^(?:var|const)\s+([A-Z]\w*)(?:\s|=|$)`)

	// Item inside a var(/const( block: an exported name at line start, followed
	// by a type, an assignment, or end-of-line (iota-style bare names included).
	goVarConstBlockItemRegex = regexp.MustCompile(`^([A-Z]\w*)(?:\s|=|,|$)`)
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
// Shallow pass — best-effort and line-based, not a real Go parser:
//   - Handled: single-line/typed/grouped exported var & const declarations,
//     import blocks, top-level funcs, methods, and type declarations.
//   - Exports holds top-level exported var/const names. Go does NOT use Exports
//     for exported funcs/types — those land in Functions/Classes respectively
//     (per-parser semantics; other parsers populate Exports differently).
//   - Not handled: build tags, cgo, multiline func/type signatures, or values
//     that span lines. Only top-level declarations (no leading whitespace) are
//     captured, so var/const inside a func body is correctly ignored.
func (p *GoParser) Parse(source string) (FileStructure, error) {
	fs := FileStructure{
		Imports:   []string{},
		Functions: []string{},
		Classes:   []string{}, // used for Go types (struct, interface, etc.)
		Exports:   []string{}, // top-level exported var/const names (see doc above)
	}

	lines := strings.Split(source, "\n")
	inImportBlock := false
	inVarConstBlock := false

	for _, line := range lines {
		line = stripLineComment(line)
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

		// Grouped var/const block: var ( or const ( on its own line. Items
		// inside are indented, so we match against the trimmed line; only
		// exported (capitalized) names are captured.
		if trimmed == "var (" || trimmed == "const (" {
			inVarConstBlock = true
			continue
		}
		if inVarConstBlock {
			if trimmed == ")" {
				inVarConstBlock = false
				continue
			}
			if m := goVarConstBlockItemRegex.FindStringSubmatch(trimmed); m != nil {
				fs.Exports = append(fs.Exports, m[1])
			}
			continue
		}

		// Single-line var/const (matched on the raw line so leading whitespace —
		// i.e. a declaration inside a func body — is excluded; top-level only).
		if m := goVarConstSingleRegex.FindStringSubmatch(line); m != nil {
			fs.Exports = append(fs.Exports, m[1])
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
	sort.Strings(fs.Exports)

	fs.Imports = deduplicate(fs.Imports)
	fs.Functions = deduplicate(fs.Functions)
	fs.Classes = deduplicate(fs.Classes)
	fs.Exports = deduplicate(fs.Exports)

	return fs, nil
}
