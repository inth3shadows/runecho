package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

func openTempDB(t *testing.T) *snapshot.DB {
	t.Helper()
	db, err := snapshot.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// enrollTopLevel enrolls the git working tree containing root, using the path git
// itself reports (the enrolled path must match gitTopLevelFor for the path tier).
func enrollTopLevel(t *testing.T, db *snapshot.DB, root string) (int64, string) {
	t.Helper()
	top, err := gitTopLevelFor(root)
	if err != nil {
		t.Fatalf("gitTopLevelFor: %v", err)
	}
	id, err := db.EnrollRepo("r", top, top, 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	return id, top
}

// Tier 1: a repo enrolled with its common_dir resolves in O(1) from a subdir.
func TestResolveRepo_CommonDirFastPath(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	db := openTempDB(t)
	id, top := enrollTopLevel(t, db, root)

	cd, err := gitutil.CommonDir(top)
	if err != nil {
		t.Fatalf("CommonDir: %v", err)
	}
	if err := db.SetRepoCommonDir(id, cd); err != nil {
		t.Fatalf("SetRepoCommonDir: %v", err)
	}

	sub := filepath.Join(top, "pkg")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	repo, repoRoot, ok := resolveRepo(db, sub)
	if !ok {
		t.Fatal("resolveRepo did not resolve via common-dir fast path")
	}
	if repo.ID != id {
		t.Errorf("repo.ID = %d, want %d", repo.ID, id)
	}
	if repoRoot != repo.Path {
		t.Errorf("repoRoot = %q, want repo.Path %q", repoRoot, repo.Path)
	}
}

// Tier 2: a repo enrolled WITHOUT common_dir (pre-V4) resolves via its enrolled
// path and backfills common_dir so the next fire takes the fast path.
func TestResolveRepo_PathTierBackfillsCommonDir(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	db := openTempDB(t)
	id, top := enrollTopLevel(t, db, root)
	// Deliberately no SetRepoCommonDir — simulates a pre-V4 enrollment.

	repo, repoRoot, ok := resolveRepo(db, top)
	if !ok {
		t.Fatal("resolveRepo did not resolve via enrolled-path tier")
	}
	if repo.ID != id || repoRoot != top {
		t.Errorf("got id=%d root=%q, want id=%d root=%q", repo.ID, repoRoot, id, top)
	}

	got, err := db.GetRepoByName("r")
	if err != nil {
		t.Fatalf("GetRepoByName: %v", err)
	}
	if got.CommonDir == "" {
		t.Error("common_dir was not backfilled after path-tier resolution")
	}
}

// A directory under no enrolled repo must not resolve.
func TestResolveRepo_Unenrolled(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	db := openTempDB(t)

	if _, _, ok := resolveRepo(db, root); ok {
		t.Error("resolveRepo resolved an unenrolled repo")
	}
}
