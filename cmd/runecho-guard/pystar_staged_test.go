package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// P5: pre-commit judges the STAGED star source (#524), not the working tree.
// The staged models.py has a literal __all__ (complete, so not_in_all is a
// hallucination); an unstaged edit then removes it from the working tree, which
// would make the source "unknown" and silently abstain.
func TestRunArgs_StarSourceReadFromStagedBlob(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"real_func"})
	pyStarWrite(t, repoRoot, "pkg/__init__.py", "")
	pyStarWrite(t, repoRoot, "pkg/models.py", "__all__ = [\"real_func\"]\ndef real_func(): pass\n")
	pyStarWrite(t, repoRoot, "pkg/app.py", "from pkg.models import *\n\nreal_func()\nnot_in_all()\n")
	if out, err := exec.Command("git", "-C", repoRoot, "add", "pkg").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	// Unstaged working-tree edit: no __all__ any more.
	if err := os.WriteFile(filepath.Join(repoRoot, "pkg", "models.py"), []byte("def real_func(): pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repoRoot)
	if code := runArgs(nil); code != 1 {
		t.Fatalf("exit = %d, want 1: not_in_all is outside the staged source's literal __all__", code)
	}
}
