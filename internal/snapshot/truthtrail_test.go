package snapshot

import (
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/ir"
)

func TestTruthTrail_NoChanges(t *testing.T) {
	db, _ := openTemp(t)
	repoID, _ := db.EnrollRepo("r", "/tmp/r", "", 0)
	irData := makeIR("h1", "Foo", "Bar")
	snapID, err := db.SaveSnapshot(repoID, "", "session-start", "/tmp/r", irData)
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	meta, _ := db.GetByID(snapID)

	trail, err := TruthTrail(db, repoID, *meta, irData, 0, "")
	if err != nil {
		t.Fatalf("TruthTrail: %v", err)
	}
	if len(trail.Diff.Files) != 0 {
		t.Errorf("expected no diff, got %d files", len(trail.Diff.Files))
	}
	if trail.StaleClaims != nil {
		t.Error("StaleClaims should be nil when no text provided")
	}
	out := FormatTrail(trail)
	if !strings.Contains(out, "No structural changes") {
		t.Errorf("expected 'No structural changes', got: %q", out)
	}
}

func TestTruthTrail_RemovedSymbolWithCallers(t *testing.T) {
	db, _ := openTemp(t)
	repoID, _ := db.EnrollRepo("r", "/tmp/r", "", 0)

	// Baseline: OldFunc defined in main.go, referenced by helper.go.
	baseIR := &ir.IR{
		Version:  ir.IRVersion,
		RootHash: "h1",
		Files: map[string]ir.FileIR{
			"main.go":   {Hash: "h1a", Functions: []string{"OldFunc"}},
			"helper.go": {Hash: "h1b", Refs: []string{"OldFunc"}},
		},
	}
	snapID, err := db.SaveSnapshot(repoID, "", "session-start", "/tmp/r", baseIR)
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	meta, _ := db.GetByID(snapID)

	// Live: OldFunc removed, NewFunc added.
	liveIR := &ir.IR{
		Version:  ir.IRVersion,
		RootHash: "h2",
		Files: map[string]ir.FileIR{
			"main.go":   {Hash: "h2a", Functions: []string{"NewFunc"}},
			"helper.go": {Hash: "h2b", Refs: []string{}},
		},
	}

	trail, err := TruthTrail(db, repoID, *meta, liveIR, 0, "")
	if err != nil {
		t.Fatalf("TruthTrail: %v", err)
	}
	if len(trail.Diff.Files) == 0 {
		t.Fatal("expected diff, got none")
	}
	callers := trail.Callers["OldFunc"]
	if len(callers) != 1 || callers[0] != "helper.go" {
		t.Errorf("callers of OldFunc = %v, want [helper.go]", callers)
	}
	out := FormatTrail(trail)
	if !strings.Contains(out, "OldFunc") {
		t.Errorf("format missing OldFunc: %q", out)
	}
	if !strings.Contains(out, "helper.go") {
		t.Errorf("format missing caller helper.go: %q", out)
	}
}

func TestTruthTrail_StaleClaims(t *testing.T) {
	db, _ := openTemp(t)
	repoID, _ := db.EnrollRepo("r", "/tmp/r", "", 0)
	irData := makeIR("h1", "RealFunc")
	snapID, _ := db.SaveSnapshot(repoID, "", "session-start", "/tmp/r", irData)
	meta, _ := db.GetByID(snapID)

	text := "The `PhantomFunc` handles routing and `RealFunc` does the work."
	trail, err := TruthTrail(db, repoID, *meta, irData, 0, text)
	if err != nil {
		t.Fatalf("TruthTrail: %v", err)
	}
	if len(trail.StaleClaims) != 1 {
		t.Fatalf("expected 1 stale claim, got %d: %v", len(trail.StaleClaims), trail.StaleClaims)
	}
	if trail.StaleClaims[0].Symbol != "PhantomFunc" {
		t.Errorf("stale claim symbol = %q, want PhantomFunc", trail.StaleClaims[0].Symbol)
	}
	out := FormatTrail(trail)
	if !strings.Contains(out, "STALE CLAIMS") {
		t.Errorf("format missing STALE CLAIMS: %q", out)
	}
	if !strings.Contains(out, "PhantomFunc") {
		t.Errorf("format missing PhantomFunc: %q", out)
	}
}

func TestTruthTrail_NoCallers(t *testing.T) {
	db, _ := openTemp(t)
	repoID, _ := db.EnrollRepo("r", "/tmp/r", "", 0)
	snapID, _ := db.SaveSnapshot(repoID, "", "session-start", "/tmp/r", makeIR("h1", "FuncA"))
	meta, _ := db.GetByID(snapID)

	trail, err := TruthTrail(db, repoID, *meta, makeIR("h2", "FuncB"), 0, "")
	if err != nil {
		t.Fatalf("TruthTrail: %v", err)
	}
	if callers := trail.Callers["FuncA"]; len(callers) != 0 {
		t.Errorf("expected no callers, got %v", callers)
	}
	if trail.StaleClaims != nil {
		t.Error("StaleClaims should be nil when no text provided")
	}
	out := FormatTrail(trail)
	if strings.Contains(out, "callers:") {
		t.Errorf("should not show callers annotation when none: %q", out)
	}
}

func TestFormatTrail_HotLabel(t *testing.T) {
	cases := []struct {
		changes, total int
		want           string
	}{
		{0, 0, ""},
		{0, 5, "  [stable]"},
		{1, 5, "  [stable]"},
		{2, 5, "  [HOT 2/5]"},
		{4, 10, "  [HOT 4/10]"},
	}
	for _, tc := range cases {
		got := hotLabel(tc.changes, tc.total)
		if got != tc.want {
			t.Errorf("hotLabel(%d,%d) = %q, want %q", tc.changes, tc.total, got, tc.want)
		}
	}
}

func TestFormatTrail_ManyCallers(t *testing.T) {
	r := TrailResult{
		SnapshotRef: SnapshotMeta{Label: "session-start"},
		Diff: DiffResult{
			Files: []FileDiff{{
				Path:    "main.go",
				Status:  "modified",
				Removed: []SymbolDelta{{Name: "BigFunc", Kind: "function"}},
			}},
			TotalRemoved: 1,
		},
		Callers: map[string][]string{
			"BigFunc": {"a.go", "b.go", "c.go", "d.go", "e.go"},
		},
		FileHot: map[string]int{},
	}
	out := FormatTrail(r)
	if !strings.Contains(out, "(+2 more)") {
		t.Errorf("expected truncation annotation, got: %q", out)
	}
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "c.go") {
		t.Errorf("expected first 3 callers visible: %q", out)
	}
}
