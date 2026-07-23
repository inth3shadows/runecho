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

// Edge cases found while reviewing the parser. Each line pins a behavior that a
// plausible alternative implementation would get wrong.
func TestRustParser_Edges(t *testing.T) {
	src := `pub fn outer() { fn inner_nested() {} }
#[cfg(test)]
mod tests { fn t1() {} }
extern "C" { fn c_func(); }
impl Reader { const CAP: u32 = 4; type Out = u8; }
impl<T> Wrap<T> { pub fn get(&self) -> T { todo!() } }
impl std::fmt::Display for Reader { fn fmt(&self) -> u8 { 0 } }
pub(crate) fn crate_vis() {}
mod a { mod b { pub fn deep() {} } }
`
	got, _ := NewRustParser().Parse(src)

	// A fn nested inside another fn's body is not referenceable from outside it;
	// extracting it as a top-level symbol would let the guard resolve a call to
	// it from anywhere — a false negative. Function bodies are not walked.
	if contains(got.Functions, "inner_nested") || contains(got.Functions, "outer.inner_nested") {
		t.Errorf("fn nested in a body must not be extracted; got %q", got.Functions)
	}
	// Generic impl target reduces to its base name.
	if !contains(got.Functions, "Wrap.get") {
		t.Errorf("want Wrap.get from impl<T> Wrap<T>; got %q", got.Functions)
	}
	// A path-qualified trait impl still keys on the bare type name.
	if !contains(got.Functions, "Reader.fmt") {
		t.Errorf("want Reader.fmt from impl std::fmt::Display for Reader; got %q", got.Functions)
	}
	// mod nesting composes.
	if !contains(got.Functions, "a.b.deep") {
		t.Errorf("want a.b.deep; got %q", got.Functions)
	}
	// pub(crate) counts as exported.
	if !contains(got.Exports, "crate_vis") {
		t.Errorf("pub(crate) must be exported; got %q", got.Exports)
	}
	// An extern "C" signature is a real callable.
	if !contains(got.Functions, "c_func") {
		t.Errorf("want c_func from an extern block; got %q", got.Functions)
	}
	// Associated items inside an impl are qualified by the impl type.
	if !contains(got.Classes, "Reader.Out") {
		t.Errorf("want Reader.Out; got %q", got.Classes)
	}
	// const/static land in Exports even without `pub` — Exports is the only
	// bucket for value symbols, so gating it would drop private consts from the
	// IR entirely rather than just from the public surface.
	if !contains(got.Exports, "Reader.CAP") {
		t.Errorf("non-pub const must still be recorded; got %q", got.Exports)
	}
}

// Logic-bug regression: an impl target with no plain-identifier name must NOT
// fall back to the enclosing prefix. Doing so emitted the method under its bare
// name at file scope, where it is indistinguishable from a real top-level fn —
// and because recordHash combines on collision, editing the impl method flipped
// the top-level function's hash, reporting an unedited symbol as modified.
func TestRustParser_UnnameableImplTargetDoesNotCollide(t *testing.T) {
	src := `pub fn helper() -> u8 { 1 }
impl MyTrait for &str { fn helper(&self) -> u8 { 2 } }
impl MyTrait for (A, B) { fn tuple_method(&self) {} }
`
	got, _ := NewRustParser().Parse(src)
	if !contains(got.Functions, "helper") {
		t.Errorf("real top-level fn lost; got %q", got.Functions)
	}
	if !contains(got.Functions, "&str.helper") {
		t.Errorf("impl method must keep a non-colliding prefix; got %q", got.Functions)
	}
	if !contains(got.Functions, "(A, B).tuple_method") {
		t.Errorf("tuple impl target must qualify by its type text; got %q", got.Functions)
	}

	// The decisive check: editing the impl method must not disturb the real
	// top-level function's hash.
	edited := strings.Replace(src, "fn helper(&self) -> u8 { 2 }", "fn helper(&self) -> u8 { 99 }", 1)
	after, _ := NewRustParser().Parse(edited)
	if after.SymbolHashes["function:helper"] != got.SymbolHashes["function:helper"] {
		t.Error("editing an impl method changed an unrelated top-level function's hash")
	}
	if after.SymbolHashes["function:&str.helper"] == got.SymbolHashes["function:&str.helper"] {
		t.Error("editing the impl method did not change its own hash")
	}
}
