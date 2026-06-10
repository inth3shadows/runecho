package parser

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// JSParser parses .js, .ts, .jsx, .tsx, .gs files. Imports and exports are
// extracted with deterministic regex (line-oriented constructs). Functions and
// classes use a real tree-sitter AST via a pure-Go (CGO-free) runtime when the
// matching grammar is embedded in the build, so they carry per-symbol start
// lines and function body hashes (matching the Python parser's FileStructure
// contract). When the grammar is absent (a build without the grammar_subset_*
// tags), it degrades to the former regex extraction — names only, no spans —
// so symbol coverage never regresses.
//
// Altitude: like the Go parser, it captures top-level functions/classes plus
// class methods (qualified as Class.method); it does not descend into function
// bodies, so nested closures/callbacks are intentionally omitted.
//
// Best-effort: the shipped grammars are reduced "subset" grammars covering the
// common surface (declarations, classes/interfaces/enums, methods, arrow and
// function consts). Some advanced TypeScript syntax — notably a return-typed
// arrow const, `const f = (x: T): R => ...` — does not parse cleanly and yields
// fewer symbols. This matches the prior regex parser's gap (no regression); it
// is documented rather than silently dropped.
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
	// Matches: export default function Foo / export default class Foo / export default ident.
	// Three capture groups — first non-empty wins; keywords (function/class/async) in
	// group 3 are discarded so anonymous defaults don't pollute Exports.
	exportDefaultRegex = regexp.MustCompile(`export\s+default\s+(?:(?:async\s+)?function\s+(\w+)|class\s+(\w+)|(\w+))`)
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

// Parse satisfies the Parser interface. Without the file extension it cannot
// pick the most specific grammar, so it assumes JavaScript (the JS grammar also
// parses JSX). The generator calls ParseExt instead (via the ExtAwareParser
// interface), which selects typescript/tsx for .ts/.tsx.
func (p *JSParser) Parse(source string) (FileStructure, error) {
	return p.parse(source, "")
}

// ParseExt is the extension-aware entry point (see ExtAwareParser). ext selects
// the tree-sitter grammar: .ts → typescript, .tsx → tsx, everything else → js.
func (p *JSParser) ParseExt(source, ext string) (FileStructure, error) {
	return p.parse(source, ext)
}

func (p *JSParser) parse(source, ext string) (FileStructure, error) {
	// Normalize line endings so spans/hashes are independent of CRLF vs LF
	// (parity with the Python parser).
	source = strings.ReplaceAll(source, "\r\n", "\n")

	// Imports and exports stay regex (line-oriented; same split as python.go).
	// Run them on comment-stripped source so commented-out statements are ignored.
	noComments := removeComments(source)
	imports := extractImports(noComments)
	exports := extractExports(noComments)

	// Functions and classes: AST when the grammar is available, regex otherwise.
	var (
		functions, classes []string
		hashes             map[string]string
		lines              map[string]int
	)
	if lang := jsLanguageFor(ext); lang != nil {
		functions, classes, hashes, lines = jsSymbolsFromAST(source, lang)
	} else {
		// No grammar embedded in this build — degrade to names only, no spans
		// (preserves the regex-era contract; better than dropping JS symbols).
		functions = extractFunctions(noComments)
		classes = extractClasses(noComments)
	}

	sort.Strings(imports)
	sort.Strings(functions)
	sort.Strings(classes)
	sort.Strings(exports)

	return FileStructure{
		Imports:      deduplicate(imports),
		Functions:    deduplicate(functions),
		Classes:      deduplicate(classes),
		Exports:      deduplicate(exports),
		SymbolHashes: hashes,
		SymbolLines:  lines,
	}, nil
}

// Grammar caches: each grammar is loaded once and the *Language is safe for
// concurrent reads (a fresh ts.Parser is created per Parse since it is not
// concurrency-safe). The accessors return nil when the corresponding
// grammar_subset_* blob is not embedded in the build.
var (
	jsLangOnce, tsLangOnce, tsxLangOnce sync.Once
	jsLang, tsLang, tsxLang             *ts.Language
)

// jsLanguageFor returns the tree-sitter grammar for the given file extension,
// or nil if that grammar is not embedded in this build.
func jsLanguageFor(ext string) *ts.Language {
	switch ext {
	case ".ts":
		return cachedLang(&tsLangOnce, &tsLang, "typescript", grammars.TypescriptLanguage)
	case ".tsx":
		return cachedLang(&tsxLangOnce, &tsxLang, "tsx", grammars.TsxLanguage)
	default: // .js, .jsx, .gs, "" — the JS grammar also parses JSX.
		return cachedLang(&jsLangOnce, &jsLang, "javascript", grammars.JavascriptLanguage)
	}
}

// cachedLang lazily loads a grammar under once, recovering from a decode panic
// so a bad blob degrades to nil (no AST symbols) rather than crashing the first
// Parse call — mirrors the Python parser's grammar-load guard.
func cachedLang(once *sync.Once, dst **ts.Language, name string, load func() *ts.Language) *ts.Language {
	once.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "runecho: %s grammar failed to load (%v); %s symbols disabled\n", name, r, name)
			}
		}()
		*dst = load()
	})
	return *dst
}

// jsSymbolsFromAST walks the JS/TS AST and returns every function and class
// definition. Methods/nested defs are qualified by their enclosing scope (e.g.
// "Widget.doThing"), matching the Python and Go parsers, so identical leaf names
// in different scopes never collide. Functions/methods carry a body hash keyed
// "function:<qualified name>" for modified-symbol diffing; classes, interfaces,
// enums, and type aliases are located (start line) but not hashed (their changes
// surface through their members).
func jsSymbolsFromAST(source string, lang *ts.Language) (functions, classes []string, hashes map[string]string, lines map[string]int) {
	src := []byte(source)
	tree, err := ts.NewParser(lang).Parse(src)
	if err != nil || tree == nil || tree.RootNode() == nil {
		return nil, nil, nil, nil
	}

	hashes = make(map[string]string)
	lines = make(map[string]int)

	recordHash := func(key string, span []byte) {
		h := hashBytesHex(span)
		if existing, ok := hashes[key]; ok {
			h = hashBytesHex([]byte(existing + h))
		}
		hashes[key] = h
	}
	recordLine := func(key string, line int) {
		if _, ok := lines[key]; !ok {
			lines[key] = line
		}
	}
	recordFunc := func(full string, span *ts.Node) {
		functions = append(functions, full)
		recordHash("function:"+full, src[span.StartByte():span.EndByte()])
		recordLine("function:"+full, int(span.StartPoint().Row)+1)
	}
	recordClass := func(full string, node *ts.Node) {
		classes = append(classes, full)
		recordLine("class:"+full, int(node.StartPoint().Row)+1)
	}
	fieldText := func(n *ts.Node, field string) string {
		if f := n.ChildByFieldName(field, lang); f != nil {
			return f.Text(src)
		}
		return ""
	}
	// childOfType returns the first named child of n whose type is in types.
	childOfType := func(n *ts.Node, types ...string) *ts.Node {
		for i := 0; i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			for _, want := range types {
				if c.Type(lang) == want {
					return c
				}
			}
		}
		return nil
	}

	var walk func(n *ts.Node, prefix string)
	walk = func(n *ts.Node, prefix string) {
		for i := 0; i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			switch c.Type(lang) {
			case "function_declaration", "generator_function_declaration",
				"method_definition", "method_signature":
				// A named function/method. We do NOT recurse its body: like the Go
				// parser (and unlike Python), JS/TS symbols are top-level decls plus
				// class methods — capturing nested closures/callbacks would just add
				// orientation noise. Bare function_expressions (e.g. a named
				// callback `setTimeout(function tick(){})`) are intentionally NOT a
				// case here; only those bound to a variable (below) are captured.
				name := fieldText(c, "name")
				if name == "" {
					continue
				}
				recordFunc(qualify(prefix, name), c)

			case "class_declaration", "abstract_class_declaration",
				"interface_declaration", "enum_declaration", "type_alias_declaration":
				name := fieldText(c, "name")
				if name == "" {
					continue
				}
				full := qualify(prefix, name)
				recordClass(full, c)
				walk(c, full) // descend into the body so methods become Class.method

			case "variable_declarator":
				// `const name = () => ...` / `= function(){}`: attribute the
				// function to the bound variable name, spanning the function value
				// so a body change flips the hash. Body is not recursed (top-level
				// altitude — see the function case above).
				name := fieldText(c, "name")
				if fn := childOfType(c, "arrow_function", "function_expression"); fn != nil && name != "" {
					recordFunc(qualify(prefix, name), fn)
					continue
				}
				// `const X = class {...}` / `= class Named {...}`: record the class
				// under the bound variable name and descend for its methods.
				if cls := childOfType(c, "class", "class_expression"); cls != nil && name != "" {
					full := qualify(prefix, name)
					recordClass(full, cls)
					walk(cls, full)
					continue
				}
				walk(c, prefix)

			default:
				// Recurse through wrappers (export_statement, lexical_declaration,
				// class_body, statement_block, and ERROR-recovery nodes) so
				// declarations nested inside them are still found.
				walk(c, prefix)
			}
		}
	}
	walk(tree.RootNode(), "")

	if len(hashes) == 0 {
		hashes = nil
	}
	if len(lines) == 0 {
		lines = nil
	}
	return functions, classes, hashes, lines
}

// Parse extracts top-level structure from JavaScript/TypeScript source.
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

// extractFunctions finds all top-level function declarations (regex fallback
// used only when no tree-sitter grammar is embedded).
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

// extractClasses finds all class declarations (regex fallback).
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

	// Default exports: export default [function|class] Foo
	matches = exportDefaultRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		name := match[1] // export default [async] function Foo
		if name == "" {
			name = match[2] // export default class Foo
		}
		if name == "" && match[3] != "function" && match[3] != "class" && match[3] != "async" {
			name = match[3] // export default identifier
		}
		if name != "" {
			exports = append(exports, name)
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
