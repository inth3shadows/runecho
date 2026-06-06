package ir

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// DefaultIRPath is the default location for IR storage.
const DefaultIRPath = ".ai/ir.json"

// IRVersion is the current IR format version. Version 2 added per-file Refs
// (bare call sites). A loaded IR with an older version must be fully
// regenerated, not incrementally updated — Update reuses entries for unchanged
// files verbatim, which would leave their new fields empty forever.
const IRVersion = 2

// IR represents the complete intermediate representation of a codebase.
type IR struct {
	Version  int               `json:"version"`
	RootHash string            `json:"root_hash"`
	Files    map[string]FileIR `json:"-"` // Excluded from direct marshalling
}

// FileIR represents the parsed structure of a single file.
type FileIR struct {
	Hash      string   `json:"hash"`      // SHA256 lowercase hex
	Imports   []string `json:"imports"`   // Sorted
	Functions []string `json:"functions"` // Sorted
	Classes   []string `json:"classes"`   // Sorted
	Exports   []string `json:"exports"`   // Sorted
	// Refs are the bare function-call targets that appear in the file, sorted
	// and deduplicated (IR v2). Extraction is shared with runecho-guard
	// (guard.ExtractRefs) so edit-time validation and index-time facts can
	// never disagree: qualified calls, builtins, and (for Go) unexported names
	// are excluded by the same rules at both ends.
	Refs []string `json:"refs"`
}

// MarshalJSON implements deterministic JSON marshalling for IR.
// Files are sorted by path to ensure stable output across runs.
func (ir *IR) MarshalJSON() ([]byte, error) {
	paths := make([]string, 0, len(ir.Files))
	for path := range ir.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	ordered := make(map[string]FileIR, len(paths))
	for _, path := range paths {
		ordered[path] = ir.Files[path]
	}

	// encoding/json sorts map keys before encoding (since Go 1.12) — that is
	// what enforces stable file ordering in the output, not the map itself.
	return json.MarshalIndent(&struct {
		Version  int               `json:"version"`
		RootHash string            `json:"root_hash"`
		Files    map[string]FileIR `json:"files"`
	}{
		Version:  ir.Version,
		RootHash: ir.RootHash,
		Files:    ordered,
	}, "", "  ")
}

// UnmarshalJSON implements JSON unmarshalling for IR.
func (ir *IR) UnmarshalJSON(data []byte) error {
	aux := &struct {
		Version  int               `json:"version"`
		RootHash string            `json:"root_hash"`
		Files    map[string]FileIR `json:"files"`
	}{}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	ir.Version = aux.Version
	ir.RootHash = aux.RootHash
	ir.Files = aux.Files

	return nil
}

// Save writes IR to a file with deterministic formatting.
// If path is empty string, uses DefaultIRPath.
func (ir *IR) Save(path string) error {
	if path == "" {
		path = DefaultIRPath
	}

	// Ensure the parent dir exists — the DefaultIRPath default (.ai/ir.json)
	// must work standalone, not only when the caller pre-created .ai/.
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create IR dir: %w", err)
		}
	}

	data, err := json.Marshal(ir)
	if err != nil {
		return fmt.Errorf("failed to marshal IR: %w", err)
	}

	// Write with consistent permissions
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write IR file: %w", err)
	}

	return nil
}

// Load reads IR from a file.
func Load(path string) (*IR, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read IR file: %w", err)
	}

	var ir IR
	if err := json.Unmarshal(data, &ir); err != nil {
		return nil, fmt.Errorf("failed to unmarshal IR: %w", err)
	}

	return &ir, nil
}
