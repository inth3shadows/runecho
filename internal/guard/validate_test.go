package guard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRun_CleanDiff(t *testing.T) {
	symbols := map[string]struct{}{"ProcessFoo": {}, "Helper": {}}
	diffs := []FileDiff{{
		Path:       "main.go",
		AddedLines: lines(`result := ProcessFoo(ctx)`, `Helper()`),
	}}
	violations := Run(symbols, "", diffs)
	if len(violations) != 0 {
		t.Errorf("expected no violations, got %v", violations)
	}
}

// TestRun_ImportedNameNotFlagged pins the #76/#80 fix: a bare call to a name
// bound by an import in the same diff (Python `from X import Y`, JS `import {Y}`)
// must resolve, not read as a hallucination. A genuinely-unknown bare call on the
// same line still flags — the import fold must not blanket-suppress.
func TestRun_ImportedNameNotFlagged(t *testing.T) {
	symbols := map[string]struct{}{} // empty IR — resolution rides on the diff's import
	diffs := []FileDiff{
		{
			Path:       "scripts/render.py",
			AddedLines: lines(`from pathlib import Path`, `p = Path(args.output)`),
		},
		{
			Path:       "src/m.ts",
			AddedLines: lines(`import { Widget } from './lib'`, `const w = Widget(cfg)`),
		},
	}
	if v := Run(symbols, "", diffs); len(v) != 0 {
		t.Fatalf("imported names must resolve, got violations: %+v", v)
	}

	// Negative: an unimported, undefined bare call still flags.
	bad := []FileDiff{{
		Path:       "scripts/render.py",
		AddedLines: lines(`from pathlib import Path`, `q = NotImported(x)`),
	}}
	v := Run(symbols, "", bad)
	if len(v) != 1 || v[0].Symbol != "NotImported" {
		t.Fatalf("want 1 violation for NotImported, got %+v", v)
	}
}

// TestRun_HunkInsideDocstringNotFlagged pins #145: a hunk that adds prose INSIDE a
// pre-existing module docstring (the opening `"""` is on an unchanged line above the
// hunk, so absent from the added lines) must be masked, not scanned as code. With
// AbsPath set, ExtractRefs seeds the string-open state from the file above the hunk,
// so prose like `headers (lowercase)` no longer reads as an unresolved `headers(` call.
func TestRun_HunkInsideDocstringNotFlagged(t *testing.T) {
	dir := t.TempDir()
	// Docstring opens on line 1 and stays open through the added prose (lines 3-4).
	content := "\"\"\"Shared HTTP-proxy plumbing.\n" +
		"\n" +
		"The redactor sees the (projected) response body string.\n" +
		"Names exact response headers (lowercase) to surface upstream.\n" +
		"\"\"\"\n" +
		"import os\n"
	path := filepath.Join(dir, "runner.py")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	diffs := []FileDiff{{
		Path:    "runner.py",
		AbsPath: path,
		AddedLines: []AddedLine{
			{LineNo: 3, Text: "The redactor sees the (projected) response body string."},
			{LineNo: 4, Text: "Names exact response headers (lowercase) to surface upstream."},
		},
	}}
	if v := Run(map[string]struct{}{}, "", diffs); len(v) != 0 {
		t.Fatalf("docstring prose must be masked via the AbsPath seed, got violations: %+v", v)
	}

	// Control: without the seed the same hunk starts in the outside-string state, so
	// the prose IS scanned as code and flags — the exact false-positive #145 fixes.
	diffs[0].AbsPath = ""
	if v := Run(map[string]struct{}{}, "", diffs); len(v) == 0 {
		t.Fatal("expected the unseeded hunk to still flag docstring prose (seed control)")
	}
}

// TestRun_DictKeyEditedInExistingLiteralIsChecked pins the code-review finding
// on PR #290: the #289 fix's own tests only covered a dict literal wholly
// contained in the diff. The dominant real-world edit shape is adding/editing ONE
// key inside an EXISTING multi-line dict — the opening `{` is unchanged context
// above the hunk and never appears in AddedLines. Without AbsPath seeding
// pyBraceDepth (the counterpart to the #145 open-string seed), that hunk starts
// scanning at depth 0 and the key misreads as a top-level definition, never
// checked as a reference.
func TestRun_DictKeyEditedInExistingLiteralIsChecked(t *testing.T) {
	dir := t.TempDir()
	// Dict opens on line 1 and stays open through the edited key (line 3).
	content := "result = {\n" +
		"    \"a\": 1,\n" +
		"    MAX_VALUE: 5,\n" +
		"}\n"
	path := filepath.Join(dir, "runner.py")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	diffs := []FileDiff{{
		Path:    "runner.py",
		AbsPath: path,
		AddedLines: []AddedLine{
			{LineNo: 3, Text: "    MAX_VALUE: 5,"},
		},
	}}
	v := Run(map[string]struct{}{}, "", diffs)
	if len(v) != 1 || v[0].Symbol != "MAX_VALUE" {
		t.Fatalf("MAX_VALUE must be checked as a reference (and flagged, unresolved) via the AbsPath seed, got %+v", v)
	}

	// Control: without the seed, the same hunk starts scanning at depth 0, so
	// MAX_VALUE misreads as a definition and is never checked — the exact false
	// negative this seeding closes.
	diffs[0].AbsPath = ""
	if v := Run(map[string]struct{}{}, "", diffs); len(v) != 0 {
		t.Fatalf("unseeded control should NOT flag MAX_VALUE (misread as a definition); got %+v — test no longer covers seeding", v)
	}
}

// TestRun_DocstringSeedThreadedIntoPass1 pins a round-3 code-review finding on
// PR #290: Pass 1 (extractDefsSeeded) got braceDepthSeed but not openSeed, so its
// own local string-open tracker started unseeded at each hunk while Pass 2's
// (extractRefs) started correctly seeded from context above. On a hunk that
// begins INSIDE a pre-existing docstring, that desync meant Pass 1 read the
// docstring's own closing `"""` as OPENING a new string, and a `{` in the prose
// above it (never really code) as a genuine dict-open — driving Pass 1's brace
// count to 1 by the time the hunk reaches TIMEOUT_S, which reads as a dict key
// and gets dropped from `known`, even though it is really a same-hunk top-level
// definition after the docstring closes. Pass 2 always flags TIMEOUT_S as a
// reference (line 5's `sleep(TIMEOUT_S)` argument doesn't depend on brace depth
// at all), so the desync alone turns a real definition into a false positive.
func TestRun_DocstringSeedThreadedIntoPass1(t *testing.T) {
	added := []AddedLine{
		{LineNo: 1, Text: `    Example JSON: {`},
		{LineNo: 2, Text: `    more prose here`},
		{LineNo: 3, Text: `    """`},
		{LineNo: 4, Text: `    TIMEOUT_S = 30`},
		{LineNo: 5, Text: `    return sleep(TIMEOUT_S)`},
	}
	diffs := []FileDiff{{
		Path:       "m.py",
		AddedLines: added,
		// The hunk starts on line 1, already INSIDE an open docstring.
		SeedByLine: map[int]string{1: `"""`},
	}}
	v := Run(map[string]struct{}{"sleep": {}}, "", diffs)
	if len(v) != 0 {
		t.Fatalf("TIMEOUT_S is a genuine same-hunk definition once the docstring (masked, seeded) closes on line 3; Pass 1 must not desync from Pass 2's masking, got %+v", v)
	}
}

// TestExtractRefs_StrayCloseBraceDoesNotDesyncLaterDict and its ExtractDefs
// counterpart below pin a round-3 code-review finding on PR #290: pyBraceDepth
// was never floor-clamped at 0, unlike PyDeclaredNames' own bracket counter in
// this file (which clamps for the identical reason). A stray unmatched `}`
// earlier in the same added-line run — closing something opened in unseeded/
// unchanged context — drove depth negative, so a REAL dict opened a few lines
// later read as depth 0 and reopened the exact #289 false negative this whole
// seeding mechanism exists to close.
func TestExtractRefs_StrayCloseBraceDoesNotDesyncLaterDict(t *testing.T) {
	lines := []AddedLine{
		{LineNo: 1, Text: `    "b": 2,`},
		{LineNo: 2, Text: "}"},
		{LineNo: 3, Text: "cfg2 = {"},
		{LineNo: 4, Text: "    MAX_VALUE: 5,"},
		{LineNo: 5, Text: "}"},
	}
	refs := ExtractRefs(LangPython, lines)
	for _, r := range refs {
		if r.Name == "MAX_VALUE" {
			return
		}
	}
	t.Errorf("ExtractRefs(%+v) = %+v, want a MAX_VALUE reference — a stray unmatched `}` earlier in the run must not leave depth negative", lines, refs)
}

func TestExtractDefs_StrayCloseBraceDoesNotDesyncLaterDict(t *testing.T) {
	lines := []AddedLine{
		{LineNo: 1, Text: `    "b": 2,`},
		{LineNo: 2, Text: "}"},
		{LineNo: 3, Text: "cfg2 = {"},
		{LineNo: 4, Text: "    MAX_VALUE: 5,"},
		{LineNo: 5, Text: "}"},
	}
	for _, d := range ExtractDefs(LangPython, lines) {
		if d == "MAX_VALUE" {
			t.Fatalf("ExtractDefs(%+v) must NOT read MAX_VALUE as a definition — it is a dict key inside cfg2's open literal, and a stray unmatched `}` earlier in the run must not leave depth negative", lines)
		}
	}
}

func TestRun_HallucinatedCall(t *testing.T) {
	symbols := map[string]struct{}{"ProcessFoo": {}}
	diffs := []FileDiff{{
		Path:       "main.go",
		AddedLines: lines(`result := TotallyFakeSymbol(ctx)`),
	}}
	violations := Run(symbols, "", diffs)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %v", violations)
	}
	if violations[0].Symbol != "TotallyFakeSymbol" {
		t.Errorf("symbol = %q, want TotallyFakeSymbol", violations[0].Symbol)
	}
}

func TestRun_SameCommitDef_NotFlagged(t *testing.T) {
	// Symbol defined in one file, called in another — same diff.
	symbols := map[string]struct{}{} // empty IR
	diffs := []FileDiff{
		{
			Path:       "helper.go",
			AddedLines: lines(`func NewHelper(x int) *Helper {`),
		},
		{
			Path:       "main.go",
			AddedLines: lines(`h := NewHelper(42)`),
		},
	}
	violations := Run(symbols, "", diffs)
	if len(violations) != 0 {
		t.Errorf("same-commit def should not be flagged, got %v", violations)
	}
}

func TestRun_IgnoreFile_SuppressesViolation(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".runechoguardignore")
	if err := os.WriteFile(ignorePath, []byte("# comment\nTotallyFakeSymbol\n"), 0644); err != nil {
		t.Fatal(err)
	}

	symbols := map[string]struct{}{}
	diffs := []FileDiff{{
		Path:       "main.go",
		AddedLines: lines(`TotallyFakeSymbol()`),
	}}
	violations := Run(symbols, ignorePath, diffs)
	if len(violations) != 0 {
		t.Errorf("ignorefile should suppress violation, got %v", violations)
	}
}

func TestRun_IgnoreFile_GlobSuppressesNamespace(t *testing.T) {
	// Bare (unqualified) calls only — a qualified call like React.useState()
	// is already exempt regardless of the ignore file (see
	// TestRun_QualifiedCall_NotFlagged), so the glob path needs its own
	// bare-call fixture to actually exercise matchesIgnoreGlob.
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".runechoguardignore")
	if err := os.WriteFile(ignorePath, []byte("# comment\ntrack*\nLiteralName\n"), 0644); err != nil {
		t.Fatal(err)
	}

	symbols := map[string]struct{}{}
	diffs := []FileDiff{{
		Path: "main.js",
		AddedLines: lines(
			`trackClick()`,
			`trackView()`,
			`LiteralName()`,
			`NotIgnored()`,
		),
	}}
	violations := Run(symbols, ignorePath, diffs)
	if len(violations) != 1 || violations[0].Symbol != "NotIgnored" {
		t.Errorf("want only NotIgnored flagged, got %v", violations)
	}
}

func TestRun_BuiltinCall_NotFlagged(t *testing.T) {
	symbols := map[string]struct{}{}
	diffs := []FileDiff{{
		Path:       "main.go",
		AddedLines: lines(`n := len(items)`, `buf := make([]byte, n)`),
	}}
	violations := Run(symbols, "", diffs)
	if len(violations) != 0 {
		t.Errorf("builtins should not be flagged, got %v", violations)
	}
}

func TestRun_QualifiedCall_NotFlagged(t *testing.T) {
	symbols := map[string]struct{}{}
	diffs := []FileDiff{{
		Path:       "main.go",
		AddedLines: lines(`os.ReadFile(path)`, `fmt.Printf("%v", x)`),
	}}
	violations := Run(symbols, "", diffs)
	if len(violations) != 0 {
		t.Errorf("qualified calls should not be flagged, got %v", violations)
	}
}

func TestRun_Deduplication(t *testing.T) {
	// Same symbol called multiple times in the same file — only one violation.
	symbols := map[string]struct{}{}
	diffs := []FileDiff{{
		Path: "main.go",
		AddedLines: lines(
			`FakeFunc()`,
			`if err := FakeFunc(); err != nil {`,
		),
	}}
	violations := Run(symbols, "", diffs)
	if len(violations) != 1 {
		t.Errorf("expected 1 deduplicated violation, got %d: %v", len(violations), violations)
	}
}

func TestRun_UnknownLang_Skipped(t *testing.T) {
	symbols := map[string]struct{}{}
	diffs := []FileDiff{{
		Path:       "config.json",
		AddedLines: lines(`{"key": "value"}`),
	}}
	violations := Run(symbols, "", diffs)
	if len(violations) != 0 {
		t.Errorf("unknown lang files should be skipped, got %v", violations)
	}
}
