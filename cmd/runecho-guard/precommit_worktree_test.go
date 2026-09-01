package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// Issue #369. The pre-commit path read the staged diff from the ENROLLED row's
// path. Sibling worktrees of a bare repo share one common-dir, so ResolveRepo
// can hand back a different worktree's path — and each worktree has its own
// index. Two consequences, both measured before the fix:
//
//   - the enrolled sibling still exists: `git -C <sibling> diff --cached` returns
//     that worktree's staged changes (usually none), so the guard reported success
//     without ever looking at the commit — silently, with no warning at all;
//   - the enrolled sibling was deleted: git exits 128, and the error path warned
//     and exited 0, which git reads as "allow". Six commits landed unguarded.
//
// The fix reads everything derived from the working tree out of the committing
// worktree, and blocks when the enrolment points at a tree that is gone.

// bareWorktrees builds the claudew layout — a container holding .bare plus two
// linked worktrees — and returns (container, wtA, wtB). wtA carries one commit;
// wtB branches from it.
func bareWorktrees(t *testing.T) (container, wtA, wtB string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container = t.TempDir()
	bare := filepath.Join(container, ".bare")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
		}
	}
	run(container, "init", "-q", "--bare", bare)
	wtA = filepath.Join(container, "wtA")
	wtB = filepath.Join(container, "wtB")
	run(bare, "worktree", "add", "-q", "--orphan", "-b", "wtA", wtA)
	if err := os.WriteFile(filepath.Join(wtA, "lib.py"), []byte("def real_helper():\n    return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(wtA, "add", "lib.py")
	run(wtA, "commit", "-qm", "init")
	run(bare, "symbolic-ref", "HEAD", "refs/heads/wtA")
	run(bare, "worktree", "add", "-q", "-b", "wtB", wtB, "wtA")
	return container, wtA, wtB
}

// enrollWithSnapshot enrols root and gives it one snapshot carrying syms, so
// runPreCommit gets past its "no snapshot yet" early return.
func enrollWithSnapshot(t *testing.T, db *snapshot.DB, name, root string, syms ...string) int64 {
	t.Helper()
	id, err := db.EnrollRepo(name, root, "", 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	if cd, err := gitutil.CommonDir(root); err == nil {
		if err := db.SetRepoCommonDir(id, cd); err != nil {
			t.Fatalf("SetRepoCommonDir: %v", err)
		}
	}
	symbols := make([]ir.Symbol, 0, len(syms))
	for _, s := range syms {
		symbols = append(symbols, ir.Symbol{Name: s, Kind: "function"})
	}
	irData := &ir.IR{
		Version:  1,
		RootHash: "h1",
		Files:    map[string]ir.FileIR{"lib.py": {Hash: "h1", Symbols: symbols}},
	}
	if _, err := db.SaveSnapshot(id, "s1", "v1", root, irData); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	return id
}

// storeAt points RUNECHO_HOME at a temp dir and returns an open store there, so
// runPreCommit's runechoDir() resolves to the same history.db this test writes.
func storeAt(t *testing.T) *snapshot.DB {
	t.Helper()
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func stage(t *testing.T, wt, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", wt, "add", name).CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
}

// The wiring test for the #369 fix. It fails if the ParseStagedDiff call site is
// reverted to repoRoot: the guard then reads the enrolled SIBLING's empty index,
// finds nothing to check, and returns 0 while a hallucinated call sits staged in
// the worktree actually being committed.
func TestRunPreCommit_ReadsStagedDiffFromCommittingWorktree(t *testing.T) {
	_, wtA, wtB := bareWorktrees(t)
	db := storeAt(t)
	enrollWithSnapshot(t, db, "container-wtA", wtA, "real_helper")

	// Staged ONLY in wtB. wtA's index stays empty.
	stage(t, wtB, "app.py", "def caller():\n    return missing_helper_xyz()\n")

	t.Chdir(wtB)
	if got := runPreCommit(false, false); got != 1 {
		t.Fatalf("runPreCommit = %d, want 1 — the staged hallucination in the committing "+
			"worktree went unseen; the diff was read from the enrolled sibling %s", got, wtA)
	}
}

// The counterpart: reading from the committing worktree must not turn the guard
// into something that always blocks. A clean staged change still passes.
func TestRunPreCommit_CommittingWorktreeCleanChangePasses(t *testing.T) {
	_, wtA, wtB := bareWorktrees(t)
	db := storeAt(t)
	enrollWithSnapshot(t, db, "container-wtA", wtA, "real_helper")

	stage(t, wtB, "app.py", "def caller():\n    return real_helper()\n")

	t.Chdir(wtB)
	if got := runPreCommit(false, false); got != 0 {
		t.Fatalf("runPreCommit = %d, want 0 — a call to an indexed symbol must not block", got)
	}
}

// A resolved enrolment whose root is gone means the snapshot describes a tree
// that no longer exists. Before the fix this exited 0 with a warning; git allows
// a commit on 0, so the warning was the only trace and it scrolled past.
func TestRunPreCommit_StaleEnrollmentBlocks(t *testing.T) {
	_, wtA, wtB := bareWorktrees(t)
	db := storeAt(t)
	enrollWithSnapshot(t, db, "container-wtA", wtA, "real_helper")

	stage(t, wtB, "app.py", "def caller():\n    return real_helper()\n")
	if err := os.RemoveAll(wtA); err != nil {
		t.Fatal(err)
	}

	t.Chdir(wtB)
	if got := runPreCommit(false, false); got != 1 {
		t.Fatalf("runPreCommit = %d, want 1 — an enrolment pointing at a deleted tree "+
			"must block, not warn and allow", got)
	}
}

// The block above is deliberately NOT gated on RUNECHO_GUARD_STRICT: a deleted
// root is permanent and not self-healing, unlike the transient faults
// degradedExit covers. Pinning that here stops it being quietly folded back into
// degradedExit, which would restore the fail-open behaviour #369 reported.
func TestRunPreCommit_StaleEnrollmentBlocksWithoutStrict(t *testing.T) {
	_, wtA, wtB := bareWorktrees(t)
	db := storeAt(t)
	enrollWithSnapshot(t, db, "container-wtA", wtA, "real_helper")
	stage(t, wtB, "app.py", "def caller():\n    return real_helper()\n")
	if err := os.RemoveAll(wtA); err != nil {
		t.Fatal(err)
	}

	t.Setenv("RUNECHO_GUARD_STRICT", "")
	t.Chdir(wtB)
	if strictMode() {
		t.Fatal("fixture error: strict mode is on, so this test cannot distinguish the two paths")
	}
	if got := runPreCommit(false, false); got != 1 {
		t.Fatalf("runPreCommit = %d, want 1 without strict mode", got)
	}
}

// Issue #371's half of the same root cause. Staged paths are relative to the
// tree the diff came from, so the on-disk seed must join them against that tree.
// Joining against the enrolled row instead produces a path that does not exist,
// seeding silently does nothing, and a symbol defined in the staged file itself
// — above the hunk, so invisible in the added lines alone — reads as a
// hallucination.
//
// The snapshot here deliberately does NOT contain `helper`, so the seed is the
// only thing that can resolve it. That is what makes the assertion decisive:
// with the seed rooted correctly the commit passes, and with it rooted at the
// enrolled sibling the guard blocks a call that is right there in the file.
func TestRunPreCommit_SeedsInFileDefsFromCommittingWorktree(t *testing.T) {
	_, wtA, wtB := bareWorktrees(t)
	db := storeAt(t)
	enrollWithSnapshot(t, db, "container-wtA", wtA, "real_helper") // no `helper`

	// Commit the definition, with padding, so the later hunk is far below it and
	// carries no `def helper` line of its own.
	body := "def helper(a, b):\n    return a == b\n"
	for i := 0; i < 40; i++ {
		body += "\n\ndef pad(): \n    return 1\n"
	}
	stage(t, wtB, "mod.py", body)
	if out, err := exec.Command("git", "-C", wtB, "-c", "user.email=t@example.com",
		"-c", "user.name=t", "commit", "-qm", "add mod.py").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}

	// Now stage ONLY a call, in its own hunk at the bottom of the file.
	stage(t, wtB, "mod.py", body+"\n\ndef caller(x, y):\n    return helper(x, y)\n")

	t.Chdir(wtB)
	if got := runPreCommit(false, false); got != 0 {
		t.Fatalf("runPreCommit = %d, want 0 — `helper` is defined in the staged file "+
			"itself; the seed was rooted at the enrolled sibling %s, so the file could "+
			"not be read and a real definition read as a hallucination", got, wtA)
	}
}

func TestWorktreeRootFor_PrefersCommittingWorktreeOverEnrolledSibling(t *testing.T) {
	_, wtA, wtB := bareWorktrees(t)
	got := worktreeRootFor(wtB, wtA)
	want, err := gitutil.TopLevel(wtB)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("worktreeRootFor(%q, %q) = %q, want %q — it returned the enrolled sibling",
			wtB, wtA, got, want)
	}
}

// Outside a worktree there is nothing better to use, so repoRoot stands. Same
// fallback shape as ignorePathFor.
func TestWorktreeRootFor_FallsBackToRepoRootOutsideAWorktree(t *testing.T) {
	notARepo := t.TempDir()
	repoRoot := t.TempDir()
	if got := worktreeRootFor(notARepo, repoRoot); got != repoRoot {
		t.Fatalf("worktreeRootFor = %q, want fallback to repoRoot %q", got, repoRoot)
	}
}

func TestDirExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"directory", dir, true},
		{"regular file", file, false},
		{"missing", filepath.Join(dir, "nope"), false},
	} {
		if got := dirExists(tc.path); got != tc.want {
			t.Errorf("dirExists(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
