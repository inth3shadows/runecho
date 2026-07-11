package ir

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// DefaultIRPath is the default location for IR storage.
const DefaultIRPath = ".ai/ir.json"

// IRVersion is the current IR format version. v2 added per-file Refs (bare call
// sites); v3 added per-symbol body hashes; v4 added per-symbol start lines. v5
// unifies the per-symbol data into a single FileIR.Symbols slice — the on-disk
// JSON still emits the legacy functions/classes/exports/imports arrays and the
// symbol_hashes/symbol_lines maps for backward compatibility (e.g. kb-mcp reads
// functions/classes), alongside a canonical `symbols` array. v6 adds body hashes
// for class/struct symbols (previously located but never hashed). A loaded IR
// with an older version must be fully regenerated, not incrementally updated —
// Update reuses unchanged-file entries verbatim, which would leave new fields
// (or, as of v6, newly-populated existing fields) empty/stale forever.
const IRVersion = 6

// IR represents the complete intermediate representation of a codebase.
type IR struct {
	Version  int               `json:"version"`
	RootHash string            `json:"root_hash"`
	Files    map[string]FileIR `json:"-"` // Excluded from direct marshalling
}

// Symbol is one declared symbol. Kind is function | class | export | import |
// import_name | export_wildcard (a JS/TS bare `export * from './mod'`
// specifier — see FileStructure.WildcardReexports). Line is the 1-based start
// line (0 = unknown). Hash is the symbol's body hash, empty unless the parser
// isolated a body (AST functions/methods carry it).
type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Line int    `json:"line,omitempty"`
	Hash string `json:"hash,omitempty"`
}

// FileIR represents the parsed structure of a single file. Symbols is the
// canonical per-symbol model (sorted by kind, then name); the legacy parallel
// arrays/maps that .ai/ir.json still carries are derived at the JSON boundary by
// MarshalJSON. Custom Marshal/UnmarshalJSON bridge the two, so existing
// consumers of the legacy fields keep working while internal code uses Symbols.
type FileIR struct {
	Hash    string   // SHA256 lowercase hex
	Symbols []Symbol // canonical symbol set; kept sorted by (kind, name)
	// Refs are the bare function-call targets that appear in the file, sorted
	// and deduplicated (IR v2). Extraction is shared with runecho-guard
	// (guard.ExtractRefs) so edit-time validation and index-time facts can
	// never disagree.
	Refs []string
}

// namesOf returns the names of all symbols of the given kind. Symbols is kept
// sorted by (kind, name), so the result is sorted.
func (f FileIR) namesOf(kind string) []string {
	var out []string
	for _, s := range f.Symbols {
		if s.Kind == kind {
			out = append(out, s.Name)
		}
	}
	return out
}

// sortSymbols orders symbols deterministically by kind, then name.
func sortSymbols(syms []Symbol) {
	sort.Slice(syms, func(i, j int) bool {
		if syms[i].Kind != syms[j].Kind {
			return syms[i].Kind < syms[j].Kind
		}
		return syms[i].Name < syms[j].Name
	})
}

// fileIRJSON is the on-disk shape of a FileIR: the canonical `symbols` array
// PLUS the legacy fields, kept so existing .ai/ir.json consumers do not break.
type fileIRJSON struct {
	Hash         string            `json:"hash"`
	Imports      []string          `json:"imports"`
	Functions    []string          `json:"functions"`
	Classes      []string          `json:"classes"`
	Exports      []string          `json:"exports"`
	Refs         []string          `json:"refs"`
	SymbolHashes map[string]string `json:"symbol_hashes,omitempty"`
	SymbolLines  map[string]int    `json:"symbol_lines,omitempty"`
	Symbols      []Symbol          `json:"symbols"`
}

func emptySliceIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// MarshalJSON emits the legacy arrays/maps (derived from Symbols) for backward
// compatibility, plus the canonical `symbols` array (IR v5). Output is
// deterministic: namesOf preserves the (kind, name) sort.
func (f FileIR) MarshalJSON() ([]byte, error) {
	hashes := map[string]string{}
	lines := map[string]int{}
	for _, s := range f.Symbols {
		key := s.Kind + ":" + s.Name
		if s.Hash != "" {
			hashes[key] = s.Hash
		}
		if s.Line != 0 {
			lines[key] = s.Line
		}
	}
	out := fileIRJSON{
		Hash:      f.Hash,
		Imports:   emptySliceIfNil(f.namesOf("import")),
		Functions: emptySliceIfNil(f.namesOf("function")),
		Classes:   emptySliceIfNil(f.namesOf("class")),
		Exports:   emptySliceIfNil(f.namesOf("export")),
		Refs:      emptySliceIfNil(f.Refs),
		Symbols:   emptySliceIfNil(f.Symbols),
	}
	if len(hashes) > 0 {
		out.SymbolHashes = hashes
	}
	if len(lines) > 0 {
		out.SymbolLines = lines
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads either the v5 shape (canonical `symbols`) or a legacy
// pre-v5 ir.json (parallel arrays + symbol_hashes/symbol_lines maps), so an
// older on-disk IR still loads before the version guard regenerates it.
func (f *FileIR) UnmarshalJSON(data []byte) error {
	var in fileIRJSON
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	f.Hash = in.Hash
	f.Refs = in.Refs
	if len(in.Symbols) > 0 {
		f.Symbols = in.Symbols
	} else {
		f.Symbols = legacySymbols(in)
	}
	sortSymbols(f.Symbols)
	return nil
}

// legacySymbols reconstructs the Symbols slice from a pre-v5 ir.json's parallel
// arrays and "kind:name"-keyed hash/line maps.
func legacySymbols(in fileIRJSON) []Symbol {
	var syms []Symbol
	add := func(names []string, kind string) {
		for _, n := range names {
			key := kind + ":" + n
			syms = append(syms, Symbol{Name: n, Kind: kind, Line: in.SymbolLines[key], Hash: in.SymbolHashes[key]})
		}
	}
	add(in.Functions, "function")
	add(in.Classes, "class")
	add(in.Exports, "export")
	add(in.Imports, "import")
	return syms
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
		// 0700 dir / 0600 file (below): the IR holds symbol and import names
		// derived from the source; keep it owner-only rather than world-readable
		// on a shared host, consistent with the central store's perms.
		if err := os.MkdirAll(dir, 0700); err != nil {
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
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("failed to write IR file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup; the real file is untouched
		return fmt.Errorf("failed to replace IR file: %w", err)
	}

	return nil
}

// maxIRBytes caps the size of an .ai/ir.json Load will read into memory. The IR
// holds only symbol names, hashes, and line numbers, so even a large monorepo
// stays far below this. The cap stops a crafted or corrupt file — e.g. one
// planted in an untrusted repo, which the PostToolUse guard auto-reads on every
// edit (refreshIRForFile) — from OOMing the process. An oversized file returns
// an error; the auto-refresh hook already degrades to a full regenerate when
// Load errors, so it self-heals rather than trusting the giant file.
const maxIRBytes = 100 << 20 // 100 MiB

// Load reads IR from a file.
func Load(path string) (*IR, error) { return loadCapped(path, maxIRBytes) }

// loadCapped is Load with an explicit size limit (seam for tests). It reads at
// most max+1 bytes so a giant file never fully buffers, then rejects if the file
// exceeds max.
func loadCapped(path string, max int64) (*IR, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read IR file: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read IR file: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("IR file %q exceeds size cap of %d bytes", path, max)
	}

	var ir IR
	if err := json.Unmarshal(data, &ir); err != nil {
		return nil, fmt.Errorf("failed to unmarshal IR: %w", err)
	}

	return &ir, nil
}
