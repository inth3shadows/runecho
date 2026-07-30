package guard

import (
	"strings"
	"testing"
)

// declShapeOf builds the index over source and returns the sole shape for name.
func declShapeOf(t *testing.T, source, name string) pyDeclShape {
	t.Helper()
	shapes := newPyDeclIndex(TextToAddedLines(source)).shapesFor(name)
	if len(shapes) != 1 {
		t.Fatalf("shapesFor(%q): want exactly 1 declaration, got %d (%+v)", name, len(shapes), shapes)
	}
	return shapes[0]
}

func TestPyDeclKeywords(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		fn       string
		keywords []string
		kwStar   bool
	}{
		{
			name:     "plain positional params are keyword-callable",
			src:      "def f(a, b):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b"},
		},
		{
			name: "no params",
			src:  "def f():\n    pass\n",
			fn:   "f",
		},
		{
			name:     "defaults",
			src:      "def f(a, b=1, c=None):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b", "c"},
		},
		{
			name:     "annotations",
			src:      "def f(a: int, b: str = \"x\"):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b"},
		},
		{
			// A string default containing a comma must not split the list. Masking is
			// what makes this work: the quote bytes stay, the content is blanked.
			name:     "string default containing a comma and a paren",
			src:      "def f(sep=\", \", end=\")\"):\n    pass\n",
			fn:       "f",
			keywords: []string{"sep", "end"},
		},
		{
			// The FP-critical case: `a` is a real parameter name but `f(a=1)` is an
			// error, so it must NOT be in the accepted set.
			name:     "positional-only params before / are dropped",
			src:      "def f(a, b, /, c, d=1):\n    pass\n",
			fn:       "f",
			keywords: []string{"c", "d"},
		},
		{
			name: "all params positional-only",
			src:  "def f(a, b, /):\n    pass\n",
			fn:   "f",
		},
		{
			name:     "keyword-only after bare star stay callable",
			src:      "def f(a, *, b, c=1):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b", "c"},
		},
		{
			name:     "star-args name excluded, keyword-only after it kept",
			src:      "def f(a, *args, b, c=2):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b", "c"},
		},
		{
			name:     "double-star sets HasKwStar and excludes its own name",
			src:      "def f(a, **kwargs):\n    pass\n",
			fn:       "f",
			keywords: []string{"a"},
			kwStar:   true,
		},
		{
			// An annotated **kwargs still accepts every keyword. Missing this would
			// report a knowable set for a declaration that accepts anything — a
			// false-positive source, which is why it has its own case.
			name:     "annotated double-star still sets HasKwStar",
			src:      "def f(a, **kwargs: object):\n    pass\n",
			fn:       "f",
			keywords: []string{"a"},
			kwStar:   true,
		},
		{
			name:     "annotated star-args name still excluded",
			src:      "def f(a, *args: int, b):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b"},
		},
		{
			name:     "everything at once",
			src:      "def f(a, b: int, /, c, d: str = \"x\", *args, e, f2: int = 3, **kw):\n    pass\n",
			fn:       "f",
			keywords: []string{"c", "d", "e", "f2"},
			kwStar:   true,
		},
		{
			name:     "async def",
			src:      "async def f(a, b=1):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b"},
		},
		{
			name:     "multi-line signature",
			src:      "def f(\n    a,\n    b=1,\n):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b"},
		},
		{
			// A parameter list broken mid-list must still split into two names, not
			// concatenate into one — the reason signatureContent joins lines with a
			// space rather than with nothing.
			name:     "line break directly before a comma",
			src:      "def f(a\n, b):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b"},
		},
		{
			name:     "subscripted annotation with its own comma",
			src:      "def f(a: Dict[str, int], b=1):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b"},
		},
		{
			name:     "default is a call with its own arguments",
			src:      "def f(a=make(1, 2), b=3):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b"},
		},
		{
			name:     "trailing comma",
			src:      "def f(a, b,):\n    pass\n",
			fn:       "f",
			keywords: []string{"a", "b"},
		},
		{
			name:     "inline body on the def line",
			src:      "def f(a, b=1): return a\n",
			fn:       "f",
			keywords: []string{"a", "b"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := declShapeOf(t, tc.src, tc.fn)
			if got.Unknowable {
				t.Fatalf("Unknowable = true, want a readable signature")
			}
			if strings.Join(got.Keywords, ",") != strings.Join(tc.keywords, ",") {
				t.Errorf("Keywords = %v, want %v", got.Keywords, tc.keywords)
			}
			if got.HasKwStar != tc.kwStar {
				t.Errorf("HasKwStar = %v, want %v", got.HasKwStar, tc.kwStar)
			}
			if got.Decorated {
				t.Errorf("Decorated = true, want false")
			}
		})
	}
}

func TestPyDeclUnknowable(t *testing.T) {
	for _, tc := range []struct{ name, src, fn string }{
		{
			// A lambda's own parameter commas sit at the enclosing list's bracket
			// depth, so `key=lambda a, b: a` splits into two unreadable segments.
			// Reading it as two parameters would invent an accepted name.
			name: "top-level lambda default",
			src:  "def f(key=lambda a, b: a):\n    pass\n",
			fn:   "f",
		},
		{
			name: "parameter list never closes",
			src:  "def f(a, b\n",
			fn:   "f",
		},
		{
			name: "segment is not a parameter",
			src:  "def f(a, self.b):\n    pass\n",
			fn:   "f",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := declShapeOf(t, tc.src, tc.fn)
			if !got.Unknowable {
				t.Errorf("Unknowable = false (keywords %v), want true — an unreadable signature must abstain", got.Keywords)
			}
			if got.usable() {
				t.Errorf("usable() = true; an unknowable shape must never be compared against")
			}
		})
	}
}

func TestPyDeclLineIsTheDefLine(t *testing.T) {
	// Not the decorator's line: a finding must point at the signature the reader
	// compares the call against.
	if got := declShapeOf(t, "\n\n@deco\ndef f(a):\n    pass\n", "f").Line; got != 4 {
		t.Errorf("Line = %d, want 4 (the def line, not the decorator on line 3)", got)
	}
}

func TestPyDeclDecoratorDetection(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"undecorated", "x = 1\n\ndef f(a):\n    pass\n", false},
		{"single decorator", "@retry\ndef f(a):\n    pass\n", true},
		{"stacked decorators", "@a\n@b\ndef f(a):\n    pass\n", true},
		{"decorator with a blank line below it", "@retry\n\ndef f(a):\n    pass\n", true},
		{"decorator with a comment below it", "@retry\n# why\ndef f(a):\n    pass\n", true},
		{
			// The case a pattern match gets wrong: the line directly above the def is
			// a bare `)`. Walking up by bracket balance finds the `@`.
			name: "multi-line decorator call",
			src:  "@app.route(\n    \"/x\",\n    methods=[\"GET\"],\n)\ndef f(a):\n    pass\n",
			want: true,
		},
		{
			// The control the case above needs: a preceding statement that also ends
			// in `)`. Treating any `)` as a decorator tail would abstain on the most
			// ordinary shape in Python — a module-level call above a function.
			name: "preceding call statement is not a decorator",
			src:  "logger = logging.getLogger(\n    __name__,\n)\n\ndef f(a):\n    pass\n",
			want: false,
		},
		{
			name: "preceding def is not a decorator",
			src:  "def g(z):\n    pass\n\ndef f(a):\n    pass\n",
			want: false,
		},
		{
			// A `@` inside a docstring is masked, so it cannot fabricate a decorator.
			name: "at-sign inside a docstring above the def",
			src:  "\"\"\"\n@retry\n\"\"\"\n\n\ndef f(a):\n    pass\n",
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := declShapeOf(t, tc.src, "f").Decorated; got != tc.want {
				t.Errorf("Decorated = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPyDeclColumnZeroOnly(t *testing.T) {
	// Methods, nested defs and conditionally-declared functions are all indented.
	// An unqualified call cannot reach a method at all; the other two are the
	// documented reach cost of not parsing the whole file.
	src := `def top(a):
    def inner(zzz):
        pass
    return inner

class C:
    def method(self, mkw=1):
        pass

if TYPE_CHECKING:
    def conditional(ckw=1):
        pass
`
	idx := newPyDeclIndex(TextToAddedLines(src))
	if got := idx.shapesFor("top"); len(got) != 1 {
		t.Errorf("column-zero def `top`: got %d shapes, want 1", len(got))
	}
	for _, absent := range []string{"inner", "method", "conditional"} {
		if got := idx.shapesFor(absent); len(got) != 0 {
			t.Errorf("shapesFor(%q) = %+v; only column-zero defs may be resolved", absent, got)
		}
	}
}

func TestPyDeclDocstringIsNotADeclaration(t *testing.T) {
	// The #145 class: a `def` inside a docstring is prose, not code. Reading it as a
	// declaration would compare real calls against an invented signature.
	src := "\"\"\"Usage:\n\ndef fetch(url, retries=3):\n    ...\n\"\"\"\n\ndef fetch(url, timeout=1):\n    return url\n"
	got := declShapeOf(t, src, "fetch")
	if strings.Join(got.Keywords, ",") != "url,timeout" {
		t.Errorf("Keywords = %v, want the REAL signature [url timeout]", got.Keywords)
	}
	if got.Line != 7 {
		t.Errorf("Line = %d, want 7 (the real def, not the one in the docstring)", got.Line)
	}
}

func TestPyDeclRepeatedDeclarationsBothReturned(t *testing.T) {
	// Two column-zero declarations of one name, second overwriting the first. Both
	// are returned so soleShape can see they disagree and the caller abstains.
	src := "def f(a, fast=True):\n    pass\n\ndef f(a, slow=True):\n    pass\n"
	got := newPyDeclIndex(TextToAddedLines(src)).shapesFor("f")
	if len(got) != 2 {
		t.Fatalf("want 2 shapes, got %d: %+v", len(got), got)
	}
	if _, ok := soleShape(got); ok {
		t.Errorf("soleShape accepted two disagreeing declarations; the caller must abstain")
	}
}

func TestPyDeclIndexDynamic(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"clean file", "def f(a):\n    pass\n", false},
		{"star import", "from os.path import *\n\ndef f(a):\n    pass\n", true},
		{"globals assignment", "def f(a):\n    pass\n\nglobals()[\"f\"] = g\n", true},
		{"exec", "exec(src)\n\ndef f(a):\n    pass\n", true},
		{
			// setattr/vars/importlib are deliberately NOT markers here: they bind
			// object attributes or module objects, neither of which can rewrite a
			// signature. Treating them as markers abstained 218 of 283 checkable
			// stdlib sites for nothing.
			name: "setattr and importlib are not markers for this check",
			src:  "import importlib\n\ndef f(a):\n    setattr(o, \"x\", 1)\n",
			want: false,
		},
		{
			name: "marker named only inside a docstring",
			src:  "\"\"\"Do not call eval( here.\"\"\"\n\ndef f(a):\n    pass\n",
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newPyDeclIndex(TextToAddedLines(tc.src)).dynamic; got != tc.want {
				t.Errorf("dynamic = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPyDeclParamsIncludeNestedDefs(t *testing.T) {
	// The shadow set must see a NESTED def's parameters too: a parameter named the
	// same as a module-level function shadows it inside that function, and this
	// check cannot tell whether a call sits inside it without scope analysis.
	src := "def outer(a):\n    def inner(fetch, b=1):\n        pass\n\ndef fetch(url):\n    return url\n"
	params := newPyDeclIndex(TextToAddedLines(src)).params()
	for _, want := range []string{"a", "fetch", "b", "url"} {
		if _, ok := params[want]; !ok {
			t.Errorf("params() missing %q; got %v", want, params)
		}
	}
}

func TestPyDeclSignatureCeiling(t *testing.T) {
	// A parameter list longer than the ceiling abstains rather than scanning on.
	var b strings.Builder
	b.WriteString("def f(\n")
	for i := 0; i < pyDeclMaxSignatureLines+5; i++ {
		b.WriteString("    p")
		b.WriteString(strings.Repeat("x", i%3))
		b.WriteString("=1,\n")
	}
	b.WriteString("):\n    pass\n")
	if got := declShapeOf(t, b.String(), "f"); !got.Unknowable {
		t.Errorf("a signature past the %d-line ceiling must abstain, got keywords %v",
			pyDeclMaxSignatureLines, got.Keywords)
	}
}

func TestPyDeclDynamicMarkerNeedsAWordBoundary(t *testing.T) {
	// A bare strings.Contains made `def get_locals(a, b)` set dynamic=true, which
	// returns nil for the WHOLE file — an ordinary helper name silently switched the
	// check off for its entire module. Found by adversarial review.
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"helper name ending in locals", "def get_locals(a, b):\n    return a\n", false},
		{"helper name ending in eval", "def retrieval(a):\n    return a\n", false},
		{"helper name ending in exec", "def do_exec(a):\n    return a\n", false},
		{"helper name ending in globals", "def merge_globals(a):\n    return a\n", false},
		{"the real builtin still counts", "def f(a):\n    globals()[\"f\"] = a\n", true},
		{"the real builtin at line start", "eval(src)\n", true},
		// A method call cannot rebind a module-level name, which is the only reason
		// these markers abstain in the first place.
		{"attribute access is a method, not the builtin", "def f(a):\n    return self.eval(a)\n", false},
		{"builtin after an operator", "def f(a):\n    return 1 + eval(a)\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := newPyDeclIndex(TextToAddedLines(tc.src)).dynamic; got != tc.want {
				t.Errorf("dynamic = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPyDeclTabAfterDefIsStillADeclaration(t *testing.T) {
	// Both def regexes accept `def[ \t]+`, so the cheap prefilter must not require a
	// literal space — a tab-separated declaration would be invisible to resolution
	// AND absent from the parameter shadow set.
	got := declShapeOf(t, "def\tfetch(url, timeout=1):\n    return url\n", "fetch")
	if strings.Join(got.Keywords, ",") != "url,timeout" {
		t.Errorf("Keywords = %v, want [url timeout]", got.Keywords)
	}
	if _, ok := newPyDeclIndex(TextToAddedLines("def\thelper(fetch):\n    pass\n")).params()["fetch"]; !ok {
		t.Errorf("a tab-separated def's parameters must reach the shadow set")
	}
}
