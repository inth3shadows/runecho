package snapshot

import (
	"sync"
	"testing"
)

func countLabel(t *testing.T, db *DB, repoID int64, label string) int {
	t.Helper()
	metas, err := db.List(repoID, 1000)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	n := 0
	for _, m := range metas {
		if m.Label == label {
			n++
		}
	}
	return n
}

// TestRollAutoSnapshot_Semantics: a roll replaces the prior auto snapshot (never
// appends) and never touches manual snapshots.
func TestRollAutoSnapshot_Semantics(t *testing.T) {
	db, _ := openTemp(t)
	id, err := db.EnrollRepo("r", "/tmp/r", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveSnapshot(id, "sess", "reindex", "/tmp/r", makeIR("m1", "Manual")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RollAutoSnapshot(id, "", "/tmp/r", makeIR("a1", "Foo")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RollAutoSnapshot(id, "", "/tmp/r", makeIR("a2", "Foo", "Bar")); err != nil {
		t.Fatal(err)
	}
	if n := countLabel(t, db, id, "auto"); n != 1 {
		t.Errorf("auto count = %d, want 1 (roll must replace, not append)", n)
	}
	if n := countLabel(t, db, id, "reindex"); n != 1 {
		t.Errorf("manual snapshot lost: reindex count = %d, want 1", n)
	}
	syms, err := db.SymbolsForLatestSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := syms["Bar"]; !ok {
		t.Error("latest auto snapshot missing Bar from the second roll")
	}
}

// TestRollAutoSnapshot_ConcurrentInvariant is the post-review regression guard:
// the single-transaction roll must keep at most one auto snapshot even under
// cross-connection contention (the shape of two concurrent PostToolUse hooks).
// The prior two-transaction roll could interleave and leave two.
func TestRollAutoSnapshot_ConcurrentInvariant(t *testing.T) {
	db, path := openTemp(t)
	id, err := db.EnrollRepo("r", "/tmp/r", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// A second independent handle to the same file => real cross-connection
	// SQLite contention, not just goroutines sharing one pool.
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	const rounds = 25
	var wg sync.WaitGroup
	for i := 0; i < rounds; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = db.RollAutoSnapshot(id, "", "/tmp/r", makeIR("x", "Foo")) }()
		go func() { defer wg.Done(); _, _ = db2.RollAutoSnapshot(id, "", "/tmp/r", makeIR("y", "Foo")) }()
	}
	wg.Wait()

	// Errors from busy_timeout are tolerated — a dropped roll just doesn't add a
	// snapshot. The invariant is what matters: never more than one auto.
	if got := countLabel(t, db, id, "auto"); got != 1 {
		t.Errorf("auto count after concurrent rolls = %d, want exactly 1", got)
	}
}
