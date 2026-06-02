package snapshot

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

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
	if _, err := db.EnrollRepo("r", "/tmp/r", "", 0); err != nil {
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
	id, err := db1.EnrollRepo("r", "/tmp/r", "", 0)
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

	idA, err := db.EnrollRepo("alpha", "/repos/alpha", "", 100)
	if err != nil {
		t.Fatalf("enroll alpha: %v", err)
	}
	if _, err := db.EnrollRepo("beta", "/repos/beta", "", 0); err != nil {
		t.Fatalf("enroll beta: %v", err)
	}

	// Duplicate name and duplicate path must both fail (UNIQUE).
	if _, err := db.EnrollRepo("alpha", "/repos/other", "", 0); err == nil {
		t.Fatal("expected duplicate-name enroll to fail")
	}
	if _, err := db.EnrollRepo("gamma", "/repos/alpha", "", 0); err == nil {
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
	idA, _ := db.EnrollRepo("alpha", "/repos/alpha", "", 0)
	idB, _ := db.EnrollRepo("beta", "/repos/beta", "", 0)

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
	idA, _ := db.EnrollRepo("alpha", "/repos/alpha", "", 0)
	idB, _ := db.EnrollRepo("beta", "/repos/beta", "", 0)
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
	idA, _ := db.EnrollRepo("alpha", "/repos/alpha", "", 0)
	idB, _ := db.EnrollRepo("beta", "/repos/beta", "", 0)

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
	id, err := db.EnrollRepo("r", "/tmp/r", "", 0)
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
	id, err := db.EnrollRepo("r2", "/tmp/r2", "", 0)
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
	id, _ := db.EnrollRepo("r", "/tmp/r", "", 0)

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
	if _, err := db.EnrollRepo("alpha", "/repos/alpha", "", 0); err != nil {
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
	if _, err := db.EnrollRepo("myrepo", "/tmp/myrepo", "", 0); err != nil {
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
	if _, err := db.EnrollRepo("myrepo-2", "/tmp/myrepo-2", "", 0); err != nil {
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

// TestEnrollRepo_SourceRoot verifies EffectiveSourceRoot behavior:
// empty sourceRoot defaults to path; explicit sourceRoot is used when set.
func TestEnrollRepo_SourceRoot(t *testing.T) {
	db, _ := openTemp(t)

	// Empty sourceRoot → defaults to path.
	if _, err := db.EnrollRepo("default", "/repos/default", "", 0); err != nil {
		t.Fatalf("enroll default: %v", err)
	}
	r, err := db.GetRepoByPath("/repos/default")
	if err != nil || r == nil {
		t.Fatalf("GetRepoByPath: %v %v", r, err)
	}
	if r.SourceRoot != "/repos/default" {
		t.Errorf("SourceRoot = %q, want /repos/default", r.SourceRoot)
	}
	if r.EffectiveSourceRoot() != "/repos/default" {
		t.Errorf("EffectiveSourceRoot = %q, want /repos/default", r.EffectiveSourceRoot())
	}

	// Explicit sourceRoot differs from path (bare-repo worktree layout).
	if _, err := db.EnrollRepo("bare", "/repos/bare/main", "/repos/bare", 0); err != nil {
		t.Fatalf("enroll bare: %v", err)
	}
	r2, err := db.GetRepoByPath("/repos/bare/main")
	if err != nil || r2 == nil {
		t.Fatalf("GetRepoByPath bare: %v %v", r2, err)
	}
	if r2.Path != "/repos/bare/main" {
		t.Errorf("Path = %q, want /repos/bare/main", r2.Path)
	}
	if r2.SourceRoot != "/repos/bare" {
		t.Errorf("SourceRoot = %q, want /repos/bare", r2.SourceRoot)
	}
	if r2.EffectiveSourceRoot() != "/repos/bare" {
		t.Errorf("EffectiveSourceRoot = %q, want /repos/bare", r2.EffectiveSourceRoot())
	}
}

// TestMigrateV3BackwardCompat simulates a V2 database (before source_root existed)
// being upgraded to V3 and verifies existing rows get source_root = path.
func TestMigrateV3BackwardCompat(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v2.db")
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := conn.Exec(p); err != nil {
			t.Fatalf("pragma: %v", err)
		}
	}
	// Run only V1 and V2 migrations, stopping before V3.
	for v := 0; v < 2; v++ {
		tx, _ := conn.Begin()
		if err := migrations[v](tx); err != nil {
			t.Fatalf("migration v%d: %v", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			t.Fatalf("set user_version: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit migration v%d: %v", v+1, err)
		}
	}
	// Insert a V2-style repo row (source_root column does not exist yet).
	_, err = conn.Exec(
		`INSERT INTO repos (name, path, file_cap, enrolled_at) VALUES ('legacy', '/repos/legacy', 0, '2026-01-01T00:00:00Z')`,
	)
	if err != nil {
		t.Fatalf("insert legacy repo: %v", err)
	}
	conn.Close()

	// Re-open with snapshot.Open, which applies V3 (adds and backfills source_root).
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open after V3: %v", err)
	}
	defer db.Close()

	if v := userVersion(t, db); v != SchemaVersion {
		t.Fatalf("user_version = %d, want %d", v, SchemaVersion)
	}
	repo, err := db.GetRepoByPath("/repos/legacy")
	if err != nil || repo == nil {
		t.Fatalf("GetRepoByPath: %v %v", repo, err)
	}
	if repo.SourceRoot != "/repos/legacy" {
		t.Errorf("source_root after V3 migration = %q, want /repos/legacy", repo.SourceRoot)
	}
	if repo.EffectiveSourceRoot() != "/repos/legacy" {
		t.Errorf("EffectiveSourceRoot = %q, want /repos/legacy", repo.EffectiveSourceRoot())
	}
}

// TestRepoCommonDir verifies the V4 common_dir lookup key: a freshly enrolled
// repo has no common_dir until set, SetRepoCommonDir persists it, and
// GetRepoByCommonDir round-trips. An empty arg must never match a NULL row.
func TestRepoCommonDir(t *testing.T) {
	db, _ := openTemp(t)

	id, err := db.EnrollRepo("r", "/repos/r/main", "/repos/r", 0)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}

	// Fresh repo: common_dir empty, and an empty lookup must not match it.
	fresh, _ := db.GetRepoByName("r")
	if fresh.CommonDir != "" {
		t.Fatalf("fresh repo CommonDir = %q, want empty", fresh.CommonDir)
	}
	if miss, err := db.GetRepoByCommonDir(""); err != nil || miss != nil {
		t.Fatalf("empty common_dir lookup must be nil,nil: %v %v", miss, err)
	}

	// Set and round-trip.
	if err := db.SetRepoCommonDir(id, "/repos/r/.bare"); err != nil {
		t.Fatalf("SetRepoCommonDir: %v", err)
	}
	got, err := db.GetRepoByCommonDir("/repos/r/.bare")
	if err != nil || got == nil {
		t.Fatalf("GetRepoByCommonDir: %v %v", got, err)
	}
	if got.ID != id || got.CommonDir != "/repos/r/.bare" {
		t.Fatalf("GetRepoByCommonDir mismatch: %+v", got)
	}
	// CommonDir must surface through the other readers too.
	if byName, _ := db.GetRepoByName("r"); byName.CommonDir != "/repos/r/.bare" {
		t.Errorf("GetRepoByName CommonDir = %q, want /repos/r/.bare", byName.CommonDir)
	}

	if miss, err := db.GetRepoByCommonDir("/nope"); err != nil || miss != nil {
		t.Fatalf("unknown common_dir must be nil,nil: %v %v", miss, err)
	}
}

// TestMigrateV4BackwardCompat simulates a V3 database (before common_dir existed)
// being upgraded to V4 and verifies existing rows get a NULL common_dir that the
// readers tolerate, and that an empty-key lookup never matches such a row.
func TestMigrateV4BackwardCompat(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v3.db")
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := conn.Exec(p); err != nil {
			t.Fatalf("pragma: %v", err)
		}
	}
	// Run V1..V3, stopping before V4.
	for v := 0; v < 3; v++ {
		tx, _ := conn.Begin()
		if err := migrations[v](tx); err != nil {
			t.Fatalf("migration v%d: %v", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			t.Fatalf("set user_version: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit migration v%d: %v", v+1, err)
		}
	}
	// Insert a V3-style repo row (common_dir column does not exist yet).
	if _, err := conn.Exec(
		`INSERT INTO repos (name, path, source_root, file_cap, enrolled_at)
		 VALUES ('legacy', '/repos/legacy', '/repos/legacy', 0, '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert legacy repo: %v", err)
	}
	conn.Close()

	// Re-open with snapshot.Open, which applies V4 (adds common_dir, no backfill).
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open after V4: %v", err)
	}
	defer db.Close()

	if v := userVersion(t, db); v != SchemaVersion {
		t.Fatalf("user_version = %d, want %d", v, SchemaVersion)
	}
	repo, err := db.GetRepoByPath("/repos/legacy")
	if err != nil || repo == nil {
		t.Fatalf("GetRepoByPath: %v %v", repo, err)
	}
	if repo.CommonDir != "" {
		t.Errorf("pre-V4 row common_dir after migration = %q, want empty", repo.CommonDir)
	}
	// An empty-key lookup must not resolve the NULL-common_dir legacy row.
	if miss, err := db.GetRepoByCommonDir(""); err != nil || miss != nil {
		t.Fatalf("empty common_dir lookup must be nil,nil: %v %v", miss, err)
	}
	// Backfill then resolve via the fast path.
	if err := db.SetRepoCommonDir(repo.ID, "/repos/legacy/.git"); err != nil {
		t.Fatalf("SetRepoCommonDir: %v", err)
	}
	if got, err := db.GetRepoByCommonDir("/repos/legacy/.git"); err != nil || got == nil || got.ID != repo.ID {
		t.Fatalf("after backfill GetRepoByCommonDir: %v %v", got, err)
	}
}
