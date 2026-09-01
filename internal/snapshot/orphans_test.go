package snapshot

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
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

// PurgeRepo must succeed on a repo that has an active contract, and must leave
// no contracts row behind. Before #370's fix, ActivateContract's row (V9,
// contracts.repo_id REFERENCES repos(id), no ON DELETE) blocked the
// `DELETE FROM repos` inside PurgeRepo's own transaction: the whole purge
// rolled back with a foreign-key error, and the enrollment became permanently
// unpurgeable — reproducing issue #370's stuck row (enrollment id=430; the
// `(787)` in that issue's error output is SQLITE_CONSTRAINT_FOREIGNKEY, the
// extended error code, not a row id), which survived
// `repo prune-missing --yes` and reappeared on every subsequent run.
func TestPurgeRepo_WithActiveContract(t *testing.T) {
	db, _ := openTemp(t)
	victim, err := db.EnrollRepo("victim", "/tmp/victim", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ActivateContract(victim, "sess-1", "my-contract", "/tmp/victim/CONTRACT.md", "hash123"); err != nil {
		t.Fatalf("ActivateContract: %v", err)
	}

	if err := db.PurgeRepo(victim); err != nil {
		t.Fatalf("PurgeRepo with an active contract: %v", err)
	}

	var repoCount, contractCount int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM repos WHERE id = ?`, victim).Scan(&repoCount); err != nil {
		t.Fatal(err)
	}
	if repoCount != 0 {
		t.Errorf("repos row for purged repo %d still present", victim)
	}
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM contracts WHERE repo_id = ?`, victim).Scan(&contractCount); err != nil {
		t.Fatal(err)
	}
	if contractCount != 0 {
		t.Errorf("contracts row for purged repo %d still present: %d", victim, contractCount)
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

// A sibling repo's contract must survive the purge. TestPurgeRepo_WithActiveContract
// proves the victim's row goes; only this proves the DELETE kept its WHERE clause.
// A `DELETE FROM contracts` with no predicate passes every assertion there.
func TestPurgeRepo_LeavesASiblingsContractAlone(t *testing.T) {
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
		t.Fatalf("PurgeRepo: %v", err)
	}
	var n int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM contracts WHERE repo_id = ?`, bystander).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the bystander's contract was collateral damage: want 1 row, got %d", n)
	}
}

// repoChild is one foreign key pointing at `repos`: which table holds it and
// which column, both read from the schema rather than assumed.
type repoChild struct{ table, column string }

// scanRepoChildren reads a live schema and returns every direct child of
// `repos`, plus a complaint for every foreign key ANYWHERE that declares an
// ON DELETE action.
//
// Extracted from the test that uses it for one reason: both of its rules only
// fire on a schema that does not exist yet (today nothing cascades and every
// child column is named `repo_id`), so mutating them against the real schema
// survives — the assertions cannot fire. Against a synthetic schema they can.
// See TestScanRepoChildrenDetectsWhatTheFenceIsFor, which is what makes the
// fence a check rather than a comment.
func scanRepoChildren(conn interface {
	Query(string, ...any) (*sql.Rows, error)
}) (children []repoChild, cascades []string, err error) {
	rows, err := conn.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, nil, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, nil, err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	for _, table := range tables {
		fks, err := conn.Query(`SELECT "table", "from", "on_delete" FROM pragma_foreign_key_list(?)`, table)
		if err != nil {
			return nil, nil, fmt.Errorf("foreign_key_list(%s): %w", table, err)
		}
		for fks.Next() {
			var parent, from, onDelete string
			if err := fks.Scan(&parent, &from, &onDelete); err != nil {
				fks.Close()
				return nil, nil, err
			}
			// Schema-wide, not only for repos' children: #13's argument is that
			// the MIGRATION adding a cascade is what silently empties tables, so
			// the property that matters is "no cascade anywhere".
			if onDelete != "NO ACTION" {
				cascades = append(cascades, fmt.Sprintf("%s.%s -> %s ON DELETE %s", table, from, parent, onDelete))
			}
			if parent == "repos" {
				children = append(children, repoChild{table, from})
			}
		}
		fks.Close()
	}
	return children, cascades, nil
}

// Proves the fence's two schema rules actually fire, against a synthetic schema
// that breaks both. Without this they are unfalsifiable on the real schema —
// nothing cascades and every child column is `repo_id` — so a mutation removing
// either rule survives and the fence is decoration.
func TestScanRepoChildrenDetectsWhatTheFenceIsFor(t *testing.T) {
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, ddl := range []string{
		`CREATE TABLE repos (id INTEGER PRIMARY KEY)`,
		// A column that is NOT `repo_id` — hardcoding the name would miss it.
		`CREATE TABLE widgets (id INTEGER PRIMARY KEY, owner_repo_id INTEGER NOT NULL REFERENCES repos(id))`,
		// The thing #13 forbids.
		`CREATE TABLE gadgets (id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE)`,
	} {
		if _, err := conn.Exec(ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	children, cascades, err := scanRepoChildren(conn)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range children {
		got[c.table] = c.column
	}
	if got["widgets"] != "owner_repo_id" {
		t.Errorf("child column not read from the schema: got %q for widgets, want owner_repo_id", got["widgets"])
	}
	if got["gadgets"] != "repo_id" {
		t.Errorf("gadgets not discovered as a child of repos: %v", children)
	}
	if len(cascades) != 1 || !strings.Contains(cascades[0], "gadgets") {
		t.Errorf("ON DELETE CASCADE not flagged: %v", cascades)
	}
}

// The fence that stops #370 recurring for the NEXT child table.
//
// D1 was found years after the table that caused it was added. Nothing connected
// "new table referencing repos" to "PurgeRepo must delete it", so this reads the
// live schema and requires the discovered set of repos-children to equal, EXACTLY,
// the set PurgeRepo handles. Exactly rather than as a superset: a superset check
// can be satisfied by editing the list instead of the code, and a mutation adding
// a phantom entry survived until this was tightened.
//
// Known limit, stated rather than papered over: the row-count half only proves
// the tables this test plants a fixture in. Someone adding a child table, adding
// it to `handled`, and adding the DELETE — but no fixture — gets a vacuous zero.
// Exact equality is what forces them to touch this test at all; the fixture is
// on them.
func TestPurgeRepoDeletesEveryTableThatReferencesRepos(t *testing.T) {
	db, _ := openTemp(t)

	children, cascades, err := scanRepoChildren(db.conn)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cascades {
		t.Errorf("%s — this schema deliberately carries no ON DELETE action "+
			"(see deleteSnapshotsTx and #13)", c)
	}

	var names []string
	for _, c := range children {
		names = append(names, c.table)
	}
	handled := []string{"contracts", "snapshots"}
	sort.Strings(names)
	if !slices.Equal(names, handled) {
		t.Errorf("tables referencing repos = %v, PurgeRepo handles %v.\n"+
			"A table here that PurgeRepo does not delete is #370 happening again: "+
			"add a DELETE for it in PurgeRepo, then add it to `handled`.", names, handled)
	}

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
	for _, c := range children {
		var n int
		// The column comes from the schema. Hardcoding `repo_id` would query a
		// nonexistent column on a future `owner_repo_id` child and fail as a test
		// bug rather than as the finding it is.
		q := `SELECT COUNT(*) FROM "` + c.table + `" WHERE "` + c.column + `" = ?`
		if err := db.conn.QueryRow(q, victim).Scan(&n); err != nil {
			t.Fatalf("count %s.%s: %v", c.table, c.column, err)
		}
		if n != 0 {
			t.Errorf("%s still holds %d row(s) for the purged repo", c.table, n)
		}
	}
}
