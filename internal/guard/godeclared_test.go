package guard

import (
	"os"
	"slices"
	"sort"
	"testing"
)

func declaredNames(src string) []string {
	out := GoDeclaredNames(TextToAddedLines(src))
	sort.Strings(out)
	return out
}

func hasAll(got []string, want ...string) []string {
	var missing []string
	for _, w := range want {
		if !slices.Contains(got, w) {
			missing = append(missing, w)
		}
	}
	return missing
}

func TestGoDeclaredNames_BindingForms(t *testing.T) {
	src := `package p

import "fmt"

var pkgLevel = 1

const (
	alpha = 1
	beta  = 2
)

type local struct{}

func run(ctx Context, name string) (n int, err error) {
	handler := lookup()
	a, b := two()
	var buf Buffer
	for k, v := range m {
		fmt.Println(k, v)
	}
	switch t := x.(type) {
	default:
		_ = t
	}
	type inner int
	cb := func(arg string) {}
	_ = cb
	return
}
`
	got := declaredNames(src)
	want := []string{
		"pkgLevel", "alpha", "beta", "local", // package-level and block forms
		"ctx", "name", "n", "err", // params and named returns
		"handler", "a", "b", "buf", // short decls and var
		"k", "v", "t", // range and type switch
		"inner", "cb", "arg", // local type, func literal and its param
	}
	if missing := hasAll(got, want...); len(missing) > 0 {
		t.Errorf("GoDeclaredNames missed %v\ngot: %v", missing, got)
	}
	if slices.Contains(got, "_") {
		t.Error("the blank identifier must never be collected")
	}
}

// TestGoDeclaredNames_NestedParenParamList is the regression that the
// compiler-oracle differential caught within minutes of this extractor going
// live. A func-typed parameter nests parentheses, and the original `[^)]*`
// capture stopped at the FIRST close paren — silently dropping every parameter
// after it, which surfaced as a proven false positive on `braceDepthSeed`.
func TestGoDeclaredNames_NestedParenParamList(t *testing.T) {
	src := "package p\n\n" +
		"func extractDefsSeeded(lang Lang, lines []AddedLine, openSeed func(lineNo int) string, braceDepthSeed func(lineNo int) int) []string {\n" +
		"\treturn nil\n}\n"
	got := declaredNames(src)
	if missing := hasAll(got, "lang", "lines", "openSeed", "braceDepthSeed"); len(missing) > 0 {
		t.Errorf("parameters after a func-typed one must still bind; missed %v\ngot: %v", missing, got)
	}
}

func TestGoDeclaredNames_MethodReceiverBinds(t *testing.T) {
	src := `package p

func (r *Reader) Fetch(limit int) error {
	return nil
}
`
	got := declaredNames(src)
	if missing := hasAll(got, "r", "limit"); len(missing) > 0 {
		t.Errorf("receiver and parameter must bind; missed %v\ngot: %v", missing, got)
	}
}

// TestGoDeclaredNames_DoesNotBindTypes is the precision half. Folding a type
// name into the known set would mask a genuinely undefined type — turning a
// visible false positive into a silent false negative, which is the trade
// JSDeclaredNames was written to avoid (PR #156).
func TestGoDeclaredNames_DoesNotBindTypes(t *testing.T) {
	src := `package p

func run(cfg HallucinatedConfig) {
	var w HallucinatedWriter
	_ = w
}
`
	got := declaredNames(src)
	for _, typ := range []string{"HallucinatedConfig", "HallucinatedWriter"} {
		if slices.Contains(got, typ) {
			t.Errorf("%q is a type, not a binding — collecting it would mask an undefined type", typ)
		}
	}
	if missing := hasAll(got, "cfg", "w"); len(missing) > 0 {
		t.Errorf("the names themselves must still bind; missed %v", missing)
	}
}

func TestGoDeclaredNames_IgnoresCommentsAndStrings(t *testing.T) {
	src := "package p\n\n" +
		"func run() {\n" +
		"\t// fromComment := 1\n" +
		"\ts := `fromRawString := 2`\n" +
		"\t_ = s\n}\n"
	got := declaredNames(src)
	for _, name := range []string{"fromComment", "fromRawString"} {
		if slices.Contains(got, name) {
			t.Errorf("%q comes from masked content and must not bind; got %v", name, got)
		}
	}
}

// BenchmarkGoDeclaredNames pins the cost of the whole-file scan this change adds
// to every Go hook invocation, against a ~12ms end-to-end budget.
//
// Measured on internal/guard/extract.go (2,213 lines, the largest file here),
// with the pre-existing passes for calibration:
//
//	ExtractDefs        0.52ms
//	GoDeclaredNames    1.49ms   <- this pass
//	ExtractRefs        6.56ms
//
// The first cut of this extractor ran at 6.75ms — over half the budget on its
// own — because it fed every line to four regexes and stripped literals from all
// of them. Two filters fixed it: a substring test before each regex group, and
// skipping the literal strip on lines with no quote character (it allocates a
// byte slice per line and was the single largest cost). Keep both if this is
// ever rewritten.
func BenchmarkGoDeclaredNames(b *testing.B) {
	data, err := os.ReadFile("extract.go") // the largest file in this package
	if err != nil {
		b.Skipf("read corpus file: %v", err)
	}
	lines := TextToAddedLines(string(data))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = GoDeclaredNames(lines)
	}
}
