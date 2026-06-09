package ir

import "sort"

// SymbolLoc is one symbol's location — a deterministic projection of FileIR.
// Both `runecho-ir map` (CLI) and the MCP `locate` tool render this same shape
// so the two surfaces cannot drift.
type SymbolLoc struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // function | class | export | import
	File string `json:"file"`
	Line int    `json:"line"` // 1-based; 0 = unknown (no span / pre-v4 index)
	Hash string `json:"hash,omitempty"`
}

// SymbolLocations flattens the IR into a sorted slice of every indexed symbol's
// location (functions, classes, exports, imports). Deterministic ordering: by
// name, then file, then line. Hash is the full body hash where available;
// callers shorten it for display.
func (ir *IR) SymbolLocations() []SymbolLoc {
	var out []SymbolLoc
	for path, f := range ir.Files {
		emit := func(names []string, kind string) {
			for _, name := range names {
				key := kind + ":" + name
				out = append(out, SymbolLoc{
					Name: name,
					Kind: kind,
					File: path,
					Line: f.SymbolLines[key],
					Hash: f.SymbolHashes[key],
				})
			}
		}
		emit(f.Functions, "function")
		emit(f.Classes, "class")
		emit(f.Exports, "export")
		emit(f.Imports, "import")
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}
