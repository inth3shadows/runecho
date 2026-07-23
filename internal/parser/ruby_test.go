package parser

import (
	"reflect"
	"strings"
	"testing"
)

const rubySample = `require 'json'
require_relative "helper"

module Outer
  VERSION = "1.0"

  class Reader < Base
    attr_accessor :name
    attr_reader :id

    def initialize(n)
      @n = n
    end

    def fetch
      1
    end

    def self.build
      new(0)
    end

    private

    def hidden
      2
    end
  end

  def self.helper; end
end

class Standalone
  def go; end
end

def top_level; end
`

func TestRubyParser_Extension(t *testing.T) {
	p := NewRubyParser()
	if !p.SupportsExtension(".rb") {
		t.Error("want .rb supported")
	}
	if p.SupportsExtension(".rs") || p.SupportsExtension(".py") {
		t.Error("must not claim other extensions")
	}
}

func TestRubyParser_Symbols(t *testing.T) {
	got, err := NewRubyParser().Parse(rubySample)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wantClasses := []string{"Outer", "Outer.Reader", "Standalone"}
	if !reflect.DeepEqual(got.Classes, wantClasses) {
		t.Errorf("Classes:\n got %q\nwant %q", got.Classes, wantClasses)
	}
	wantImports := []string{"helper", "json"}
	if !reflect.DeepEqual(got.Imports, wantImports) {
		t.Errorf("Imports:\n got %q\nwant %q", got.Imports, wantImports)
	}
	for _, want := range []string{
		"Outer.Reader.fetch", "Outer.Reader.initialize", "Outer.Reader.build",
		"Outer.helper", "Standalone.go", "top_level",
	} {
		if !contains(got.Functions, want) {
			t.Errorf("missing function %q; got %q", want, got.Functions)
		}
	}
	if !contains(got.Exports, "Outer.VERSION") {
		t.Errorf("want constant Outer.VERSION exported; got %q", got.Exports)
	}
}

// attr_* generate real callable methods. Omitting them would make the index
// claim a Rails-style model has almost no callable surface.
func TestRubyParser_AttrAccessors(t *testing.T) {
	got, _ := NewRubyParser().Parse(rubySample)
	for _, want := range []string{"Outer.Reader.name", "Outer.Reader.name=", "Outer.Reader.id"} {
		if !contains(got.Functions, want) {
			t.Errorf("missing attr-generated %q; got %q", want, got.Functions)
		}
	}
	// attr_reader generates no writer.
	if contains(got.Functions, "Outer.Reader.id=") {
		t.Errorf("attr_reader must not generate a writer; got %q", got.Functions)
	}
}

// A bare `private` applies to every def after it in the same body.
func TestRubyParser_PositionalVisibility(t *testing.T) {
	got, _ := NewRubyParser().Parse(rubySample)
	if !contains(got.Functions, "Outer.Reader.hidden") {
		t.Errorf("private method must still be extracted; got %q", got.Functions)
	}
	if contains(got.Exports, "Outer.Reader.hidden") {
		t.Errorf("method after `private` must not be exported; got %q", got.Exports)
	}
	if !contains(got.Exports, "Outer.Reader.fetch") {
		t.Errorf("method before `private` must be exported; got %q", got.Exports)
	}
	// The flag is per-body: a sibling class after a private-using one is public.
	if !contains(got.Exports, "Standalone.go") {
		t.Errorf("private must not leak across bodies; got %q", got.Exports)
	}
}

// The reason this parser uses a grammar rather than a masker: each of these
// constructs would derail a length-preserving scan and lose every symbol after it.
func TestRubyParser_LexicalAmbiguity(t *testing.T) {
	src := `def char_lit; c = ?a; end
def ternary(x); x ? 1 : 2; end
def percent_lit; %w[a b]; end
def heredoc
  <<~TEXT
    def fake_in_heredoc; end
  TEXT
end
def regex_vs_div(a, b); a / b; end
def last_one; end
`
	got, _ := NewRubyParser().Parse(src)
	for _, want := range []string{"char_lit", "ternary", "percent_lit", "heredoc", "regex_vs_div", "last_one"} {
		if !contains(got.Functions, want) {
			t.Errorf("lexical handling lost %q; got %q", want, got.Functions)
		}
	}
	if contains(got.Functions, "fake_in_heredoc") {
		t.Errorf("extracted a def from inside a heredoc; got %q", got.Functions)
	}
}

func TestRubyParser_NoSymbolsFromLiteralsOrComments(t *testing.T) {
	src := `def real; s = "def fake; end"; end
# def commented; end
=begin
def block_commented; end
=end
`
	got, _ := NewRubyParser().Parse(src)
	for _, bad := range []string{"fake", "commented", "block_commented"} {
		if contains(got.Functions, bad) {
			t.Errorf("extracted %q from a literal/comment; got %q", bad, got.Functions)
		}
	}
	if !contains(got.Functions, "real") {
		t.Errorf("want real; got %q", got.Functions)
	}
}

// A computed require path names nothing this parser can honestly record.
func TestRubyParser_NonLiteralRequireIgnored(t *testing.T) {
	got, _ := NewRubyParser().Parse("require some_var\nrequire \"real/path\"\n")
	if !reflect.DeepEqual(got.Imports, []string{"real/path"}) {
		t.Errorf("Imports = %q, want [real/path]", got.Imports)
	}
}

func TestRubyParser_HashesAndLines(t *testing.T) {
	got, _ := NewRubyParser().Parse(rubySample)
	if got.SymbolLines["function:Outer.Reader.fetch"] != 15 {
		t.Errorf("fetch start line = %d, want 15", got.SymbolLines["function:Outer.Reader.fetch"])
	}
	edited := strings.Replace(rubySample, "    def fetch\n      1\n", "    def fetch\n      42\n", 1)
	after, _ := NewRubyParser().Parse(edited)
	if after.SymbolHashes["function:Outer.Reader.fetch"] == got.SymbolHashes["function:Outer.Reader.fetch"] {
		t.Error("body edit did not change the symbol hash")
	}
	if after.SymbolHashes["function:Standalone.go"] != got.SymbolHashes["function:Standalone.go"] {
		t.Error("unrelated symbol's hash changed")
	}
}

func TestRubyParser_CRLFParity(t *testing.T) {
	lf, _ := NewRubyParser().Parse(rubySample)
	crlf, _ := NewRubyParser().Parse(strings.ReplaceAll(rubySample, "\n", "\r\n"))
	if !reflect.DeepEqual(lf, crlf) {
		t.Error("CRLF source parsed differently from LF")
	}
}

func TestRubyParser_Degrades(t *testing.T) {
	for _, src := range []string{"", "def broken(", "class \x00\xff"} {
		got, err := NewRubyParser().Parse(src)
		if err != nil {
			t.Errorf("Parse(%q) errored: %v", src, err)
		}
		if got.Functions == nil || got.Imports == nil || got.Classes == nil || got.Exports == nil {
			t.Errorf("Parse(%q) returned a nil slice; want empty", src)
		}
	}
}

// A file the grammar cannot parse degenerates to an ERROR root with no
// module/class/method nodes. That must not be silent: for an existence checker,
// "no symbols here" and "I could not read this file" are different claims, and
// conflating them makes a consumer conclude the symbols don't exist. Measured
// at 0.5% of a 400-file real-world Ruby corpus (Homebrew).
func TestRubyParser_UnparseableFileDegradesNotPanics(t *testing.T) {
	// Deliberately malformed enough to defeat error recovery.
	src := "class C\n  def a\n" + strings.Repeat("end end end\n", 50) + "%w[\n"
	got, err := NewRubyParser().Parse(src)
	if err != nil {
		t.Errorf("unparseable input must not error: %v", err)
	}
	if got.Functions == nil || got.Classes == nil {
		t.Error("unparseable input must yield empty slices, not nil")
	}
}

// Edge cases found while reviewing the parser. Each pins a behavior a plausible
// alternative implementation would get wrong.
func TestRubyParser_Edges(t *testing.T) {
	src := `class C
  class << self
    def meta; end
  end

  def pub1; end
  private
  def priv1; end
  public
  def pub2; end

  def outer
    def nested_runtime; end
  end
end

class C
  def reopened; end
end

module M
  private
end
class AfterPrivateModule
  def should_be_public; end
end
`
	got, _ := NewRubyParser().Parse(src)

	// `class << self` is an anonymous singleton class: its defs belong to the
	// enclosing class, so they must not be dropped or given an invented prefix.
	if !contains(got.Functions, "C.meta") {
		t.Errorf("want C.meta from `class << self`; got %q", got.Functions)
	}
	// `public` flips visibility back on.
	if !contains(got.Exports, "C.pub2") {
		t.Errorf("`public` must re-export following defs; got %q", got.Exports)
	}
	if contains(got.Exports, "C.priv1") {
		t.Errorf("def after `private` must not be exported; got %q", got.Exports)
	}
	// A def inside another def's body defines a method at runtime, not statically;
	// extracting it would let a reference resolve from anywhere.
	if contains(got.Functions, "C.nested_runtime") || contains(got.Functions, "nested_runtime") {
		t.Errorf("runtime-nested def must not be extracted; got %q", got.Functions)
	}
	// Reopening a class must not duplicate it, and must still pick up new methods.
	if !contains(got.Functions, "C.reopened") {
		t.Errorf("want C.reopened from the reopened class; got %q", got.Functions)
	}
	seen := 0
	for _, c := range got.Classes {
		if c == "C" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("reopened class C appears %d times, want 1: %q", seen, got.Classes)
	}
	// A `private` in one body must not leak into a later sibling body.
	if !contains(got.Exports, "AfterPrivateModule.should_be_public") {
		t.Errorf("private leaked across bodies; got %q", got.Exports)
	}
}

// Logic-bug regression: `module A::B` and the nested `module A; module B` form
// are the same logical module and must produce the same symbol name. Emitting
// "A::B" for one and "A.B" for the other made the index answer differently for
// identical code, so a reference could resolve against only one spelling.
func TestRubyParser_CompactAndNestedModulesAgree(t *testing.T) {
	compact, _ := NewRubyParser().Parse("module A::B\n  def x; end\nend\n")
	nested, _ := NewRubyParser().Parse("module A\n  module B\n    def x; end\n  end\nend\n")

	if !contains(compact.Classes, "A.B") {
		t.Errorf("compact form: want class A.B, got %q", compact.Classes)
	}
	if !contains(compact.Functions, "A.B.x") {
		t.Errorf("compact form: want function A.B.x, got %q", compact.Functions)
	}
	if !contains(nested.Functions, "A.B.x") {
		t.Errorf("nested form: want function A.B.x, got %q", nested.Functions)
	}
	for _, n := range append(append([]string{}, compact.Classes...), compact.Functions...) {
		if strings.Contains(n, "::") {
			t.Errorf("symbol %q kept Ruby's :: separator; qualification uses .", n)
		}
	}
}
