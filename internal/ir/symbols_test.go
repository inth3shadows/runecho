package ir

import "testing"

// TestSymbolLocations_KindTiebreaker exercises the final sort key in
// SymbolLocations: when two symbols share the same name, file, and line (all
// zero here, the pre-v4 unspanned case), the Kind field breaks the tie
// alphabetically so the output is deterministic. "export" < "function"
// lexicographically, so the export entry must always sort first.
func TestSymbolLocations_KindTiebreaker(t *testing.T) {
	subject := &IR{
		Version:  IRVersion,
		RootHash: "x",
		Files: map[string]FileIR{
			"a.go": {
				Hash: "h1",
				Symbols: []Symbol{
					// Intentionally listed in reverse alphabetical Kind order to
					// confirm the sort—not the insertion order—governs the result.
					{Name: "Foo", Kind: "function", Line: 0},
					{Name: "Foo", Kind: "export", Line: 0},
				},
			},
		},
	}

	locs := subject.SymbolLocations()
	if len(locs) != 2 {
		t.Fatalf("got %d locations, want 2", len(locs))
	}

	// Alphabetical Kind tiebreaker: "export" < "function".
	if locs[0].Kind != "export" || locs[1].Kind != "function" {
		t.Errorf("Kind ordering = [%q, %q], want [\"export\", \"function\"]",
			locs[0].Kind, locs[1].Kind)
	}
	if locs[0].Name != "Foo" || locs[1].Name != "Foo" {
		t.Errorf("Name = [%q, %q], want both \"Foo\"", locs[0].Name, locs[1].Name)
	}
	if locs[0].File != "a.go" || locs[1].File != "a.go" {
		t.Errorf("File = [%q, %q], want both \"a.go\"", locs[0].File, locs[1].File)
	}

	// Determinism: 100 iterations must produce the same ordering. The sort is
	// over a slice built from a map, so without all four tiebreakers the order
	// can vary run-to-run.
	for i := 0; i < 100; i++ {
		got := subject.SymbolLocations()
		if len(got) != 2 || got[0].Kind != "export" || got[1].Kind != "function" {
			t.Fatalf("iteration %d: non-deterministic ordering [%q, %q]",
				i, got[0].Kind, got[1].Kind)
		}
	}
}
