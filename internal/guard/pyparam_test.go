package guard

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func pyLines(src string) []AddedLine {
	var lines []AddedLine
	for i, s := range strings.Split(src, "\n") {
		lines = append(lines, AddedLine{LineNo: i + 1, Text: s})
	}
	return lines
}

func TestPyParamNames(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"simple positional", "def f(a, b, c):", []string{"a", "b", "c"}},
		{"typed params bind name only", "def f(cb: Handler, n: int):", []string{"cb", "n"}},
		{"default value stripped", "def f(x=1, y=compute()):", []string{"x", "y"}},
		{"typed with default", "def f(cb: Callable[[], T] = _start):", []string{"cb"}},
		{"varargs and kwargs", "def f(*args, **kwargs):", []string{"args", "kwargs"}},
		{"keyword-only marker binds nothing", "def f(a, *, b):", []string{"a", "b"}},
		{"positional-only marker binds nothing", "def f(a, /, b):", []string{"a", "b"}},
		{
			"multi-line signature",
			"def build(\n    starter: Callable[[], Thread] = _default,\n    n: int = 3,\n):",
			[]string{"starter", "n"},
		},
		{"lambda params", "sort(xs, key=lambda item, idx: item)", []string{"item", "idx"}},
		{"lambda inline in call", "setattr(o, \"x\", lambda name, fetch, **kw: fetch())", []string{"name", "fetch", "kw"}},
		{"async def", "async def handle(req: Request, timeout: float):", []string{"req", "timeout"}},
		{"no params", "def f():", nil},
		{"not a def line", "x = f(a, b)", nil},
		// The type annotation must never appear as a bound name — this is the
		// false-negative guard: folding a type would mask a hallucinated call to it.
		{"type name never bound", "def f(handler: SomeType):", []string{"handler"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PyParamNames(pyLines(tc.src))
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if len(got) == 0 && len(want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("PyParamNames(%q) = %v, want %v", tc.src, got, want)
			}
		})
	}
}

// The three live survivors from the decision log, end-to-end through Run: a
// parameter used as a callable must not flag.
func TestRun_ParamUsedAsCallable_NotFlagged(t *testing.T) {
	cases := []struct{ name, src string }{
		{"typed callable param + default", "def build(\n    watchdog_starter: Callable[[], Thread] = _start,\n):\n    watchdog_starter()"},
		{"typed callable param", "def pump(src, dst, transform: Callable[[str], Any]):\n    out = transform(line)"},
		{"lambda param", "monkeypatch.setattr(sg, \"load_or_fetch\", lambda name, fetch, **kw: fetch())"},
	}
	known := map[string]struct{}{
		"build": {}, "pump": {}, "src": {}, "dst": {}, "line": {},
		"monkeypatch": {}, "sg": {},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if v := Run(known, "", []FileDiff{{Path: "m.py", AddedLines: pyLines(tc.src)}}); len(v) != 0 {
				t.Errorf("param-as-callable must not flag, got %+v", v)
			}
		})
	}
}

// A genuine hallucination whose name collides with nothing must still flag —
// folding parameter names must not blanket-suppress. And crucially a type
// annotation used as a constructor call is still caught (the type is never folded).
func TestRun_ParamFoldPreservesRecall(t *testing.T) {
	// zznope() is not a param, def, import, or known symbol anywhere.
	src := "def pump(transform: Callable[[str], Any]):\n    transform(zznope())"
	v := Run(map[string]struct{}{"pump": {}}, "", []FileDiff{{Path: "m.py", AddedLines: pyLines(src)}})
	if len(v) != 1 || v[0].Symbol != "zznope" {
		t.Fatalf("a real hallucinated call alongside a param must still flag, got %+v", v)
	}

	// The type annotation `Handler` called as `Handler()` is a hallucination when
	// Handler is defined nowhere: folding the param name `cb` must not fold its type.
	src2 := "def f(cb: Handler):\n    return Handler()"
	v2 := Run(map[string]struct{}{"f": {}}, "", []FileDiff{{Path: "m.py", AddedLines: pyLines(src2)}})
	if len(v2) != 1 || v2[0].Symbol != "Handler" {
		t.Fatalf("a called type annotation must still flag, got %+v", v2)
	}
}
