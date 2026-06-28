package claims

import (
	"testing"
	"unicode/utf8"
)

// FuzzClaims asserts the claim-symbol extractor never panics on arbitrary input
// and keeps its output invariants. ExtractSymbolRefs parses untrusted free-text
// transcripts (an LLM's prose) on the validate-claims / truth-trail path, so it
// is exactly the adversarial-input surface the C2 fail-safe must cover. Run:
// go test -run=x -fuzz=FuzzClaims ./internal/claims
func FuzzClaims(f *testing.F) {
	seeds := []string{
		"calls `Reader.fetch` then `ProcessData`",
		"func Foo() {}\ntype Bar struct{}\nvar MaxSize, MinSize int",
		"func (r *Reader[T]) Fetch() {}",
		"var (\n\tMaxSize int\n\tMinSize int\n)",
		"class Widget:\n    def render(self): pass",
		"export function processData() {}",
		"", "``", "`.`", "`...`", "`\x00`", "func ", "var (\n", "type (",
		"`" + "verylongnamethatexceedstheeightybytetruncationboundaryusedbythesnippethelperabcdefg`",
		"日本語Identifier `MixedCaseΣymbol`",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		refs := ExtractSymbolRefs(text) // must never panic
		for sym, snippet := range refs {
			// Every recorded key must pass the same gate add() applied — no symbol
			// is stored that IsCodeSymbol would reject.
			if !IsCodeSymbol(sym) {
				t.Fatalf("stored a non-code-symbol key: %q", sym)
			}
			// Truncation cuts on rune boundaries, so a snippet drawn from valid
			// UTF-8 input must stay valid — they are JSON-marshaled downstream.
			// (A short invalid-UTF-8 line passes through untruncated, so the
			// guarantee is conditional on the input being valid in the first place.)
			if utf8.ValidString(text) && !utf8.ValidString(snippet) {
				t.Fatalf("snippet for %q became invalid UTF-8 from valid input: %q", sym, snippet)
			}
		}
	})
}
