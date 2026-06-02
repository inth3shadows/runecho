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
			"main.go": {Hash: "h2", Functions: []string{"Bar"}},
			"new.go":  {Hash: "h3", Functions: []string{"Baz"}},
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
		Files:    map[string]ir.FileIR{"main.go": {Hash: "h1", Functions: []string{"Foo"}}},
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
