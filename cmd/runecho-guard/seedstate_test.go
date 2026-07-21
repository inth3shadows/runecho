package main

import (
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
