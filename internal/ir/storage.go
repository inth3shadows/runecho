package ir

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultIRPath is the default location for IR storage.
const DefaultIRPath = ".ai/ir.json"

// IRVersion is the current IR format version. Version 2 added per-file Refs
// (bare call sites); version 3 added per-symbol body hashes (SymbolHashes) and
// the AST-backed Python symbol set; version 4 added per-symbol start lines
// (SymbolLines) for the repo map. A loaded IR with an older version must be
// fully regenerated, not incrementally updated — Update reuses entries for
// unchanged files verbatim, which would leave their new fields empty forever.
const IRVersion = 4

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
	// SymbolHashes maps "kind:name" to a hash of that symbol's source body, for
	// parsers that extract per-symbol spans (IR v3). It drives modified-symbol
	// diffing — see parser.FileStructure.SymbolHashes. Omitted when empty.
	SymbolHashes map[string]string `json:"symbol_hashes,omitempty"`
	// SymbolLines maps "kind:name" to the symbol's 1-based start line (IR v4),
	// for `runecho-ir map`. Omitted when empty. The pre-existing functions/
	// classes/exports/imports arrays are unchanged, so older consumers of
	// .ai/ir.json keep working.
	SymbolLines map[string]int `json:"symbol_lines,omitempty"`
}

// MarshalJSON implements deterministic JSON marshalling for IR.
// Files are sorted by path to ensure stable output across runs.
func (ir *IR) MarshalJSON() ([]byte, error) {
	// encoding/json sorts map keys before encoding (since Go 1.12), so file
	// ordering in the output is already deterministic — no need to pre-sort into
	// a second map; marshal ir.Files directly.
	return json.MarshalIndent(&struct {
		Version  int               `json:"version"`
		RootHash string            `json:"root_hash"`
		Files    map[string]FileIR `json:"files"`
	}{
		Version:  ir.Version,
		RootHash: ir.RootHash,
		Files:    ir.Files,
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

	// Write atomically: a crash mid-write must never leave a half-written
	// ir.json that fails to unmarshal. Write a sibling temp file, then rename
	// (atomic on the same filesystem on Linux/macOS).
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("failed to write IR file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup; the real file is untouched
		return fmt.Errorf("failed to replace IR file: %w", err)
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
