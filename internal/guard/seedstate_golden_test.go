package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedGoldenInputs are the files TestSeedStateGolden characterises. They cover
// every pre-hunk seed tracker's hard cases: multi-line f-string interpolation
// (#291), a dict closing and a statement opening on one line (#292), stray
// closers, docstrings holding `{` and `def x(`, and multi-line def defaults
// spanning brackets (#294). The .js/.go files exercise the open-string seed,
// the only one that applies outside Python.
var seedGoldenInputs = []string{
	"testdata/seedstate/adversarial.py",
	"testdata/seedstate/template.js",
	"testdata/seedstate/raw.go",
	"testdata/callshape/corpus.py",
	"../../cmd/runecho-guard/testdata/lintcorpus/both_rules.py",
	"../../cmd/runecho-guard/testdata/lintcorpus/clean.py",
	"../../cmd/runecho-guard/testdata/lintcorpus/redefinition.py",
	"../../cmd/runecho-guard/testdata/lintcorpus/star_import.py",
	"../../cmd/runecho-guard/testdata/lintcorpus/undefined_name.py",
}

// TestSeedStateGolden characterises all five pre-hunk seeds — open-string
// state, dict brace depth, bracket depth, def-signature paren depth and
// PyParamNames' signature depth — at the start of every line, from BOTH
// providers: the pre-commit one (seedFunc & co. over FileDiff.AbsPath) and the
// hook one (OpenStateBefore & co. over the file's lines).
//
// It was captured before the #335/#295 consolidation merged the five
// hand-kept per-line rules into one; a diff here during that refactor is a bug
// in the refactor. Regenerate deliberately with:
//
//	RUNECHO_GOLDEN_UPDATE=1 go test ./internal/guard/ -run TestSeedStateGolden
func TestSeedStateGolden(t *testing.T) {
	var b strings.Builder
	for _, in := range seedGoldenInputs {
		abs, err := filepath.Abs(in)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			t.Fatal(err)
		}
		lang := LangFor(abs)
		fd := FileDiff{Path: filepath.Base(abs), AbsPath: abs}
		open := seedFunc(lang, fd)
		brace := braceDepthSeedFunc(lang, fd)
		bracket := bracketDepthSeedFunc(lang, fd)
		defSig := defSigDepthSeedFunc(lang, fd)
		paramSig := paramSigDepthSeedFunc(lang, fd)
		if open == nil {
			t.Fatalf("%s: seedFunc returned nil for a readable file", in)
		}
		py := lang == LangPython
		if py != (brace != nil) || py != (bracket != nil) || py != (defSig != nil) || py != (paramSig != nil) {
			t.Fatalf("%s: int seed funcs must be non-nil exactly for Python", in)
		}
		fileLines := TextToAddedLines(string(data))

		fmt.Fprintf(&b, "== %s (%s)\n", filepath.ToSlash(in), lang)
		// One row per line start, plus one past the end (the clamp's upper edge):
		// lineNo  precommit[open brace bracket defSig paramSig] | hook[...]
		for lineNo := 1; lineNo <= len(fileLines)+1; lineNo++ {
			idx := lineNo - 1
			fmt.Fprintf(&b, "%3d  %q", lineNo, open(lineNo))
			if py {
				fmt.Fprintf(&b, " %d %d %d %d", brace(lineNo), bracket(lineNo), defSig(lineNo), paramSig(lineNo))
			}
			fmt.Fprintf(&b, "  |  %q", OpenStateBefore(lang, fileLines, idx))
			if py {
				fmt.Fprintf(&b, " %d %d %d %d", PyBraceDepthBefore(fileLines, idx), PyBracketDepthBefore(fileLines, idx),
					PyDefSigDepthBefore(fileLines, idx), PyParamSigDepthBefore(fileLines, idx))
			}
			b.WriteByte('\n')
		}
	}
	got := b.String()
	golden := filepath.Join("testdata", "seedstate.golden")
	if os.Getenv("RUNECHO_GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (regenerate with RUNECHO_GOLDEN_UPDATE=1): %v", err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) && i < len(wl); i++ {
			if gl[i] != wl[i] {
				t.Fatalf("seed golden drift at row %d:\n got: %s\nwant: %s", i+1, gl[i], wl[i])
			}
		}
		t.Fatalf("seed golden drift: %d rows, want %d", len(gl), len(wl))
	}
}
