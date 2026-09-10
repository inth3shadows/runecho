package store

import (
	"path/filepath"
	"testing"
)

// TestRefreshLockPath pins the exact lock-file name. The guard hook and the CLI
// (reindex/index) both derive their lock path here; a drift in this name would
// silently give them different lock files and defeat the mutual exclusion (#137).
func TestRefreshLockPath(t *testing.T) {
	got := RefreshLockPath("/store", 42)
	want := filepath.Join("/store", "e6-refresh-42.lock")
	if got != want {
		t.Errorf("RefreshLockPath = %q, want %q", got, want)
	}
}

// TestRefreshLockIDRoundTrip pins the invariant #384's sweeper rests on: every
// name RefreshLockPath produces must parse back to the same id. A divergence
// here means the sweeper silently deletes nothing.
func TestRefreshLockIDRoundTrip(t *testing.T) {
	for _, id := range []int64{1, 7, 42, 102, 999999} {
		base := filepath.Base(RefreshLockPath("/tmp/store", id))
		got, ok := RefreshLockID(base)
		if !ok || got != id {
			t.Errorf("RefreshLockID(%q) = (%d, %v), want (%d, true)", base, got, ok, id)
		}
	}
}

// TestRefreshLockIDRefusesForeignNames pins the other direction, which is the
// one with teeth: RefreshLockID is the predicate a --yes sweep DELETES on, so
// anything it did not write must read as "not ours".
func TestRefreshLockIDRefusesForeignNames(t *testing.T) {
	for _, base := range []string{
		"history.db",
		"decisions.jsonl",
		"e6-refresh-.lock",       // no id at all
		"e6-refresh-abc.lock",    // not a number
		"e6-refresh-0.lock",      // no repo has id 0
		"e6-refresh--3.lock",     // negative
		"e6-refresh-12.lock.bak", // suffix does not close the name
		"prefix-e6-refresh-12.lock",
		"e6-refresh-12",
		"e6-refresh-1 2.lock",
	} {
		if id, ok := RefreshLockID(base); ok {
			t.Errorf("RefreshLockID(%q) = (%d, true), want refused", base, id)
		}
	}
}

// TestRefreshLockGlobMatchesWhatPathWrites keeps the glob and the writer in
// agreement — a glob that missed the real name would make the sweeper a no-op
// that still reports success.
func TestRefreshLockGlobMatchesWhatPathWrites(t *testing.T) {
	dir := t.TempDir()
	want := RefreshLockPath(dir, 5)
	matched, err := filepath.Match(RefreshLockGlob(dir), want)
	if err != nil || !matched {
		t.Fatalf("glob %q does not match %q (err=%v)", RefreshLockGlob(dir), want, err)
	}
}
