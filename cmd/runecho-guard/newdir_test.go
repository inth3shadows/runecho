package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
			if d.Hook.PermissionDec != "ask" || !strings.Contains(d.Hook.PermissionReason, "HallucinatedFn") {
				rec := readLastDecisionLog(t)
				t.Fatalf("decision = %q (%q), want an ask naming HallucinatedFn; log: %v",
					d.Hook.PermissionDec, d.Hook.PermissionReason, rec)
			}
		})
	}
}

// lookupDir walks up only through directories that definitively do not exist.
// A path component that is a file, or a directory that exists but cannot be
// read, stops the walk: resolution then runs in the original (missing)
// directory and misses, as it did before #472, instead of climbing into an
// enclosing repository.
func TestLookupDir(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	if err := os.WriteFile(filepath.Join(root, "f.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ file, want string }{
		{filepath.Join(root, "a.go"), root},
		{filepath.Join(root, "x", "y", "a.go"), root},
		{filepath.Join(root, "f.go", "a.go"), filepath.Join(root, "f.go")},
	} {
		if got := lookupDir(tc.file); got != tc.want {
			t.Errorf("lookupDir(%q) = %q, want %q", tc.file, got, tc.want)
		}
	}
	if os.Geteuid() != 0 {
		locked := filepath.Join(root, "locked")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		want := filepath.Join(locked, "new")
		if got := lookupDir(filepath.Join(want, "a.go")); got != want {
			t.Errorf("lookupDir past an unreadable dir = %q, want %q (no climb)", got, want)
		}
	}
}

// #538 review F1: in a claudew-style bare container (<C>/.bare plus a .git
// pointer file, worktrees beneath), a new path that is in NO worktree must not
// be attributed to an enrolled one. The nearest existing ancestor is the
// container, where git finds the common dir but no work tree.
func TestRunHookMode_NewDirInBareContainerIsNotAdopted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	c := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@e", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(c, "init", "-q", "--bare", ".bare")
	if err := os.WriteFile(filepath.Join(c, ".git"), []byte("gitdir: ./.bare\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(c, "worktree", "add", "-q", "--orphan", "-b", "main", "main-wt")
	wt := filepath.Join(c, "main-wt")
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(wt, "add", "-A")
	run(wt, "commit", "-q", "-m", "init")
	goModRepo(t, wt, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_STRICT", "1")

	// Control: a new directory INSIDE the enrolled worktree is checked.
	_, _, d := runHook(t, payload(t, "Write", filepath.Join(wt, "newpkg", "x.go"), "", "package pkg\n\nfunc X() { HallucinatedFn() }\n", nil))
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("control: new dir inside the worktree = %q, want ask", d.Hook.PermissionDec)
	}
	_, _, d = runHook(t, payload(t, "Write", filepath.Join(c, "claude-new", "x.go"), "", "package pkg\n\nfunc X() { HallucinatedFn() }\n", nil))
	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("a path in no worktree was checked against an enrolled one: %q", d.Hook.PermissionReason)
	}
	if rec := readLastDecisionLog(t); rec["reason"] != "no-repo" {
		t.Errorf("reason = %v, want no-repo", rec["reason"])
	}
}

// relWithinRoot's symlink retry resolves the nearest existing ancestor and
// re-attaches the not-yet-created tail, so a Write creating a new package
// under a symlinked repo path is still placed inside the repo (#472).
func TestRelWithinRoot_SymlinkedNewDirectory(t *testing.T) {
	real := t.TempDir()
	gitInit(t, real) // relWithinRoot runs only after the path resolved to a repo
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
