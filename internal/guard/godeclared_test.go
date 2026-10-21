package guard

import (
	"os"
	"slices"
	"sort"
	"strings"
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

// TestGoDeclaredNames_ReceiveOnlyChanParam pins #500 item 5: reGoParamName's
// type-start class had no `<`, so a `<-chan T` parameter bound nothing. A bare
// `<` (a comparison in a call group after func) must still bind nothing.
func TestGoDeclaredNames_ReceiveOnlyChanParam(t *testing.T) {
	got := declaredNames("package p\n\nfunc drain(in <-chan int, done <-chan struct{}, out chan<- int) {\n}\n")
	if missing := hasAll(got, "in", "done", "out"); len(missing) > 0 {
		t.Errorf("channel params not bound: missing %v (got %v)", missing, got)
	}
	got = declaredNames("package p\n\nfunc f(xs ...<-chan int) (out <-chan int) {\n}\n")
	if missing := hasAll(got, "xs", "out"); len(missing) > 0 {
		t.Errorf("variadic / named-result channel not bound: missing %v (got %v)", missing, got)
	}
	got = declaredNames("package p\n\nfunc g(a, b <- chan int) {\n}\n")
	if missing := hasAll(got, "a", "b"); len(missing) > 0 {
		t.Errorf("spaced `<- chan` not bound: missing %v (got %v)", missing, got)
	}
	got = declaredNames("package p\n\nvar (\n\tin <-chan int\n\tn  int\n)\n")
	if !slices.Equal(got, []string{"in", "n"}) {
		t.Errorf("var block with a receive-only channel: got %v, want [in n]", got)
	}
	// A type start the pattern does not know must not split the name either:
	// without `\b` this bound "c" (and "a" for `as`).
	got = declaredNames("package p\n\nvar (\n\tch (chan int)\n\tas (int)\n)\n")
	if slices.Contains(got, "c") || slices.Contains(got, "a") {
		t.Errorf("a var-block name was split into a shorter bogus name: %v", got)
	}
	// Comparisons, never types: `x <-1` is `x < -1` before gofmt.
	for _, src := range []string{
		"package p\n\nvar r = (func(a int) bool)(x < y)\n",
		"package p\n\nvar r = (func(a int) bool)(x <= y)\n",
		"package p\n\nvar r = (func(a int) bool)(f, x <-ch)\n",
		"package p\n\nvar r = (func(a int) bool)(x <-1)\n",
		"package p\n\nvar r = (func(a int) bool)(f, x <-chanVar)\n",
	} {
		got = declaredNames(src)
		if slices.Contains(got, "x") || slices.Contains(got, "f") {
			t.Errorf("a comparison operand was bound as a param in %q: %v", src, got)
		}
	}
}

// TestGoDeclaredNames_PointerStarIsNotMultiplication pins #506 item 1: `*` in
// the type-start set let a gofmt'd product in a group after func read as a
// pointer-typed param (`(x * y)` bound x). A pointer type's `*` is glued to its
// base type; a binary `*` never is on one side only.
func TestGoDeclaredNames_PointerStarIsNotMultiplication(t *testing.T) {
	for _, src := range []string{
		"package p\n\nvar r = (func(a int) bool)(x * y)\n",
		"package p\n\nvar r = (func(a int) bool)(x * (y + 1))\n",
		"package p\n\nvar r = (func(a int) bool)(f, x * y)\n",
		"package p\n\nvar r = (func(a int) bool)(x *<-ch)\n",
		// The var-block pattern shares goTypeStart: a spaced product on a
		// continuation line is not a declaration.
		"package p\n\nvar (\n\ttotal = a +\n\t\tx * c\n)\n",
	} {
		got := declaredNames(src)
		if slices.Contains(got, "x") || slices.Contains(got, "f") {
			t.Errorf("a product operand was bound as a param in %q: %v", src, got)
		}
	}
	got := declaredNames("package p\n\nfunc f(p *T, pp **T, s *[]int, m *map[K]V, fn *func(), q *pkg.T, xs ...*T, c *<-chan int, st *struct{}, it *interface{}, ch *chan int, ar *[4]int, pa *(T), u *Ärger) {\n}\n\nvar (\n\tptr *T\n)\n")
	if missing := hasAll(got, "p", "pp", "s", "m", "fn", "q", "xs", "c", "st", "it", "ch", "ar", "pa", "u", "ptr"); len(missing) > 0 {
		t.Errorf("pointer-typed names not bound: missing %v (got %v)", missing, got)
	}
}

// TestGoDeclaredNames_BlockGluedProductVsGenericEntry pins #506 item 1b. gofmt
// glues a product inside a lower-precedence expression, so a var-block
// continuation `x*(y+1)` read as `x *(T)` and bound x. gofmt also glues a
// generic type entry's `[` (`List[T any] struct{`), so the fix must keep those.
func TestGoDeclaredNames_BlockGluedProductVsGenericEntry(t *testing.T) {
	for _, line := range []string{"x*c", "x*(y+1)", "x*Ärger", "x**p", "x.Sel |", "x[:],", "x[i] = 1"} {
		src := "package p\n\nvar (\n\ttotal = a +\n\t\t" + line + "\n)\n"
		if got := declaredNames(src); slices.Contains(got, "x") {
			t.Errorf("continuation %q bound x: %v", line, got)
		}
	}
	src := "package p\n\ntype (\n\tA int\n\tList[T any] struct {\n\t\tv T\n\t}\n\tPair[K comparable, V any] = struct{ k K }\n\tS[_ any] struct{}\n\tN1[P any] *M1[P]\n\tT1[P T1[P]] interface{}\n\tC[T ~int | ~string] []T\n\tI[T interface{ M() }] T\n)\n\nvar (\n\tx int\n\ta, b []string\n\tp *T\n\tch <-chan int\n\tn = 1\n\tm=2\n)\n"
	want := []string{"A", "C", "I", "List", "N1", "Pair", "S", "T1", "a", "b", "ch", "m", "n", "p", "x"}
	if missing := hasAll(declaredNames(src), want...); len(missing) > 0 {
		t.Errorf("block declarations not bound: missing %v (got %v)", missing, declaredNames(src))
	}

	// gofmt layouts from the #509 review: a wrapped type-parameter list and a
	// receive-only channel constraint still bind the type name; the wrapped
	// list's own names, a func literal's body and a composite literal's entries
	// are deeper than the block's entries and bind nothing; a comparison or a
	// generic call continuing the line above binds nothing.
	src = "package p\n\ntype (\n\tBig[\n\t\tK comparable,\n\t\tV any,\n\t] struct{ f K }\n\tG[T <-chan int] struct{}\n)\n\nvar (\n\th = func() {\n\t\terr = f()\n\t}\n\terrs = map[int]string{\n\t\tsqlite3.Abort: \"a\",\n\t}\n\tok = a &&\n\t\tx == y\n\tc = b ||\n\t\tMap[string, chan int](nil)\n\tlast int\n)\n"
	got := declaredNames(src)
	if missing := hasAll(got, "Big", "G", "h", "errs", "ok", "c", "last"); len(missing) > 0 {
		t.Errorf("block entries not bound: missing %v (got %v)", missing, got)
	}
	for _, bogus := range []string{"K", "V", "f", "err", "sqlite3", "x", "Map"} {
		if slices.Contains(got, bogus) {
			t.Errorf("%q bound from inside an entry: %v", bogus, got)
		}
	}

	// Shapes the corpus differential found the continuation rule must not
	// swallow: a name list wrapped one name per line (x/sys's zsyscall files),
	// and a float literal ending in its dot (go/types testdata).
	got = declaredNames("package p\n\nvar (\n\tlibc_umount,\n\tlibc_lseek,\n\tlibc_mmap64 syscallFunc\n\tnext int\n)\n\nconst (\n\ttf0 float32 = 0.\n\ttf1 float32 = 1.\n)\n")
	if missing := hasAll(got, "libc_umount", "libc_lseek", "libc_mmap64", "next", "tf0", "tf1"); len(missing) > 0 {
		t.Errorf("wrapped name list / float-dot entries not bound: missing %v (got %v)", missing, got)
	}
}

// TestGoDeclaredNames_BlockStateResetsAtHunkGap: in hunk mode only the added
// lines are scanned, so an opener or a trailing operator whose other half is an
// unchanged line must not carry across a gap and hide the next entry (review
// on #509).
func TestGoDeclaredNames_BlockStateResetsAtHunkGap(t *testing.T) {
	for _, ls := range [][]AddedLine{
		{{LineNo: 1, Text: "var ("}, {LineNo: 2, Text: "\terrs = map[int]string{"}, {LineNo: 10, Text: "\ty int"}},
		{{LineNo: 1, Text: "var ("}, {LineNo: 2, Text: "\tx = a +"}, {LineNo: 10, Text: "\ty int"}},
	} {
		if got := GoDeclaredNames(ls); !slices.Contains(got, "y") {
			t.Errorf("entry after a hunk gap not bound: %v", got)
		}
	}
}

// TestGoDeclaredNames_DroppedCarryKeepsInnerLiterals pins #506 item 3: lines
// joined into a carry are not scanned alone (#504), so when the carry is
// dropped unclosed (the line cap, or a hunk gap) a func literal inside it must
// still bind its own params.
func TestGoDeclaredNames_DroppedCarryKeepsInnerLiterals(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\nfunc f() {\n\tc := NewCache[string, func() error](\n\t\tfunc(k string) error { return k2(k) },\n")
	for i := 0; i < maxWrappedSigLines+6; i++ {
		b.WriteString("\t\targ,\n")
	}
	b.WriteString("\t)\n}\n")
	if got := declaredNames(b.String()); !slices.Contains(got, "k") {
		t.Errorf("func literal param lost when the carry hit the cap: %v", got)
	}
	gap := []AddedLine{
		{LineNo: 1, Text: "c := NewCache[string, func() error]("},
		{LineNo: 2, Text: "\tfunc(k string) error { return nil },"},
		{LineNo: 9, Text: "\tother(q)"},
	}
	if got := GoDeclaredNames(gap); !slices.Contains(got, "k") {
		t.Errorf("func literal param lost when the carry was cut by a hunk gap: %v", got)
	}
	// The added lines can also simply stop inside the call.
	if got := GoDeclaredNames(gap[:2]); !slices.Contains(got, "k") {
		t.Errorf("func literal param lost when the input ended inside the call: %v", got)
	}
	// #504's exclusion still holds for a carry that is dropped: a func-typed
	// constraint or param inside a real wrapped signature binds nothing.
	for _, ls := range [][]AddedLine{
		{{LineNo: 1, Text: "func Map["}, {LineNo: 2, Text: "\tF func(x int) bool,"}, {LineNo: 9, Text: "\tother(q)"}},
		{{LineNo: 1, Text: "func Register("}, {LineNo: 2, Text: "\thandler func(ctx int, req string) error,"}, {LineNo: 9, Text: "\tother(q)"}},
	} {
		got := GoDeclaredNames(ls)
		for _, bogus := range []string{"x", "ctx", "req"} {
			if slices.Contains(got, bogus) {
				t.Errorf("%q bound from inside a dropped signature: %v", bogus, got)
			}
		}
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

// TestGoDeclaredNames_ShortDeclInEveryPosition pins the three binding positions
// the original line-anchored regexes missed. Each one produced a false positive
// on ordinary Go: registry dispatch via `if fn, ok := m[k]`, a channel receive in
// a select, and a bind-then-call inside a one-line func literal.
func TestGoDeclaredNames_ShortDeclInEveryPosition(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"if-init", "package p\n\nfunc f() {\n\tif fn, ok := m[\"k\"]; ok {\n\t\tfn()\n\t}\n}\n", "fn"},
		{"case-receive", "package p\n\nfunc f() {\n\tselect {\n\tcase fn := <-ch:\n\t\tfn()\n\t}\n}\n", "fn"},
		{"inline-literal", "package p\n\nfunc f() {\n\tfunc() { g := mk(); g() }()\n}\n", "g"},
		{"range-clause", "package p\n\nfunc f() {\n\tfor k, v := range m {\n\t\t_, _ = k, v\n\t}\n}\n", "v"},
		{"type-switch", "package p\n\nfunc f() {\n\tswitch t := x.(type) {\n\tdefault:\n\t\t_ = t\n\t}\n}\n", "t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := declaredNames(tc.src)
			if !slices.Contains(got, tc.want) {
				t.Errorf("%q must bind; got %v", tc.want, got)
			}
			// The outcome that matters: no violation from the additive check.
			lines := TextToAddedLines(tc.src)
			known := map[string]struct{}{"f": {}, "m": {}, "ch": {}, "mk": {}, "x": {}}
			FoldInFileDefs(known, lines, LangGo)
			if v := Run(known, "", []FileDiff{{Path: "p.go", AddedLines: lines}}); len(v) != 0 {
				t.Errorf("locally bound call must not be flagged, got %+v", v)
			}
		})
	}
}

// TestGoDeclaredNames_VarBlockWithWrappedCall pins the nested-paren bug. A
// wrapped call inside a `var (` block closes a paren on an indented line without
// ending the block, and closing on the first such line dropped every name after
// it. The parser's AST rewrite fixed exactly this for the exported half; the
// regex extractor reintroduced it.
func TestGoDeclaredNames_VarBlockWithWrappedCall(t *testing.T) {
	src := "package p\n\nvar (\n\ta = compute(\n\t\t1,\n\t)\n\tb = mk\n)\n\nfunc f() { b() }\n"
	got := declaredNames(src)
	if missing := hasAll(got, "a", "b"); len(missing) > 0 {
		t.Errorf("names after a wrapped call in a var block must bind; missed %v\ngot: %v", missing, got)
	}
}

// TestGoDeclaredNames_BlockCommentNotFolded is the over-inclusion direction. A
// `/*` opener carries no quote character, so the literal-strip fast path skipped
// it and declarations inside commented-out code were folded into the known set —
// which silently masks real hallucinations.
func TestGoDeclaredNames_BlockCommentNotFolded(t *testing.T) {
	src := "package p\n\n/*\nvar handler = 1\n*/\n\nfunc f() { handler() }\n"
	if got := declaredNames(src); slices.Contains(got, "handler") {
		t.Errorf("a declaration inside a block comment must not bind; got %v", got)
	}
	lines := TextToAddedLines(src)
	known := map[string]struct{}{"f": {}}
	FoldInFileDefs(known, lines, LangGo)
	if v := Run(known, "", []FileDiff{{Path: "p.go", AddedLines: lines}}); len(v) != 1 || v[0].Symbol != "handler" {
		t.Errorf("a call to a commented-out declaration is a real violation, got %+v", v)
	}
}

// TestGoDeclaredNames_WrappedSignatureBindsParams pins #473: gofmt wraps a long
// signature one parameter per line, and goFuncGroups stopped at the group that
// did not close on the `func` line, so a bare call to a parameter (`next(w, r)`)
// was flagged. The repro is the issue's fixture.
func TestGoDeclaredNames_WrappedSignatureBindsParams(t *testing.T) {
	src := `package p

import "net/http"

func middleware(
	next http.HandlerFunc,
	fn func() error,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = fn()
		next(w, r)
	}
}

func (s *server) handle(ctx context.Context,
	req *Request) (resp *Response,
	err error) {
	return nil, nil
}
`
	got := declaredNames(src)
	if missing := hasAll(got, "next", "fn", "w", "r", "s", "ctx", "req", "resp", "err"); len(missing) > 0 {
		t.Errorf("wrapped signature params not bound: missing %v (got %v)", missing, got)
	}
	// The body is not a parameter list: nothing inside it may be bound by the
	// carried-over group.
	if slices.Contains(got, "http") || slices.Contains(got, "error") {
		t.Errorf("type names bound as params: %v", got)
	}
}

// TestGoDeclaredNames_WrappedSignatureStopsAtGap: a hunk whose wrapped signature
// is cut by a line gap must not join unrelated text into a parameter list.
func TestGoDeclaredNames_WrappedSignatureStopsAtGap(t *testing.T) {
	ls := []AddedLine{
		{LineNo: 1, Text: "func f("},
		{LineNo: 9, Text: "	stray int,"},
		{LineNo: 10, Text: ") {"},
	}
	if got := GoDeclaredNames(ls); slices.Contains(got, "stray") {
		t.Errorf("a line across a gap was bound as a parameter: %v", got)
	}
}

// TestGoDeclaredNames_WrappedSignatureIgnoresCommentParens: a `//` comment on a
// carried line is not masked by the pre-filtered stripper, so a paren inside it
// must not shift the carried depth (review on #499).
func TestGoDeclaredNames_WrappedSignatureIgnoresCommentParens(t *testing.T) {
	src := "package p\n\nfunc New(\n\taddr string, // see (a\n\ttimeout int,\n) *S {\n\t// 1) step\n\tfoo bar\n\treturn nil\n}\n"
	got := declaredNames(src)
	if missing := hasAll(got, "addr", "timeout"); len(missing) > 0 {
		t.Errorf("params after a comment paren not bound: missing %v (got %v)", missing, got)
	}
	if slices.Contains(got, "foo") {
		t.Errorf("body text bound as a param: %v", got)
	}
}

// TestGoDeclaredNames_WrappedSignatureCap: a group that never closes is dropped
// after maxWrappedSigLines, and a signature opened on the line that closes the
// previous one gets its own budget.
func TestGoDeclaredNames_WrappedSignatureCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("func f(\n")
	for i := 0; i < maxWrappedSigLines+5; i++ {
		b.WriteString("\tx int,\n")
	}
	b.WriteString("\tlate int,\n) {}\n")
	if got := GoDeclaredNames(TextToAddedLines(b.String())); slices.Contains(got, "late") {
		t.Errorf("a group past the cap was still carried: %v", got)
	}

	b.Reset()
	b.WriteString("func g(\n")
	for i := 0; i < maxWrappedSigLines-2; i++ {
		b.WriteString("\ta int,\n")
	}
	b.WriteString(") { go func(\n")
	for i := 0; i < 10; i++ {
		b.WriteString("\tb int,\n")
	}
	b.WriteString("\tlast int,\n) {}()\n}\n")
	if missing := hasAll(GoDeclaredNames(TextToAddedLines(b.String())), "a", "b", "last"); len(missing) > 0 {
		t.Errorf("second signature inherited the first one's budget: missing %v", missing)
	}
}

// TestGoDeclaredNamesSeeded_WrappedSignature: the hunk path binds a wrapped
// signature too, and resets it at a gap.
func TestGoDeclaredNamesSeeded_WrappedSignature(t *testing.T) {
	seed := func(int) string { return "" }
	ls := []AddedLine{
		{LineNo: 3, Text: "func h("},
		{LineNo: 4, Text: "\tnext Handler,"},
		{LineNo: 5, Text: ") {"},
		{LineNo: 20, Text: "func k("},
		{LineNo: 30, Text: "\tstray int,"},
		{LineNo: 31, Text: ") {"},
	}
	got := goDeclaredNamesSeeded(ls, seed)
	if !slices.Contains(got, "next") || slices.Contains(got, "stray") {
		t.Errorf("seeded wrapped signature: got %v, want next bound and stray not", got)
	}
}

// TestGoDeclaredNames_WrappedSignatureTrailingComments: one comment per
// parameter is ordinary gofmt output; the comment text must neither hide the
// next parameter nor bind its own words, and a `func (` inside a comment must
// not open a carry that shadows the real signature after it (review on #499).
func TestGoDeclaredNames_WrappedSignatureTrailingComments(t *testing.T) {
	src := "package p\n\nfunc f(\n\tnext http.HandlerFunc, // downstream handler\n\tfn func() error,\n\ttimeout int, // seconds, in total\n\tretries int,\n) {\n}\n"
	got := declaredNames(src)
	if missing := hasAll(got, "next", "fn", "timeout", "retries"); len(missing) > 0 {
		t.Errorf("params after a trailing comment not bound: missing %v (got %v)", missing, got)
	}
	if slices.Contains(got, "in") {
		t.Errorf("a word from a comment was bound: %v", got)
	}
	src = "package p\n\nx := 1 // wraps func (see below\nfunc real(\n\ta int,\n) {\n}\n"
	if missing := hasAll(declaredNames(src), "x", "a"); len(missing) > 0 {
		t.Errorf("a func in a comment shadowed the real signature: missing %v", missing)
	}
}

// TestGoDeclaredNames_TrailingCommentBindsNothing pins #500 items 1-2: a
// trailing `//` comment is unmasked on a line with no quote, so its text reached
// the `:=` and var-block extractors. A name bound from a comment masks a real
// hallucination of that name.
func TestGoDeclaredNames_TrailingCommentBindsNothing(t *testing.T) {
	got := declaredNames("package p\n\nfunc f() {\n\tx := 1 // or y := 2\n}\n")
	if slices.Contains(got, "y") || !slices.Contains(got, "x") {
		t.Errorf(":= in a trailing comment: got %v, want x bound and y not", got)
	}
	got = declaredNames("package p\n\nvar (\n\ta = 1 // see (x\n\tb = 2\n)\n\nfunc main() {\n\tresult = compute()\n}\n")
	if missing := hasAll(got, "a", "b"); len(missing) > 0 {
		t.Errorf("var block after a comment paren: missing %v (got %v)", missing, got)
	}
	if slices.Contains(got, "result") || slices.Contains(got, "func") {
		t.Errorf("text outside the var block bound: %v", got)
	}
}

// TestGoDeclaredNames_CommentAfterBlockOpener: with the comment cut, a
// `var ( // note` opener now starts a block (before, the trailing comment failed
// reGoVarBlockOpen and nothing inside was bound). A quoted line's own `//` stays
// masked, and the comment after it binds nothing.
func TestGoDeclaredNames_CommentAfterBlockOpener(t *testing.T) {
	got := declaredNames("package p\n\nvar ( // group\n\ta = 1\n\tb = 2\n)\n\nconst ( // c\n\tk = 1\n) // end\n\nfunc f() {\n\tu := \"http://x\" // y := 1\n\tv := 2 // w := \"q\"\n}\n")
	if missing := hasAll(got, "a", "b", "k", "u", "v"); len(missing) > 0 {
		t.Errorf("missing %v (got %v)", missing, got)
	}
	for _, bogus := range []string{"y", "w"} {
		if slices.Contains(got, bogus) {
			t.Errorf("%q bound from a comment: %v", bogus, got)
		}
	}
}

// TestGoDeclaredNames_BlockCommentCloseOnParamLine pins #500 item 3: a block
// comment closing on a wrapped-param line must not hide the name after it. The
// stripper used to leave the carried comment's `*/`, which sat between the comma
// and the name so reGoParamName's `[,(]\s*` anchor missed it.
func TestGoDeclaredNames_BlockCommentCloseOnParamLine(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{"package p\n\nfunc F(\n\ta int, /* note\n\t   more */\n\tc int,\n) {\n}\n", []string{"a", "c"}},
		{"package p\n\nfunc F(\n\t/* note\n\t   more */\n\tc int,\n) {\n}\n", []string{"c"}},
		{"package p\n\nfunc F(\n\ta int, /* note\n\t   more */ c int,\n\td int,\n) {\n}\n", []string{"a", "c", "d"}},
		{"package p\n\nfunc F(\n\tp string, /* (legacy\n\tstill comment */ n int,\n\tm int,\n) {\n}\n", []string{"m", "n", "p"}},
	}
	for _, c := range cases {
		if got := declaredNames(c.src); !slices.Equal(got, c.want) {
			t.Errorf("%q\n  got  %v\n  want %v", c.src, got, c.want)
		}
	}
}

// TestGoDeclaredNames_WrappedGenericSignature pins #500 item 4: gofmt wraps a
// long type-parameter list too, and goFuncGroups ignored `[`, so a line ending
// inside one started no carry and the parameter list after it was never
// scanned; an `interface{ ~int }` constraint read as the body brace. Type
// parameters themselves are types and stay unbound (see #474).
func TestGoDeclaredNames_WrappedGenericSignature(t *testing.T) {
	cases := []struct {
		src  string
		want []string
	}{
		{"package p\n\nfunc Map[\n\tT any,\n](xs []T, fn func(T) T) {\n\tfn(xs[0])\n}\n", []string{"fn", "xs"}},
		{"package p\n\nfunc Map[T any,\n\tU any](\n\txs []T,\n\tfn func(T) U,\n) []U {\n\treturn nil\n}\n", []string{"fn", "xs"}},
		{"package p\n\nfunc Sum[T interface{ ~int }](xs []T) (total T) {\n\treturn\n}\n", []string{"total", "xs"}},
		{"package p\n\nfunc Sum[\n\tT interface {\n\t\t~int | ~int64\n\t},\n](\n\txs []T,\n) T {\n\treturn 0\n}\n", []string{"xs"}},
		{"package p\n\nfunc (s *Set[K]) Add(\n\tk K,\n) []K {\n\treturn nil\n}\n", []string{"k", "s"}},
		{"package p\n\nfunc f() map[string]int {\n\tm := map[string]int{}\n\treturn m\n}\n", []string{"m"}},
		// A func-typed constraint's own parameter names are inside the type list
		// too: x must stay unbound, single-line and wrapped (review on #504).
		{"package p\n\nfunc Apply[F func(x int) bool](f F) {\n}\n", []string{"f"}},
		{"package p\n\nfunc Apply[\n\tF func(x int) bool,\n](f F) {\n}\n", []string{"f"}},
	}
	for _, c := range cases {
		if got := declaredNames(c.src); !slices.Equal(got, c.want) {
			t.Errorf("%q\n  got  %v\n  want %v", c.src, got, c.want)
		}
	}
}
