package main

import (
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
)

// TestFileScopeViolations_FlagGating pins the default-off contract and the File
// stamping. The check is the most false-positive-delicate surface the guard has,
// so "silent unless explicitly enabled" is a behavioural guarantee, not a detail.
func TestFileScopeViolations_FlagGating(t *testing.T) {
	whole := []guard.AddedLine{
		{Text: "import pytest", LineNo: 1},
		{Text: "", LineNo: 2},
		{Text: "def test_it():", LineNo: 3},
		{Text: "    pass", LineNo: 4},
	}
	added := []guard.AddedLine{{Text: "    out = render(digest)", LineNo: 1}}
	// `render` exists in the repo but is not imported or defined in this file.
	repo := map[string]struct{}{"render": {}, "pytest": {}}

	// Flag OFF → no-op regardless of content.
	t.Setenv("RUNECHO_GUARD_FILESCOPE", "")
	if v := fileScopeViolations(guard.LangPython, whole, added, repo, "tests/test_r.py"); v != nil {
		t.Fatalf("flag off must yield no violations, got %+v", v)
	}

	// Flag ON → the out-of-scope reference is flagged and File is stamped.
	t.Setenv("RUNECHO_GUARD_FILESCOPE", "1")
	v := fileScopeViolations(guard.LangPython, whole, added, repo, "tests/test_r.py")
	if len(v) != 1 {
		t.Fatalf("flag on must flag the out-of-scope reference, got %+v", v)
	}
	if v[0].Symbol != "render" || v[0].File != "tests/test_r.py" {
		t.Errorf("unexpected violation %+v", v[0])
	}

	// Non-Python is a no-op even with the flag on (v1 language scope).
	for _, lang := range []guard.Lang{guard.LangGo, guard.LangJS} {
		if v := fileScopeViolations(lang, whole, added, repo, "x.go"); v != nil {
			t.Errorf("lang %v must be a no-op in v1, got %+v", lang, v)
		}
	}
}

// TestSnapshotSymbols_IsolatesLaterFolds is the regression for the learned-allow
// hazard: the hook widens the symbol set in place (in-file defs, learned-allow)
// AFTER the snapshot is taken. If the snapshot aliased the original map, a
// learned-allow name — one the user explicitly taught the guard to accept — could
// pass the firewall and be re-raised as an out-of-scope violation.
func TestSnapshotSymbols_IsolatesLaterFolds(t *testing.T) {
	original := map[string]struct{}{"render": {}}
	snap := snapshotSymbols(original)

	// Simulate the later in-place folds the hook performs.
	original["learned_allowed_name"] = struct{}{}
	original["in_file_def"] = struct{}{}

	if _, leaked := snap["learned_allowed_name"]; leaked {
		t.Error("snapshot aliased the live map — learned-allow names would reach the firewall")
	}
	if _, leaked := snap["in_file_def"]; leaked {
		t.Error("snapshot aliased the live map — later folds leaked into the firewall set")
	}
	if _, ok := snap["render"]; !ok {
		t.Error("snapshot dropped a symbol that was present at snapshot time")
	}
}
