package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/store"
)

// seedLocks writes one refresh lock per id plus every extra name verbatim, and
// returns the store dir. The extras exist to prove the sweeper leaves anything
// it did not write alone — that is the property with teeth, since a --yes run
// deletes what this reports.
func seedLocks(t *testing.T, ids []int64, extras ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	for _, id := range ids {
		if err := os.WriteFile(store.RefreshLockPath(home, id), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range extras {
		if err := os.WriteFile(filepath.Join(home, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func reposWithIDs(ids ...int64) []snapshot.Repo {
	repos := make([]snapshot.Repo, 0, len(ids))
	for _, id := range ids {
		repos = append(repos, snapshot.Repo{ID: id})
	}
	return repos
}

// TestOrphanRefreshLocks_OnlyUnenrolledIDs pins #384's core question: a lock is
// an orphan exactly when its id has no repos row.
func TestOrphanRefreshLocks_OnlyUnenrolledIDs(t *testing.T) {
	home := seedLocks(t, []int64{1, 2, 3, 4})
	orphans, err := orphanRefreshLocks(liveRepoIDs(reposWithIDs(2, 4)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{store.RefreshLockPath(home, 1), store.RefreshLockPath(home, 3)}
	if len(orphans) != len(want) {
		t.Fatalf("got %v, want %v", orphans, want)
	}
	for i := range want {
		if orphans[i] != want[i] {
			t.Errorf("orphans[%d] = %q, want %q", i, orphans[i], want[i])
		}
	}
}

// TestOrphanRefreshLocks_LeavesForeignFilesAlone is the safety property. A file
// in the store dir that RefreshLockPath would never have produced must never
// reach the delete list, no matter how much it resembles one.
func TestOrphanRefreshLocks_LeavesForeignFilesAlone(t *testing.T) {
	seedLocks(t, nil,
		"history.db", "decisions.jsonl", "e6-refresh-abc.lock",
		"e6-refresh-.lock", "e6-refresh-7.lock.bak", "depcache",
	)
	orphans, err := orphanRefreshLocks(liveRepoIDs(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Errorf("got %v, want none — none of these are locks this code writes", orphans)
	}
}

// TestSweepRefreshLocks_RemovesAndCounts checks the sweep itself, including the
// best-effort contract: a path that is already gone is not an error and is not
// counted, and it does not stop the rest of the sweep.
func TestSweepRefreshLocks_RemovesAndCounts(t *testing.T) {
	home := seedLocks(t, []int64{1, 2})
	paths := []string{
		store.RefreshLockPath(home, 1),
		store.RefreshLockPath(home, 99), // never existed
		store.RefreshLockPath(home, 2),
	}
	if removed := sweepRefreshLocks(paths); removed != 2 {
		t.Errorf("removed %d, want 2", removed)
	}
	for _, id := range []int64{1, 2} {
		if _, err := os.Stat(store.RefreshLockPath(home, id)); !os.IsNotExist(err) {
			t.Errorf("lock %d still present", id)
		}
	}
}

// TestOrphanRefreshLocks_EmptyStoreDir pins the no-store case: a box that has
// never taken the E6 path reports nothing rather than erroring.
func TestOrphanRefreshLocks_EmptyStoreDir(t *testing.T) {
	seedLocks(t, nil)
	orphans, err := orphanRefreshLocks(liveRepoIDs(reposWithIDs(1)))
	if err != nil || len(orphans) != 0 {
		t.Errorf("got (%v, %v), want (none, nil)", orphans, err)
	}
}
