package snapshot

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/inth3shadows/runecho/internal/ir"
)

// makeIR builds a minimal IR with one file carrying the given function names.
func makeIR(rootHash string, fns ...string) *ir.IR {
	return &ir.IR{
		Version:  1,
		RootHash: rootHash,
		Files: map[string]ir.FileIR{
			"main.go": {Hash: rootHash, Functions: fns},
		},
	}
}

// openTemp opens a fresh central store in a temp dir.
func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func userVersion(t *testing.T, db *DB) int {
	t.Helper()
	var v int
	if err := db.conn.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

// TestMigrateToLatest asserts a fresh DB lands on the latest schema version and
// that quick_check passes (Open runs it).
func TestMigrateToLatest(t *testing.T) {
	db, _ := openTemp(t)
	if got := userVersion(t, db); got != SchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, SchemaVersion)
	}
	// repos table must exist (v2) — enrolling proves the schema is present.
	if _, err := db.EnrollRepo("r", "/tmp/r", 0); err != nil {
		t.Fatalf("EnrollRepo on fresh schema: %v", err)
	}
}

// TestMigrateIdempotent asserts re-opening the same DB file is a no-op and the
// version is unchanged (no double-apply, no error).
func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db1, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	id, err := db1.EnrollRepo("r", "/tmp/r", 0)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	db1.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer db2.Close()
	if got := userVersion(t, db2); got != SchemaVersion {
		t.Fatalf("after re-open user_version = %d, want %d", got, SchemaVersion)
	}
	// Data survives; no re-migration clobbered it.
	repo, err := db2.GetRepoByName("r")
	if err != nil || repo == nil || repo.ID != id {
		t.Fatalf("GetRepoByName after re-open: repo=%v err=%v", repo, err)
	}
}

// TestRegistry covers enroll / lookup / list / duplicate constraints / touch.
func TestRegistry(t *testing.T) {
	db, _ := openTemp(t)

	idA, err := db.EnrollRepo("alpha", "/repos/alpha", 100)
	if err != nil {
		t.Fatalf("enroll alpha: %v", err)
	}
	if _, err := db.EnrollRepo("beta", "/repos/beta", 0); err != nil {
		t.Fatalf("enroll beta: %v", err)
	}

	// Duplicate name and duplicate path must both fail (UNIQUE).
	if _, err := db.EnrollRepo("alpha", "/repos/other", 0); err == nil {
		t.Fatal("expected duplicate-name enroll to fail")
	}
	if _, err := db.EnrollRepo("gamma", "/repos/alpha", 0); err == nil {
		t.Fatal("expected duplicate-path enroll to fail")
	}

	got, err := db.GetRepoByPath("/repos/alpha")
	if err != nil || got == nil {
		t.Fatalf("GetRepoByPath: %v %v", got, err)
	}
	if got.ID != idA || got.Name != "alpha" || got.FileCap != 100 {
		t.Fatalf("GetRepoByPath mismatch: %+v", got)
	}
	if !got.LastIndexed.IsZero() {
		t.Fatalf("fresh repo should have zero LastIndexed, got %v", got.LastIndexed)
	}

	if miss, err := db.GetRepoByPath("/nope"); err != nil || miss != nil {
		t.Fatalf("missing path should be nil,nil: %v %v", miss, err)
	}

	repos, err := db.ListRepos()
	if err != nil || len(repos) != 2 {
		t.Fatalf("ListRepos: %d repos, err=%v", len(repos), err)
	}
	if repos[0].Name != "alpha" || repos[1].Name != "beta" {
		t.Fatalf("ListRepos not name-ordered: %+v", repos)
	}

	when := time.Now().UTC().Truncate(time.Second)
	if err := db.TouchRepo(idA, when, 3); err != nil {
		t.Fatalf("TouchRepo: %v", err)
	}
	after, _ := db.GetRepoByPath("/repos/alpha")
	if after.ParseErrors != 3 || !after.LastIndexed.Equal(when) {
		t.Fatalf("TouchRepo not persisted: errs=%d lastIndexed=%v want %v",
			after.ParseErrors, after.LastIndexed, when)
	}
}

// TestRepoScopedHistory is the central-store guarantee: two repos in one DB keep
// separate histories. List/Diff scoped by repo_id never leak across repos.
func TestRepoScopedHistory(t *testing.T) {
	db, _ := openTemp(t)
	idA, _ := db.EnrollRepo("alpha", "/repos/alpha", 0)
	idB, _ := db.EnrollRepo("beta", "/repos/beta", 0)

	// Repo A: two snapshots, second adds Foo.
	a1, err := db.SaveSnapshot(idA, "s", "v1", "/repos/alpha", makeIR("ha1"))
	if err != nil {
		t.Fatalf("save a1: %v", err)
	}
	a2, err := db.SaveSnapshot(idA, "s", "v2", "/repos/alpha", makeIR("ha2", "Foo"))
	if err != nil {
		t.Fatalf("save a2: %v", err)
	}
	// Repo B: one snapshot, must not appear in A's history.
	if _, err := db.SaveSnapshot(idB, "s", "v1", "/repos/beta", makeIR("hb1", "Bar")); err != nil {
		t.Fatalf("save b1: %v", err)
	}

	listA, err := db.List(idA, 10)
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	if len(listA) != 2 {
		t.Fatalf("repo A should have 2 snapshots, got %d", len(listA))
	}
	for _, m := range listA {
		if m.RepoID != idA {
			t.Fatalf("repo A list leaked repo_id %d", m.RepoID)
		}
	}
	listB, _ := db.List(idB, 10)
	if len(listB) != 1 {
		t.Fatalf("repo B should have 1 snapshot, got %d", len(listB))
	}

	// Diff within repo A: v1→v2 added Foo.
	metaA1, _ := db.GetByID(a1)
	metaA2, _ := db.GetByID(a2)
	diff, err := db.Diff(*metaA1, *metaA2)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if diff.TotalAdded != 1 {
		t.Fatalf("expected +1 symbol (Foo), got +%d", diff.TotalAdded)
	}
}

// TestPurgeRepo asserts cascade deletion leaves no orphaned snapshot/file/symbol
// rows and does not touch other repos.
func TestPurgeRepo(t *testing.T) {
	db, _ := openTemp(t)
	idA, _ := db.EnrollRepo("alpha", "/repos/alpha", 0)
	idB, _ := db.EnrollRepo("beta", "/repos/beta", 0)
	if _, err := db.SaveSnapshot(idA, "s", "v1", "/repos/alpha", makeIR("ha", "Foo")); err != nil {
		t.Fatalf("save a: %v", err)
	}
	if _, err := db.SaveSnapshot(idB, "s", "v1", "/repos/beta", makeIR("hb", "Bar")); err != nil {
		t.Fatalf("save b: %v", err)
	}

	if err := db.PurgeRepo(idA); err != nil {
		t.Fatalf("PurgeRepo: %v", err)
	}

	if gone, _ := db.GetRepoByName("alpha"); gone != nil {
		t.Fatal("alpha repo row survived purge")
	}
	if list, _ := db.List(idA, 10); len(list) != 0 {
		t.Fatalf("alpha snapshots survived purge: %d", len(list))
	}
	// No orphaned files/symbols left behind.
	var files, syms int
	db.conn.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&files)
	db.conn.QueryRow(`SELECT COUNT(*) FROM symbols`).Scan(&syms)
	// beta still has exactly its 1 file + 1 symbol.
	if files != 1 || syms != 1 {
		t.Fatalf("orphans after purge: files=%d symbols=%d (want 1,1 for beta)", files, syms)
	}
	if beta, _ := db.GetRepoByName("beta"); beta == nil {
		t.Fatal("purge of alpha wrongly removed beta")
	}
}

// TestSymbolsForLatestSnapshot asserts the method returns the right symbol set
// scoped to the most recent snapshot and ignores other repos.
func TestSymbolsForLatestSnapshot(t *testing.T) {
	db, _ := openTemp(t)
	idA, _ := db.EnrollRepo("alpha", "/repos/alpha", 0)
	idB, _ := db.EnrollRepo("beta", "/repos/beta", 0)

	// alpha: two snapshots — only the latest should be returned.
	if _, err := db.SaveSnapshot(idA, "s1", "old", "/repos/alpha", makeIR("h1", "OldFunc")); err != nil {
		t.Fatalf("save old: %v", err)
	}
	if _, err := db.SaveSnapshot(idA, "s2", "new", "/repos/alpha", makeIR("h2", "NewFunc")); err != nil {
		t.Fatalf("save new: %v", err)
	}
	// beta: should not appear in alpha's result.
	if _, err := db.SaveSnapshot(idB, "s3", "v1", "/repos/beta", makeIR("h3", "BetaFunc")); err != nil {
		t.Fatalf("save beta: %v", err)
	}

	syms, err := db.SymbolsForLatestSnapshot(idA)
	if err != nil {
		t.Fatalf("SymbolsForLatestSnapshot: %v", err)
	}
	if _, ok := syms["NewFunc"]; !ok {
		t.Errorf("expected NewFunc in symbol set, got %v", syms)
	}
	if _, ok := syms["OldFunc"]; ok {
		t.Errorf("OldFunc from stale snapshot leaked into result")
	}
	if _, ok := syms["BetaFunc"]; ok {
		t.Errorf("BetaFunc from other repo leaked into result")
	}

	// Empty repo: no error, empty map.
	empty, err := db.SymbolsForLatestSnapshot(idB + 999)
	if err != nil {
		t.Fatalf("SymbolsForLatestSnapshot for unknown repo: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected empty map for unknown repo, got %v", empty)
	}
}

// TestRemoveRepo_WithSnapshots_Errors asserts that RemoveRepo refuses to delete
// a repo that still has snapshot history — callers must use PurgeRepo instead.
func TestRemoveRepo_WithSnapshots_Errors(t *testing.T) {
	db, _ := openTemp(t)
	id, err := db.EnrollRepo("r", "/tmp/r", 0)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if _, err := db.SaveSnapshot(id, "", "test", "/tmp/r", makeIR("h1", "Foo")); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	if err := db.RemoveRepo(id); err == nil {
		t.Fatal("RemoveRepo with snapshots should return an error")
	}
	// Repo must still exist after the failed remove.
	repo, err := db.GetRepoByName("r")
	if err != nil || repo == nil {
		t.Fatalf("repo should still exist after failed remove: err=%v repo=%v", err, repo)
	}
}

// TestRemoveRepo_NoSnapshots_Succeeds asserts that RemoveRepo works cleanly
// when no snapshots exist for the repo.
func TestRemoveRepo_NoSnapshots_Succeeds(t *testing.T) {
	db, _ := openTemp(t)
	id, err := db.EnrollRepo("r2", "/tmp/r2", 0)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := db.RemoveRepo(id); err != nil {
		t.Fatalf("RemoveRepo with no snapshots: %v", err)
	}
	repo, err := db.GetRepoByName("r2")
	if err != nil || repo != nil {
		t.Fatalf("repo should be gone after remove: err=%v repo=%v", err, repo)
	}
}


// TestGetLatestByLabel_TiebreakById asserts that when two snapshots share the
// same RFC3339 timestamp, the one with the higher row id wins — deterministic
// even in fast CI where sub-second collisions are possible.
func TestGetLatestByLabel_TiebreakById(t *testing.T) {
	db, _ := openTemp(t)
	id, _ := db.EnrollRepo("r", "/tmp/r", 0)

	// Insert two rows with identical timestamps directly to control ordering.
	ts := "2026-06-01T12:00:00Z"
	if _, err := db.conn.Exec(
		`INSERT INTO snapshots (repo_id, session_id, label, timestamp, root, root_hash) VALUES (?, 's', 'v1', ?, '/tmp', 'h-lower')`,
		id, ts,
	); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	if _, err := db.conn.Exec(
		`INSERT INTO snapshots (repo_id, session_id, label, timestamp, root, root_hash) VALUES (?, 's', 'v1', ?, '/tmp', 'h-higher')`,
		id, ts,
	); err != nil {
		t.Fatalf("insert second: %v", err)
	}

	m, err := db.GetLatestByLabel(id, "v1")
	if err != nil {
		t.Fatalf("GetLatestByLabel: %v", err)
	}
	if m == nil {
		t.Fatal("expected a result, got nil")
	}
	if m.RootHash != "h-higher" {
		t.Errorf("tiebreak: got root_hash %q, want h-higher (higher id wins)", m.RootHash)
	}
}

// TestBackupTo asserts VACUUM INTO produces a usable copy with the same data.
func TestBackupTo(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.EnrollRepo("alpha", "/repos/alpha", 0); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := db.BackupTo(backup); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	restored, err := Open(backup)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer restored.Close()
	repo, err := restored.GetRepoByName("alpha")
	if err != nil || repo == nil {
		t.Fatalf("backup missing data: %v %v", repo, err)
	}
}

// TestDeriveRepoName covers the two-segment and root-fallback cases.
func TestDeriveRepoName(t *testing.T) {
	cases := []struct {
		root string
		want string
	}{
		{"/home/ericm/repos/runecho/master", "runecho-master"},
		{"/repos/myapp", "repos-myapp"},
		{"/singledir", "singledir"},
		{"/a/b", "a-b"},
	}
	for _, tc := range cases {
		if got := DeriveRepoName(tc.root); got != tc.want {
			t.Errorf("DeriveRepoName(%q) = %q, want %q", tc.root, got, tc.want)
		}
	}
}

// TestUniqueName asserts disambiguation when a name is already taken.
func TestUniqueName(t *testing.T) {
	db, _ := openTemp(t)
	if _, err := db.EnrollRepo("myrepo", "/tmp/myrepo", 0); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	name, err := UniqueName(db, "myrepo")
	if err != nil {
		t.Fatalf("UniqueName: %v", err)
	}
	if name != "myrepo-2" {
		t.Errorf("got %q, want myrepo-2", name)
	}

	// Enroll myrepo-2 too; next should be myrepo-3.
	if _, err := db.EnrollRepo("myrepo-2", "/tmp/myrepo-2", 0); err != nil {
		t.Fatalf("enroll myrepo-2: %v", err)
	}
	name, err = UniqueName(db, "myrepo")
	if err != nil {
		t.Fatalf("UniqueName second pass: %v", err)
	}
	if name != "myrepo-3" {
		t.Errorf("got %q, want myrepo-3", name)
	}
}

// TestUniqueName_Free asserts that a completely free name is returned unchanged.
func TestUniqueName_Free(t *testing.T) {
	db, _ := openTemp(t)
	name, err := UniqueName(db, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if name != "fresh" {
		t.Errorf("got %q, want fresh", name)
	}
}
