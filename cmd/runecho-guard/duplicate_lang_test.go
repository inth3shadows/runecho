package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/ir"
)

// The E5 duplicate check is Go-only: its same-directory rule encodes Go's
// package==directory model. Python and JS/TS files are independent module
// namespaces, so two sibling scripts each defining `main` collide with nothing.
//
// This asserts the language gate short-circuits BEFORE any store access, which
// is what makes the fix total rather than best-effort: dir points at a path with
// no store at all, so a non-Go language reaching the store would surface as a
// queryErr rather than the clean (nil, 0) asserted here.
func TestCheckDuplicateDefs_SkipsNonGoLanguages(t *testing.T) {
	noStore := filepath.Join(t.TempDir(), "definitely-not-a-store")

	for _, tc := range []struct {
		lang guard.Lang
		file string
		sym  string
	}{
		// The dominant real-world false positive: every standalone script in a
		// scripts/ directory defines its own entry point.
		{guard.LangPython, "/repo/scripts/discover.py", "main"},
		{guard.LangJS, "/repo/scripts/export-lithology.mjs", "main"},
		// Per-script local helpers, each file defining its own copy.
		{guard.LangPython, "/repo/scripts/probe.py", "_log_text"},
		{guard.LangJS, "/repo/scripts/check.mjs", "parseArgs"},
		// A TS type re-declared by a sibling script for its own use.
		{guard.LangJS, "/repo/src/lib/brief.ts", "TrackBStratum"},
	} {
		warns, qErrs := checkDuplicateDefs(tc.lang, noStore, tc.file, []string{tc.sym})
		if warns != nil || qErrs != 0 {
			t.Errorf("lang=%v sym=%q: want no warning and no store access, got (%v, %d)",
				tc.lang, tc.sym, warns, qErrs)
		}
	}
}

// End-to-end contrast through the real hook path, on the exact shape that
// produced the live false positives: two sibling scripts in one directory, each
// defining `main`. Python must stay silent — separate modules, no collision.
func TestDuplicate_PythonSiblingMainDefers(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, map[string]ir.FileIR{
		"scripts/discover.py": {Hash: "h1", Symbols: funcsToSymbols([]string{"main"})},
		"scripts/verify.py":   {Hash: "h2", Symbols: funcsToSymbols([]string{"other"})},
	})
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	file := filepath.Join(top, "scripts", "verify.py")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("def other():\n    pass\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "def other():\n    pass",
		"def other():\n    pass\n\ndef main():\n    other()", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("a sibling Python script defining its own main() must not ask:\n%s",
			d.Hook.PermissionReason)
	}
}

// Positive control on the identical layout: Go DOES collide, because the
// directory is the package. Without this, the test above would still pass if the
// duplicate check were broken outright rather than correctly language-scoped.
func TestDuplicate_GoSiblingSameDirStillAsks(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, map[string]ir.FileIR{
		"pkg/existing.go": {Hash: "h1", Symbols: funcsToSymbols([]string{"Helper"})},
		"pkg/new.go":      {Hash: "h2", Symbols: funcsToSymbols([]string{"Placeholder"})},
	})
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	file := filepath.Join(top, "pkg", "new.go")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("package pkg\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "func Placeholder() {}",
		"func Placeholder() {}\nfunc Helper() {}", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("Go: a sibling file in the same package defining Helper must still ask, got %q",
			d.Hook.PermissionDec)
	}
	if !strings.Contains(d.Hook.PermissionReason, "Helper") {
		t.Errorf("reason should name the colliding symbol:\n%s", d.Hook.PermissionReason)
	}
}
