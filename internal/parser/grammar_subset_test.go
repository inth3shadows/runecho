package parser

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every tree-sitter grammar this package uses must actually be present in the
// binary install.sh ships. The test suite builds with the FULL grammar set, so a
// parser can pass every test and still be inert in production: install.sh builds
// with `grammar_subset` tags, and a grammar whose tag is missing loads as nil,
// which the parsers degrade on silently by design.
//
// That is exactly what happened when Rust and Ruby shipped — both indexed to
// nothing in the installed binary while 22 unit tests, two fuzz targets, a
// 25-file Rust crate and a 400-file Ruby corpus were all green.
//
// The language list is DERIVED from this package's own sources rather than
// hand-maintained. A hand-kept list is the same failure mode that caused the
// bug: someone adds a parser and forgets the other place. Scanning for
// `grammars.XxxLanguage` means a new parser extends this check automatically.
func TestInstallShipsEveryGrammarTag(t *testing.T) {
	used := grammarsUsedByThisPackage(t)
	if len(used) == 0 {
		t.Fatal("found no grammars.XxxLanguage references — the scanner is broken, " +
			"which would make this test silently vacuous")
	}
	t.Logf("grammars used by internal/parser: %s", strings.Join(used, " "))

	raw, err := os.ReadFile(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatalf("cannot read install.sh (%v) — this check must not be skipped quietly, "+
			"it is the only thing standing between a new parser and shipping inert", err)
	}
	m := regexp.MustCompile(`GRAMMAR_TAGS="([^"]*)"`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("could not find GRAMMAR_TAGS in install.sh")
	}
	tags := " " + strings.Join(strings.Fields(string(m[1])), " ") + " "

	for _, lang := range used {
		want := "grammar_subset_" + lang
		if !strings.Contains(tags, " "+want+" ") {
			t.Errorf("install.sh GRAMMAR_TAGS is missing %q — files for that language would "+
				"index to NOTHING in the installed binary even though every test here passes", want)
		}
	}
}

// grammarsUsedByThisPackage scans the package's non-test sources for
// `grammars.XxxLanguage` and returns the lowercased language names, which is how
// the vendored runtime names its subset build tags (RustLanguage →
// grammar_subset_rust). It matches the identifier without requiring a call,
// because the JS parser passes these as function VALUES to a cache helper rather
// than calling them directly — a call-shaped regex would have missed all three
// of javascript/typescript/tsx.
func grammarsUsedByThisPackage(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	re := regexp.MustCompile(`grammars\.([A-Z][A-Za-z0-9]*)Language\b`)
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, m := range re.FindAllSubmatch(src, -1) {
			seen[strings.ToLower(string(m[1]))] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Belt and braces: under the tags install.sh actually uses, the grammars this
// package depends on must LOAD rather than return nil. Run with the ship tags:
//
//	go test -tags "grammar_subset grammar_subset_python grammar_subset_javascript \
//	  grammar_subset_typescript grammar_subset_tsx grammar_subset_rust \
//	  grammar_subset_ruby" ./internal/parser
//
// Under the default full-grammar build this is trivially true; its value is that
// running the suite with the ship tags catches a subset mismatch the string
// check above cannot see (a tag that exists but embeds nothing).
func TestASTGrammarsLoad(t *testing.T) {
	if rustLanguage() == nil {
		t.Error("rust grammar is nil under these build tags — .rs files index to nothing")
	}
	if rubyLanguage() == nil {
		t.Error("ruby grammar is nil under these build tags — .rb files index to nothing")
	}
	if pythonLanguage() == nil {
		t.Error("python grammar is nil under these build tags — .py files index to nothing")
	}
}
