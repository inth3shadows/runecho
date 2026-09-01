package snapshot

import (
	"slices"
	"sort"
	"testing"
)

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

// A repo with an active contract must purge. This is #370's stuck row: on the
// store that motivated the issue exactly one enrolment had a contracts row, so
// `prune-missing --yes` reported "Purged 515 of 516" and that enrolment was
// unprunable by the documented command, forever.
func TestPurgeRepoDeletesContracts(t *testing.T) {
	db, _ := openTemp(t)
	victim, err := db.EnrollRepo("victim", "/tmp/victim", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	bystander, err := db.EnrollRepo("bystander", "/tmp/bystander", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{victim, bystander} {
		if err := db.ActivateContract(id, "sess", "c", "/tmp/c.md", "h"); err != nil {
			t.Fatalf("ActivateContract(%d): %v", id, err)
		}
	}

	if err := db.PurgeRepo(victim); err != nil {
		t.Fatalf("PurgeRepo with a contract: %v", err)
	}

	var n int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM contracts WHERE repo_id = ?`, victim).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("victim's contracts survived the purge: %d row(s)", n)
	}
	// The bystander's contract must survive: a delete that dropped its WHERE
	// clause would pass every assertion above and show up only here.
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM contracts WHERE repo_id = ?`, bystander).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("bystander's contract was collateral damage: want 1 row, got %d", n)
	}
}

// The fence that stops #370 recurring for the NEXT table.
//
// Reads the live schema rather than a hand-maintained list: for every table
// holding a foreign key to `repos`, plant a row and require PurgeRepo to remove
// it. A new child table added without a matching delete fails here at the point
// it is added, instead of years later as an unexplained "FOREIGN KEY constraint
// failed" in the middle of a bulk prune.
//
// Tables are discovered through PRAGMA foreign_key_list, so this cannot drift
// from the schema the way the comment on deleteSnapshotsTx did.
func TestPurgeRepoDeletesEveryTableThatReferencesRepos(t *testing.T) {
	db, _ := openTemp(t)

	var tables []string
	rows, err := db.conn.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// Direct children of `repos` only. The snapshot subtree reaches `repos`
	// transitively and is already covered by TestPurgeRepo_LeavesNoOrphans.
	var children []string
	for _, table := range tables {
		fks, err := db.conn.Query(`SELECT "table" FROM pragma_foreign_key_list(?)`, table)
		if err != nil {
			t.Fatalf("foreign_key_list(%s): %v", table, err)
		}
		for fks.Next() {
			var parent string
			if err := fks.Scan(&parent); err != nil {
				fks.Close()
				t.Fatal(err)
			}
			if parent == "repos" {
				children = append(children, table)
			}
		}
		fks.Close()
	}

	// EXACT match, not "every discovered table is in the list". A superset
	// check passes when the list names a table that does not exist, which makes
	// it possible to satisfy the fence by editing the list instead of the code —
	// and a mutation that added a phantom entry survived until this was tightened.
	handled := []string{"contracts", "snapshots"}
	sort.Strings(children)
	if !slices.Equal(children, handled) {
		t.Errorf("tables referencing repos = %v, PurgeRepo handles %v.\n"+
			"A table here that PurgeRepo does not delete is #370 happening again: "+
			"add a DELETE for it in PurgeRepo, then add it to `handled`.", children, handled)
	}

	// And prove the handled ones really are handled, rather than only listed.
	victim, err := db.EnrollRepo("victim", "/tmp/victim", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveSnapshot(victim, "sess", "reindex", "/tmp/victim", makeIR("h", "Alpha")); err != nil {
		t.Fatal(err)
	}
	if err := db.ActivateContract(victim, "sess", "c", "/tmp/c.md", "h"); err != nil {
		t.Fatal(err)
	}
	if err := db.PurgeRepo(victim); err != nil {
		t.Fatalf("PurgeRepo: %v", err)
	}
	for _, table := range children {
		var n int
		q := `SELECT COUNT(*) FROM "` + table + `" WHERE repo_id = ?`
		if err := db.conn.QueryRow(q, victim).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s still holds %d row(s) for the purged repo", table, n)
		}
	}
}
