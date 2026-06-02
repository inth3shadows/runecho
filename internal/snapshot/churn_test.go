package snapshot

import (
	"strings"
	"testing"
	"time"

	"github.com/inth3shadows/runecho/internal/ir"
)

// twoFileIR builds an IR with two files, each with an explicit hash.
// hashA and hashB are independent — use the same value across snapshots to
// indicate a file did not change between those snapshots.
func twoFileIR(rootHash, hashA, hashB string, fnsA, fnsB []string) *ir.IR {
	return &ir.IR{
		Version:  1,
		RootHash: rootHash,
		Files: map[string]ir.FileIR{
			"a.go": {Hash: hashA, Functions: fnsA},
			"b.go": {Hash: hashB, Functions: fnsB},
		},
	}
}

func TestChurn_InsufficientSnapshots(t *testing.T) {
	db, _ := openTemp(t)
	repoID, _ := db.EnrollRepo("r", "/r", "", 0)

	// 0 snapshots
	r, err := db.Churn(repoID, 10)
	if err != nil {
		t.Fatalf("churn: %v", err)
	}
	if r.SnapshotCount != 0 || r.DiffCount != 0 {
		t.Errorf("zero-snapshot churn: %+v", r)
	}

	// 1 snapshot
	db.SaveSnapshot(repoID, "", "s1", "/r", makeIR("h1", "Foo"))
	r, err = db.Churn(repoID, 10)
	if err != nil {
		t.Fatalf("churn with 1 snap: %v", err)
	}
	if r.SnapshotCount != 1 || r.DiffCount != 0 {
		t.Errorf("single-snapshot churn: %+v", r)
	}
}

func TestChurn_BasicFileAndSymbolCounting(t *testing.T) {
	db, _ := openTemp(t)
	repoID, _ := db.EnrollRepo("r", "/r", "", 0)

	// Snapshot 1: a.go={"Alpha"} b.go={"Beta"}
	//   a.go hash "a1", b.go hash "b1"
	db.SaveSnapshot(repoID, "", "s1", "/r",
		twoFileIR("root1", "a1", "b1", []string{"Alpha"}, []string{"Beta"}))
	// Snapshot 2: a.go gains Gamma (new hash "a2"); b.go unchanged (hash stays "b1")
	db.SaveSnapshot(repoID, "", "s2", "/r",
		twoFileIR("root2", "a2", "b1", []string{"Alpha", "Gamma"}, []string{"Beta"}))
	// Snapshot 3: b.go gains Delta (new hash "b2"); a.go unchanged (hash stays "a2")
	db.SaveSnapshot(repoID, "", "s3", "/r",
		twoFileIR("root3", "a2", "b2", []string{"Alpha", "Gamma"}, []string{"Beta", "Delta"}))

	r, err := db.Churn(repoID, 10)
	if err != nil {
		t.Fatalf("churn: %v", err)
	}
	if r.SnapshotCount != 3 || r.DiffCount != 2 {
		t.Errorf("counts: snaps=%d diffs=%d", r.SnapshotCount, r.DiffCount)
	}
	// a.go changed in diff 1→2 only; b.go changed in diff 2→3 only
	fileChanges := map[string]int{}
	for _, f := range r.Files {
		fileChanges[f.Path] = f.Changes
	}
	if fileChanges["a.go"] != 1 {
		t.Errorf("a.go changes = %d, want 1", fileChanges["a.go"])
	}
	if fileChanges["b.go"] != 1 {
		t.Errorf("b.go changes = %d, want 1", fileChanges["b.go"])
	}
	symbolChanges := map[string]int{}
	for _, s := range r.Symbols {
		symbolChanges[s.Name] = s.Changes
	}
	if symbolChanges["Gamma"] != 1 {
		t.Errorf("Gamma changes = %d, want 1", symbolChanges["Gamma"])
	}
	if symbolChanges["Delta"] != 1 {
		t.Errorf("Delta changes = %d, want 1", symbolChanges["Delta"])
	}
}

func TestChurn_FileChangesMultipleTimes(t *testing.T) {
	db, _ := openTemp(t)
	repoID, _ := db.EnrollRepo("r", "/r", "", 0)

	// a.go changes in every diff
	db.SaveSnapshot(repoID, "", "s1", "/r", makeIR("h1", "A"))
	db.SaveSnapshot(repoID, "", "s2", "/r", makeIR("h2", "A", "B"))
	db.SaveSnapshot(repoID, "", "s3", "/r", makeIR("h3", "A", "B", "C"))

	r, err := db.Churn(repoID, 10)
	if err != nil {
		t.Fatalf("churn: %v", err)
	}
	// main.go should have changed in both diffs
	if len(r.Files) != 1 || r.Files[0].Changes != 2 {
		t.Errorf("hot file: %+v", r.Files)
	}
}

func TestFormatChurn_InsufficientSnapshots(t *testing.T) {
	r := ChurnReport{SnapshotCount: 1}
	out := FormatChurn(r, 2)
	if !strings.Contains(out, "insufficient") {
		t.Errorf("expected insufficient notice, got: %s", out)
	}
}

func TestFormatChurn_Full(t *testing.T) {
	r := ChurnReport{
		SnapshotCount: 3,
		DiffCount:     2,
		Since:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Until:         time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
		Files: []FileChurn{
			{Path: "hot.go", Changes: 3, DiffCount: 2},
			{Path: "cold.go", Changes: 1, DiffCount: 2},
		},
		Symbols: []SymbolChurn{
			{Name: "HotFunc", Kind: "function", FilePath: "hot.go", Changes: 2, DiffCount: 2},
		},
	}
	out := FormatChurn(r, 2)
	if !strings.Contains(out, "hot.go") {
		t.Error("hot file should appear in output")
	}
	if !strings.Contains(out, "HotFunc") {
		t.Error("hot symbol should appear in output")
	}
	// cold.go has changes=1 < minChanges=2, so should not appear in hot files
	if strings.Contains(out, "cold.go") {
		t.Error("cold file should be filtered out at minChanges=2")
	}
}

func TestFormatChurnCompact(t *testing.T) {
	r := ChurnReport{
		SnapshotCount: 5,
		DiffCount:     4,
		Since:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Until:         time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
		Files: []FileChurn{
			{Path: "a.go", Changes: 3, DiffCount: 4},
			{Path: "b.go", Changes: 1, DiffCount: 4},
		},
		Symbols: []SymbolChurn{
			{Name: "Foo", Kind: "function", Changes: 2, DiffCount: 4},
		},
	}
	out := FormatChurnCompact(r)
	// Should be a single line containing counts
	if strings.Contains(out, "\n") {
		t.Errorf("compact output should be one line, got: %s", out)
	}
	if !strings.Contains(out, "CHURN") {
		t.Errorf("compact output should start with CHURN, got: %s", out)
	}
}
