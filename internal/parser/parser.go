package parser

// FileStructure represents the parsed structure of a source file.
type FileStructure struct {
	Imports   []string // Import paths (sorted)
	Functions []string // Function names, incl. methods/nested as Outer.name (sorted)
	Classes   []string // Class names, nested qualified as Outer.Inner (sorted)
	Exports   []string // Exported symbol names (sorted)

	// WildcardReexports lists the raw module specifiers pulled in via a bare
	// `export * from './mod'` re-export (JS/TS only; sorted). A single-file
	// parser cannot enumerate the target module's own bindings, so these names
	// are never added to Exports — recording the specifier here at least makes
	// the gap visible to downstream consumers (the IR generator resolves it
	// in-repo where possible) instead of silently dropping it. Nil for parsers
	// without this construct (everything but JS/TS) or files with no bare
	// wildcard re-export.
	WildcardReexports []string

	// Unexported lists a file's unexported top-level declarations (sorted). Go
	// only today. These are NOT part of the exported API surface, and consumers
	// that render that surface must keep filtering them out by kind — they are
	// indexed so that edit-time resolution has something to validate an
	// unexported reference against, which is a different job from describing a
	// package's public shape.
	//
	// Funcs are bare (`parse`), methods are receiver-qualified (`Reader.parse`),
	// matching how the exported halves are named. Types, consts and vars are
	// included and are not optional: a Go bare "call" can be a type conversion
	// (`myType(x)`) or a call through a package-level func-typed var
	// (`handler()`), so omitting them would manufacture false positives in
	// exactly the population this field exists to make checkable.
	Unexported []string

	// Fields lists struct field names, receiver-qualified (`Reader.buf`), sorted.
	// Go only today; exported and unexported alike, because this is not an
	// API-surface list — it exists so that `v.name` resolution can tell an
	// invented member from a real one. A func-typed field is called exactly like
	// a method (`rb.flushF(rb)`), so a checker that knows only methods reports
	// every such call as a hallucination.
	//
	// Embedded fields are omitted: they have no name of their own, and the names
	// they promote come from another type the single-file parser cannot resolve.
	Fields []string

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

	// SymbolDocs maps "kind:name" to the FIRST line of the doc comment attached
	// to that symbol — a Go doc comment or a Python docstring — with comment
	// syntax stripped and length capped (see firstDocLine). Absent from the map
	// when the symbol carries no doc comment; never synthesized, never a summary
	// of the code.
	//
	// This is the one field in the IR that is NOT derived from code semantics: a
	// doc comment is human text that can drift from the behaviour it describes.
	// That asymmetry is why it is called "doc" (the first line of a comment)
	// rather than "purpose" (verified intent) — consumers must not treat it as
	// checked against the implementation. Nil for parsers that don't extract
	// comments and for files whose symbols are all undocumented.
	SymbolDocs map[string]string
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
