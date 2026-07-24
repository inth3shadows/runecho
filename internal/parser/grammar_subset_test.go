package parser

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every tree-sitter grammar this package uses must actually be present in EVERY
// binary this project ships. The test suite builds with the FULL grammar set, so
// a parser can pass every test and still be inert in production: the shipping
// builds use `grammar_subset` tags, and a grammar whose tag is missing loads as
// nil, which the parsers degrade on silently by design.
//
// There are TWO shipping channels and they are built by different files:
// `install.sh` (what you get building from source) and `.goreleaser.yaml` (what
// you get downloading a release). Checking only one is not enough — this test
// originally did, and .goreleaser.yaml silently lacked grammar_subset_rust and
// grammar_subset_ruby, so every downloaded release binary indexed Rust and Ruby
// to nothing while the locally built one worked and every test passed. Its own
// header comment claimed the flags "mirror install.sh exactly". Both files are
// parsed here now, and they must agree with each other as well as with the code.
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

	channels := map[string]string{
		"install.sh":       shipTags(t, "install.sh", `GRAMMAR_TAGS="([^"]*)"`),
		".goreleaser.yaml": shipTags(t, ".goreleaser.yaml", `"-tags=([^"]*)"`),
	}

	for channel, tags := range channels {
		for _, lang := range used {
			want := "grammar_subset_" + lang
			if !strings.Contains(" "+tags+" ", " "+want+" ") {
				t.Errorf("%s is missing build tag %q — files for that language would index to "+
					"NOTHING in the binary that channel ships, even though every test here passes",
					channel, want)
			}
		}
	}

	// No THIRD copy. install.sh is canonical and .goreleaser.yaml is a checked
	// mirror (YAML cannot compute one). Anything else that builds a binary must
	// DERIVE the list, not restate it — scripts/agent-eval/audit.sh carried its
	// own stale five-grammar copy, so any eval it ran over Rust or Ruby measured
	// a parser that indexed those languages to nothing and reported it as a
	// result. A test that only compares the two known copies cannot see a third.
	assertDerivesTags(t, filepath.Join("..", "..", "scripts", "agent-eval", "audit.sh"))

	// The two channels must also agree with EACH OTHER. A tag present in one and
	// absent from the other means two builds of the same version behave
	// differently, which is worse than both being wrong: it is unreproducible.
	if a, b := channels["install.sh"], channels[".goreleaser.yaml"]; a != b {
		t.Errorf("shipping channels disagree.\n  install.sh:       %s\n  .goreleaser.yaml: %s", a, b)
	}
}

// shipTags extracts and normalises the build-tag list a shipping channel uses.
// Normalising (fields joined by single spaces, sorted) is what lets the two
// channels be compared for equality rather than merely for coverage — a
// difference in ORDER is not a bug, a difference in CONTENT is.
func shipTags(t *testing.T, name, pattern string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("cannot read %s (%v) — this check must not be skipped quietly, "+
			"it is the only thing standing between a new parser and shipping inert", name, err)
	}
	// A file may declare the tags more than once (.goreleaser.yaml has one build
	// block per binary). Every occurrence must match, or one binary ships with a
	// grammar the others lack.
	ms := regexp.MustCompile(pattern).FindAllSubmatch(raw, -1)
	if len(ms) == 0 {
		t.Fatalf("could not find a build-tag list in %s (pattern %s)", name, pattern)
	}
	var first string
	for i, m := range ms {
		fields := strings.Fields(string(m[1]))
		sort.Strings(fields)
		got := strings.Join(fields, " ")
		if i == 0 {
			first = got
		} else if got != first {
			t.Errorf("%s declares differing build-tag lists across its build blocks:\n  %s\n  %s",
				name, first, got)
		}
	}
	return first
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
	// JS/TS/TSX were absent from this check, so three of the six AST grammars
	// could have gone nil under a runtime upgrade without any test noticing —
	// and unlike Rust/Ruby, JS degrades to a REGEX path rather than to nothing,
	// which still returns names and so looks like it works.
	for _, ext := range []string{".js", ".ts", ".tsx"} {
		if jsLanguageFor(ext) == nil {
			t.Errorf("%s grammar is nil under these build tags — those files fall back to "+
				"the regex path (names only, no spans) while still appearing to parse", ext)
		}
	}
}

// assertDerivesTags fails if a build script restates the grammar tag list
// literally instead of reading it from install.sh.
func assertDerivesTags(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v — this check must not skip quietly", path, err)
	}
	if regexp.MustCompile(`GRAMMAR_TAGS="grammar_subset`).Match(raw) {
		t.Errorf("%s hardcodes its own grammar tag list; derive it from install.sh instead "+
			"(a third copy is a third thing to forget when a parser is added)", path)
	}
}
