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
