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
