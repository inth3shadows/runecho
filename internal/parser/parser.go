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
