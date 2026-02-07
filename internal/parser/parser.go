package parser

// FileStructure represents the shallow parsed structure of a source file.
// It contains only top-level declarations, no nested scopes.
type FileStructure struct {
	Imports   []string // Import paths (sorted)
	Functions []string // Top-level function names (sorted)
	Classes   []string // Class names (sorted)
	Exports   []string // Exported symbol names (sorted)
}

// Parser extracts shallow structural information from source files.
type Parser interface {
	// Parse extracts top-level structure from source code.
	// Returns partial structure on parse errors (best-effort).
	Parse(source string) (FileStructure, error)

	// SupportsExtension returns true if this parser handles the file extension.
	SupportsExtension(ext string) bool
}
