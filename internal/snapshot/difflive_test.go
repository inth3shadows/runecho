package snapshot

import (
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/ir"
)

// TestDiffLive covers the live-IR diff path (DiffLive → irToMaps → computeDiff)
// and FormatFull rendering — exercised in production by every `diff --since`,
// `verify`, and default MCP `diff`, but otherwise untested in this package.
func TestDiffLive(t *testing.T) {
	db, _ := openTemp(t)
	id, err := db.EnrollRepo("r", "/repos/r", "", 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	// Baseline snapshot: main.go defines Foo.
	sid, err := db.SaveSnapshot(id, "s", "base", "/repos/r", makeIR("h1", "Foo"))
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	base, err := db.GetByID(sid)
	if err != nil || base == nil {
		t.Fatalf("GetByID: %v", err)
	}

	// Live IR: main.go now defines Bar (Foo gone), and a new file appears.
	live := &ir.IR{
		Version:  1,
		RootHash: "h2",
		Files: map[string]ir.FileIR{
			"main.go": {Hash: "h2", Symbols: []ir.Symbol{{Name: "Bar", Kind: "function"}}},
			"new.go":  {Hash: "h3", Symbols: []ir.Symbol{{Name: "Baz", Kind: "function"}}},
		},
	}

	res, err := db.DiffLive(*base, live)
	if err != nil {
		t.Fatalf("DiffLive: %v", err)
	}

	if res.SnapshotB.ID != -1 {
		t.Errorf("live sentinel ID = %d, want -1", res.SnapshotB.ID)
	}
	// Bar added + Baz added = 2; Foo removed = 1.
	if res.TotalAdded != 2 {
		t.Errorf("TotalAdded = %d, want 2", res.TotalAdded)
	}
	if res.TotalRemoved != 1 {
		t.Errorf("TotalRemoved = %d, want 1", res.TotalRemoved)
	}

	out := FormatFull(res)
	if !strings.Contains(out, "new.go") {
		t.Errorf("FormatFull missing the added file:\n%s", out)
	}
	if !strings.Contains(out, "+ Bar") || !strings.Contains(out, "- Foo") {
		t.Errorf("FormatFull missing expected symbol deltas:\n%s", out)
	}
}

// TestDiffLive_ModifiedSymbol covers in-place symbol changes (Bug 2): a function
// whose body hash changed while its name is unchanged is reported "modified", not
// invisible. It also covers the new-function case and asserts the file hash alone
// no longer hides symbol-level drift.
func TestDiffLive_ModifiedSymbol(t *testing.T) {
	db, _ := openTemp(t)
	id, _ := db.EnrollRepo("r", "/repos/r", "", 0)

	// Baseline: reads.py defines get_scope (body hash hA) and set_scope.
	baseIR := &ir.IR{
		Version:  ir.IRVersion,
		RootHash: "r1",
		Files: map[string]ir.FileIR{
			"reads.py": {
				Hash: "f1",
				Symbols: []ir.Symbol{
					{Name: "get_scope", Kind: "function", Hash: "hA"},
					{Name: "set_scope", Kind: "function", Hash: "hS"},
				},
			},
		},
	}
	sid, err := db.SaveSnapshot(id, "s", "base", "/repos/r", baseIR)
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	base, _ := db.GetByID(sid)

	// Live: get_scope body changed (hA→hB), new _probe added, set_scope unchanged.
	live := &ir.IR{
		Version:  ir.IRVersion,
		RootHash: "r2",
		Files: map[string]ir.FileIR{
			"reads.py": {
				Hash: "f2",
				Symbols: []ir.Symbol{
					{Name: "_probe", Kind: "function", Hash: "hP"},
					{Name: "get_scope", Kind: "function", Hash: "hB"},
					{Name: "set_scope", Kind: "function", Hash: "hS"},
				},
			},
		},
	}
	res, err := db.DiffLive(*base, live)
	if err != nil {
		t.Fatalf("DiffLive: %v", err)
	}
	if res.TotalAdded != 1 {
		t.Errorf("TotalAdded = %d, want 1 (_probe)", res.TotalAdded)
	}
	if res.TotalModified != 1 {
		t.Errorf("TotalModified = %d, want 1 (get_scope)", res.TotalModified)
	}
	if res.TotalRemoved != 0 {
		t.Errorf("TotalRemoved = %d, want 0", res.TotalRemoved)
	}
	out := FormatFull(res)
	if !strings.Contains(out, "+ _probe") || !strings.Contains(out, "~ get_scope") {
		t.Errorf("FormatFull missing expected deltas:\n%s", out)
	}
}

// TestDiffLive_NoFalseModified guarantees the cross-version safety rule: when one
// side carries no body hash (e.g. a pre-v3 snapshot), a body change is NOT
// reported "modified" — only a real hash-vs-hash difference qualifies.
func TestDiffLive_NoFalseModified(t *testing.T) {
	db, _ := openTemp(t)
	id, _ := db.EnrollRepo("r", "/repos/r", "", 0)

	// Baseline written without symbol hashes (simulates an older snapshot).
	baseIR := &ir.IR{
		Version:  ir.IRVersion,
		RootHash: "r1",
		Files:    map[string]ir.FileIR{"reads.py": {Hash: "f1", Symbols: []ir.Symbol{{Name: "get_scope", Kind: "function"}}}},
	}
	sid, _ := db.SaveSnapshot(id, "s", "base", "/repos/r", baseIR)
	base, _ := db.GetByID(sid)

	// Live now has a hash for get_scope, and the file hash changed.
	live := &ir.IR{
		Version:  ir.IRVersion,
		RootHash: "r2",
		Files: map[string]ir.FileIR{
			"reads.py": {Hash: "f2", Symbols: []ir.Symbol{{Name: "get_scope", Kind: "function", Hash: "hB"}}},
		},
	}
	res, err := db.DiffLive(*base, live)
	if err != nil {
		t.Fatalf("DiffLive: %v", err)
	}
	if res.TotalModified != 0 {
		t.Errorf("TotalModified = %d, want 0 (one side has no hash)", res.TotalModified)
	}
}

// TestDiffLive_NoChange asserts an identical live IR reports zero drift and
// FormatFull renders the "No structural changes" branch.
func TestDiffLive_NoChange(t *testing.T) {
	db, _ := openTemp(t)
	id, _ := db.EnrollRepo("r", "/repos/r", "", 0)
	sid, err := db.SaveSnapshot(id, "s", "base", "/repos/r", makeIR("h1", "Foo"))
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	base, _ := db.GetByID(sid)

	live := &ir.IR{
		Version:  1,
		RootHash: "h1",
		Files:    map[string]ir.FileIR{"main.go": {Hash: "h1", Symbols: []ir.Symbol{{Name: "Foo", Kind: "function"}}}},
	}
	res, err := db.DiffLive(*base, live)
	if err != nil {
		t.Fatalf("DiffLive: %v", err)
	}
	if len(res.Files) != 0 || res.TotalAdded != 0 || res.TotalRemoved != 0 {
		t.Errorf("expected zero drift, got %+v", res)
	}
	if !strings.Contains(FormatFull(res), "No structural changes") {
		t.Errorf("FormatFull should report no changes:\n%s", FormatFull(res))
	}
}
