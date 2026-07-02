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
