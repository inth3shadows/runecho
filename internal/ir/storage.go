package ir

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// DefaultIRPath is the default location for IR storage.
const DefaultIRPath = ".ai/ir.json"

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
}

// orderedFile is used for deterministic JSON marshalling.
type orderedFile struct {
	Path string `json:"path"`
	Data FileIR `json:"data"`
}

// MarshalJSON implements deterministic JSON marshalling for IR.
// Files are sorted by path to ensure stable output.
func (ir *IR) MarshalJSON() ([]byte, error) {
	// Sort file paths
	paths := make([]string, 0, len(ir.Files))
	for path := range ir.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	// Build ordered list
	orderedFiles := make(map[string]FileIR)
	for _, path := range paths {
		orderedFiles[path] = ir.Files[path]
	}

	// Create anonymous struct with ordered fields
	return json.MarshalIndent(&struct {
		Version  int               `json:"version"`
		RootHash string            `json:"root_hash"`
		Files    map[string]FileIR `json:"files"`
	}{
		Version:  ir.Version,
		RootHash: ir.RootHash,
		Files:    orderedFiles,
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
