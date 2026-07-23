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
