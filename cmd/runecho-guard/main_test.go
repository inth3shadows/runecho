package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/guard"
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

func TestIgnorePathFor_PrefersWorktreeOverEnrolledContainer(t *testing.T) {
	// The bare-worktree bug: repoRoot is enrolled as the CONTAINER (".../terse",
	// which holds .bare + linked worktrees and NO ignore file) while the real ignore
	// file lives in the linked worktree (".../terse/main"). The guard must read the
	// worktree's file, not the non-existent container path — else every ignore entry
	// is silently dropped and all false positives fire.
	wt := t.TempDir()
	gitInit(t, wt)
	if err := os.WriteFile(filepath.Join(wt, ".runechoguardignore"),
		[]byte("Path\nCounter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	container := t.TempDir() // stands in for ".../terse": no ignore file here

	got := ignorePathFor(wt, container)
	if !fileExists(got) {
		t.Fatalf("returned non-existent path %q — fell back to the container (the bug)", got)
	}
	if b, _ := os.ReadFile(got); string(b) != "Path\nCounter\n" {
		t.Fatalf("read the wrong ignore file %q: %q", got, b)
	}
}

func TestIgnorePathFor_FallsBackToRepoRootWhenWorktreeHasNone(t *testing.T) {
	wt := t.TempDir()
	gitInit(t, wt) // no ignore file in the worktree
	container := t.TempDir()
	if got := ignorePathFor(wt, container); got != filepath.Join(container, ".runechoguardignore") {
		t.Fatalf("expected fallback to repoRoot %q, got %q", container, got)
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
// itself reports (the enrolled path must match the path tier in ResolveRepo).
func enrollTopLevel(t *testing.T, db *snapshot.DB, root string) (int64, string) {
	t.Helper()
	top, err := gitutil.TopLevel(root)
	if err != nil {
		t.Fatalf("gitutil.TopLevel: %v", err)
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

	repo, repoRoot, ok := db.ResolveRepo(sub)
	if !ok {
		t.Fatal("db.ResolveRepo did not resolve via common-dir fast path")
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

	repo, repoRoot, ok := db.ResolveRepo(top)
	if !ok {
		t.Fatal("db.ResolveRepo did not resolve via enrolled-path tier")
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

	if _, _, ok := db.ResolveRepo(root); ok {
		t.Error("db.ResolveRepo resolved an unenrolled repo")
	}
}

func TestHookText_ByTool(t *testing.T) {
	edits := []editOp{{NewString: "a := Foo()"}, {NewString: ""}, {NewString: "b := Bar()"}}

	if got := hookText("Edit", "x := Edited()", "ignored", nil); got != "x := Edited()" {
		t.Errorf("Edit text = %q", got)
	}
	if got := hookText("Write", "ignored", "full file content", nil); got != "full file content" {
		t.Errorf("Write text = %q", got)
	}
	// MultiEdit joins non-empty replacements so symbols in any edit are checked.
	if got := hookText("MultiEdit", "", "", edits); got != "a := Foo()\nb := Bar()" {
		t.Errorf("MultiEdit text = %q, want both edits joined (empty skipped)", got)
	}
	if got := hookText("Read", "x", "y", edits); got != "" {
		t.Errorf("unhandled tool should yield empty text, got %q", got)
	}
}

func TestAddInFileDefs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mod.py")
	src := "def _private_helper(x):\n    return x + 1\n\n" +
		"def public_thing(y):\n    inner = lambda z: z\n    return _private_helper(inner(y))\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	symbols := map[string]struct{}{}
	addInFileDefs(symbols, path, guard.LangPython)

	// Top-level AND indented defs are folded in (the def regex is ^\s*-anchored),
	// so a hunk-scoped Edit calling either won't false-positive.
	for _, want := range []string{"_private_helper", "public_thing"} {
		if _, ok := symbols[want]; !ok {
			t.Errorf("expected %q folded into known set, have %v", want, symbols)
		}
	}
}

func TestAddInFileDefs_MissingFileIsSilent(t *testing.T) {
	symbols := map[string]struct{}{}
	// A brand-new file (Write/Edit creating it) does not exist yet — must add
	// nothing and not panic.
	addInFileDefs(symbols, filepath.Join(t.TempDir(), "does-not-exist.go"), guard.LangGo)
	if len(symbols) != 0 {
		t.Errorf("missing file should add nothing, got %v", symbols)
	}
}

// wholeFileBoundNames is the dropped-import check's whole-file counterpart to
// addInFileDefs: it must see a plain assignment rebind (`re = ...`), which
// ExtractDefs alone does not capture (it only sees def/class/const forms).
func TestWholeFileBoundNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mod.py")
	src := "import re\n\ndef run():\n    re = custom_regex_module()\n    return re\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	bound := wholeFileBoundNames(path, guard.LangPython)
	if _, ok := bound["re"]; !ok {
		t.Errorf("expected re folded in as a whole-file rebind, got %v", bound)
	}
}

func TestWholeFileBoundNames_MissingFileIsSilent(t *testing.T) {
	bound := wholeFileBoundNames(filepath.Join(t.TempDir(), "does-not-exist.py"), guard.LangPython)
	if bound != nil {
		t.Errorf("missing file should yield nil, got %v", bound)
	}
}

func TestSuggestionSuffix(t *testing.T) {
	if got := suggestionSuffix(""); got != "" {
		t.Errorf("empty suggestion should render nothing, got %q", got)
	}
	if got := suggestionSuffix("RealName"); got != "  (did you mean \"RealName\"?)" {
		t.Errorf("suffix = %q", got)
	}
}
