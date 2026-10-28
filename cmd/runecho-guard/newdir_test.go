package main

import (
	"os"
	"path/filepath"
	"testing"
)

// #472: a Write that creates a new package directory is the edit most likely
// to carry whole-file hallucinations, and it was never checked — repo
// resolution ran git inside a directory that does not exist yet, missed, and
// deferred "no-repo" even under STRICT.
func TestRunHookMode_WriteIntoNewDirectoryIsChecked(t *testing.T) {
	for _, rel := range []string{"newpkg/b.go", "pkg/sub/deep/b.go"} {
		t.Run(rel, func(t *testing.T) {
			repoRoot := t.TempDir()
			gitInit(t, repoRoot)
			goModRepo(t, repoRoot, []string{"KnownFunc"})
			t.Setenv("RUNECHO_GUARD_STRICT", "1")

			target := filepath.Join(repoRoot, rel)
			if _, err := os.Stat(filepath.Dir(target)); !os.IsNotExist(err) {
				t.Fatalf("precondition: %s must not exist", filepath.Dir(target))
			}
			_, _, d := runHook(t, payload(t, "Write", target, "", "package pkg\n\nfunc X() { HallucinatedFn() }\n", nil))
			if d.Hook.PermissionDec != "ask" {
				rec := readLastDecisionLog(t)
				t.Fatalf("permissionDecision = %q, want ask; log: %v", d.Hook.PermissionDec, rec)
			}
		})
	}
}

// The ancestor walk must not reach past the repository: a new directory
// outside any repo is still unenrolled.
func TestExistingDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ file, want string }{
		{filepath.Join(root, "a.go"), root},
		{filepath.Join(root, "x", "y", "a.go"), root},
		// A path component that is a FILE is not a directory to resolve from.
		{filepath.Join(root, "f.go", "a.go"), root},
	} {
		if got := existingDir(tc.file); got != tc.want {
			t.Errorf("existingDir(%q) = %q, want %q", tc.file, got, tc.want)
		}
	}
}

// relWithinRoot's symlink retry resolves the nearest existing ancestor and
// re-attaches the not-yet-created tail, so a Write creating a new package
// under a symlinked repo path is still placed inside the repo (#472).
func TestRelWithinRoot_SymlinkedNewDirectory(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	root, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := relWithinRoot(root, filepath.Join(link, "newpkg", "deep", "b.go"))
	if !ok || got != "newpkg/deep/b.go" {
		t.Errorf("relWithinRoot = (%q, %v), want (\"newpkg/deep/b.go\", true)", got, ok)
	}
	if _, ok := relWithinRoot(root, filepath.Join(t.TempDir(), "other", "b.go")); ok {
		t.Error("a new path outside the root was placed inside it")
	}
}
