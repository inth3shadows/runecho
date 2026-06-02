package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/snapshot"
)

// newOracleRepo creates a temp central store + a temp repo dir with one Go file,
// enrolls it, and returns the oracle plus the repo name.
func newOracleRepo(t *testing.T) (*Oracle, string, *snapshot.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "history.db")
	db, err := snapshot.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repoDir := t.TempDir()
	src := "package demo\n\nfunc Alpha() {}\nfunc Beta() {}\n"
	if err := os.WriteFile(filepath.Join(repoDir, "demo.go"), []byte(src), 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if _, err := db.EnrollRepo("demo", repoDir, "", 0); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	return NewOracle(db, dbPath), "demo", db
}

// TestOracleDiff_CappedRepoNoPhantomDrift is the regression guard for the P4-B
// cap-consistency bug: a repo enrolled with FileCap>0 stores a capped snapshot,
// and the oracle's live IR must be generated under the same cap. If liveIR were
// uncapped, latest-vs-live diff would report every file beyond the cap as a
// phantom addition on an unchanged repo.
func TestOracleDiff_CappedRepoNoPhantomDrift(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	db, err := snapshot.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Repo with 5 source files, enrolled with a cap of 2.
	repoDir := t.TempDir()
	for _, n := range []string{"a.go", "b.go", "c.go", "d.go", "e.go"} {
		if err := os.WriteFile(filepath.Join(repoDir, n), []byte("package demo\n\nfunc F"+n[:1]+"() {}\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	repoID, err := db.EnrollRepo("capped", repoDir, "", 2)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}

	// Store a capped snapshot (mirrors `repo reindex` honoring FileCap=2).
	capped, err := liveIR(repoDir, 2)
	if err != nil {
		t.Fatalf("liveIR capped: %v", err)
	}
	if len(capped.Files) != 2 {
		t.Fatalf("capped snapshot has %d files, want 2", len(capped.Files))
	}
	if _, err := db.SaveSnapshot(repoID, "", "reindex", repoDir, capped); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	// Default latest-vs-live diff on the UNCHANGED repo must report zero drift.
	o := NewOracle(db, dbPath)
	out := call(t, o.diff, `{"repo":"capped"}`)
	if out["total_added"].(float64) != 0 || out["total_removed"].(float64) != 0 {
		t.Errorf("capped repo phantom drift: added=%v removed=%v (want 0/0) — liveIR not honoring FileCap",
			out["total_added"], out["total_removed"])
	}
}

func call(t *testing.T, fn func(json.RawMessage) (string, error), args string) map[string]any {
	t.Helper()
	out, err := fn([]byte(args))
	if err != nil {
		t.Fatalf("tool error: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("decode tool output: %v\n%s", err, out)
	}
	return m
}

func TestOracleHashAndStructure(t *testing.T) {
	o, name, _ := newOracleRepo(t)
	args := `{"repo":"` + name + `"}`

	h := call(t, o.hash, args)
	if h["file_count"].(float64) != 1 {
		t.Errorf("hash file_count = %v, want 1", h["file_count"])
	}
	hash1 := h["root_hash"].(string)
	if hash1 == "" {
		t.Fatal("empty root_hash")
	}
	// Determinism: same code → identical hash on a second call.
	if h2 := call(t, o.hash, args); h2["root_hash"] != hash1 {
		t.Errorf("non-deterministic hash: %v vs %v", h2["root_hash"], hash1)
	}

	s := call(t, o.structure, args)
	if s["symbol_count"].(float64) != 2 { // Alpha, Beta
		t.Errorf("structure symbol_count = %v, want 2", s["symbol_count"])
	}
}

func TestOracleStatusAndHealth(t *testing.T) {
	o, name, _ := newOracleRepo(t)

	st := call(t, o.status, `{"repo":"`+name+`"}`)
	if st["snapshot_count"].(float64) != 0 {
		t.Errorf("fresh repo snapshot_count = %v, want 0", st["snapshot_count"])
	}
	if st["last_indexed"] != nil {
		t.Errorf("fresh repo last_indexed = %v, want nil", st["last_indexed"])
	}

	h := call(t, o.health, `{}`)
	if h["integrity"] != "ok" {
		t.Errorf("integrity = %v, want ok", h["integrity"])
	}
	if h["repo_count"].(float64) != 1 {
		t.Errorf("repo_count = %v, want 1", h["repo_count"])
	}
}

func TestOracleDiffDetectsDrift(t *testing.T) {
	o, name, db := newOracleRepo(t)
	repo, _ := db.GetRepoByName(name)

	// Snapshot the current state (Alpha, Beta), then add Gamma to the live code.
	live, err := liveIR(repo.Path, 0)
	if err != nil {
		t.Fatalf("liveIR: %v", err)
	}
	if _, err := db.SaveSnapshot(repo.ID, "", "base", repo.Path, live); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	add := "package demo\n\nfunc Alpha() {}\nfunc Beta() {}\nfunc Gamma() {}\n"
	if err := os.WriteFile(filepath.Join(repo.Path, "demo.go"), []byte(add), 0644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	// Default diff = latest snapshot vs live → should see +1 (Gamma).
	d := call(t, o.diff, `{"repo":"`+name+`"}`)
	if d["total_added"].(float64) != 1 {
		t.Errorf("diff total_added = %v, want 1 (Gamma)", d["total_added"])
	}
}

func TestOracleUnenrolledRepoErrors(t *testing.T) {
	o, _, _ := newOracleRepo(t)
	if _, err := o.hash([]byte(`{"repo":"ghost"}`)); err == nil {
		t.Fatal("expected error for unenrolled repo")
	}
}

// F3: a half-specified pair (only `a`, no `b`) must error, not silently fall
// through to latest-vs-live and answer a different question.
func TestOracleDiffPartialPairErrors(t *testing.T) {
	o, name, _ := newOracleRepo(t)
	if _, err := o.diff([]byte(`{"repo":"` + name + `","a":1}`)); err == nil {
		t.Error("diff with only `a` should error, got success")
	}
	if _, err := o.diff([]byte(`{"repo":"` + name + `","b":2}`)); err == nil {
		t.Error("diff with only `b` should error, got success")
	}
}

// TestOracleDiffCrossRepoBlocked verifies that snapshot IDs from a different
// repo are rejected — diffs must never cross repo boundaries.
func TestOracleDiffCrossRepoBlocked(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "history.db")
	db, err := snapshot.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	dir1, dir2 := t.TempDir(), t.TempDir()
	srcA := []byte("package demo\nfunc FuncA() {}\n")
	srcB := []byte("package demo\nfunc FuncB() {}\n")
	os.WriteFile(filepath.Join(dir1, "a.go"), srcA, 0644)
	os.WriteFile(filepath.Join(dir2, "b.go"), srcB, 0644)

	repoID1, _ := db.EnrollRepo("repoA", dir1, "", 0)
	repoID2, _ := db.EnrollRepo("repoB", dir2, "", 0)

	live1, _ := liveIR(dir1, 0)
	live2, _ := liveIR(dir2, 0)
	snapID1, _ := db.SaveSnapshot(repoID1, "", "s", dir1, live1)
	snapID2, _ := db.SaveSnapshot(repoID2, "", "s", dir2, live2)

	o := NewOracle(db, dbPath)

	args := []byte(fmt.Sprintf(`{"repo":"repoA","a":%d,"b":%d}`, snapID1, snapID2))
	if _, err := o.diff(args); err == nil {
		t.Error("cross-repo diff should be rejected, got success")
	}
}

// TestOracleDiffSinceMode verifies the `since` label mode: diff latest snapshot
// with that label against live code.
func TestOracleDiffSinceMode(t *testing.T) {
	o, name, db := newOracleRepo(t)
	repo, _ := db.GetRepoByName(name)

	live, _ := liveIR(repo.Path, 0)
	db.SaveSnapshot(repo.ID, "", "session-start", repo.Path, live)

	// Add a third function to the live code.
	extra := []byte("package demo\n\nfunc Alpha() {}\nfunc Beta() {}\nfunc Gamma() {}\n")
	os.WriteFile(filepath.Join(repo.Path, "demo.go"), extra, 0644)

	d := call(t, o.diff, `{"repo":"`+name+`","since":"session-start"}`)
	if d["total_added"].(float64) != 1 {
		t.Errorf("since-mode diff total_added = %v, want 1 (Gamma)", d["total_added"])
	}
}

// TestOracleDiffSinceMissingLabel errors cleanly when the label does not exist.
func TestOracleDiffSinceMissingLabel(t *testing.T) {
	o, name, _ := newOracleRepo(t)
	if _, err := o.diff([]byte(`{"repo":"` + name + `","since":"no-such-label"}`)); err == nil {
		t.Error("unknown since label should return error")
	}
}
