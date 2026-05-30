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
	})
}
