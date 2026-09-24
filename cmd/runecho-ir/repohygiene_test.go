package main

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitignoreCoversCmdBuildOutputs pins that `go build .` inside any
// cmd/<name>/ produces a binary .gitignore already covers, so `git add -A`
// cannot commit it (a 40MB runecho-ir nearly shipped on the #375 branch). It
// enumerates cmd/ rather than listing names, so a new command fails here until
// its binary is ignored too.
func TestGitignoreCoversCmdBuildOutputs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		t.Skip("not inside a git work tree")
	}
	entries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		if !e.IsDir() || !hasMainPackage(t, filepath.Join(root, "cmd", e.Name())) {
			continue
		}
		checked++
		bin := filepath.ToSlash(filepath.Join("cmd", e.Name(), e.Name()))
		// core.excludesFile=/dev/null: only the repo's own rules count — a
		// developer's global ignore covering the name would otherwise pass here
		// and still let `git add -A` commit the binary on another machine.
		// ($GIT_DIR/info/exclude is also local-only, but worktrees share it and
		// nothing in this repo writes to it.)
		if err := exec.Command("git", "-c", "core.excludesFile=/dev/null", "-C", root, "check-ignore", "-q", "--no-index", bin).Run(); err != nil {
			t.Errorf("%s (the output of `go build .` in cmd/%s) is not ignored by .gitignore", bin, e.Name())
		}
	}
	if checked == 0 {
		t.Fatal("found no package-main directories under cmd/; the test is checking nothing")
	}
}

func hasMainPackage(t *testing.T, dir string) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.PackageClauseOnly)
		if err != nil {
			t.Fatal(err)
		}
		if file.Name.Name == "main" {
			return true
		}
	}
	return false
}
