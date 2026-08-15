package snapshot

import (
	"testing"
	"time"
)

// These tests pin SaveReindexSnapshot's two halves: it must not re-write an
// unchanged tree, and it must still advance the snapshot's own timestamp when
// it declines to write. The second half is the one that is easy to get wrong —
// see the "touch, don't skip" reasoning on SaveReindexSnapshot itself.

// childRowCounts returns how many files/symbols/refs rows hang off snapshotID.
// A "touch" that quietly re-inserted children would defeat the entire fix while
// leaving the snapshot count looking correct.
func childRowCounts(t *testing.T, db *DB, snapshotID int64) (files, symbols, refs int) {
	t.Helper()
	q := func(sql string) int {
		var n int
		if err := db.conn.QueryRow(sql, snapshotID).Scan(&n); err != nil {
			t.Fatalf("count (%s): %v", sql, err)
		}
		return n
	}
	return q(`SELECT COUNT(*) FROM files WHERE snapshot_id = ?`),
		q(`SELECT COUNT(*) FROM symbols WHERE file_id IN (SELECT id FROM files WHERE snapshot_id = ?)`),
		q(`SELECT COUNT(*) FROM refs WHERE file_id IN (SELECT id FROM files WHERE snapshot_id = ?)`)
}

func snapshotTimestamp(t *testing.T, db *DB, snapshotID int64) string {
	t.Helper()
	var ts string
	if err := db.conn.QueryRow(`SELECT timestamp FROM snapshots WHERE id = ?`, snapshotID).Scan(&ts); err != nil {
		t.Fatalf("read timestamp of snapshot %d: %v", snapshotID, err)
	}
	return ts
}

func enrollForDedup(t *testing.T, db *DB, name string) int64 {
	t.Helper()
	id, err := db.EnrollRepo(name, "/tmp/"+name, "", 0)
	if err != nil {
		t.Fatalf("EnrollRepo(%s): %v", name, err)
	}
	return id
}

// TestSaveReindexSnapshot_FirstCallAlwaysWrites: with no prior reindex snapshot
// there is nothing to compare against, so sql.ErrNoRows must mean "write", not
// "unchanged".
func TestSaveReindexSnapshot_FirstCallAlwaysWrites(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForDedup(t, db, "first")

	id, wrote, err := db.SaveReindexSnapshot(repo, "", "/tmp/first", makeIR("hash-a", "Alpha"))
	if err != nil {
		t.Fatalf("SaveReindexSnapshot: %v", err)
	}
	if !wrote {
		t.Error("wrote = false on the very first reindex snapshot; ErrNoRows was treated as 'unchanged'")
	}
	if id == 0 {
		t.Error("returned snapshot id 0")
	}
	if n := countLabel(t, db, repo, ReindexLabel); n != 1 {
		t.Errorf("reindex snapshots = %d, want 1", n)
	}
}

// TestSaveReindexSnapshot_TouchesOnUnchangedHash is the core dedup assertion:
// an identical root hash must reuse the existing row rather than insert.
func TestSaveReindexSnapshot_TouchesOnUnchangedHash(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForDedup(t, db, "unchanged")
	ir := makeIR("hash-a", "Alpha")

	firstID, wrote, err := db.SaveReindexSnapshot(repo, "", "/tmp/unchanged", ir)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	if !wrote {
		t.Fatal("first save did not write")
	}

	secondID, wrote, err := db.SaveReindexSnapshot(repo, "", "/tmp/unchanged", ir)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if wrote {
		t.Error("wrote = true for an unchanged root hash; the duplicate write was not skipped")
	}
	if secondID != firstID {
		t.Errorf("second save returned id %d, want the existing %d", secondID, firstID)
	}
	if n := countLabel(t, db, repo, ReindexLabel); n != 1 {
		t.Errorf("reindex snapshots = %d after two identical saves, want 1", n)
	}
}

// TestSaveReindexSnapshot_TouchAdvancesTimestamp pins the "touch, don't skip"
// half. The guard's staleness check reads the newest snapshot's OWN timestamp
// (cmd/runecho-guard/main.go: time.Since(snaps[0].Timestamp) > maxAge), NOT
// repos.last_indexed — so a bare skip would freeze it and start emitting false
// "IR is stale" advisories on any repo that stopped changing. Deleting the
// UPDATE in SaveReindexSnapshot leaves every other test in this file green and
// fails only this one.
func TestSaveReindexSnapshot_TouchAdvancesTimestamp(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForDedup(t, db, "touched")
	ir := makeIR("hash-a", "Alpha")

	id, _, err := db.SaveReindexSnapshot(repo, "", "/tmp/touched", ir)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	before := snapshotTimestamp(t, db, id)

	// Timestamps are RFC3339 (second granularity), so back-date the row rather
	// than sleeping a second — the assertion is "the touch moved it", and a
	// same-second re-save would be indistinguishable from a no-op.
	stale := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	if _, err := db.conn.Exec(`UPDATE snapshots SET timestamp = ? WHERE id = ?`, stale, id); err != nil {
		t.Fatalf("back-date snapshot: %v", err)
	}

	if _, wrote, err := db.SaveReindexSnapshot(repo, "", "/tmp/touched", ir); err != nil {
		t.Fatalf("second save: %v", err)
	} else if wrote {
		t.Fatal("second save wrote a new row; this test is about the touch path")
	}

	after := snapshotTimestamp(t, db, id)
	if after == stale {
		t.Error("timestamp still back-dated after an unchanged reindex — the touch was skipped, so the guard will report this repo stale past RUNECHO_GUARD_MAX_AGE")
	}
	if after < before {
		t.Errorf("timestamp went backwards: %s -> %s", before, after)
	}
}

// TestSaveReindexSnapshot_TouchDoesNotDuplicateChildren: the whole point is not
// re-writing the files/symbols/refs tree. A touch that still inserted children
// would keep the snapshot count at 1 and grow the store anyway.
func TestSaveReindexSnapshot_TouchDoesNotDuplicateChildren(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForDedup(t, db, "children")
	ir := makeIR("hash-a", "Alpha", "Beta")

	id, _, err := db.SaveReindexSnapshot(repo, "", "/tmp/children", ir)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	wantF, wantS, wantR := childRowCounts(t, db, id)
	if wantF == 0 || wantS == 0 {
		t.Fatalf("fixture produced no child rows (files=%d symbols=%d); the test would pass vacuously", wantF, wantS)
	}

	if _, _, err := db.SaveReindexSnapshot(repo, "", "/tmp/children", ir); err != nil {
		t.Fatalf("second save: %v", err)
	}

	gotF, gotS, gotR := childRowCounts(t, db, id)
	if gotF != wantF || gotS != wantS || gotR != wantR {
		t.Errorf("child rows after an unchanged reindex = files %d symbols %d refs %d, want %d/%d/%d unchanged",
			gotF, gotS, gotR, wantF, wantS, wantR)
	}
}

// TestSaveReindexSnapshot_WritesOnChangedHash is the dangerous direction: an
// inverted comparison would silently drop real changes, and the guard would
// then judge new edits against a stale symbol set. The prior row must also
// survive — dedup appends, it does not replace.
func TestSaveReindexSnapshot_WritesOnChangedHash(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForDedup(t, db, "changed")

	firstID, _, err := db.SaveReindexSnapshot(repo, "", "/tmp/changed", makeIR("hash-a", "Alpha"))
	if err != nil {
		t.Fatalf("first save: %v", err)
	}

	secondID, wrote, err := db.SaveReindexSnapshot(repo, "", "/tmp/changed", makeIR("hash-b", "Alpha", "Beta"))
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if !wrote {
		t.Error("wrote = false for a CHANGED root hash — a real content change was dropped")
	}
	if secondID == firstID {
		t.Errorf("changed content reused snapshot id %d instead of inserting", firstID)
	}
	if n := countLabel(t, db, repo, ReindexLabel); n != 2 {
		t.Errorf("reindex snapshots = %d, want 2 (dedup must append, not replace)", n)
	}
	// The older snapshot must still be intact — `diff --since=` can pin it.
	if f, _, _ := childRowCounts(t, db, firstID); f == 0 {
		t.Error("the prior snapshot lost its child rows; dedup replaced instead of appended")
	}
}

// TestSaveReindexSnapshot_ScopedPerRepo: the hash comparison must be keyed on
// repo_id. Two repos that legitimately share a root hash (an empty tree, or a
// worktree of the same commit) must not silently share one snapshot row.
func TestSaveReindexSnapshot_ScopedPerRepo(t *testing.T) {
	db, _ := openTemp(t)
	a := enrollForDedup(t, db, "repo-a")
	b := enrollForDedup(t, db, "repo-b")
	ir := makeIR("same-hash", "Shared")

	idA, _, err := db.SaveReindexSnapshot(a, "", "/tmp/repo-a", ir)
	if err != nil {
		t.Fatalf("save a: %v", err)
	}
	idB, wrote, err := db.SaveReindexSnapshot(b, "", "/tmp/repo-b", ir)
	if err != nil {
		t.Fatalf("save b: %v", err)
	}
	if !wrote {
		t.Error("repo B's first snapshot was deduped against repo A's — the query is missing its repo_id scope")
	}
	if idA == idB {
		t.Errorf("both repos resolved to snapshot id %d", idA)
	}
}
