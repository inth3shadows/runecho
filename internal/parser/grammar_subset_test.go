package parser

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every AST parser's grammar must actually be present in the binary install.sh
// ships. The test suite builds with the FULL grammar set, so a parser can pass
// every test and still be inert in production: install.sh builds with
// `grammar_subset` tags, and a grammar whose tag is missing loads as nil, which
// the parsers degrade on silently by design. That is exactly what happened when
// Rust and Ruby shipped — both indexed to nothing in the installed binary while
// every test was green.
//
// This test reads install.sh and asserts each language's subset tag is listed,
// so adding a parser without adding its tag fails here rather than in the field.
func TestInstallShipsEveryGrammarTag(t *testing.T) {
	raw, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Skipf("install.sh not readable from here: %v", err)
	}
	m := regexp.MustCompile(`GRAMMAR_TAGS="([^"]*)"`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("could not find GRAMMAR_TAGS in install.sh")
	}
	tags := " " + string(m[1]) + " "

	// One entry per AST-backed parser. A regex parser (shell) needs no grammar.
	for _, lang := range []string{"python", "javascript", "typescript", "tsx", "rust", "ruby"} {
		want := "grammar_subset_" + lang
		if !strings.Contains(tags, " "+want+" ") {
			t.Errorf("install.sh GRAMMAR_TAGS is missing %q — .%s files would index to nothing "+
				"in the installed binary even though every test passes here", want, lang)
		}
	}
}

// Belt and braces: under the tags install.sh actually uses, the grammars this
// package depends on must load rather than return nil. Run with the ship tags:
//
//	go test -tags "grammar_subset grammar_subset_python grammar_subset_javascript \
//	  grammar_subset_typescript grammar_subset_tsx grammar_subset_rust \
//	  grammar_subset_ruby" ./internal/parser
//
// Under the default (full-grammar) test build this is trivially true; its value
// is that CI or a maintainer can run it with the ship tags and catch a subset
// mismatch the string check above cannot see.
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
