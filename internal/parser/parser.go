package parser

// FileStructure represents the parsed structure of a source file.
type FileStructure struct {
	Imports   []string // Import paths (sorted)
	Functions []string // Function names, incl. methods/nested as Outer.name (sorted)
	Classes   []string // Class names, nested qualified as Outer.Inner (sorted)
	Exports   []string // Exported symbol names (sorted)

	// SymbolHashes maps "kind:name" (e.g. "function:Reader.fetch") to a hash of
	// that symbol's source body, for parsers that extract per-symbol spans (the
	// AST-backed Python parser). It enables modified-symbol diffing: a symbol
	// present in both snapshots whose body hash changed is reported "modified"
	// rather than invisible. Nil for regex parsers that cannot isolate a body —
	// the diff then degrades to add/remove only for their symbols.
	SymbolHashes map[string]string

	// SymbolLines maps "kind:name" to the symbol's 1-based start line, for
	// parsers that know it (the AST-backed Python parser). Powers `runecho-ir
	// map` (symbol → file:line). Nil for parsers without span info; consumers
	// render an unknown line as "?".
	SymbolLines map[string]int
}

// Parser extracts shallow structural information from source files.
type Parser interface {
	// Parse extracts top-level structure from source code.
	// Returns partial structure on parse errors (best-effort).
	Parse(source string) (FileStructure, error)

	// SupportsExtension returns true if this parser handles the file extension.
	SupportsExtension(ext string) bool
}

// maxParseNestDepth bounds the bracket/brace/paren nesting depth that the
// tree-sitter-backed parsers (jsSymbolsFromAST, pySymbolsFromAST) will accept,
// and the maximum AST recursion depth their walk functions descend. The vendored
// pure-Go tree-sitter runtime is super-linear in nesting depth — a crafted
// ~100 KB source file of nested brackets parses for minutes, hanging the indexer
// or MCP server (a local denial of service). Separately, an AST nested deeper
// than the goroutine stack can hold overflows with a runtime throw, which the
// parsers' recover() guards cannot catch, crashing the process. Real
// hand-authored code nests only a few dozen deep; 1000 leaves generous headroom
// while capping worst-case parse time to tens of milliseconds and walk recursion
// to a safe stack depth. Input over the cap degrades to no AST symbols (the same
// fail-safe as a parser panic). The check is a pure function of the bytes —
// unlike a wall-clock timeout, it preserves RunEcho's same-input-same-output
// determinism guarantee.
const maxParseNestDepth = 1000

// exceedsNestDepth reports whether src nests (), [], or {} deeper than
// maxParseNestDepth. It is a single linear byte scan tracking the running count
// of all three opener classes together — a sound upper bound on AST nesting,
// which is all that's needed to reject pathological input cheaply before the
// expensive parse. It does not match bracket kinds; brackets inside string or
// comment literals inflate the count, but only ever toward skipping a parse we
// would rather skip anyway (a 1000-deep run of literal brackets is not
// hand-authored source).
func exceedsNestDepth(src []byte) bool {
	depth := 0
	for _, b := range src {
		switch b {
		case '(', '[', '{':
			depth++
			if depth > maxParseNestDepth {
				return true
			}
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return false
}

// ExtAwareParser is an optional extension implemented by parsers that need the
// file extension to do their job — currently the JS/TS parser, which selects a
// tree-sitter grammar by .js/.ts/.tsx. The generator passes the extension via
// ParseExt when a parser implements this; parsers that don't are called via the
// plain Parse method. Keeping it optional avoids churning the Go and Python
// parsers (whose extension is unambiguous) and all their callers.
type ExtAwareParser interface {
	ParseExt(source, ext string) (FileStructure, error)
}
