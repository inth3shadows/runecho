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
