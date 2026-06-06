package parser

import (
	"regexp"
	"sort"
	"strings"
)

// JSParser implements shallow parsing for .js, .ts, .gs files.
// Uses regex patterns - not semantically correct, but deterministic.
type JSParser struct{}

var (
	// Import patterns (ESM and CommonJS)
	// Matches: import ... from "path"
	importESMRegex = regexp.MustCompile(`import\s+(?:[\w\s{},*]*\s+from\s+)?['"]([^'"]+)['"]`)
	// Matches: require("path")
	importCJSRegex = regexp.MustCompile(`require\s*\(\s*['"]([^'"]+)['"]\s*\)`)

	// Function declarations
	// Matches: function name(...) or async function name(...)
	funcDeclRegex = regexp.MustCompile(`(?:^|\s)(?:async\s+)?function\s+(\w+)\s*\(`)
	// Matches: const/let/var name = function(...) or name = async function(...)
	funcExprRegex = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?function\s*\(`)
	// Matches: const/let/var name = (...) => or name = async (...) =>
	arrowFuncRegex = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[\w]+)\s*=>`)

	// Class declarations
	// Matches: class Name or export class Name or export default class Name
	classDeclRegex = regexp.MustCompile(`(?:^|\s)(?:export\s+(?:default\s+)?)?class\s+(\w+)`)

	// Export patterns
	// Matches: export { name1, name2 }
	exportNamedRegex = regexp.MustCompile(`export\s+\{([^}]+)\}`)
	// Matches: export const/let/var/function/class name
	exportDeclRegex = regexp.MustCompile(`export\s+(?:const|let|var|function|class|async\s+function)\s+(\w+)`)
	// Matches: export default name
	exportDefaultRegex = regexp.MustCompile(`export\s+default\s+(\w+)`)
)

// NewJSParser creates a new JavaScript/TypeScript parser.
func NewJSParser() *JSParser {
	return &JSParser{}
}

// SupportsExtension returns true for .js, .ts, .jsx, .tsx, .gs files.
func (p *JSParser) SupportsExtension(ext string) bool {
	switch ext {
	case ".js", ".ts", ".jsx", ".tsx", ".gs":
		return true
	default:
		return false
	}
}

// Parse extracts top-level structure from JavaScript/TypeScript source.
// This is a shallow parse - it may miss nested declarations or misparse
// complex syntax (JSX, template literals, decorators). Best-effort only.
func (p *JSParser) Parse(source string) (FileStructure, error) {
	fs := FileStructure{
		Imports:   []string{},
		Functions: []string{},
		Classes:   []string{},
		Exports:   []string{},
	}

	// Remove comments to avoid false matches
	source = removeComments(source)

	// Extract imports
	fs.Imports = extractImports(source)

	// Extract functions
	fs.Functions = extractFunctions(source)

	// Extract classes
	fs.Classes = extractClasses(source)

	// Extract exports
	fs.Exports = extractExports(source)

	// Sort all slices for determinism
	sort.Strings(fs.Imports)
	sort.Strings(fs.Functions)
	sort.Strings(fs.Classes)
	sort.Strings(fs.Exports)

	// Deduplicate
	fs.Imports = deduplicate(fs.Imports)
	fs.Functions = deduplicate(fs.Functions)
	fs.Classes = deduplicate(fs.Classes)
	fs.Exports = deduplicate(fs.Exports)

	return fs, nil
}

// removeComments strips single-line and multi-line comments.
// Multi-line /* … */ comments are removed via regex. Single-line // comments
// are stripped per-line with string-literal awareness so that URLs inside
// import strings (e.g. import 'http://example.com') are preserved correctly.
func removeComments(source string) string {
	multiLineRegex := regexp.MustCompile(`/\*[\s\S]*?\*/`)
	source = multiLineRegex.ReplaceAllString(source, "")

	lines := strings.Split(source, "\n")
	for i, line := range lines {
		lines[i] = stripLineComment(line)
	}
	return strings.Join(lines, "\n")
}

// stripLineComment removes a // comment from a line, skipping // that appears
// inside a string literal (single-quote, double-quote, or backtick).
// Handles backslash escapes inside string literals. Template literal nesting
// and exotic Unicode escapes are not tracked — this is still best-effort.
func stripLineComment(line string) string {
	inStr := false
	var strChar byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inStr {
			if c == '\\' {
				i++ // skip the escaped character
				continue
			}
			if c == strChar {
				inStr = false
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			inStr = true
			strChar = c
			continue
		}
		if c == '/' && i+1 < len(line) && line[i+1] == '/' {
			return line[:i]
		}
	}
	return line
}

// extractImports finds all import statements.
func extractImports(source string) []string {
	imports := []string{}

	// ESM imports
	matches := importESMRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			imports = append(imports, match[1])
		}
	}

	// CommonJS requires
	matches = importCJSRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			imports = append(imports, match[1])
		}
	}

	return imports
}

// extractFunctions finds all top-level function declarations.
func extractFunctions(source string) []string {
	functions := []string{}

	// Function declarations
	matches := funcDeclRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			functions = append(functions, match[1])
		}
	}

	// Function expressions
	matches = funcExprRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			functions = append(functions, match[1])
		}
	}

	// Arrow functions
	matches = arrowFuncRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			functions = append(functions, match[1])
		}
	}

	return functions
}

// extractClasses finds all class declarations.
func extractClasses(source string) []string {
	classes := []string{}

	matches := classDeclRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			classes = append(classes, match[1])
		}
	}

	return classes
}

// extractExports finds all exported symbol names.
func extractExports(source string) []string {
	exports := []string{}

	// Named exports: export { foo, bar }
	matches := exportNamedRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			names := strings.Split(match[1], ",")
			for _, name := range names {
				name = strings.TrimSpace(name)
				// Handle "as" syntax: export { foo as bar } -> extract "foo"
				if idx := strings.Index(name, " as "); idx >= 0 {
					name = strings.TrimSpace(name[:idx])
				}
				if name != "" {
					exports = append(exports, name)
				}
			}
		}
	}

	// Declaration exports: export const foo = ...
	matches = exportDeclRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			exports = append(exports, match[1])
		}
	}

	// Default exports: export default foo
	matches = exportDefaultRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			exports = append(exports, match[1])
		}
	}

	return exports
}

// deduplicate removes duplicate entries from a sorted slice.
func deduplicate(sorted []string) []string {
	if len(sorted) == 0 {
		return sorted
	}

	result := []string{sorted[0]}
	for i := 1; i < len(sorted); i++ {
		if sorted[i] != sorted[i-1] {
			result = append(result, sorted[i])
		}
	}

	return result
}
