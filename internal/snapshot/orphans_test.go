package snapshot

import "testing"

// countOrphans returns how many child rows survive with no live parent. The
// schema deliberately carries no ON DELETE CASCADE (issue #13 — see
// deleteSnapshotsTx for why), so this invariant is enforced by code rather than
// by a constraint. These tests are the fence that replaces the constraint: if a
// delete path ever stops going through deleteSnapshotsTx, they fail.
func countOrphans(t *testing.T, db *DB) (refs, symbols, files int) {
	t.Helper()
	q := func(sql string) int {
		var n int
		if err := db.conn.QueryRow(sql).Scan(&n); err != nil {
			t.Fatalf("count orphans (%s): %v", sql, err)
		}
		return n
	}
	return q(`SELECT COUNT(*) FROM refs  WHERE file_id     NOT IN (SELECT id FROM files)`),
		q(`SELECT COUNT(*) FROM symbols WHERE file_id     NOT IN (SELECT id FROM files)`),
		q(`SELECT COUNT(*) FROM files   WHERE snapshot_id NOT IN (SELECT id FROM snapshots)`)
}

func assertNoOrphans(t *testing.T, db *DB, after string) {
	t.Helper()
	r, s, f := countOrphans(t, db)
	if r != 0 || s != 0 || f != 0 {
		t.Errorf("after %s: orphaned rows remain — refs=%d symbols=%d files=%d", after, r, s, f)
	}
}

// PurgeRepo must leave nothing behind, and must not touch a sibling repo.
func TestPurgeRepo_LeavesNoOrphans(t *testing.T) {
	db, _ := openTemp(t)
	victim, err := db.EnrollRepo("victim", "/tmp/victim", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	bystander, err := db.EnrollRepo("bystander", "/tmp/bystander", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"reindex", "session-start"} {
		if _, err := db.SaveSnapshot(victim, "sess", label, "/tmp/victim", makeIR("h-"+label, "Alpha", "Beta")); err != nil {
			t.Fatalf("SaveSnapshot(%s): %v", label, err)
		}
	}
	if _, err := db.SaveSnapshot(bystander, "sess", "reindex", "/tmp/bystander", makeIR("hb", "Gamma")); err != nil {
		t.Fatal(err)
	}

	if err := db.PurgeRepo(victim); err != nil {
		t.Fatalf("PurgeRepo: %v", err)
	}
	assertNoOrphans(t, db, "PurgeRepo")

	// The bystander's rows must survive: a shared deletion helper that widened
	// its predicate would show up here and nowhere else.
	metas, err := db.List(bystander, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 {
		t.Errorf("bystander repo lost snapshots: got %d, want 1", len(metas))
	}
	var n int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM symbols`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("purging one repo deleted every symbol row in the store")
	}
}

// The auto-snapshot roll is the second delete path; it must be equally clean and
// must never touch manual snapshots.
func TestDeleteAutoSnapshots_LeavesNoOrphans(t *testing.T) {
	db, _ := openTemp(t)
	id, err := db.EnrollRepo("r", "/tmp/r", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveSnapshot(id, "sess", "reindex", "/tmp/r", makeIR("m1", "Manual")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RollAutoSnapshot(id, "sess", "/tmp/r", makeIR("a1", "AutoOne")); err != nil {
		t.Fatalf("RollAutoSnapshot: %v", err)
	}
	if _, err := db.RollAutoSnapshot(id, "sess", "/tmp/r", makeIR("a2", "AutoTwo")); err != nil {
		t.Fatalf("RollAutoSnapshot: %v", err)
	}
	assertNoOrphans(t, db, "RollAutoSnapshot (replacing a prior auto)")

	if err := db.DeleteAutoSnapshots(id); err != nil {
		t.Fatalf("DeleteAutoSnapshots: %v", err)
	}
	assertNoOrphans(t, db, "DeleteAutoSnapshots")

	if got := countLabel(t, db, id, "reindex"); got != 1 {
		t.Errorf("manual snapshot count = %d, want 1 (auto deletion must not touch it)", got)
	}
	if got := countLabel(t, db, id, autoSnapshotLabel); got != 0 {
		t.Errorf("auto snapshot count = %d, want 0", got)
	}
}

// The stronger property, found while writing this test: with `PRAGMA
// foreign_keys=ON` (set at Open) and no ON DELETE action, SQLite does not
// silently orphan rows — it REFUSES the delete. A path that forgets to remove
// children fails loudly and rolls back rather than corrupting the store.
//
// That is why the missing CASCADE (issue #13) is not an integrity gap: NO ACTION
// already guarantees integrity, and CASCADE would only be a convenience that
// deletes children instead of erroring. Pinning it here because the reasoning on
// #13 depends on it — if the pragma is ever turned off, this test fails and the
// decision must be revisited.
func TestForgettingChildrenFailsLoudlyRatherThanOrphaning(t *testing.T) {
	db, _ := openTemp(t)
	id, err := db.EnrollRepo("r", "/tmp/r", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := db.SaveSnapshot(id, "sess", "reindex", "/tmp/r", makeIR("h1", "Ghost"))
	if err != nil {
		t.Fatal(err)
	}
	// Exactly what a forgetful delete path would do: remove the parent rows
	// without first removing their children.
	if _, err := db.conn.Exec(`DELETE FROM files WHERE snapshot_id = ?`, sid); err == nil {
		t.Fatal("deleting files out from under their symbols succeeded; " +
			"foreign_keys enforcement is off and #13's reasoning no longer holds")
	}
	// The store is untouched, not half-deleted.
	assertNoOrphans(t, db, "a rejected out-of-order delete")
	paths, err := db.DefsOfName(sid, "Ghost")
	if err != nil {
		t.Fatalf("DefsOfName: %v", err)
	}
	if len(paths) != 1 {
		t.Errorf("rejected delete damaged the snapshot: DefsOfName = %q, want 1 path", paths)
	}
}

// The "the DB already refuses" argument on #13 is only as broad as the FKs that
// actually exist, so pin the top of the chain too: snapshots.repo_id REFERENCES
// repos(id) (migrateV2). RemoveRepo's own count check gives a friendlier error,
// but the schema must refuse independently — otherwise deleting a repo could
// strand its whole snapshot history and the decision on #13 would not hold for
// that level.
//
// Chain covered: repos -> snapshots -> files -> symbols/refs.
func TestRepoDeleteIsRefusedWhileSnapshotsExist(t *testing.T) {
	db, _ := openTemp(t)
	id, err := db.EnrollRepo("r", "/tmp/r", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveSnapshot(id, "sess", "reindex", "/tmp/r", makeIR("h1", "Alpha")); err != nil {
		t.Fatal(err)
	}
	// Bypass RemoveRepo's count guard and go straight at the schema.
	if _, err := db.conn.Exec(`DELETE FROM repos WHERE id = ?`, id); err == nil {
		t.Fatal("deleting a repo with live snapshots succeeded; the repos->snapshots " +
			"foreign key is not enforced and #13's reasoning does not cover this level")
	}
	assertNoOrphans(t, db, "a rejected repo delete")
	if got := countLabel(t, db, id, "reindex"); got != 1 {
		t.Errorf("rejected repo delete damaged history: %d snapshots, want 1", got)
	}
}
