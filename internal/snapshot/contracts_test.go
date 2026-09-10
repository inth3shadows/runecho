package snapshot

import (
	"errors"
	"testing"
)

func TestContract_ActivateGetDeactivate(t *testing.T) {
	db, _ := openTemp(t)
	id, err := db.EnrollRepo("r", "/tmp/r", "", 0)
	if err != nil {
		t.Fatal(err)
	}

	// No binding yet: callers must get the sentinel, never an empty struct that
	// could be mistaken for "a contract allowing nothing".
	if _, err := db.GetActiveContract(id, "s1"); !errors.Is(err, ErrNoActiveContract) {
		t.Fatalf("want ErrNoActiveContract, got %v", err)
	}

	if err := db.ActivateContract(id, "s1", "scope-a", "/tmp/r/.runecho/contracts/scope-a", "hash-a"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetActiveContract(id, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "scope-a" || got.ContentHash != "hash-a" {
		t.Errorf("got %+v", got)
	}
	if got.ActivatedAt.IsZero() {
		t.Error("want a parsed activation time")
	}

	if err := db.DeactivateContract(id, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetActiveContract(id, "s1"); !errors.Is(err, ErrNoActiveContract) {
		t.Errorf("after deactivate, want ErrNoActiveContract, got %v", err)
	}
	// Deactivating twice must be safe — the caller's intent is satisfied either way.
	if err := db.DeactivateContract(id, "s1"); err != nil {
		t.Errorf("second deactivate errored: %v", err)
	}
}

// Activating a second contract must REPLACE the first, not stack. "What is in
// scope" has to have a single answer, and the schema enforces that rather than
// leaving it to convention.
func TestContract_ActivateReplacesRatherThanStacks(t *testing.T) {
	db, _ := openTemp(t)
	id, _ := db.EnrollRepo("r", "/tmp/r", "", 0)
	if err := db.ActivateContract(id, "s1", "first", "/p/first", "h1"); err != nil {
		t.Fatal(err)
	}
	if err := db.ActivateContract(id, "s1", "second", "/p/second", "h2"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetActiveContract(id, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "second" || got.ContentHash != "h2" {
		t.Errorf("second activation did not replace the first: %+v", got)
	}
	var n int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM contracts WHERE repo_id = ? AND session_id = ?`, id, "s1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("got %d rows for one (repo, session), want 1", n)
	}
}

// Sessions and repos are independent bindings.
func TestContract_BindingsAreScopedToRepoAndSession(t *testing.T) {
	db, _ := openTemp(t)
	a, _ := db.EnrollRepo("a", "/tmp/a", "", 0)
	b, _ := db.EnrollRepo("b", "/tmp/b", "", 0)
	if err := db.ActivateContract(a, "s1", "for-a", "/p/a", "ha"); err != nil {
		t.Fatal(err)
	}
	if err := db.ActivateContract(a, "s2", "other-session", "/p/a2", "ha2"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetActiveContract(b, "s1"); !errors.Is(err, ErrNoActiveContract) {
		t.Error("a binding leaked across repos")
	}
	got, _ := db.GetActiveContract(a, "s2")
	if got.Name != "other-session" {
		t.Errorf("a binding leaked across sessions: %+v", got)
	}
}

func TestContract_EmptySessionRejected(t *testing.T) {
	db, _ := openTemp(t)
	id, _ := db.EnrollRepo("r", "/tmp/r", "", 0)
	if err := db.ActivateContract(id, "", "x", "/p/x", "h"); err == nil {
		t.Error("activating with an empty session id should error")
	}
}

// enrollWithCommonDir enrolls a repo and records its git-common-dir, which is
// the join key FindSiblingContract uses.
func enrollWithCommonDir(t *testing.T, db *DB, name, path, commonDir string) int64 {
	t.Helper()
	id, err := db.EnrollRepo(name, path, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if commonDir != "" {
		if err := db.SetRepoCommonDir(id, commonDir); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// TestFindSiblingContract_FindsAcrossWorktrees pins #385's whole point: when a
// cwd resolves to a repo id with no contract, a contract bound to a SIBLING
// worktree of the same repository must be discoverable, so the guard can say
// the declared scope is unenforced instead of abstaining silently.
func TestFindSiblingContract_FindsAcrossWorktrees(t *testing.T) {
	db, _ := openTemp(t)
	id1 := enrollWithCommonDir(t, db, "wt-a", "/tmp/wt-a", "/tmp/repo/.bare")
	id2 := enrollWithCommonDir(t, db, "wt-b", "/tmp/wt-b", "/tmp/repo/.bare")

	if err := db.ActivateContract(id1, "s1", "scope-a", "/tmp/wt-a/scope-a", "h"); err != nil {
		t.Fatal(err)
	}
	// Precondition: the sibling's own lookup misses, which is the silent case.
	if _, err := db.GetActiveContract(id2, "s1"); !errors.Is(err, ErrNoActiveContract) {
		t.Fatalf("want ErrNoActiveContract for the sibling id, got %v", err)
	}

	sib, err := db.FindSiblingContract(id2, "s1")
	if err != nil {
		t.Fatalf("want the sibling's contract, got %v", err)
	}
	if sib.RepoID != id1 || sib.Name != "scope-a" || sib.RepoName != "wt-a" {
		t.Errorf("got %+v, want id=%d name=scope-a repo=wt-a", sib, id1)
	}
}

// TestFindSiblingContract_IgnoresUnrelatedRepos is the safety property: a
// contract in a DIFFERENT repository must never be reported. Reporting one
// would tell the user their scope is unenforced when no such scope was ever
// declared for this tree.
func TestFindSiblingContract_IgnoresUnrelatedRepos(t *testing.T) {
	db, _ := openTemp(t)
	other := enrollWithCommonDir(t, db, "other", "/tmp/other", "/tmp/other/.git")
	mine := enrollWithCommonDir(t, db, "mine", "/tmp/mine", "/tmp/mine/.git")
	if err := db.ActivateContract(other, "s1", "scope-x", "/tmp/other/scope-x", "h"); err != nil {
		t.Fatal(err)
	}
	if sib, err := db.FindSiblingContract(mine, "s1"); !errors.Is(err, ErrNoActiveContract) {
		t.Errorf("got %+v (err %v), want ErrNoActiveContract — different repository", sib, err)
	}
}

// TestFindSiblingContract_EmptyCommonDirIsNotAMatch pins the pre-V4 case. Rows
// with no recorded common-dir carry missing data, not a repository they all
// share, so two of them must not read as siblings of each other.
func TestFindSiblingContract_EmptyCommonDirIsNotAMatch(t *testing.T) {
	db, _ := openTemp(t)
	a := enrollWithCommonDir(t, db, "a", "/tmp/a", "")
	b := enrollWithCommonDir(t, db, "b", "/tmp/b", "")
	if err := db.ActivateContract(a, "s1", "scope-a", "/tmp/a/scope-a", "h"); err != nil {
		t.Fatal(err)
	}
	if sib, err := db.FindSiblingContract(b, "s1"); !errors.Is(err, ErrNoActiveContract) {
		t.Errorf("got %+v (err %v), want ErrNoActiveContract — empty common-dir is missing data", sib, err)
	}
}

// TestFindSiblingContract_ScopedToSession keeps the session dimension intact:
// another session's contract on a sibling says nothing about this one.
func TestFindSiblingContract_ScopedToSession(t *testing.T) {
	db, _ := openTemp(t)
	id1 := enrollWithCommonDir(t, db, "wt-a", "/tmp/wt-a", "/tmp/repo/.bare")
	id2 := enrollWithCommonDir(t, db, "wt-b", "/tmp/wt-b", "/tmp/repo/.bare")
	if err := db.ActivateContract(id1, "s1", "scope-a", "/tmp/wt-a/scope-a", "h"); err != nil {
		t.Fatal(err)
	}
	if sib, err := db.FindSiblingContract(id2, "s2"); !errors.Is(err, ErrNoActiveContract) {
		t.Errorf("got %+v (err %v), want ErrNoActiveContract — different session", sib, err)
	}
}

// TestFindSiblingContract_DoesNotReportSelf guards the degenerate case: a repo
// that HAS its own contract must not be reported as its own orphaned sibling.
// The guard only calls this after its own lookup missed, but the query must not
// depend on that for correctness.
func TestFindSiblingContract_DoesNotReportSelf(t *testing.T) {
	db, _ := openTemp(t)
	id := enrollWithCommonDir(t, db, "wt-a", "/tmp/wt-a", "/tmp/repo/.bare")
	if err := db.ActivateContract(id, "s1", "scope-a", "/tmp/wt-a/scope-a", "h"); err != nil {
		t.Fatal(err)
	}
	if sib, err := db.FindSiblingContract(id, "s1"); !errors.Is(err, ErrNoActiveContract) {
		t.Errorf("got %+v (err %v), want ErrNoActiveContract — that is its own contract", sib, err)
	}
}
