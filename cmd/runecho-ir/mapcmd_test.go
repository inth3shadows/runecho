package main

import (
	"testing"

	"github.com/inth3shadows/runecho/internal/ir"
)

func mapTestIR() *ir.IR {
	return &ir.IR{
		Version:  ir.IRVersion,
		RootHash: "r",
		Files: map[string]ir.FileIR{
			"src/reads.py": {
				Hash: "h1",
				Symbols: []ir.Symbol{
					{Name: "_helper", Kind: "function", Line: 33, Hash: "620a0001"},
					{Name: "get_scope", Kind: "function", Line: 7, Hash: "dfc7abcd"},
					{Name: "search", Kind: "function", Line: 20, Hash: "3359beef"},
					{Name: "Reader", Kind: "class", Line: 4},
					{Name: "get_scope", Kind: "export"},
				},
			},
			"src/writes.py": {
				Hash: "h2",
				Symbols: []ir.Symbol{
					{Name: "put_node", Kind: "function", Line: 12},
				},
			},
		},
	}
}

func TestNormalizeKind(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"":         {"", true},
		"func":     {"function", true},
		"function": {"function", true},
		"class":    {"class", true},
		"exp":      {"export", true},
		"import":   {"import", true},
		"bogus":    {"", false},
	}
	for in, exp := range cases {
		got, ok := normalizeKind(in)
		if got != exp.want || ok != exp.ok {
			t.Errorf("normalizeKind(%q) = (%q,%v), want (%q,%v)", in, got, ok, exp.want, exp.ok)
		}
	}
}

func TestCollectMapSymbols_DefaultKindsAndSort(t *testing.T) {
	syms := collectMapSymbols(mapTestIR(), "", "", nil)
	// Default kinds are function+class; exports/imports excluded.
	wantOrder := []string{"Reader", "_helper", "get_scope", "put_node", "search"}
	if len(syms) != len(wantOrder) {
		t.Fatalf("got %d symbols, want %d: %+v", len(syms), len(wantOrder), syms)
	}
	for i, w := range wantOrder {
		if syms[i].Name != w {
			t.Errorf("sort[%d] = %q, want %q", i, syms[i].Name, w)
		}
	}
	// Line + short hash threaded through.
	for _, s := range syms {
		if s.Name == "get_scope" && s.Kind == "function" {
			if s.Line != 7 {
				t.Errorf("get_scope line = %d, want 7", s.Line)
			}
			if s.Hash != "dfc7" {
				t.Errorf("get_scope hash = %q, want dfc7 (4-char)", s.Hash)
			}
		}
		if s.Name == "Reader" && s.Hash != "" {
			t.Errorf("class Reader should have no body hash, got %q", s.Hash)
		}
	}
}

func TestCollectMapSymbols_KindAndDirFilters(t *testing.T) {
	// --kind=export surfaces exports (no line → 0).
	exp := collectMapSymbols(mapTestIR(), "export", "", nil)
	if len(exp) != 1 || exp[0].Name != "get_scope" || exp[0].Kind != "export" || exp[0].Line != 0 {
		t.Errorf("export filter = %+v, want one get_scope export with line 0", exp)
	}
	// --dir scopes to a subtree.
	dir := collectMapSymbols(mapTestIR(), "", "src/writes", nil)
	if len(dir) != 1 || dir[0].Name != "put_node" {
		t.Errorf("dir filter = %+v, want only put_node", dir)
	}
}

func TestCollectMapSymbols_ChangedFilter(t *testing.T) {
	changed := map[string]map[string]bool{
		"src/reads.py": {"function:get_scope": true},
	}
	syms := collectMapSymbols(mapTestIR(), "", "", changed)
	if len(syms) != 1 || syms[0].Name != "get_scope" {
		t.Errorf("changed filter = %+v, want only get_scope", syms)
	}
}

func TestShortSymAndLineStr(t *testing.T) {
	if shortSym("abcdef") != "abcd" {
		t.Error("shortSym should truncate to 4")
	}
	if shortSym("ab") != "ab" {
		t.Error("shortSym should pass through short input")
	}
	if shortSym("") != "" {
		t.Error("shortSym empty should stay empty")
	}
	if lineStr(0) != "?" || lineStr(-1) != "?" {
		t.Error("lineStr(<=0) should be ?")
	}
	if lineStr(42) != "42" {
		t.Error("lineStr(42) should be 42")
	}
}
