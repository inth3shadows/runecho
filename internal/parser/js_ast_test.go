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
	// Functions/methods are hashed; classes are not (parity with Python/Go).
	for _, key := range []string{"function:topLevel", "function:Widget.doThing", "function:arrow"} {
		if fs.SymbolHashes[key] == "" {
			t.Errorf("%s has no body hash", key)
		}
	}
	if _, ok := fs.SymbolHashes["class:Widget"]; ok {
		t.Error("class:Widget should not be hashed")
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
	// The class itself is located, not hashed.
	if fs.SymbolLines["class:Server"] == 0 {
		t.Error("class:Server has no line")
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
