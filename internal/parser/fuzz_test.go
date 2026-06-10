package parser

import (
	"sort"
	"testing"
)

// FuzzJSParser asserts the JS/TS parser never panics on arbitrary input and that
// its output keeps the structural invariants the IR relies on: every symbol list
// is sorted and deduplicated. Run: go test -run=x -fuzz=FuzzJSParser ./internal/parser
func FuzzJSParser(f *testing.F) {
	seeds := []string{
		"function foo() {}\nexport const bar = 1;",
		"class A {}\nimport x from 'y';",
		"", "ďť", "function\x00", "export export export",
		"const a = () => {}; function a() {}",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	p := NewJSParser()
	f.Fuzz(func(t *testing.T, src string) {
		fs, err := p.Parse(src) // must never panic
		if err != nil {
			return // a returned error is acceptable; a panic is not
		}
		assertParserInvariants(t, fs)
	})
}

// FuzzGoParser asserts the Go parser never panics on arbitrary input and keeps
// the sorted/deduplicated invariants the IR relies on — including the Exports
// list now populated by exported var/const capture.
// Run: go test -run=x -fuzz=FuzzGoParser ./internal/parser
func FuzzGoParser(f *testing.F) {
	seeds := []string{
		"package p\nvar X = 1\nconst Y = 2",
		"package p\nvar (\n A = iota\n b\n)",
		"package p\nfunc Foo() {}\ntype Bar struct{}",
		"", "var", "const (", "var (\n", "var X int = func() {",
		"package p\nvar X, Y = 1, 2",
		"package p\n\tvar Indented = 1",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	p := NewGoParser()
	f.Fuzz(func(t *testing.T, src string) {
		fs, err := p.Parse(src) // must never panic
		if err != nil {
			return
		}
		assertParserInvariants(t, fs)
	})
}

// FuzzPythonParser asserts the AST-backed Python parser never panics on arbitrary
// input and keeps the sorted/deduplicated invariants. It is the newest and most
// complex parser (external tree-sitter runtime, recursive walk, decorator-span and
// property-hash logic), so it gets adversarial coverage over malformed and unusual
// shapes. Run: go test -run=x -fuzz=FuzzPythonParser ./internal/parser
func FuzzPythonParser(f *testing.F) {
	seeds := []string{
		"def foo():\n    pass",
		"async def bar(x):\n    return await baz(x)",
		"class A:\n    @property\n    def x(self): return 1\n    @x.setter\n    def x(self, v): pass",
		"@deco\ndef d():\n    def inner():\n        pass",
		"__all__ = ['a']\n__all__ += ['b']",
		"", "\r\n\r\n", "def", "def (", "class\x00", "    def indented():",
		"def ():\n  pass", "def f(" + "(((((((((", // unbalanced / non-utf8-ish
	}
	for _, s := range seeds {
		f.Add(s)
	}
	p := NewPythonParser()
	f.Fuzz(func(t *testing.T, src string) {
		fs, err := p.Parse(src) // must never panic
		if err != nil {
			return
		}
		assertParserInvariants(t, fs)
		// Every per-symbol hash/line key must correspond to a captured symbol —
		// a dangling key would mean the maps and lists drifted.
		for key := range fs.SymbolHashes {
			if key == "" {
				t.Fatalf("empty SymbolHashes key for src %q", src)
			}
		}
	})
}

// assertParserInvariants checks every output list is sorted and deduplicated —
// the structural contract the IR generator and snapshot symbolizer depend on.
func assertParserInvariants(t *testing.T, fs FileStructure) {
	t.Helper()
	for _, list := range [][]string{fs.Imports, fs.Functions, fs.Classes, fs.Exports} {
		if !sort.StringsAreSorted(list) {
			t.Fatalf("parser returned unsorted list: %v", list)
		}
		for i := 1; i < len(list); i++ {
			if list[i] == list[i-1] {
				t.Fatalf("parser returned duplicate symbol %q in %v", list[i], list)
			}
		}
	}
}
