package guard

import (
	"slices"
	"sort"
	"testing"
)

// issue474Repro is the file from #474 verbatim.
const issue474Repro = `package p

import (
	"testing"

	. "github.com/onsi/gomega"
)

func Sum[T ~int | ~float64](xs []T) T {
	s := T(0)
	for _, x := range xs {
		s += x
	}
	return s
}

func TestX(t *testing.T) {
	g := NewWithT(t)
	g.Expect(Sum([]int{1})).To(Equal(1))
	Expect(1).To(BeNumerically(">", 0))
}
`

func runGoSrc(src string) []string {
	v := Run(map[string]struct{}{}, "", []FileDiff{{Path: "p/sum_test.go", AddedLines: TextToAddedLines(src)}})
	var out []string
	for _, x := range v {
		out = append(out, x.Symbol)
	}
	sort.Strings(out)
	return out
}

func TestRun_Issue474Repro(t *testing.T) {
	if got := runGoSrc(issue474Repro); len(got) != 0 {
		t.Fatalf("#474 repro: want no violations, got %v", got)
	}
}

// Type parameters alone (no dot-import) must resolve, so the dot-import abstain
// is not what makes T(0) pass.
func TestRun_TypeParamConversionNotFlagged(t *testing.T) {
	src := `package p

func Sum[T ~int | ~float64](xs []T) T {
	s := T(0)
	return s
}
`
	if got := runGoSrc(src); len(got) != 0 {
		t.Fatalf("want no violations, got %v", got)
	}
}

func TestGoDeclaredNames_TypeParams(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string // names that must be bound
		not       []string // names that must stay unbound
	}{
		{"union constraint", "func Sum[T ~int | ~float64](xs []T) T {\n}\n", []string{"T", "xs"}, nil},
		{"multiple params", "func Get[K comparable, V any](m map[K]V) V {\n}\n", []string{"K", "V", "m"}, nil},
		{"grouped", "func Pair[A, B any](a A, b B) {\n}\n", []string{"A", "B", "a", "b"}, nil},
		{"nested brackets in constraint", "func F[T interface{ ~[]E }, E any](x T) {\n}\n", []string{"T", "E", "x"}, nil},
		{"func-typed constraint keeps its param names unbound", "func Ap[F func(x int) bool](f F) {\n}\n", []string{"F", "f"}, []string{"x"}},
		{"generic type", "type List[T any] struct {\n\titems []T\n}\n", []string{"T"}, nil},
		{"generic type multi", "type Pair[K comparable, V any] struct{}\n", []string{"K", "V"}, nil},
		{"method on generic type", "func (l *List[T]) Push(v T) {\n}\n", []string{"T", "l", "v"}, nil},
		{"method on generic type multi", "func (m Map[K, V]) Get(k K) V {\n}\n", []string{"K", "V", "m", "k"}, nil},
		{"blank type param", "func F[_ any, T any](x T) {\n}\n", []string{"T", "x"}, []string{"_"}},
		{"type args are not declarations", "func f(m Map[string, Foo]) {\n}\n", []string{"m"}, []string{"Foo", "string"}},
		{"func literal arg is not a receiver", "g := func(m List[Foo]) Bar {\n}\n", nil, []string{"Foo"}},
		{"result type index is not a param list", "func f() map[K]V {\n}\n", nil, []string{"K", "V"}},
	}
	for _, c := range cases {
		got := declaredNames("package p\n\n" + c.src)
		if miss := hasAll(got, c.want...); len(miss) > 0 {
			t.Errorf("%s: not bound: %v (got %v)", c.name, miss, got)
		}
		for _, n := range c.not {
			if slices.Contains(got, n) {
				t.Errorf("%s: %q must stay unbound (got %v)", c.name, n, got)
			}
		}
	}
}

// A method hunk whose `[T]` receiver and generic declaration sit in the
// pre-edit file resolve through the whole-file fold the hook path uses.
func TestFoldInFileDefs_TypeParams(t *testing.T) {
	syms := map[string]struct{}{}
	FoldInFileDefs(syms, TextToAddedLines("package p\n\nfunc (l *List[T]) Push(v T) {\n}\n"), LangGo)
	if _, ok := syms["T"]; !ok {
		t.Fatalf("receiver type param T not folded: %v", syms)
	}
}

func TestGoHasDotImport(t *testing.T) {
	yes := []string{
		"import . \"github.com/onsi/gomega\"\n",
		"import (\n\t\"testing\"\n\t. \"github.com/onsi/ginkgo/v2\"\n)\n",
	}
	no := []string{
		"import \"fmt\"\n",
		"import (\n\t\"fmt\"\n\t_ \"embed\"\n\tg \"github.com/onsi/gomega\"\n)\n",
		"func f() {\n\tx := 1\n}\n",
	}
	for _, s := range yes {
		if !GoHasDotImport(TextToAddedLines(s)) {
			t.Errorf("want dot-import in %q", s)
		}
	}
	for _, s := range no {
		if GoHasDotImport(TextToAddedLines(s)) {
			t.Errorf("want no dot-import in %q", s)
		}
	}
}

// The dot-import is elsewhere in the file (callers set GoDotImport, #474 G8).
func TestRun_DotImportOutsideHunk(t *testing.T) {
	hunk := TextToAddedLines("Expect(1).To(Equal(1))\n")
	if v := Run(map[string]struct{}{}, "", []FileDiff{{Path: "a_test.go", AddedLines: hunk, GoDotImport: true}}); len(v) != 0 {
		t.Errorf("hook flag: want none, got %+v", v)
	}
}

// NEGATIVE: the fix must not blind the guard.
func TestRun_StillFlagsInventedGoCalls(t *testing.T) {
	noDot := `package p

import "fmt"

func f() {
	fmt.Println(1)
	notARealFunc()
}
`
	if got := runGoSrc(noDot); !slices.Equal(got, []string{"notARealFunc"}) {
		t.Errorf("no dot-import: want [notARealFunc], got %v", got)
	}
	generic := `package p

func Sum[T ~int | ~float64](xs []T) T {
	s := T(0)
	notARealFunc()
	return s
}
`
	if got := runGoSrc(generic); !slices.Equal(got, []string{"notARealFunc"}) {
		t.Errorf("generic func: want [notARealFunc], got %v", got)
	}
	// A named-import file (not a dot-import) keeps flagging too.
	named := "package p\n\nimport g \"github.com/onsi/gomega\"\n\nfunc f() {\n\tg.Expect(1)\n\tinvented()\n}\n"
	if got := runGoSrc(named); !slices.Equal(got, []string{"invented"}) {
		t.Errorf("named import: want [invented], got %v", got)
	}
}

// G1: dot-import spellings the first detector missed.
func TestGoHasDotImport_Spellings(t *testing.T) {
	yes := []string{
		"import (\n\t. \"github.com/onsi/gomega\" // matchers\n)\n",
		"import . \"github.com/onsi/gomega\" //nolint:revive\n",
		"import ( // deps\n\t. \"x\"\n)\n",
		"import . `x`\n",
		"import ( . \"x\" )\n",
		"import .\"x\"\n",
		"import (\"fmt\"; . \"x\")\n",
		"import (\n\t\"fmt\"\n\t/* c */ . \"x\"\n)\n",
	}
	for _, s := range yes {
		if !GoHasDotImport(TextToAddedLines("package p\n\n" + s)) {
			t.Errorf("G1: want dot-import in %q", s)
		}
	}
}

// G3: not inside a raw string or a block comment, and not past the imports.
func TestGoHasDotImport_NotInLiteralsOrBody(t *testing.T) {
	no := []string{
		"package p\n\nvar s = `\nimport . \"x\"\n`\n",
		"package p\n\n/*\nimport . \"x\"\n*/\n",
		"package p\n\nimport \"fmt\"\n\nfunc f() {}\n\nimport . \"x\"\n",
		"package p\n\n// import . \"x\"\nimport \"fmt\"\n",
	}
	for _, s := range no {
		if GoHasDotImport(TextToAddedLines(s)) {
			t.Errorf("G3: want no dot-import in %q", s)
		}
	}
}

// G2: a dot-import supplies exported names only; a lowercase ghost still flags.
func TestRun_DotImportAbstainsOnExportedOnly(t *testing.T) {
	src := "package p\n\nimport . \"x\"\n\nfunc f() {\n\tExported()\n\tghost()\n}\n"
	if got := runGoSrc(src); !slices.Equal(got, []string{"ghost"}) {
		t.Errorf("G2: want [ghost], got %v", got)
	}
}

// G6: a func literal / func type whose RESULT is generic or a map binds nothing.
func TestGoDeclaredNames_FuncResultTypeArgsUnbound(t *testing.T) {
	for _, src := range []string{
		"x := func(ctx Ctx) Result[User] {\n}\n",
		"x := func() map[Key]Val {\n}\n",
		"type Mk func(a int) Result[Foo, Bar]\n",
	} {
		got := declaredNames("package p\n\n" + src)
		for _, bad := range []string{"User", "Key", "Val", "Foo", "Bar"} {
			if slices.Contains(got, bad) {
				t.Errorf("G6: %q bound by %q (got %v)", bad, src, got)
			}
		}
	}
}

// G7: an array length is not a type-parameter list.
func TestGoDeclaredNames_ArrayLengthNotTypeParam(t *testing.T) {
	for _, src := range []string{
		"type T [ghost(1)]int\n",
		"type Buf [computeLen]byte\n",
		"type A [pkg.N]int\n",
		"type B [N * 2]int\n",
	} {
		got := declaredNames("package p\n\n" + src)
		for _, bad := range []string{"ghost", "computeLen", "pkg", "N"} {
			if slices.Contains(got, bad) {
				t.Errorf("G7: %q bound by %q (got %v)", bad, src, got)
			}
		}
	}
	// A real constrained list still binds.
	if got := declaredNames("package p\n\ntype L[A, B any] struct{}\n"); !slices.Equal(got, []string{"A", "B", "L"}) {
		t.Errorf("type L[A, B any]: got %v", got)
	}
}

// G11: a trailing comma ends a type-parameter list, so it binds even with a
// pointer constraint; without one, `[P *C]` is an array length (Go spec), as is
// a parenthesised call.
func TestTypeDeclParams_G11(t *testing.T) {
	cases := []struct {
		content string
		want    []string
	}{
		{"T *C,", []string{"T"}},
		{"K comparable, V any,", []string{"K", "V"}},
		{"P *C", nil},
		{"ghost (1)", nil},
	}
	for _, tc := range cases {
		if got := typeDeclParams(tc.content); !slices.Equal(got, tc.want) {
			t.Errorf("typeDeclParams(%q) = %v, want %v", tc.content, got, tc.want)
		}
	}
}

// G13: a dot spec after a block comment closes on its line, and an import on
// the package line.
func TestGoHasDotImport_G13(t *testing.T) {
	yes := []string{
		"package p\n\nimport (\n\t/* a\n\tb */ . \"x\"\n)\n",
		"package p; import . \"x\"\n",
	}
	for _, s := range yes {
		if !GoHasDotImport(TextToAddedLines(s)) {
			t.Errorf("G13: want dot-import in %q", s)
		}
	}
}
