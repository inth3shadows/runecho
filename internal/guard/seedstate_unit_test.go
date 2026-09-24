package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const seedUnitSrc = "cfg = {\n    \"a\": [\n        1,\n    ],\n}\n\"\"\"doc\n"

// TestSeedTableClampsLineNo pins seedTable.at's one clamp: line 0 and below
// read the state before line 1, and anything past the end reads the state
// after the last line (the unterminated docstring here), never a panic.
func TestSeedTableClampsLineNo(t *testing.T) {
	lines := strings.Split(seedUnitSrc, "\n")
	tb := buildSeedTable(LangPython, lines)
	first, last := tb.at(1), tb.at(len(lines)+1)
	if first != (SeedState{}) {
		t.Fatalf("state before line 1 = %+v, want zero", first)
	}
	if last.Open != `"""` {
		t.Fatalf("state after the last line = %+v, want the open docstring", last)
	}
	for _, n := range []int{0, -3} {
		if got := tb.at(n); got != first {
			t.Errorf("at(%d) = %+v, want the line-1 state %+v", n, got, first)
		}
	}
	for _, n := range []int{len(lines) + 2, len(lines) + 100} {
		if got := tb.at(n); got != last {
			t.Errorf("at(%d) = %+v, want the final state %+v", n, got, last)
		}
	}
}

// TestLoadSeedTableCapBoundary pins the fail-open read: a file of exactly
// maxSeedFileBytes seeds, one byte more does not, and a missing file does not.
func TestLoadSeedTableCapBoundary(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, n int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Repeat("x", n)), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if loadSeedTable(LangPython, write("at.py", maxSeedFileBytes)) == nil {
		t.Error("a file of exactly maxSeedFileBytes must seed")
	}
	if loadSeedTable(LangPython, write("over.py", maxSeedFileBytes+1)) != nil {
		t.Error("a file over maxSeedFileBytes must not seed")
	}
	if loadSeedTable(LangPython, filepath.Join(dir, "missing.py")) != nil {
		t.Error("an unreadable file must not seed")
	}
}

// TestIntSeedFuncsNilForNonPython pins the language gate: the four depth
// seeds exist only for Python, while the open-string seed applies to every
// language.
func TestIntSeedFuncsNilForNonPython(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(p, []byte("package a\n\nvar s = `raw\n`\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fd := FileDiff{Path: "a.go", AbsPath: p, PyBraceDepthByLine: map[int]int{1: 1}}
	if seedFunc(LangGo, fd) == nil {
		t.Error("seedFunc must seed a Go file")
	}
	for name, f := range map[string]func(Lang, FileDiff) func(int) int{
		"brace": braceDepthSeedFunc, "bracket": bracketDepthSeedFunc,
		"defSig": defSigDepthSeedFunc, "paramSig": paramSigDepthSeedFunc,
	} {
		if f(LangGo, fd) != nil {
			t.Errorf("%s seed func must be nil for Go", name)
		}
		if f(LangPython, FileDiff{Path: "a.py", AbsPath: p}) == nil {
			t.Errorf("%s seed func must be non-nil for a readable Python file", name)
		}
	}
}

// TestSeedStatesAtMatchesSinglePositions pins the one-walk multi-position form
// against the single-position one: unsorted, duplicated and out-of-range
// indices must each get exactly the state a lone lookup would, keyed by the
// index as given.
func TestSeedStatesAtMatchesSinglePositions(t *testing.T) {
	data, err := os.ReadFile("testdata/seedstate/adversarial.py")
	if err != nil {
		t.Fatal(err)
	}
	fileLines := TextToAddedLines(string(data))
	n := len(fileLines)
	idxs := []int{n - 5, 3, 22, 3, -1, 0, n, n + 7, 19}
	got := SeedStatesAt(LangPython, fileLines, idxs)
	if len(got) != 8 { // 9 requests, one duplicate
		t.Fatalf("got %d entries, want 8", len(got))
	}
	for _, idx := range idxs {
		want := SeedState{
			Open:     OpenStateBefore(LangPython, fileLines, idx),
			Brace:    PyBraceDepthBefore(fileLines, idx),
			Bracket:  PyBracketDepthBefore(fileLines, idx),
			DefSig:   PyDefSigDepthBefore(fileLines, idx),
			ParamSig: PyParamSigDepthBefore(fileLines, idx),
		}
		if got[idx] != want {
			t.Errorf("idx %d: SeedStatesAt = %+v, single lookup = %+v", idx, got[idx], want)
		}
	}
	if s := got[22]; s.Bracket == 0 && s.DefSig == 0 && s.ParamSig == 0 && s.Brace == 0 {
		t.Fatalf("idx 22 is inside a multi-line def in the fixture; a zero state means the fixture moved")
	}
}

// TestPrecommitReadsSeedFileOnce pins #295's pre-commit half: once
// PrepareSeeds has run, Run and the file-scope check together read a staged
// Python file's seed source exactly once, not once per seed func per check.
func TestPrecommitReadsSeedFileOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mod.py")
	src := "cfg = {\n    \"a\": 1,\n}\n\ndef f(a,\n      b=[1]):\n    return a\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	reads := 0
	orig := readSeedFile
	readSeedFile = func(name string) ([]byte, error) {
		if name == p {
			reads++
		}
		return orig(name)
	}
	t.Cleanup(func() { readSeedFile = orig })

	diffs := []FileDiff{{Path: "mod.py", AbsPath: p, AddedLines: []AddedLine{{LineNo: 2, Text: `    "a": helper(1),`}}}}
	PrepareSeeds(diffs)
	Run(map[string]struct{}{}, "", diffs)
	FileScopeViolationsWithReason(LangPython, TextToAddedLines(src), diffs[0], map[string]struct{}{})
	if reads != 1 {
		t.Fatalf("seed file read %d times, want exactly 1", reads)
	}
}
