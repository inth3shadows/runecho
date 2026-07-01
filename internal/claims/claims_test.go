package claims

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestIsCodeSymbol(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"IRProvider", true},
		{"ValidateClaims", true},
		{"FileIR", true},
		{"ParseData", true},
		{"ALL_CAPS", false},
		{"UPPERCASE", false},
		{"snake_case", false},
		{"emit_fault", false},
		{"__init__", false},
		{"x", false},
		{"Ab", false}, // len==2, filtered by minimum length guard
		{"Abc", true}, // len==3, valid CamelCase
	}
	for _, tc := range cases {
		if got := IsCodeSymbol(tc.name); got != tc.want {
			t.Errorf("IsCodeSymbol(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestExtractSymbolRefs_Backtick(t *testing.T) {
	text := "Call `ParseData` to load then `FileIR` to wrap."
	refs := ExtractSymbolRefs(text)
	if _, ok := refs["ParseData"]; !ok {
		t.Errorf("expected ParseData in refs: %v", refs)
	}
	if _, ok := refs["FileIR"]; !ok {
		t.Errorf("expected FileIR in refs: %v", refs)
	}
}

// Regression (#34): a dotted span like `Reader.fetch` matched nothing because
// backtickRe stopped at the first identifier; qualified/method refs were
// silently dropped. Must be captured whole, symmetric with the locate oracle.
func TestExtractSymbolRefs_DottedRef(t *testing.T) {
	text := "Call `Reader.fetch` to load, not plain `Reader`."
	refs := ExtractSymbolRefs(text)
	if _, ok := refs["Reader.fetch"]; !ok {
		t.Errorf("expected Reader.fetch in refs: %v", refs)
	}
	if _, ok := refs["Reader"]; !ok {
		t.Errorf("expected bare Reader in refs: %v", refs)
	}
}

// Regression (forage): method declarations `func (r *Reader) Fetch()` extracted
// nothing — declRe can't see past the receiver. They must yield the qualified
// Type.Name, symmetric with how the parser and oracle store methods.
func TestExtractSymbolRefs_MethodDecl(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"func (r *Reader) Fetch() error", "Reader.Fetch"},   // pointer receiver + var
		{"func (Reader) Close()", "Reader.Close"},            // value receiver, no var
		{"func (s *Set[T]) Add(v T)", "Set.Add"},             // generic receiver
	}
	for _, tc := range cases {
		refs := ExtractSymbolRefs(tc.text)
		if _, ok := refs[tc.want]; !ok {
			t.Errorf("text %q: expected %q in refs, got %v", tc.text, tc.want, refs)
		}
	}
}

// Regression (forage): a multi-name decl captured only the first name, silently
// dropping the rest from claim validation.
func TestExtractSymbolRefs_MultiNameDecl(t *testing.T) {
	refs := ExtractSymbolRefs("var MaxSize, MinSize int")
	for _, want := range []string{"MaxSize", "MinSize"} {
		if _, ok := refs[want]; !ok {
			t.Errorf("expected %q in refs: %v", want, refs)
		}
	}
}

// Regression (forage): members of a parenthesized `var (` / `const (` / `type (`
// block carry no keyword on their line, so declRe missed them entirely.
func TestExtractSymbolRefs_DeclBlock(t *testing.T) {
	text := "var (\n\tMaxSize = 100\n\tMinSize, MidSize int\n)\n" +
		"type (\n\tReader interface{ Read() }\n\tWriter = io.Writer\n)"
	refs := ExtractSymbolRefs(text)
	for _, want := range []string{"MaxSize", "MinSize", "MidSize", "Reader", "Writer"} {
		if _, ok := refs[want]; !ok {
			t.Errorf("expected %q in refs: %v", want, refs)
		}
	}
}

// Regression (forage): a member value spanning lines (inner parens) must not
// close the block early, and composite-literal field keys must NOT be captured.
func TestExtractSymbolRefs_DeclBlock_NestedAndKeys(t *testing.T) {
	text := "var (\n" +
		"\tPattern = build(\n\t\t\"x\",\n\t)\n" + // multi-line value, inner ( )
		"\tCfg = Config{\n\t\tHostName: \"h\",\n\t}\n" + // composite literal key
		"\tTrailing = 1\n" + // must still be captured after the nested value
		")"
	refs := ExtractSymbolRefs(text)
	for _, want := range []string{"Pattern", "Cfg", "Trailing"} {
		if _, ok := refs[want]; !ok {
			t.Errorf("expected %q in refs: %v", want, refs)
		}
	}
	if _, ok := refs["HostName"]; ok {
		t.Errorf("composite-literal field key HostName should not be captured: %v", refs)
	}
}

// Regression (forage): Python/JS/TS declaration keywords had no decl-pattern
// parity with Go — only backtick coverage. class/function/let/def now extract,
// accepting camelCase (these languages don't mark export by case) while
// IsCodeSymbol still filters pure snake_case/lowercase noise.
func TestExtractSymbolRefs_LangParity(t *testing.T) {
	want := map[string]string{
		"class MyHandler:":         "MyHandler",   // python/js class
		"function processData() {": "processData", // js func, camelCase
		"export class Widget {":    "Widget",      // export modifier before keyword
		"let RetryCount = 3":       "RetryCount",  // js let
		"async def DoWork(self):":  "DoWork",      // python async, CamelCase
		"const processData = (x) => x": "processData", // js/ts const, camelCase
	}
	for text, sym := range want {
		refs := ExtractSymbolRefs(text)
		if _, ok := refs[sym]; !ok {
			t.Errorf("text %q: expected %q in refs, got %v", text, sym, refs)
		}
	}
	// snake_case / lowercase decls stay filtered (consistent with Go behavior);
	// ALL_CAPS const stays filtered too (no mixed case, same as Go's declRe path).
	for _, text := range []string{"def fetch_data(self):", "function helper() {}", "const MAX_SIZE = 5"} {
		if refs := ExtractSymbolRefs(text); len(refs) != 0 {
			t.Errorf("text %q: expected no refs (noise), got %v", text, refs)
		}
	}
}

func TestExtractSymbolRefs_DeclPattern(t *testing.T) {
	text := "The function func ValidateClaims handles this.\ntype MyHandler struct"
	refs := ExtractSymbolRefs(text)
	if _, ok := refs["ValidateClaims"]; !ok {
		t.Errorf("expected ValidateClaims in refs: %v", refs)
	}
	if _, ok := refs["MyHandler"]; !ok {
		t.Errorf("expected MyHandler in refs: %v", refs)
	}
}

func TestExtractSymbolRefs_AllCapsFiltered(t *testing.T) {
	text := "`ALL_CAPS` should not appear.\n`UPPERCASE` same."
	refs := ExtractSymbolRefs(text)
	if _, ok := refs["ALL_CAPS"]; ok {
		t.Errorf("ALL_CAPS should be filtered: %v", refs)
	}
	if _, ok := refs["UPPERCASE"]; ok {
		t.Errorf("UPPERCASE should be filtered: %v", refs)
	}
}

func TestExtractSymbolRefs_SnakeCaseFiltered(t *testing.T) {
	text := "Use `emit_fault` or `validate_claims` here."
	refs := ExtractSymbolRefs(text)
	if _, ok := refs["emit_fault"]; ok {
		t.Errorf("snake_case should be filtered: %v", refs)
	}
}

func TestExtractSymbolRefs_Dedup(t *testing.T) {
	text := "`ParseData` is used here.\n`ParseData` is used again."
	refs := ExtractSymbolRefs(text)
	// Should appear exactly once; map guarantees this but verify count.
	if len(refs) != 1 {
		t.Errorf("dedup: expected 1 unique ref, got %d: %v", len(refs), refs)
	}
}

// Regression: bufio.Scanner's 64KB default cap silently dropped long lines.
// A valid symbol on a >64KB line must still be extracted.
func TestExtractSymbolRefs_LongLine(t *testing.T) {
	text := strings.Repeat("x", 70*1024) + " then call `RealSymbol` here."
	refs := ExtractSymbolRefs(text)
	if _, ok := refs["RealSymbol"]; !ok {
		t.Errorf("expected RealSymbol on 70KB line, got %v", refs)
	}
}

// Regression: ASCII-only regexes excluded Unicode identifiers, which Go, JS,
// and Python all permit.
func TestExtractSymbolRefs_UnicodeIdentifier(t *testing.T) {
	text := "See `ÜnïcödeName` and func Δelta for details."
	refs := ExtractSymbolRefs(text)
	if _, ok := refs["ÜnïcödeName"]; !ok {
		t.Errorf("expected ÜnïcödeName in refs: %v", refs)
	}
}

// Regression: truncate sliced at a byte offset, splitting multibyte runes and
// emitting invalid UTF-8 into stored snippets.
func TestTruncate_RuneBoundary(t *testing.T) {
	// 79 ASCII bytes then a 2-byte rune straddling the 80-byte cut point.
	s := strings.Repeat("a", 79) + "é" + strings.Repeat("b", 20)
	got := truncate(s, 80)
	if !utf8.ValidString(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
	if want := strings.Repeat("a", 79) + "..."; got != want {
		t.Errorf("truncate = %q, want %q", got, want)
	}
}
