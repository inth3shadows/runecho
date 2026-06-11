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
		for _, s := range f.Symbols {
			out = append(out, SymbolLoc{
				Name: s.Name,
				Kind: s.Kind,
				File: path,
				Line: s.Line,
				Hash: s.Hash,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		// Kind tiebreaker: a Go func appears as both function and export at the
		// same file/line(0); without this their order would be unstable.
		return out[i].Kind < out[j].Kind
	})
	return out
}
