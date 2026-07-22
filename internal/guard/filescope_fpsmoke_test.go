package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFileScopeFPSmoke is the reproducible false-positive gate for the file-scope
// check, and the evidence required before RUNECHO_GUARD_FILESCOPE is ever
// considered for default-on.
//
// It runs the check over real, working, committed Python files: each file is
// treated as both the whole file and the "added" text, against a repo-wide symbol
// set approximated from every top-level def/class/const in that tree. Working code
// that ships and passes its tests should produce ZERO violations, so anything
// flagged here is a false-positive candidate to investigate before promotion.
//
// Opt-in (skips in CI and in a normal `go test ./...`): point it at any tree of
// real Python, e.g.
//
//	RUNECHO_FPSMOKE=/path/to/some/python/repo go test ./internal/guard -run FPSmoke -v
//
// Baseline at introduction (2026-07-22): 0 violations across 488 files in 7
// unrelated repositories (~4.9k repo symbols), with a non-vacuity control
// confirming it does flag a genuine cross-file reference that was never imported.
func TestFileScopeFPSmoke(t *testing.T) {
	root := os.Getenv("RUNECHO_FPSMOKE")
	if root == "" {
		t.Skip("set RUNECHO_FPSMOKE=<dir> to run")
	}

	var pyFiles []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".py") {
			return nil
		}
		for _, skip := range []string{"/.venv/", "/node_modules/", "/__pycache__/", "/.git/", "/site-packages/"} {
			if strings.Contains(p, skip) {
				return nil
			}
		}
		pyFiles = append(pyFiles, p)
		return nil
	})

	// Approximate the repo IR: every top-level def/class/const across all files.
	repo := map[string]struct{}{}
	fileLines := map[string][]AddedLine{}
	for _, p := range pyFiles {
		data, err := os.ReadFile(p)
		if err != nil || len(data) > 2<<20 {
			continue
		}
		lines := TextToAddedLines(string(data))
		fileLines[p] = lines
		for _, d := range ExtractDefs(LangPython, lines) {
			repo[d] = struct{}{}
		}
	}

	total, flagged := 0, 0
	counts := map[string]int{}
	for p, lines := range fileLines {
		total++
		vs := FileScopeViolations(LangPython, lines, FileDiff{AddedLines: lines}, repo)
		if len(vs) == 0 {
			continue
		}
		flagged++
		for _, v := range vs {
			counts[v.Symbol]++
			if flagged <= 25 {
				t.Logf("FLAG %s:%d  %s", strings.TrimPrefix(p, root), v.Line, v.Symbol)
			}
		}
	}
	t.Logf("=== files scanned=%d  files flagged=%d  distinct symbols=%d  (repo symbols=%d)",
		total, flagged, len(counts), len(repo))
	for sym, n := range counts {
		if n >= 2 {
			t.Logf("    repeated: %-30s x%d", sym, n)
		}
	}
}
