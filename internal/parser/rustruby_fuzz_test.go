package parser

import (
	"reflect"
	"testing"
)

// FuzzRustParser asserts the Rust parser never panics on arbitrary input and
// keeps the sorted/deduplicated invariants the IR relies on.
// Run: go test -run=x -fuzz=FuzzRustParser ./internal/parser
func FuzzRustParser(f *testing.F) {
	seeds := []string{
		"pub fn a() {}\nstruct B;",
		"impl<T> W<T> { pub fn g(&self) -> T { todo!() } }",
		"pub fn borrow<'a>(x: &'a str) -> &'a str { x }",
		"let c = 'a'; /* /* nested */ */",
		"mod a { mod b { pub fn deep() {} } }",
		"macro_rules! m { () => {} }",
		"", "fn", "impl", "pub pub pub", "fn f(\x00) {", "r#\"unterminated",
		"use a::{b, c as d};",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	p := NewRustParser()
	f.Fuzz(func(t *testing.T, src string) {
		fs, err := p.Parse(src) // must never panic
		if err != nil {
			return
		}
		assertParserInvariants(t, fs)
		assertSpanKeysNameRealSymbols(t, fs)
	})
}

// FuzzRubyParser asserts the Ruby parser never panics on arbitrary input and
// keeps the same invariants.
// Run: go test -run=x -fuzz=FuzzRubyParser ./internal/parser
func FuzzRubyParser(f *testing.F) {
	seeds := []string{
		"class A\n  def b; end\nend",
		"module M\n  def self.x; end\nend",
		"class C\n  attr_accessor :n\n  private\n  def h; end\nend",
		"def q; c = ?a; end",
		"def h\n  <<~T\n    def fake; end\n  T\nend",
		"require 'json'\nrequire_relative \"x\"",
		"class << self\n  def m; end\nend",
		"", "def", "class", "end end end", "def f(\x00", "%w[",
		"=begin\ndef c; end\n=end",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	p := NewRubyParser()
	f.Fuzz(func(t *testing.T, src string) {
		fs, err := p.Parse(src) // must never panic
		if err != nil {
			return
		}
		assertParserInvariants(t, fs)
		assertSpanKeysNameRealSymbols(t, fs)
	})
}

// assertSpanKeysNameRealSymbols checks an invariant the shared helper does not:
// every "kind:name" key in SymbolHashes/SymbolLines must correspond to a symbol
// the parser actually listed. symbolsFromStructure looks spans up by that exact
// key, so an orphan key is a span that can never be attached to anything — dead
// weight in the IR at best, and a sign the walk recorded a symbol under a name
// it did not also emit.
func assertSpanKeysNameRealSymbols(t *testing.T, fs FileStructure) {
	t.Helper()
	listed := map[string]bool{}
	for _, n := range fs.Functions {
		listed["function:"+n] = true
	}
	for _, n := range fs.Classes {
		listed["class:"+n] = true
	}
	for _, n := range fs.Exports {
		listed["export:"+n] = true
	}
	for _, m := range []map[string]string{fs.SymbolHashes} {
		for k := range m {
			if !listed[k] {
				t.Fatalf("SymbolHashes key %q names no listed symbol", k)
			}
		}
	}
	for k := range fs.SymbolLines {
		if !listed[k] {
			t.Fatalf("SymbolLines key %q names no listed symbol", k)
		}
	}
}

// Same input must always produce the same output — RunEcho's whole contract is a
// deterministic, model-free receipt, and a map-iteration order leak or a
// time/randomness dependency in a parser would silently break snapshot diffing
// by making an unedited file look modified.
func TestParsersAreDeterministic(t *testing.T) {
	cases := []struct {
		name string
		p    Parser
		src  string
	}{
		{"rust", NewRustParser(), rustSample},
		{"ruby", NewRubyParser(), rubySample},
	}
	for _, c := range cases {
		first, err := c.p.Parse(c.src)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for i := 0; i < 20; i++ {
			again, err := c.p.Parse(c.src)
			if err != nil {
				t.Fatalf("%s run %d: %v", c.name, i, err)
			}
			if !reflect.DeepEqual(first, again) {
				t.Fatalf("%s parse is not deterministic at run %d", c.name, i)
			}
		}
	}
}

// The span maps must agree with the symbol lists on the sample sources too, not
// only under fuzzing — a fuzz corpus can miss the ordinary case.
func TestSpanKeysNameRealSymbols_Samples(t *testing.T) {
	rs, _ := NewRustParser().Parse(rustSample)
	assertSpanKeysNameRealSymbols(t, rs)
	rb, _ := NewRubyParser().Parse(rubySample)
	assertSpanKeysNameRealSymbols(t, rb)
}
