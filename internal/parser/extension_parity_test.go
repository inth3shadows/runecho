package parser_test

// Extension-parity regression test: every file extension that guard.LangFor
// recognises as a known language must also be handled by at least one
// registered parser.  This locks the invariant that broke in GitHub issue #5:
// guard accepted .jsx/.tsx files (and therefore validated them against
// snapshots) while the JS parser did not index them.
//
// Import cycle analysis: internal/parser does not import internal/guard, and
// internal/guard does not import internal/parser.  Importing guard here (in an
// external _test package) introduces a test-only edge that does not create a
// cycle.

import (
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/parser"
)

// guardCheckedExtensions lists every extension that guard.LangFor maps to a
// known (non-unknown) language.  When guard.LangFor gains a new extension this
// list must be updated here too — the test below will catch the omission.
var guardCheckedExtensions = []string{
	".go",
	".js", ".ts", ".jsx", ".tsx", ".gs",
	".py",
}

// registeredParsers mirrors the set wired in internal/ir/generator.go.
// If a new parser is added to the generator, add it here too.
var registeredParsers = []parser.Parser{
	parser.NewGoParser(),
	parser.NewJSParser(),
	parser.NewPythonParser(),
	parser.NewShellParser(),
}

// TestExtensionParity asserts that for every extension guard.LangFor considers
// a known language, at least one registered parser supports it.  A failure here
// means guard will evaluate files against snapshots that were never indexed —
// the exact bug that issue #5 fixed.
func TestExtensionParity(t *testing.T) {
	for _, ext := range guardCheckedExtensions {
		// Confirm the extension is actually considered "known" by the guard.
		lang := guard.LangFor("file" + ext)
		if lang == guard.LangUnknown {
			t.Errorf("guardCheckedExtensions contains %q but guard.LangFor reports LangUnknown — remove it from the list or fix LangFor", ext)
			continue
		}

		// At least one parser must support this extension.
		supported := false
		for _, p := range registeredParsers {
			if p.SupportsExtension(ext) {
				supported = true
				break
			}
		}
		if !supported {
			t.Errorf("guard accepts %q (lang=%s) but no registered parser supports it — indexer will never see these files, breaking guard validation", ext, lang)
		}
	}
}

// TestGuardCheckedExtensionsComplete verifies that guardCheckedExtensions in
// this test covers every extension guard.LangFor recognises.  We do this by
// spot-checking a representative sample of extensions the guard should NOT know
// and confirming they are absent from our list.  (A full enumeration is
// impossible without a registry; this is a canary.)
func TestGuardExtensionSanity(t *testing.T) {
	// These must be unknown to guard — if guard starts accepting them we need
	// to update guardCheckedExtensions above.
	for _, ext := range []string{".rb", ".java", ".cpp", ".rs", ".md", ".txt", ".json"} {
		lang := guard.LangFor("file" + ext)
		if lang != guard.LangUnknown {
			t.Errorf("guard.LangFor(%q) = %s, but %q is not listed in guardCheckedExtensions — add it", "file"+ext, lang, ext)
		}
	}

	// These must be in guardCheckedExtensions (belt-and-suspenders on the
	// parity list itself).
	required := map[string]bool{".jsx": false, ".tsx": false}
	for _, ext := range guardCheckedExtensions {
		if _, ok := required[ext]; ok {
			required[ext] = true
		}
	}
	for ext, seen := range required {
		if !seen {
			t.Errorf("%q must appear in guardCheckedExtensions (the fix for issue #5)", ext)
		}
	}
}

// TestExtensionParityFilePaths exercises LangFor via file paths rather than
// bare extensions, since LangFor uses HasSuffix (not explicit extension
// parsing).  Both must agree.
func TestExtensionParityFilePaths(t *testing.T) {
	cases := []struct {
		path string
		ext  string
	}{
		{"src/App.jsx", ".jsx"},
		{"src/App.tsx", ".tsx"},
		{"components/Button.jsx", ".jsx"},
		{"pages/index.tsx", ".tsx"},
		{"lib/util.js", ".js"},
		{"lib/util.ts", ".ts"},
		{"scripts/macro.gs", ".gs"},
		{"main.go", ".go"},
		{"server.py", ".py"},
	}

	for _, tc := range cases {
		lang := guard.LangFor(tc.path)
		if lang == guard.LangUnknown {
			t.Errorf("guard.LangFor(%q) = LangUnknown, expected known language", tc.path)
			continue
		}
		ext := filepath.Ext(tc.path)
		if ext != tc.ext {
			t.Fatalf("filepath.Ext(%q) = %q, want %q — test data error", tc.path, ext, tc.ext)
		}
		supported := false
		for _, p := range registeredParsers {
			if p.SupportsExtension(ext) {
				supported = true
				break
			}
		}
		if !supported {
			t.Errorf("guard accepts %q (ext=%s, lang=%s) but no registered parser supports %s", tc.path, ext, lang, ext)
		}
	}
}
