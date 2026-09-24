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
	// The clamps against seedTable.at's own, independent clamp: past the end is
	// the state after the last line, and below 0 is the state before line 1.
	tb := buildSeedTable(LangPython, strings.Split(string(data), "\n"))
	if got[n+7] != tb.at(n+1) || got[n] != tb.at(n+1) {
		t.Errorf("idx past the end = %+v / %+v, want the final state %+v", got[n+7], got[n], tb.at(n+1))
	}
	if got[-1] != (SeedState{}) || got[0] != (SeedState{}) {
		t.Errorf("idx -1/0 = %+v / %+v, want the zero state", got[-1], got[0])
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

// TestUnpreparedCallersReadOncePerCheck pins withSeeds: a caller that never
// ran PrepareSeeds (the test harnesses, any future caller) still pays one read
// per check, not one per seed func — each of which walks all five trackers.
func TestUnpreparedCallersReadOncePerCheck(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mod.py")
	src := "cfg = {\n    \"a\": 1,\n}\n"
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

	fd := FileDiff{Path: "mod.py", AbsPath: p, AddedLines: []AddedLine{{LineNo: 2, Text: `    "a": helper(1),`}}}
	Run(map[string]struct{}{}, "", []FileDiff{fd})
	FileScopeViolationsWithReason(LangPython, TextToAddedLines(src), fd, map[string]struct{}{})
	if reads != 2 {
		t.Fatalf("seed file read %d times across Run and file-scope unprepared, want 2", reads)
	}

	// Call-shape consults its bracket seed only once a kwarg call reaches an
	// in-file def, so it needs a file and hunk that get that far.
	cs := filepath.Join(filepath.Dir(p), "cs.py")
	csSrc := "def helper(y):\n    return y\n\nx = helper(z=1)\n"
	if err := os.WriteFile(cs, []byte(csSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	p, reads = cs, 0
	csFD := FileDiff{Path: "cs.py", AbsPath: cs, AddedLines: []AddedLine{{LineNo: 4, Text: "x = helper(z=1)"}}}
	if got, _ := PyCallShapeMismatchesWithReason(LangPython, TextToAddedLines(csSrc), csFD, nil, false); len(got) != 1 {
		t.Fatalf("call-shape found %d mismatches, want 1 (the fixture no longer reaches the seeded path)", len(got))
	}
	if reads != 1 {
		t.Fatalf("call-shape read the seed file %d times unprepared, want 1", reads)
	}
}

// TestSeedTableNonPythonStoresOnlyOpen pins the memory half of #335's review:
// outside Python the depths are always 0, so the table keeps only the
// open-string column, the 16 B/line openSeedFor cost before the merge.
func TestSeedTableNonPythonStoresOnlyOpen(t *testing.T) {
	lines := strings.Split("package a\n\nvar s = `raw\n`\n", "\n")
	if tb := buildSeedTable(LangGo, lines); tb.depth != nil || len(tb.open) != len(lines)+1 {
		t.Errorf("Go table: depth=%v open=%d, want no depth column and %d open entries", tb.depth != nil, len(tb.open), len(lines)+1)
	}
	if tb := buildSeedTable(LangPython, lines); len(tb.depth) != len(lines)+1 {
		t.Errorf("Python table must keep a depth column per line")
	}
}
