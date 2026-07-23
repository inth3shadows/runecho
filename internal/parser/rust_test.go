package parser

import (
	"reflect"
	"strings"
	"testing"
)

const rustSample = `use std::collections::HashMap;
use foo::{bar, baz as qux};

pub struct Reader { n: usize }

pub enum Kind { A, B }

pub trait Fetch {
    fn get(&self) -> u32;
}

impl Reader {
    pub fn fetch(&self) -> u32 { 1 }
    fn hidden(&self) -> u32 { 2 }
}

impl Fetch for Reader {
    fn get(&self) -> u32 { 3 }
}

pub fn top<'a>(x: &'a str) -> &'a str { x }

pub const MAX: usize = 10;
pub static NAME: &str = "x";
pub type Alias = HashMap<String, u32>;

mod inner {
    pub fn nested() {}
    pub struct Deep;
}
`

func TestRustParser_Extension(t *testing.T) {
	p := NewRustParser()
	if !p.SupportsExtension(".rs") {
		t.Error("want .rs supported")
	}
	if p.SupportsExtension(".go") || p.SupportsExtension(".py") {
		t.Error("must not claim other extensions")
	}
}

func TestRustParser_Symbols(t *testing.T) {
	got, err := NewRustParser().Parse(rustSample)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	wantFns := []string{"Fetch.get", "Reader.fetch", "Reader.get", "Reader.hidden", "inner.nested", "top"}
	if !reflect.DeepEqual(got.Functions, wantFns) {
		t.Errorf("Functions:\n got %q\nwant %q", got.Functions, wantFns)
	}

	wantClasses := []string{"Alias", "Fetch", "Kind", "Reader", "inner.Deep"}
	if !reflect.DeepEqual(got.Classes, wantClasses) {
		t.Errorf("Classes:\n got %q\nwant %q", got.Classes, wantClasses)
	}

	wantImports := []string{"foo::{bar, baz as qux}", "std::collections::HashMap"}
	if !reflect.DeepEqual(got.Imports, wantImports) {
		t.Errorf("Imports:\n got %q\nwant %q", got.Imports, wantImports)
	}
}

// A trait impl's methods must key on the implementing TYPE, not the trait —
// `impl Fetch for Reader { fn get }` is called as Reader::get.
func TestRustParser_TraitImplQualifiesByType(t *testing.T) {
	got, _ := NewRustParser().Parse(rustSample)
	if !contains(got.Functions, "Reader.get") {
		t.Errorf("want Reader.get from `impl Fetch for Reader`, got %q", got.Functions)
	}
	if !contains(got.Functions, "Fetch.get") {
		t.Errorf("want Fetch.get from the trait's own signature, got %q", got.Functions)
	}
}

// Non-pub items must still be extracted: unlike Go, a same-crate reference to a
// private fn is ordinary and has to resolve.
func TestRustParser_PrivateItemsExtracted(t *testing.T) {
	got, _ := NewRustParser().Parse(rustSample)
	if !contains(got.Functions, "Reader.hidden") {
		t.Errorf("private fn must be extracted, got %q", got.Functions)
	}
	if contains(got.Exports, "Reader.hidden") {
		t.Errorf("private fn must NOT be exported, got %q", got.Exports)
	}
	if !contains(got.Exports, "Reader.fetch") {
		t.Errorf("pub fn must be exported, got %q", got.Exports)
	}
}

// The reason this parser uses a grammar rather than a masker: `'a` is a lifetime
// here, not an unterminated char literal. A masker would blank the rest of the
// file and lose every symbol after it.
func TestRustParser_LifetimeIsNotACharLiteral(t *testing.T) {
	src := `pub fn borrow<'a>(x: &'a str) -> &'a str { x }
pub fn after() -> u8 { b'x'; 1 }
pub fn last() {}
`
	got, _ := NewRustParser().Parse(src)
	for _, want := range []string{"borrow", "after", "last"} {
		if !contains(got.Functions, want) {
			t.Errorf("lifetime/char handling lost %q; got %q", want, got.Functions)
		}
	}
}

// Rust block comments nest; a naive scanner terminates at the first `*/`.
func TestRustParser_NestedBlockComment(t *testing.T) {
	src := `/* outer /* inner */ still comment */
pub fn visible() {}
`
	got, _ := NewRustParser().Parse(src)
	if !contains(got.Functions, "visible") {
		t.Errorf("nested block comment swallowed the fn; got %q", got.Functions)
	}
}

// A fn declared inside a string or comment must not become a symbol.
func TestRustParser_NoSymbolsFromLiterals(t *testing.T) {
	src := `pub fn real() { let s = "pub fn fake() {}"; let r = r#"fn alsofake()"#; }
// fn commented() {}
`
	got, _ := NewRustParser().Parse(src)
	for _, bad := range []string{"fake", "alsofake", "commented"} {
		if contains(got.Functions, bad) {
			t.Errorf("extracted %q from a literal/comment; got %q", bad, got.Functions)
		}
	}
	if !contains(got.Functions, "real") {
		t.Errorf("want real; got %q", got.Functions)
	}
}

func TestRustParser_HashesAndLines(t *testing.T) {
	got, _ := NewRustParser().Parse(rustSample)
	if got.SymbolLines["function:top"] != 21 {
		t.Errorf("top start line = %d, want 21", got.SymbolLines["function:top"])
	}
	if got.SymbolHashes["function:Reader.fetch"] == "" {
		t.Error("want a body hash for Reader.fetch")
	}
	// A body edit must flip the hash; an unrelated edit must not.
	edited := strings.Replace(rustSample, "pub fn fetch(&self) -> u32 { 1 }", "pub fn fetch(&self) -> u32 { 42 }", 1)
	after, _ := NewRustParser().Parse(edited)
	if after.SymbolHashes["function:Reader.fetch"] == got.SymbolHashes["function:Reader.fetch"] {
		t.Error("body edit did not change the symbol hash")
	}
	if after.SymbolHashes["function:top"] != got.SymbolHashes["function:top"] {
		t.Error("unrelated symbol's hash changed")
	}
}

// CRLF checkouts must index identically to LF ones.
func TestRustParser_CRLFParity(t *testing.T) {
	lf, _ := NewRustParser().Parse(rustSample)
	crlf, _ := NewRustParser().Parse(strings.ReplaceAll(rustSample, "\n", "\r\n"))
	if !reflect.DeepEqual(lf, crlf) {
		t.Error("CRLF source parsed differently from LF")
	}
}

// Empty and malformed input must degrade, never panic, and must yield [] not nil.
func TestRustParser_Degrades(t *testing.T) {
	for _, src := range []string{"", "pub fn broken( {{{ ", "\x00\xff not rust"} {
		got, err := NewRustParser().Parse(src)
		if err != nil {
			t.Errorf("Parse(%q) errored: %v", src, err)
		}
		if got.Functions == nil || got.Imports == nil || got.Classes == nil || got.Exports == nil {
			t.Errorf("Parse(%q) returned a nil slice; want empty", src)
		}
	}
}

func TestRustTypeName(t *testing.T) {
	cases := map[string]string{
		"Reader":              "Reader",
		"Set<T>":              "Set",
		"crate::mod::Reader":  "Reader",
		"HashMap<String, u8>": "HashMap",
		"&str":                "",
		"(A, B)":              "",
		"":                    "",
	}
	for in, want := range cases {
		if got := rustTypeName(in); got != want {
			t.Errorf("rustTypeName(%q) = %q, want %q", in, got, want)
		}
	}
}
