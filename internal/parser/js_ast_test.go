package parser

import "testing"

// requireJSGrammar skips when the tree-sitter grammar for ext is not embedded
// in this build (a grammar_subset build without the JS/TS tags). The default
// `go test ./...` embeds the full grammar set, so these run in CI.
func requireJSGrammar(t *testing.T, ext string) {
	t.Helper()
	if jsLanguageFor(ext) == nil {
		t.Skipf("tree-sitter grammar for %q not embedded in this build", ext)
	}
}

// TestJSParser_SymbolSpans is the JS half of #19: per-symbol start lines and
// function body hashes, with class methods qualified as Class.method.
func TestJSParser_SymbolSpans(t *testing.T) {
	requireJSGrammar(t, ".js")
	p := NewJSParser()
	// 1-based lines are hand-counted from the layout below.
	src := `import dep from 'm';

export function topLevel() {}

export class Widget {
  doThing() {}
}

export const arrow = () => 1;
`
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := map[string]int{
		"function:topLevel":       3,
		"class:Widget":            5,
		"function:Widget.doThing": 6,
		"function:arrow":          9,
	}
	for key, want := range wantLines {
		if got := fs.SymbolLines[key]; got != want {
			t.Errorf("SymbolLines[%q] = %d, want %d", key, got, want)
		}
	}
	// Functions/methods and classes are all hashed over their full span.
	for _, key := range []string{"function:topLevel", "function:Widget.doThing", "function:arrow", "class:Widget"} {
		if fs.SymbolHashes[key] == "" {
			t.Errorf("%s has no body hash", key)
		}
	}
	// The qualified method must appear in Functions, not a bare "doThing".
	if !containsStr(fs.Functions, "Widget.doThing") {
		t.Errorf("Functions = %v, want it to contain Widget.doThing", fs.Functions)
	}
}

// TestJSParser_BodyHashChangesOnRewrite proves the diff `~ modified` criterion
// for JS: a function's body hash changes when its body changes and is stable
// when only a sibling changes.
func TestJSParser_BodyHashChangesOnRewrite(t *testing.T) {
	requireJSGrammar(t, ".js")
	p := NewJSParser()
	hashOf := func(src, key string) string {
		t.Helper()
		fs, err := p.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		h := fs.SymbolHashes[key]
		if h == "" {
			t.Fatalf("no hash for %q in:\n%s", key, src)
		}
		return h
	}
	base := "function F() { return 1; }\nfunction G() { return 10; }\n"
	bodyChange := "function F() { return 2; }\nfunction G() { return 10; }\n"
	sibling := "function F() { return 1; }\nfunction G() { return 20; }\n"

	baseF := hashOf(base, "function:F")
	if hashOf(bodyChange, "function:F") == baseF {
		t.Error("F hash unchanged after body rewrite")
	}
	if hashOf(sibling, "function:F") != baseF {
		t.Error("F hash changed when only sibling G changed")
	}
	if hashOf(sibling, "function:G") == hashOf(base, "function:G") {
		t.Error("G hash unchanged after G was rewritten")
	}
}

// TestJSParser_ClassHashChangesOnFieldEdit covers issue #53's cheaper
// alternative to per-field extraction: a class's own hash flips when a field
// is added/renamed, without a dedicated field symbol.
func TestJSParser_ClassHashChangesOnFieldEdit(t *testing.T) {
	requireJSGrammar(t, ".ts")
	p := NewJSParser()
	hashOf := func(src, key string) string {
		t.Helper()
		fs, err := p.ParseExt(src, ".ts")
		if err != nil {
			t.Fatal(err)
		}
		h := fs.SymbolHashes[key]
		if h == "" {
			t.Fatalf("no hash for %q in:\n%s", key, src)
		}
		return h
	}
	base := "class Config {\n  timeout: number;\n}\nclass Other {\n  x: number;\n}\n"
	fieldAdded := "class Config {\n  timeout: number;\n  retries: number;\n}\nclass Other {\n  x: number;\n}\n"
	fieldRenamed := "class Config {\n  deadline: number;\n}\nclass Other {\n  x: number;\n}\n"
	siblingChange := "class Config {\n  timeout: number;\n}\nclass Other {\n  x: number;\n  y: number;\n}\n"

	baseConfig := hashOf(base, "class:Config")
	if hashOf(fieldAdded, "class:Config") == baseConfig {
		t.Error("Config hash unchanged after adding a field — diff would miss the change")
	}
	if hashOf(fieldRenamed, "class:Config") == baseConfig {
		t.Error("Config hash unchanged after renaming its only field")
	}
	if hashOf(siblingChange, "class:Config") != baseConfig {
		t.Error("Config hash changed when only sibling Other changed — would over-report")
	}
}

// TestJSParser_TypeScript covers TS-only constructs via the TS grammar
// (selected by ParseExt): interfaces, type aliases, enums, generic functions,
// and typed methods.
func TestJSParser_TypeScript(t *testing.T) {
	requireJSGrammar(t, ".ts")
	p := NewJSParser()
	src := `export interface Shape { area(): number; }
export type ID = string;
export enum Color { Red, Green }
export class Server { start(): void {} }
export function build<T>(x: T): T { return x; }
`
	fs, err := p.ParseExt(src, ".ts")
	if err != nil {
		t.Fatal(err)
	}
	// Interfaces, type aliases, enums and classes are all "class-like".
	for _, name := range []string{"Color", "ID", "Server", "Shape"} {
		if !containsStr(fs.Classes, name) {
			t.Errorf("Classes = %v, want it to contain %q", fs.Classes, name)
		}
	}
	// Generic function and the qualified method must be present.
	for _, name := range []string{"build", "Server.start"} {
		if !containsStr(fs.Functions, name) {
			t.Errorf("Functions = %v, want it to contain %q", fs.Functions, name)
		}
	}
	// Every `export <kind> Name` form is enumerable in Exports — including the
	// TS-only type/interface/enum kinds, which previously landed only in Classes.
	for _, name := range []string{"Shape", "ID", "Color", "Server", "build"} {
		if !containsStr(fs.Exports, name) {
			t.Errorf("Exports = %v, want it to contain %q", fs.Exports, name)
		}
	}
	// The class itself is located and hashed over its full span.
	if fs.SymbolLines["class:Server"] == 0 {
		t.Error("class:Server has no line")
	}
	if fs.SymbolHashes["class:Server"] == "" {
		t.Error("class:Server has no hash")
	}
}

// TestJSParser_NamespaceQualification covers the post-review fix: TS namespace/
// module members must be qualified by the namespace name (NS.inner), not escape
// to top level and collide with identically-named top-level symbols.
func TestJSParser_NamespaceQualification(t *testing.T) {
	requireJSGrammar(t, ".ts")
	p := NewJSParser()
	src := `export function inner() { return 1; }
export namespace NS {
  export function inner() { return 2; }
  export class K {}
}
`
	fs, err := p.ParseExt(src, ".ts")
	if err != nil {
		t.Fatal(err)
	}
	// Both inners must be distinct keys — no collision.
	if !containsStr(fs.Functions, "inner") || !containsStr(fs.Functions, "NS.inner") {
		t.Errorf("Functions = %v, want both 'inner' and 'NS.inner'", fs.Functions)
	}
	// The namespace member class is qualified too; the namespace itself is located.
	if !containsStr(fs.Classes, "NS") || !containsStr(fs.Classes, "NS.K") {
		t.Errorf("Classes = %v, want 'NS' and 'NS.K'", fs.Classes)
	}
	// Distinct keys => distinct body hashes (no collision-combine flattening).
	if fs.SymbolHashes["function:inner"] == fs.SymbolHashes["function:NS.inner"] {
		t.Error("top-level inner and NS.inner share a hash — collision not prevented")
	}
}

// TestJSParser_AbstractMethod covers the post-review fix: abstract-class method
// signatures (abstract_method_signature) must be captured, qualified by class.
func TestJSParser_AbstractMethod(t *testing.T) {
	requireJSGrammar(t, ".ts")
	p := NewJSParser()
	src := `export abstract class A {
  abstract foo(): void;
  bar(): void {}
}
`
	fs, err := p.ParseExt(src, ".ts")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A.foo", "A.bar"} {
		if !containsStr(fs.Functions, name) {
			t.Errorf("Functions = %v, want it to contain %q", fs.Functions, name)
		}
	}
}

// TestJSParser_TypedArrowConst covers issue #84: the reduced TS grammar can't
// parse an arrow function whose parameter list carries a type annotation —
// with or without an explicit return type — and swallows the whole
// declaration into an unrecoverable ERROR subtree. Confirmed by direct AST
// inspection: `(x) => x` parses as a clean arrow_function, but `(x: string)
// => x` and `(x: string): string => x` both produce a top-level ERROR node
// with no variable_declarator/arrow_function left to walk. The regex
// fallback (name-only, no span/hash) recovers the binding instead of
// dropping it — a real hallucination-risk gap, since a missing symbol here
// means ANY call to it elsewhere in the repo is flagged unresolved by the
// guard.
func TestJSParser_TypedArrowConst(t *testing.T) {
	requireJSGrammar(t, ".ts")
	p := NewJSParser()
	src := `export const withParamType = (x: string) => x;
export const withReturnType = (x: string): string => x;
export const plain = (x) => x;
`
	fs, err := p.ParseExt(src, ".ts")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"withParamType", "withReturnType", "plain"} {
		if !containsStr(fs.Functions, name) {
			t.Errorf("Functions = %v, want it to contain %q", fs.Functions, name)
		}
	}
}

func containsStr(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
