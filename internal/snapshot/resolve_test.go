package snapshot

// Tests for (*DB).ResolveRepo — the canonical 3-tier repo resolver.
// These live in internal/snapshot because that is where ResolveRepo is defined.
// Each tier is tested in isolation; worktree-list (tier 3) is tested via a
// real linked worktree created with git worktree add.

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/gitutil"
)

// resolveGitInit creates a git repo in dir and writes a minimal Go source file
// so the worktree has at least one file (avoids bare/empty-repo edge cases).
func resolveGitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "stub.go"), []byte("package stub\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatalf("write stub.go: %v", err)
	}
}

// resolveGitCommit makes an initial commit so git worktree add can work.
func resolveGitCommit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"add", "."},
		{"-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "init"},
	} {
		full := append([]string{"-C", dir}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

// TestResolveRepo_CommonDirFastPath: tier 1 — enrolled with common_dir set;
// resolution from a subdirectory resolves in O(1).
func TestResolveRepo_CommonDirFastPath(t *testing.T) {
	root := t.TempDir()
	resolveGitInit(t, root)
	db, _ := openTemp(t)

	cd, err := gitutil.CommonDir(root)
	if err != nil {
		t.Fatalf("CommonDir: %v", err)
	}
	id, err := db.EnrollRepo("r", root, root, 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	if err := db.SetRepoCommonDir(id, cd); err != nil {
		t.Fatalf("SetRepoCommonDir: %v", err)
	}

	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	repo, repoRoot, ok := db.ResolveRepo(sub)
	if !ok {
		t.Fatal("ResolveRepo did not resolve via common-dir fast path")
	}
	if repo.ID != id {
		t.Errorf("repo.ID = %d, want %d", repo.ID, id)
	}
	if repoRoot != root {
		t.Errorf("repoRoot = %q, want %q", repoRoot, root)
	}
}

// TestResolveRepo_PathTier: tier 2 — enrolled WITHOUT common_dir (simulates
// pre-V4 row); resolves via git top-level and backfills common_dir.
func TestResolveRepo_PathTier(t *testing.T) {
	root := t.TempDir()
	resolveGitInit(t, root)
	db, _ := openTemp(t)

	id, err := db.EnrollRepo("r", root, root, 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	// No SetRepoCommonDir — simulates a pre-V4 enrollment.

	repo, repoRoot, ok := db.ResolveRepo(root)
	if !ok {
		t.Fatal("ResolveRepo did not resolve via path tier")
	}
	if repo.ID != id || repoRoot != root {
		t.Errorf("got id=%d root=%q, want id=%d root=%q", repo.ID, repoRoot, id, root)
	}
	// common_dir must be backfilled so the next call uses the fast path.
	after, err := db.GetRepoByName("r")
	if err != nil {
		t.Fatalf("GetRepoByName: %v", err)
	}
	if after.CommonDir == "" {
		t.Error("common_dir was not backfilled after path-tier resolution")
	}
}

// TestResolveRepo_WorktreeTier: tier 3 — enrolled at a worktree path; resolves
// from a different linked worktree via the worktree-list shim.
func TestResolveRepo_WorktreeTier(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	mainDir := t.TempDir()
	linkedDir := filepath.Join(t.TempDir(), "linked")

	resolveGitInit(t, mainDir)
	resolveGitCommit(t, mainDir)
	full := []string{"-C", mainDir, "worktree", "add", linkedDir, "-b", "linked-branch"}
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}

	db, _ := openTemp(t)
	// Enroll mainDir; do NOT set common_dir — simulates pre-V4, forces tier 3.
	id, err := db.EnrollRepo("r", mainDir, mainDir, 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}

	// Resolve from the linked worktree — tier 1 fails (no common_dir), tier 2
	// fails (linkedDir != mainDir), tier 3 hits mainDir via worktree list.
	repo, _, ok := db.ResolveRepo(linkedDir)
	if !ok {
		t.Fatal("ResolveRepo did not resolve linked worktree via worktree-list tier")
	}
	if repo.ID != id {
		t.Errorf("repo.ID = %d, want %d", repo.ID, id)
	}
	// common_dir must be backfilled.
	after, err := db.GetRepoByName("r")
	if err != nil {
		t.Fatalf("GetRepoByName: %v", err)
	}
	if after.CommonDir == "" {
		t.Error("common_dir was not backfilled after worktree-tier resolution")
	}
}

// TestResolveRepo_Unenrolled: no enrolled repo in dir → returns ok=false.
func TestResolveRepo_Unenrolled(t *testing.T) {
	root := t.TempDir()
	resolveGitInit(t, root)
	db, _ := openTemp(t)

	if _, _, ok := db.ResolveRepo(root); ok {
		t.Error("ResolveRepo resolved an unenrolled repo")
	}
}

// TestResolveRepo_SameIDFromBothWorktrees proves that two worktrees of the same
// repo resolve to an identical repo_id — the cross-CLI/guard parity invariant.
func TestResolveRepo_SameIDFromBothWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	mainDir := t.TempDir()
	linkedDir := filepath.Join(t.TempDir(), "linked2")

	resolveGitInit(t, mainDir)
	resolveGitCommit(t, mainDir)
	full := []string{"-C", mainDir, "worktree", "add", linkedDir, "-b", "linked-b2"}
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}

	db, _ := openTemp(t)
	id, err := db.EnrollRepo("r", mainDir, mainDir, 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	// Set common_dir so both worktrees take the fast path.
	cd, err := gitutil.CommonDir(mainDir)
	if err != nil {
		t.Fatalf("CommonDir: %v", err)
	}
	if err := db.SetRepoCommonDir(id, cd); err != nil {
		t.Fatalf("SetRepoCommonDir: %v", err)
	}

	repoFromMain, _, okMain := db.ResolveRepo(mainDir)
	repoFromLinked, _, okLinked := db.ResolveRepo(linkedDir)

	if !okMain || !okLinked {
		t.Fatalf("resolution failed: main=%v linked=%v", okMain, okLinked)
	}
	if repoFromMain.ID != repoFromLinked.ID {
		t.Errorf("repo_id mismatch: main resolved %d, linked resolved %d", repoFromMain.ID, repoFromLinked.ID)
	}
	if repoFromMain.ID != id {
		t.Errorf("unexpected repo_id: got %d, want %d", repoFromMain.ID, id)
	}
}

// TestResolveRepo_MultiEnrolledWorktreesDisambiguateByPath pins issue #61: when a
// bare repo's worktrees are EACH independently enrolled, they share one
// common-dir, so the common-dir lookup matches several rows. Each worktree must
// resolve to ITS OWN enrollment (so it is validated against its own snapshot),
// not to whichever row the common-dir query returns first.
func TestResolveRepo_MultiEnrolledWorktreesDisambiguateByPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	mainDir := t.TempDir()
	linkedDir := filepath.Join(t.TempDir(), "linked61")

	resolveGitInit(t, mainDir)
	resolveGitCommit(t, mainDir)
	full := []string{"-C", mainDir, "worktree", "add", linkedDir, "-b", "linked-61"}
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}

	db, _ := openTemp(t)
	cd, err := gitutil.CommonDir(mainDir)
	if err != nil {
		t.Fatalf("CommonDir: %v", err)
	}
	// Enroll BOTH worktrees separately, each with the SAME shared common-dir.
	mainID, err := db.EnrollRepo("main", mainDir, mainDir, 0)
	if err != nil {
		t.Fatalf("EnrollRepo main: %v", err)
	}
	linkedID, err := db.EnrollRepo("linked", linkedDir, linkedDir, 0)
	if err != nil {
		t.Fatalf("EnrollRepo linked: %v", err)
	}
	for _, id := range []int64{mainID, linkedID} {
		if err := db.SetRepoCommonDir(id, cd); err != nil {
			t.Fatalf("SetRepoCommonDir %d: %v", id, err)
		}
	}

	fromMain, _, okM := db.ResolveRepo(mainDir)
	fromLinked, _, okL := db.ResolveRepo(linkedDir)
	if !okM || !okL {
		t.Fatalf("resolution failed: main=%v linked=%v", okM, okL)
	}
	if fromMain.ID != mainID {
		t.Errorf("main worktree resolved to id %d, want its own %d", fromMain.ID, mainID)
	}
	if fromLinked.ID != linkedID {
		t.Errorf("linked worktree resolved to id %d, want its own %d (issue #61: common-dir shadowed the path)", fromLinked.ID, linkedID)
	}
}

// TestResolveRepo_DBErrorWarnsAndFailsOpen pins finding #7: a real DB fault (not
// "not enrolled") must stay fail-open (ok=false) but be surfaced on stderr rather
// than swallowed, so a transient store error is debuggable instead of looking
// identical to an unenrolled repo.
func TestResolveRepo_DBErrorWarnsAndFailsOpen(t *testing.T) {
	root := t.TempDir()
	resolveGitInit(t, root)
	db, _ := openTemp(t)
	if _, err := db.EnrollRepo("r", root, root, 0); err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	// Closing the connection forces every lookup tier to return a DB error.
	db.Close()

	origErr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr
	_, _, ok := db.ResolveRepo(root)
	wErr.Close()
	os.Stderr = origErr
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, rErr)
	stderr := buf.String()

	if ok {
		t.Fatalf("ResolveRepo ok=true on closed DB; want fail-open ok=false")
	}
	if !strings.Contains(stderr, "lookup failed") {
		t.Errorf("expected a DB-error warning on stderr, got %q", stderr)
	}
}
