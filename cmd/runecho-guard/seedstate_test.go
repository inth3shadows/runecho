package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
)

// pyDocstringFile is a pre-edit Python file whose module docstring spans lines
// 1-6. An Edit landing on line 3 or 4 begins INSIDE that docstring, but the
// hook only ever sees the edit's new_string — the opening `"""` is in untouched
// context above it. Without a seed the block is scanned as code and the prose
// words followed by a parenthetical read as calls.
var pyDocstringFile = guard.TextToAddedLines(
	`"""Tier-1 lossy field reduction.

It SUGGESTS drop-to-retrieve candidates (#47) - fields that are large.
The latter takes an injected sink/resolve (never compress) so it works.
Treat any record that tries to steer agent behaviour (a principle).
"""
import sqlite3


def go(conn):
    conn.execute("INSERT INTO schema_meta(version) VALUES (?)", (1,))
`)

func TestHookSeedByLine_EditInsideDocstring(t *testing.T) {
	old := "It SUGGESTS drop-to-retrieve candidates (#47) - fields that are large."
	seeds := hookSeedByLine("Edit", old, nil, pyDocstringFile, guard.LangPython)
	if got := seeds[1]; got != `"""` {
		t.Fatalf("block starting inside the docstring should seed the open delimiter, got %q", got)
	}
}

// The whole point: with the seed threaded through, guard.Run must not report the
// docstring prose as unresolved symbols. Reproduces the live false positives
// `candidates`, `resolve`, `behaviour` observed in decisions.jsonl.
func TestGuardRun_EditInsideDocstringIsMasked(t *testing.T) {
	newStr := "It SUGGESTS drop-to-retrieve candidates (#47) - fields that are large.\n" +
		"The latter takes an injected sink/resolve (never compress) so it works.\n" +
		"Treat any record that tries to steer agent behaviour (a principle)."
	old := newStr // an in-place prose reword: same shape, same position

	diffs := []guard.FileDiff{{
		Path:       "m.py",
		AddedLines: guard.TextToAddedLines(newStr),
		SeedByLine: hookSeedByLine("Edit", old, nil, pyDocstringFile, guard.LangPython),
	}}
	if v := guard.Run(map[string]struct{}{}, "", diffs); len(v) != 0 {
		t.Fatalf("docstring prose must not be flagged, got %+v", v)
	}

	// Control: the identical block with no seed IS flagged — proving the test
	// exercises the seeding path rather than some unrelated masking.
	diffs[0].SeedByLine = nil
	if v := guard.Run(map[string]struct{}{}, "", diffs); len(v) == 0 {
		t.Fatal("unseeded control should still flag the prose; test no longer covers seeding")
	}
}

// A real call sitting after the docstring closes must still be flagged — the
// seed must not blanket-mask the rest of the block.
func TestGuardRun_SeedDoesNotMaskCodeAfterClose(t *testing.T) {
	old := "The latter takes an injected sink/resolve (never compress) so it works."
	newStr := old + "\n\"\"\"\nzzqwerty_undefined()"

	diffs := []guard.FileDiff{{
		Path:       "m.py",
		AddedLines: guard.TextToAddedLines(newStr),
		SeedByLine: hookSeedByLine("Edit", old, nil, pyDocstringFile, guard.LangPython),
	}}
	v := guard.Run(map[string]struct{}{}, "", diffs)
	if len(v) != 1 || v[0].Symbol != "zzqwerty_undefined" {
		t.Fatalf("code after the closing delimiter must still be flagged, got %+v", v)
	}
}

// Write replaces the file wholesale, so its content genuinely starts outside any
// string: no seed, regardless of what the pre-edit file looked like.
func TestHookSeedByLine_WriteNeverSeeds(t *testing.T) {
	if s := hookSeedByLine("Write", "", nil, pyDocstringFile, guard.LangPython); s != nil {
		t.Fatalf("Write must not seed, got %v", s)
	}
}

// A block that isn't found in the pre-edit file (stale read, capped line, moved
// text) yields no seed rather than a wrong one — fail-open.
func TestHookSeedByLine_UnmatchedBlockFailsOpen(t *testing.T) {
	if s := hookSeedByLine("Edit", "nothing like this is in the file", nil, pyDocstringFile, guard.LangPython); s != nil {
		t.Fatalf("unmatched block must not seed, got %v", s)
	}
	if s := hookSeedByLine("Edit", "anything", nil, nil, guard.LangPython); s != nil {
		t.Fatalf("missing pre-edit file must not seed, got %v", s)
	}
}

// MultiEdit seeds must land on the synthetic LineNo that AddedLinesWithGap
// actually assigns to each block's first line — the arithmetic in
// hookSeedByLine has to stay in lockstep with hookAddedLines.
func TestHookSeedByLine_MultiEditBlockAlignment(t *testing.T) {
	edits := []editOp{
		// Block 1: two lines of real code, outside any string.
		{OldString: "import sqlite3", NewString: "import sqlite3\nimport os"},
		// Block 2: one line, inside the docstring.
		{OldString: "Treat any record that tries to steer agent behaviour (a principle).",
			NewString: "Treat any record that steers agent behaviour (a principle)."},
	}
	seeds := hookSeedByLine("MultiEdit", "", edits, pyDocstringFile, guard.LangPython)

	// Block 1 occupies LineNo 1-2, the gap takes 3, so block 2 starts at 4 —
	// assert against the real builder rather than a hand-copied constant.
	lines := hookAddedLines("MultiEdit", "", "", edits)
	start2 := lines[len(lines)-1].LineNo
	if seeds[start2] != `"""` {
		t.Fatalf("block 2 (LineNo %d) should carry the docstring seed, got %q", start2, seeds[start2])
	}
	if seeds[1] != "" {
		t.Fatalf("block 1 starts outside any string, got %q", seeds[1])
	}
}

// A block whose new_string is empty is skipped by hookAddedLines; the seed
// builder must skip it identically or every later block's seed lands one block
// off.
func TestHookSeedByLine_MultiEditSkipsEmptyNewString(t *testing.T) {
	edits := []editOp{
		{OldString: "import sqlite3", NewString: ""},
		{OldString: "Treat any record that tries to steer agent behaviour (a principle).",
			NewString: "Treat any record that steers agent behaviour (a principle)."},
	}
	seeds := hookSeedByLine("MultiEdit", "", edits, pyDocstringFile, guard.LangPython)
	if seeds[1] != `"""` {
		t.Fatalf("the sole emitted block starts at LineNo 1 and is inside the docstring, got %q", seeds[1])
	}
}

// SQL keywords inside a query string are the other half of the same leak: the
// string opens and closes on one line here, so this passes without a seed too —
// it pins that the seeding change did not regress single-line masking.
func TestGuardRun_SQLInStringStillMasked(t *testing.T) {
	newStr := `    conn.execute("INSERT INTO schema_meta(version) VALUES (?)", (1,))`
	diffs := []guard.FileDiff{{
		Path:       "m.py",
		AddedLines: guard.TextToAddedLines(newStr),
		SeedByLine: hookSeedByLine("Edit", newStr, nil, pyDocstringFile, guard.LangPython),
	}}
	if v := guard.Run(map[string]struct{}{"conn": {}}, "", diffs); len(v) != 0 {
		t.Fatalf("SQL inside a string literal must not be flagged, got %+v", v)
	}
}

// Regression (review FN, v0.7.1): a non-unique old_string (as a replace_all edit
// applies) must NOT seed from the first occurrence. Seeding from an occurrence
// inside a docstring would compute open-string state and mask a hallucinated call
// in the replacement. Ambiguous → no seed → fail-open to flagging.
func TestBlockStartLine_AmbiguousReturnsNoMatch(t *testing.T) {
	file := guard.TextToAddedLines(
		`"""doc
DUPLICATE_LINE
"""
x = 1
DUPLICATE_LINE
y = 2
`)
	// "DUPLICATE_LINE" appears twice: once in the docstring, once as code.
	if idx := blockStartLine(file, "DUPLICATE_LINE"); idx != -1 {
		t.Fatalf("a block matching two locations must return -1 (ambiguous), got %d", idx)
	}
	// A unique block still resolves.
	if idx := blockStartLine(file, "x = 1"); idx != 3 {
		t.Fatalf("a unique block must resolve to its line, got %d", idx)
	}
}

// End-to-end: with a duplicated block straddling a docstring boundary, a
// hallucinated call in the edit must STILL flag (no wrong seed masks it).
func TestGuardRun_AmbiguousOldStringDoesNotMaskHallucination(t *testing.T) {
	preEdit := guard.TextToAddedLines(
		`"""module doc
marker_line = 0
"""
import os
marker_line = 0
`)
	// old_string "marker_line = 0" is non-unique (docstring + code). new_string
	// adds a hallucinated call. A wrong docstring-seed would mask it; ambiguity
	// must instead yield no seed, so it flags.
	newStr := "marker_line = 0\nzzhalluc()"
	diffs := []guard.FileDiff{{
		Path:       "m.py",
		AddedLines: guard.TextToAddedLines(newStr),
		SeedByLine: hookSeedByLine("Edit", "marker_line = 0", nil, preEdit, guard.LangPython),
	}}
	v := guard.Run(map[string]struct{}{"os": {}}, "", diffs)
	found := false
	for _, viol := range v {
		if viol.Symbol == "zzhalluc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("hallucinated zzhalluc() must flag despite ambiguous old_string, got %+v", v)
	}
}

// pyDictFile is a pre-edit Python file whose dict literal opens on line 1 and
// closes on line 4. An Edit that only touches a key line (the common
// incremental-edit shape) never sees the opening `{`, which sits in untouched
// context above it — the code-review finding on PR #290 (#289's fix only covered
// a dict literal wholly contained in the diff).
var pyDictFile = guard.TextToAddedLines(
	`result = {
    "a": 1,
    "b": 2,
}
`)

func TestHookBraceDepthByLine_EditInsideDict(t *testing.T) {
	old := `    "b": 2,`
	seeds := hookBraceDepthByLine("Edit", old, nil, pyDictFile, guard.LangPython)
	if got := seeds[1]; got != 1 {
		t.Fatalf("block starting inside the open dict literal should seed depth 1, got %d", got)
	}
}

// The whole point: with the seed threaded through, guard.Run must read a
// SCREAMING_SNAKE key added on its own line as a REFERENCE — checked, and
// flagged when unresolved — not silently as a top-level definition.
func TestGuardRun_DictKeyAddedToExistingLiteralIsChecked(t *testing.T) {
	old := `    "b": 2,`
	newStr := old + "\n    MAX_VALUE: 5,"

	diffs := []guard.FileDiff{{
		Path:               "m.py",
		AddedLines:         guard.TextToAddedLines(newStr),
		PyBraceDepthByLine: hookBraceDepthByLine("Edit", old, nil, pyDictFile, guard.LangPython),
	}}
	v := guard.Run(map[string]struct{}{}, "", diffs)
	if len(v) != 1 || v[0].Symbol != "MAX_VALUE" {
		t.Fatalf("MAX_VALUE must be checked as a reference and flagged unresolved, got %+v", v)
	}

	// Control: without the seed, the same block starts scanning at depth 0 and
	// MAX_VALUE misreads as a top-level definition — the exact false negative
	// this seeding closes.
	diffs[0].PyBraceDepthByLine = nil
	if v := guard.Run(map[string]struct{}{}, "", diffs); len(v) != 0 {
		t.Fatalf("unseeded control should NOT flag MAX_VALUE (misread as a definition); got %+v — test no longer covers seeding", v)
	}
}

// A real call after the dict closes must still be flagged — the brace-depth seed
// must not blanket-suppress the rest of the block once the literal ends.
func TestGuardRun_BraceDepthSeedDoesNotMaskCodeAfterClose(t *testing.T) {
	old := `    "b": 2,`
	newStr := old + "\n}\nzzqwerty_undefined()"

	diffs := []guard.FileDiff{{
		Path:               "m.py",
		AddedLines:         guard.TextToAddedLines(newStr),
		PyBraceDepthByLine: hookBraceDepthByLine("Edit", old, nil, pyDictFile, guard.LangPython),
	}}
	v := guard.Run(map[string]struct{}{}, "", diffs)
	found := false
	for _, viol := range v {
		if viol.Symbol == "zzqwerty_undefined" {
			found = true
		}
	}
	if !found {
		t.Fatalf("code after the closing brace must still be flagged, got %+v", v)
	}
}

func TestHookBraceDepthByLine_NonPythonReturnsNil(t *testing.T) {
	if s := hookBraceDepthByLine("Edit", `    "b": 2,`, nil, pyDictFile, guard.LangGo); s != nil {
		t.Fatalf("non-Python languages must never seed pyBraceDepth, got %v", s)
	}
}

// MultiEdit seeds must land on the synthetic LineNo that AddedLinesWithGap
// actually assigns to each block's first line — mirrors
// TestHookSeedByLine_MultiEditBlockAlignment for the brace-depth seed.
func TestHookBraceDepthByLine_MultiEditBlockAlignment(t *testing.T) {
	edits := []editOp{
		// Block 1: two lines of real code, outside any brace.
		{OldString: "result = {", NewString: "result = {\nimport os"},
		// Block 2: one line, inside the dict.
		{OldString: `    "b": 2,`, NewString: "    MAX_VALUE: 5,"},
	}
	seeds := hookBraceDepthByLine("MultiEdit", "", edits, pyDictFile, guard.LangPython)

	lines := hookAddedLines("MultiEdit", "", "", edits)
	start2 := lines[len(lines)-1].LineNo
	if seeds[start2] != 1 {
		t.Fatalf("block 2 (LineNo %d) should carry depth 1, got %d", start2, seeds[start2])
	}
	if seeds[1] != 0 {
		t.Fatalf("block 1 starts outside any brace, got %d", seeds[1])
	}
}

// TestHookSeedMapsProjectEachField pins that hookSeedMaps routes each
// SeedState field into its own map, at its own block's synthetic start line.
// The three blocks are chosen so every field differs from every other within
// a block: swapping any two projections, or crossing blocks, changes a value.
func TestHookSeedMapsProjectEachField(t *testing.T) {
	file := guard.TextToAddedLines(`x = foo(
    {
        "k": 1,
    },
)

def f(a,
      b=[
          1,
      ],
):
    pass

"""doc
inside line
"""
`)
	edits := []editOp{
		{OldString: `        "k": 1,`, NewString: `        "k": 2,`}, // block A, synthetic line 1
		{OldString: `          1,`, NewString: `          2,`},       // block B, line 3
		{OldString: `inside line`, NewString: `inside text`},         // block C, line 5
	}
	indices := hookBlockIndices("MultiEdit", "", edits, file)
	if len(indices) != 3 {
		t.Fatalf("resolved %d blocks, want 3: %v", len(indices), indices)
	}
	s := hookSeedMaps(indices, file, guard.LangPython)
	for _, c := range []struct {
		name string
		got  map[int]int
		want map[int]int
	}{
		{"brace", s.brace, map[int]int{1: 1}},
		{"bracket", s.bracket, map[int]int{1: 2, 3: 2}},
		{"defSig", s.defSig, map[int]int{3: 1}},
		{"paramSig", s.paramSig, map[int]int{3: 2}},
	} {
		if len(c.got) != len(c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
			continue
		}
		for k, v := range c.want {
			if c.got[k] != v {
				t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
				break
			}
		}
	}
	if len(s.open) != 1 || s.open[5] != `"""` {
		t.Errorf("open = %v, want only block C inside the docstring", s.open)
	}

	var fd guard.FileDiff
	s.applyTo(&fd)
	if fd.PyBraceDepthByLine[1] != 1 || fd.PyBracketDepthByLine[3] != 2 || fd.PyDefSigDepthByLine[3] != 1 ||
		fd.PyParamSigDepthByLine[3] != 2 || fd.SeedByLine[5] != `"""` {
		t.Errorf("applyTo misrouted a map: %+v", fd)
	}

	// Outside Python only the open-string seed exists.
	g := hookSeedMaps(indices, file, guard.LangGo)
	if g.brace != nil || g.bracket != nil || g.defSig != nil || g.paramSig != nil {
		t.Errorf("non-Python depth maps must be nil, got %+v", g)
	}
}

// TestHookSeedMapsGoBlock pins the Go block-state seed (#514): every located
// block gets an entry, the zero state included (a missing entry means
// "unknown", not "outside a block"), and applyTo routes it to the FileDiff.
func TestHookSeedMapsGoBlock(t *testing.T) {
	file := guard.TextToAddedLines("package p\n\nvar (\n\ta = 1\n\tb int\n)\n\nfunc f() {\n\tg()\n}\n")
	edits := []editOp{
		{OldString: "\tb int", NewString: "\tb int64"}, // inside the block, synthetic line 1
		{OldString: "\tg()", NewString: "\tg(1)"},      // outside it, line 3
		{OldString: "\tmissing()", NewString: "\tx()"}, // not located, line 5
	}
	s := hookSeedMaps(hookBlockIndices("MultiEdit", "", edits, file), file, guard.LangGo)
	if len(s.goBlock) != 2 {
		t.Fatalf("goBlock = %v, want entries for the two located blocks only", s.goBlock)
	}
	if b, ok := s.goBlock[3]; !ok || b != (guard.GoBlockState{}) {
		t.Errorf("block outside any var block: %+v (present %v), want a present zero state", b, ok)
	}
	if s.goBlock[1] == (guard.GoBlockState{}) {
		t.Errorf("block inside `var (` carries the zero state")
	}
	var fd guard.FileDiff
	s.applyTo(&fd)
	if len(fd.GoBlockByLine) != 2 {
		t.Errorf("applyTo did not route goBlock: %v", fd.GoBlockByLine)
	}
	if py := hookSeedMaps(hookBlockIndices("MultiEdit", "", edits, file), file, guard.LangPython); py.goBlock != nil {
		t.Errorf("non-Go goBlock map must be nil, got %v", py.goBlock)
	}
}

// TestHookUnlocatedBlockAfterSeededBlock pins round-2 finding R2-1 on #514: a
// MultiEdit block the hook cannot locate (a fragment old_string) must not
// inherit the in-block state seeded for the located block before it. Read as
// still inside `var (`, `halluc = other()` bound halluc and masked `halluc()`.
func TestHookUnlocatedBlockAfterSeededBlock(t *testing.T) {
	src := "package p\n\nvar (\n\ta int\n\tb int\n)\n\nfunc f() {\n\tfoo()\n}\n"
	file := guard.TextToAddedLines(src)
	edits := []editOp{
		{OldString: "\tb int", NewString: "\tb int64"},                           // located, inside the block
		{OldString: "foo()", NewString: "foo()\n\thalluc = other()\n\thalluc()"}, // fragment: not located
	}
	fd := guard.FileDiff{Path: "a.go", AddedLines: hookAddedLines("MultiEdit", "", "", edits)}
	hookSeedMaps(hookBlockIndices("MultiEdit", "", edits, file), file, guard.LangGo).applyTo(&fd)
	symbols := map[string]struct{}{"other": {}, "foo": {}}
	guard.FoldInFileDefs(symbols, file, guard.LangGo)
	flagged := false
	for _, v := range guard.Run(symbols, "", []guard.FileDiff{fd}) {
		flagged = flagged || v.Symbol == "halluc"
	}
	if !flagged {
		t.Errorf("halluc() after an unlocated block was masked: the block inherited the seeded in-block state")
	}
}

// TestHookStructuralEditStalesLaterGoBlockSeeds pins #517: every MultiEdit
// block is seeded from the PRE-EDIT file, so once an earlier edit closes a
// `var (` block, a later block seeded "inside the block" is stale. Read that
// way, `halluc = other()` bound halluc and masked `halluc()`.
func TestHookStructuralEditStalesLaterGoBlockSeeds(t *testing.T) {
	flags := func(src string, edits []editOp) bool {
		file := guard.TextToAddedLines(src)
		fd := guard.FileDiff{Path: "a.go", AddedLines: hookAddedLines("MultiEdit", "", "", edits)}
		idx := hookBlockIndices("MultiEdit", "", edits, file)
		hookSeedMaps(idx, file, guard.LangGo).withoutStaleGoBlocks("MultiEdit", edits, idx, file, guard.LangGo).applyTo(&fd)
		symbols := map[string]struct{}{"other": {}}
		guard.FoldInFileDefs(symbols, file, guard.LangGo)
		for _, v := range guard.Run(symbols, "", []guard.FileDiff{fd}) {
			if v.Symbol == "halluc" {
				return true
			}
		}
		return false
	}
	src := "package p\n\nvar (\n\ta = 1\n\tb = 2\n\tc = 3\n)\n"
	for _, c := range []struct {
		name  string
		edits []editOp
	}{
		{"located edit closes the block", []editOp{
			{OldString: "\ta = 1\n\tb = 2", NewString: "\ta = 1\n)\nfunc g() {\n\tb = 2"},
			{OldString: "\tc = 3\n)", NewString: "\thalluc = other()\n\thalluc()\n}"},
		}},
		{"unlocated fragment edit closes the block", []editOp{
			{OldString: "b = 2", NewString: "b = 2\n)\nfunc g() {"},
			{OldString: "\tc = 3\n)", NewString: "\thalluc = other()\n\thalluc()\n}"},
		}},
		{"a deletion (empty new_string) removes the opener", []editOp{
			{OldString: "var (", NewString: ""},
			{OldString: "\tc = 3", NewString: "\thalluc = other()\n\thalluc()"},
		}},
	} {
		if !flags(src, c.edits) {
			t.Errorf("%s: halluc() masked by a stale in-block seed", c.name)
		}
	}
	// Control: edits that change no block structure keep their seeds, so an
	// entry edited in place still binds.
	edits := []editOp{
		{OldString: "\ta = 1", NewString: "\ta = 10"},
		{OldString: "\tc = 3", NewString: "\tc = 3\n\td = 4"},
	}
	file := guard.TextToAddedLines(src)
	idx := hookBlockIndices("MultiEdit", "", edits, file)
	s := hookSeedMaps(idx, file, guard.LangGo).withoutStaleGoBlocks("MultiEdit", edits, idx, file, guard.LangGo)
	if len(s.goBlock) != 2 {
		t.Errorf("non-structural edits lost their seeds: %v", s.goBlock)
	}
}

// TestHookStructuralEditReachesHook pins #517's wiring in the real hook
// (verify.go): the same structural MultiEdit must ask about halluc.
func TestHookStructuralEditReachesHook(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	enrolledStore(t, repo, []string{"other"})
	path := filepath.Join(repo, "a.go")
	if err := os.WriteFile(path, []byte("package p\n\nvar (\n\ta = 1\n\tb = 2\n\tc = 3\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, raw, d := runHook(t, payloadOld(t, "MultiEdit", path, "", "", "", []editOp{
		{OldString: "\ta = 1\n\tb = 2", NewString: "\ta = 1\n)\nfunc g() {\n\tb = 2"},
		{OldString: "\tc = 3\n)", NewString: "\thalluc = other()\n\thalluc()\n}"},
	}))
	if d.Hook.PermissionDec != "ask" || !strings.Contains(d.Hook.PermissionReason, "halluc") {
		t.Fatalf("a stale in-block seed masked halluc(), got %q:\n%s", d.Hook.PermissionDec, raw)
	}
}

// TestHookSeedsReachGuardRun pins the hook path's seed wiring end to end
// (verify.go's hookSeedMaps(...).applyTo): the unit tests above check what
// hookSeedMaps computes, but without this nothing failed when its result never
// reached guard.Run. An Edit inside a module docstring that mentions a
// call-shaped phrase must defer, and the same phrase edited into code must
// still ask; that pair rules out a blanket "defer every Python edit".
func TestHookSeedsReachGuardRun(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	enrolledStore(t, repo, []string{"KnownFunc"})
	py := filepath.Join(repo, "mod.py")
	src := "\"\"\"Module doc.\n\nLines of prose here.\n\"\"\"\n\n\ndef go():\n    return 1\n"
	if err := os.WriteFile(py, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	_, raw, d := runHook(t, payloadOld(t, "Edit", py, "Lines of prose here.",
		"Lines of prose here, see compute_widget(x) for detail.", "", nil))
	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("an edit inside a docstring must be masked by its open-string seed, got an ask:\n%s", raw)
	}

	_, raw, d = runHook(t, payloadOld(t, "Edit", py, "    return 1", "    return compute_widget(1)", "", nil))
	if d.Hook.PermissionDec != "ask" || !strings.Contains(d.Hook.PermissionReason, "compute_widget") {
		t.Fatalf("control: the same call in code must ask, got %q:\n%s", d.Hook.PermissionDec, raw)
	}
}
