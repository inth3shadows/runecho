package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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

// TestCommonDir_AbsoluteAndCleaned asserts the returned key is absolute, cleaned,
// and points at the repo's .git dir — the invariant the V4 lookup key relies on.
func TestCommonDir_AbsoluteAndCleaned(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cd, err := CommonDir(dir)
	if err != nil {
		t.Fatalf("CommonDir: %v", err)
	}
	if !filepath.IsAbs(cd) {
		t.Errorf("common-dir not absolute: %q", cd)
	}
	if filepath.Base(cd) != ".git" {
		t.Errorf("common-dir base = %q, want %q", filepath.Base(cd), ".git")
	}
	if cd != filepath.Clean(cd) {
		t.Errorf("common-dir not cleaned: %q", cd)
	}
}

// TestCommonDir_StableAcrossSubdirs is the core guarantee: the same key is
// produced regardless of which subdirectory the call is made from. If enroll-time
// and guard-time disagreed, the O(1) common-dir lookup would silently miss.
func TestCommonDir_StableAcrossSubdirs(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	sub := filepath.Join(dir, "pkg", "deep")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	fromRoot, err := CommonDir(dir)
	if err != nil {
		t.Fatalf("CommonDir(root): %v", err)
	}
	fromSub, err := CommonDir(sub)
	if err != nil {
		t.Fatalf("CommonDir(sub): %v", err)
	}
	if fromRoot != fromSub {
		t.Errorf("common-dir differs by cwd: root=%q sub=%q", fromRoot, fromSub)
	}
}

func TestCommonDir_NonRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := CommonDir(t.TempDir()); err == nil {
		t.Error("CommonDir on a non-git dir should error")
	}
}

// TestCommonDir_RejectsRelativeDir asserts the absolute-dir contract is enforced
// at runtime: a relative dir would join to a cwd-dependent key and silently break
// the V4 lookup. The guard fires before any git invocation, so no repo is needed.
func TestCommonDir_RejectsRelativeDir(t *testing.T) {
	if _, err := CommonDir("relative/path"); err == nil {
		t.Error("CommonDir with a relative dir should error")
	}
}

// TestTopLevel returns the working-tree root and is cwd-stable. Compared via
// EvalSymlinks because git resolves symlinks (e.g. /tmp -> /private/var) while
// t.TempDir does not.
func TestTopLevel(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	top, err := TopLevel(sub)
	if err != nil {
		t.Fatalf("TopLevel: %v", err)
	}
	if !filepath.IsAbs(top) {
		t.Errorf("TopLevel not absolute: %q", top)
	}
	want, _ := filepath.EvalSymlinks(dir)
	got, _ := filepath.EvalSymlinks(top)
	if got != want {
		t.Errorf("TopLevel = %q, want repo root %q", got, want)
	}
}

func TestTopLevel_NonRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := TopLevel(t.TempDir()); err == nil {
		t.Error("TopLevel on a non-git dir should error")
	}
}

// TestAbsGitDir is the hook-install path: it must equal CommonDir, since hooks
// live in the stable common-dir shared across worktrees.
func TestAbsGitDir(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	gd, err := AbsGitDir(dir)
	if err != nil {
		t.Fatalf("AbsGitDir: %v", err)
	}
	cd, err := CommonDir(dir)
	if err != nil {
		t.Fatalf("CommonDir: %v", err)
	}
	if gd != cd {
		t.Errorf("AbsGitDir = %q, want CommonDir %q", gd, cd)
	}
}

// TestWorktreePaths lists the main worktree of a fresh repo; the repo root must
// appear among the returned paths.
func TestWorktreePaths(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	paths := WorktreePaths(dir)
	if len(paths) == 0 {
		t.Fatalf("WorktreePaths returned none")
	}
	want, _ := filepath.EvalSymlinks(dir)
	found := false
	for _, p := range paths {
		if ep, _ := filepath.EvalSymlinks(p); ep == want {
			found = true
		}
	}
	if !found {
		t.Errorf("repo root %q not in worktree paths %v", want, paths)
	}
}

func TestWorktreePaths_NonRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if paths := WorktreePaths(t.TempDir()); paths != nil {
		t.Errorf("WorktreePaths on a non-git dir should be nil, got %v", paths)
	}
}
