package guard

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func jsParams(t *testing.T, src string) []string {
	t.Helper()
	got := JSParamNames(TextToAddedLines(src))
	sort.Strings(got)
	return got
}

func hasName(ns []string, n string) bool {
	for _, x := range ns {
		if x == n {
			return true
		}
	}
	return false
}

// TestJSParamNamesBinds covers the shapes that MUST bind — the false-positive
// direction. The multi-line cases are the point: #302's live FP (`onChange` in
// coriolis-local SoloPage.tsx) is the canonical React component signature, and
// no single-line regex can see it.
func TestJSParamNamesBinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []string
	}{
		{"multiline destructured function param (#302's live FP)", `function Picker({
  value,
  onChange,
  disabled,
}: PickerProps) {
  return onChange(value);
}`, []string{"disabled", "onChange", "value"}},

		{"single-line destructured function param", `function Single({ v2, onChange2 }: PickerProps) {}`,
			[]string{"onChange2", "v2"}},

		{"multiline destructured arrow param", `const Arrow = ({
  v3,
  onChange3,
}: Props) => onChange3(v3);`, []string{"onChange3", "v3"}},

		{"plain annotated params", `function handle(ctx: RouteContext, next: NextFn) {}`,
			[]string{"ctx", "next"}},

		{"rest and defaults", `function g(x: Foo = mkFoo(), ...rest: T[]) {}`,
			[]string{"rest", "x"}},

		{"object rename binds the value side only", `function r({ a: renamed }) {}`,
			[]string{"renamed"}},

		{"array pattern param", `function arr([first, second]: [A, B]) {}`,
			[]string{"first", "second"}},

		{"async function", `async function load({ id, signal }) {}`,
			[]string{"id", "signal"}},

		{"arrow with return annotation before the fat arrow", `const f = (a): Foo => a;`,
			[]string{"a"}},

		{"multiline arrow spanning the close", `const cb = (
  err,
  data
) => data;`, []string{"data", "err"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := jsParams(t, tc.src)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("JSParamNames = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestJSParamNamesBindsNothing is the false-NEGATIVE direction, and it is the
// half that matters more. Every name folded into the additive known set is a
// hallucination of that name that stops being caught, so a parameter list in a
// TYPE position must bind nothing at all.
func TestJSParamNamesBindsNothing(t *testing.T) {
	for _, tc := range []struct{ name, src, mustNotBind string }{
		{"interface member arrow type", `interface Props {
  value: string | null;
  onChange: (v: string | null) => void;
}`, "v"},

		{"one-line type alias arrow", `type Handler = (evt: MouseEvent) => void;`, "evt"},

		{"exported type alias", `export type Reducer = (state: S, action: A) => S;`, "state"},

		{"interface method signature", `interface Api {
  fetch(url: string): Promise<void>;
}`, "url"},

		{"multiline type alias object", `type Config = {
  onDone: (result: R) => void;
};`, "result"},

		{"declare-d type alias", `declare type Cb = (chunk: Buffer) => void;`, "chunk"},

		{"plain call is not a parameter list", `doThing(userId, payload);`, "userId"},

		{"class property function type (guarded only by the ':' test)", `class C {
  onChange: (v: string) => void;
}`, "v"},

		{"multiline class property function type", `class C {
  handler: (
    a: A,
    b: B
  ) => void;
}`, "a"},

		{"object-literal arrow prop — knowingly given up, must stay unbound", `const opts = {
  onDone: (result: R) => log(result),
};`, "result"},

		{"annotated property arrow in an interface", `interface I {
  cb: (
    a: A,
    b: B
  ) => void;
}`, "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := jsParams(t, tc.src)
			if hasName(got, tc.mustNotBind) {
				t.Errorf("JSParamNames bound %q from a type position (got %v) — "+
					"this masks a real undefined reference", tc.mustNotBind, got)
			}
		})
	}
}

// TestJSParamNamesNoTypeLeak pins the reason JSDeclaredNames gave for excluding
// parameters in the first place: a TS annotation must never become a bound name.
func TestJSParamNamesNoTypeLeak(t *testing.T) {
	src := `function f({ value, onChange }: PickerProps, ctx: RouteContext, x: Foo = mkFoo()) {}`
	got := jsParams(t, src)
	for _, leak := range []string{"PickerProps", "RouteContext", "Foo", "mkFoo"} {
		if hasName(got, leak) {
			t.Errorf("type name %q leaked into bindings: %v", leak, got)
		}
	}
	for _, want := range []string{"value", "onChange", "ctx", "x"} {
		if !hasName(got, want) {
			t.Errorf("expected binding %q missing from %v", want, got)
		}
	}
}

// TestJSParamNamesClosesLiveFP is the end-to-end proof for #302: the exact
// shape that produced the 2026-08-16 false positive on `onChange` in
// coriolis-local's SoloPage.tsx (guard v0.43.1), run through Run() rather than
// through the extractor alone. An extractor-level assertion would not show that
// the fold actually reaches the additive check.
func TestJSParamNamesClosesLiveFP(t *testing.T) {
	src := `interface ControlVerdictPickerProps {
  value: ControlVerdict | null;
  onChange: (v: ControlVerdict | null) => void;
  disabled: boolean;
}

function ControlVerdictPicker({
  value,
  onChange,
  disabled,
}: ControlVerdictPickerProps) {
  return (
    <Button
      disabled={disabled}
      onClick={() => onChange(value === verdict ? null : verdict)}
    />
  );
}
`
	fd := FileDiff{Path: "SoloPage.tsx", AddedLines: TextToAddedLines(src)}
	// An empty index: nothing in the repo declares onChange, so if the parameter
	// does not bind it, the additive check must flag it — which is exactly what
	// happened in the log.
	got := Run(map[string]struct{}{}, "", []FileDiff{fd})
	for _, v := range got {
		if v.Symbol == "onChange" {
			t.Fatalf("onChange still flagged — #302's live false positive is not fixed (all: %v)", got)
		}
	}
}

// TestJSParamsDoNotMaskHallucination is the guard against buying that fix with a
// false negative: a genuinely undefined callee in the same file, with the same
// parameter list present, must still be caught.
func TestJSParamsDoNotMaskHallucination(t *testing.T) {
	src := `function ControlVerdictPicker({
  value,
  onChange,
}: Props) {
  totallyMadeUpHelper(value);
  return onChange(value);
}
`
	fd := FileDiff{Path: "SoloPage.tsx", AddedLines: TextToAddedLines(src)}
	got := Run(map[string]struct{}{}, "", []FileDiff{fd})
	var found bool
	for _, v := range got {
		if v.Symbol == "totallyMadeUpHelper" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the parameter fold swallowed a real hallucination: totallyMadeUpHelper "+
			"was not flagged (got %v)", got)
	}
}

// TestLocallyBoundNamesMultilineJSParams covers defect 2: LocallyBoundNames'
// JS parameter capture went through `[^)]*` regexes that cannot cross a
// newline, so a wrapped signature bound nothing. Python's twin was fixed long
// ago (rePyDefOpen's comment records it); the JS pair was left behind.
//
// The set is deliberately over-inclusive — it only suppresses dropped-import
// warnings — so this asserts reach, not precision.
func TestLocallyBoundNamesMultilineJSParams(t *testing.T) {
	bound := func(src string) map[string]struct{} {
		return LocallyBoundNames(LangJS, TextToAddedLines(src), nil)
	}
	for _, tc := range []struct{ name, src, want string }{
		{"multiline function param", `function Picker({
  value,
  onChange,
}: Props) {}`, "onChange"},
		{"multiline arrow param", `const Arrow = ({
  v3,
  onChange3,
}: Props) => onChange3(v3);`, "onChange3"},
		{"single-line still works (regression)", `function S({ v2, onChange2 }: Props) {}`, "onChange2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := bound(tc.src)[tc.want]; !ok {
				t.Errorf("%q not bound by LocallyBoundNames", tc.want)
			}
		})
	}

	// The arrow/function test must survive the multi-line path: a wrapped CALL's
	// arguments are not parameters, and binding them would suppress a real
	// dropped-import warning for a name passed as an argument.
	t.Run("multiline call arguments are not bound", func(t *testing.T) {
		src := `doThing(
  droppedImportName,
  other
);`
		if _, ok := bound(src)["droppedImportName"]; ok {
			t.Error("a multi-line call's arguments were bound as parameters — " +
				"this suppresses genuine dropped-import findings")
		}
	})
}

// TestJSParamNamesKeywordLedArrows pins review finding 1. The identifier-byte
// rejection in jsParamListOpen exists to skip calls before the line accumulator
// runs, but a KEYWORD may also sit directly before a real parameter list —
// `async (url) => …` above all, which is the dominant arrow form in the TS/React
// code this extractor serves. Rejecting on the raw byte dropped every one, while
// the identical non-async form bound correctly.
func TestJSParamNamesKeywordLedArrows(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"async arrow", `const fetchData = async (url) => { return url; };`, "url"},
		{"async arrow multi-line", `const fetchData = async (
  url,
  opts
) => url;`, "opts"},
		{"async destructured arrow", `const load = async ({ id, signal }) => id;`, "signal"},
		{"return-led arrow", `function mk() { return (a) => a; }`, "a"},
		{"async function keeps working", `async function load({ id }) {}`, "id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsParams(t, tc.src); !hasName(got, tc.want) {
				t.Errorf("%q not bound (got %v) — a keyword-led parameter list was "+
					"read as a call", tc.want, got)
			}
		})
	}
}

// TestJSParamNamesUnbalancedParenContainment pins review finding 2.
// stripLiteralsStateful does not strip JS regex literals, so `/\(/` contributes
// a naked open paren. Unbounded, that latched the multi-line accumulator and
// swallowed every subsequent line — silencing all bindings in the file and
// reopening #302 for it.
func TestJSParamNamesUnbalancedParenContainment(t *testing.T) {
	src := `const OPEN = /\(/;

export function Picker({ value, onChange }) {
  return onChange(value);
}
`
	got := jsParams(t, src)
	for _, want := range []string{"value", "onChange"} {
		if !hasName(got, want) {
			t.Fatalf("%q not bound (got %v): an unbalanced paren from a regex "+
				"literal swallowed the rest of the file", want, got)
		}
	}

	// The backstop for a latch with no column-zero statement after it.
	var deep strings.Builder
	deep.WriteString("function outer() {\n  const OPEN = /\\(/;\n")
	for i := 0; i < maxJSSigLines+5; i++ {
		deep.WriteString("  noop();\n")
	}
	deep.WriteString("  const inner = ({ bound }) => bound;\n}\n")
	if got := jsParams(t, deep.String()); !hasName(got, "bound") {
		t.Errorf("line bound did not release the accumulator (got %v)", got)
	}
}

// TestJSParamNamesGenericCommaNoTypeLeak pins review finding 3.
// splitTopLevelCommas tracks ()/[]/{} but not <>, so `m: Map<string, Handler>`
// split into `m: Map<string` and `Handler>` — and binding the fragment folded a
// TYPE NAME into the resolvable set, masking a hallucinated `Handler(...)`.
func TestJSParamNamesGenericCommaNoTypeLeak(t *testing.T) {
	for _, tc := range []struct {
		name, src, leak, keep string
	}{
		{"generic with comma", `function handle(m: Map<string, Handler>, next) {}`, "Handler", "next"},
		{"nested generic", `function f(x: Record<string, Array<Widget>>, cb) {}`, "Widget", "cb"},
		{"comparison default is not a generic", `function g(a = b < c, d) {}`, "", "d"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := jsParams(t, tc.src)
			if tc.leak != "" && hasName(got, tc.leak) {
				t.Errorf("type name %q leaked from a split generic: %v", tc.leak, got)
			}
			if !hasName(got, tc.keep) {
				t.Errorf("real binding %q lost: %v", tc.keep, got)
			}
		})
	}
}

// TestJSParamNamesMultilineTypeAlias pins review finding 4: a type alias whose
// function type starts on a CONTINUATION line escaped the type-position guard,
// because that guard keyed only on the opening line's bracket delta.
func TestJSParamNamesMultilineTypeAlias(t *testing.T) {
	for _, src := range []string{
		"type H =\n  (e: MouseEvent) => void;",
		"export type Reducer =\n  (state: S, action: A) => S;",
	} {
		if got := jsParams(t, src); len(got) > 0 {
			t.Errorf("bound %v from a multi-line type alias — masks real references", got)
		}
	}
}

// TestJSParamNamesSecondOpenerOnLine pins review finding 5: skipping a
// `:`-preceded paren must not abandon the whole line, or one object-literal
// arrow property suppresses every later parameter list on it.
func TestJSParamNamesSecondOpenerOnLine(t *testing.T) {
	src := `const routes = { '/x': (req) => h(req) }; const f = (a) => a;`
	if got := jsParams(t, src); !hasName(got, "a") {
		t.Errorf("second parameter list on the line was lost: %v", got)
	}
}

// TestJSTopLevelStatementStartClosesPattern guards the near-miss found while
// fixing finding 2: a column-zero `}` usually closes a block, but `}: Props) {`
// closes a DESTRUCTURED PARAMETER — the closing line of #302's headline
// signature. Treating it as a statement boundary abandoned the list one line
// before it completed.
func TestJSTopLevelStatementStartClosesPattern(t *testing.T) {
	for _, tc := range []struct {
		line string
		want bool
	}{
		{"}", true},
		{"};", true},
		{"}: PickerProps) {", false},
		{"}) => a;", false},
		{"}, next) {", false},
		{"export function f() {", true},
		{"  indented();", false},
	} {
		if got := jsTopLevelStatementStart(tc.line); got != tc.want {
			t.Errorf("jsTopLevelStatementStart(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

// TestJSParamNamesSemiFreeTypeAlias pins review-round-2 finding 1. The
// type-alias continuation guard originally tested "line has no semicolon", but
// under `semi: false` (prettier) a COMPLETE statement has none either — so
// every type alias consumed the line after it, blanking the code below and
// bringing #302's false positive back for that whole class of TS codebase.
func TestJSParamNamesSemiFreeTypeAlias(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"type alias without semicolon", `type Props = { a: string }
export function Picker({ onChange }: Props) { return onChange(1); }`},
		{"interface without semicolon", `interface Props { a: string }
export function Picker({ onChange }: Props) { return onChange(1); }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsParams(t, tc.src); !hasName(got, "onChange") {
				t.Errorf("onChange not bound (got %v): a complete type head "+
					"swallowed the following line", got)
			}
		})
	}
	// The genuinely unfinished head must still carry over.
	if got := jsParams(t, "type H =\n  (e: MouseEvent) => void;"); len(got) > 0 {
		t.Errorf("bound %v from an unfinished type-alias head", got)
	}
}

// TestJSArrowFollowsRejectsChainedCalls pins review-round-2 findings 2 and 3.
// Scanning ahead for any `=>` before `{`/`;` read a parenthesised EXPRESSION
// followed by a chained call as a parameter list, folding a non-parameter into
// the known set — the false-negative direction.
func TestJSArrowFollowsRejectsChainedCalls(t *testing.T) {
	if got := jsParams(t, "const out = (\n  items\n).map((i) => i);"); hasName(got, "items") {
		t.Errorf("bound %v: a parenthesised expression was read as a parameter list", got)
	}
	// The dropped-import path has the same shape, where it additionally
	// suppresses a real warning for a name passed as an argument.
	lb := LocallyBoundNames(LangJS, TextToAddedLines("register(\n  Dropped,\n  other\n).then(v => v);"), nil)
	if _, ok := lb["Dropped"]; ok {
		t.Error("a multi-line call's argument was bound as a parameter — " +
			"this suppresses a genuine dropped-import finding")
	}
	// Real arrows, including with a return-type annotation, must still bind.
	for _, tc := range []struct{ src, want string }{
		{"const f = (a) => a;", "a"},
		{"const g = (b): Foo => b;", "b"},
		{"const h = (c): Map<string, X> => c;", "c"},
	} {
		if got := jsParams(t, tc.src); !hasName(got, tc.want) {
			t.Errorf("%q lost from %q: %v", tc.want, tc.src, got)
		}
	}
}

// TestJSParamNamesAngleClamp pins review-round-2 finding 4: an unclamped angle
// depth driven negative by a comparison in a default value let a later
// generic's split-off type fragment bind its TYPE NAME.
func TestJSParamNamesAngleClamp(t *testing.T) {
	got := jsParams(t, "function f(a: number = n > 0 ? 1 : 2, m: Map<string, Handler>) {}")
	if hasName(got, "Handler") {
		t.Errorf("type name Handler leaked after a negative angle depth: %v", got)
	}
	for _, want := range []string{"a", "m"} {
		if !hasName(got, want) {
			t.Errorf("real binding %q lost: %v", want, got)
		}
	}
}

// TestJSParamNamesReturnParenJSX pins review-round-2 finding 5. `return (` is
// followed by multi-line JSX far more often than by a wrapped arrow signature,
// so letting it latch swallowed the JSX body and lost every arrow inside it —
// a false negative in exactly the code shape this extractor was written for.
func TestJSParamNamesReturnParenJSX(t *testing.T) {
	withReturn := `export function C(p) {
  return (
    <div>{items.map((render) => render())}</div>
  );
}`
	withoutReturn := `export function C(p) {
    <div>{items.map((render) => render())}</div>
}`
	got, ctrl := jsParams(t, withReturn), jsParams(t, withoutReturn)
	if !hasName(got, "render") {
		t.Errorf("render lost inside a `return (` JSX body: got %v, control %v", got, ctrl)
	}
	// async/await DO lead wrappable parameter lists and must still latch.
	if g := jsParams(t, "const send = async (\n  url,\n  opts\n) => url;"); !hasName(g, "opts") {
		t.Errorf("async multi-line parameter list stopped latching: %v", g)
	}
}

// TestJSParamNamesGenericSplitBothDirections pins review-round-3 findings 1
// and 2. An earlier revision skipped generic fragments by accumulating angle
// depth ACROSS split elements, measured only after a top-level `:`. That
// heuristic was wrong in both directions and shipped one bug of each kind, so
// both are pinned here: splitJSParamList replaced it by never splitting inside
// a generic in the first place.
func TestJSParamNamesGenericSplitBothDirections(t *testing.T) {
	// Direction 1 — a generic in a DEFAULT value never opened the guard, so the
	// type name leaked into the known set and masked a real hallucination.
	src := `function build(reg = new Map<string, TotallyMadeUp>()) { return TotallyMadeUp(reg); }`
	if got := jsParams(t, src); hasName(got, "TotallyMadeUp") {
		t.Errorf("type name leaked from a generic in a default value: %v", got)
	}
	viol := Run(map[string]struct{}{}, "", []FileDiff{{Path: "x.ts", AddedLines: TextToAddedLines(src)}})
	var flagged bool
	for _, v := range viol {
		if v.Symbol == "TotallyMadeUp" {
			flagged = true
		}
	}
	if !flagged {
		t.Errorf("hallucinated TotallyMadeUp(...) no longer flagged: %v", viol)
	}

	// Direction 2 — a `<` COMPARISON in an annotated default opened the guard
	// spuriously and swallowed every later parameter, producing a FRESH false
	// positive of exactly the class this file exists to close.
	src2 := `function f(a: number = x < y, cb) { cb(); }`
	if got := jsParams(t, src2); !hasName(got, "cb") {
		t.Errorf("cb lost to a comparison mistaken for an open generic: %v", got)
	}
	if v := Run(map[string]struct{}{}, "", []FileDiff{{Path: "x.ts", AddedLines: TextToAddedLines(src2)}}); len(v) > 0 {
		t.Errorf("fresh false positive: %v", v)
	}

	// The annotated generic that motivated the tracking must still be handled.
	if got := jsParams(t, `function h(m: Map<string, Handler>, next) {}`); hasName(got, "Handler") {
		t.Errorf("type name leaked from an annotated generic: %v", got)
	}
}

// TestJSParamNamesArrowBodyParen pins review-round-3 finding 3: a `(` directly
// after a fat arrow or a `?` opens a BODY or a ternary branch, never a
// parameter list, so it must not latch across lines — the same wrapped-JSX
// hazard `return (` is exempted for.
func TestJSParamNamesArrowBodyParen(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"arrow body paren", "export const run = () => (\n  list.map((row) => row())\n);"},
		{"ternary branch paren", "const v = cond ? (\n  list.map((row) => row())\n) : null;"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsParams(t, tc.src); !hasName(got, "row") {
				t.Errorf("row lost — the wrapped block was swallowed: %v", got)
			}
		})
	}
}

// TestJSParamNamesCurriedArrow pins review-round-3 finding 4. The opener retry
// loop used to stop at the first list that bound anything, which lost the inner
// half of a single-line curried arrow — one definition, not two, and the
// standard redux-middleware / HOC shape.
func TestJSParamNamesCurriedArrow(t *testing.T) {
	got := jsParams(t, `const mw = (store) => (next) => next(store);`)
	for _, want := range []string{"store", "next"} {
		if !hasName(got, want) {
			t.Errorf("%q not bound from a curried arrow: %v", want, got)
		}
	}
}

// TestJSParamNamesGenericFunctionConstraint pins review-round-3 finding 5.
// jsFunctionOpen's `[^(]*` took the first `(` after the keyword, which is the
// wrong paren when a generic constraint contains a function type — binding a
// name from a pure type position AND missing the real list, both error
// directions from one mismatch.
func TestJSParamNamesGenericFunctionConstraint(t *testing.T) {
	got := jsParams(t, `function apply<T extends (a: number) => void>(cb: T) { return cb(); }`)
	if hasName(got, "a") {
		t.Errorf("bound `a` from inside a generic constraint: %v", got)
	}
	if !hasName(got, "cb") {
		t.Errorf("real parameter cb not bound: %v", got)
	}
}

// TestJSParamNamesMultilineTypeStatements pins review-round-3 finding 6: a type
// STATEMENT ends at its `;`, not merely when brackets balance. Clearing on
// balance dropped out of type position after one line for heads that span lines
// without nesting.
func TestJSParamNamesMultilineTypeStatements(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"conditional type", "type A = B extends C\n  ? (x: D) => void\n  : never;"},
		{"wrapped generic", "type Reg = Record<\n  string,\n  (v: W) => void\n>;"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsParams(t, tc.src); len(got) > 0 {
				t.Errorf("bound %v from a pure type position", got)
			}
		})
	}
	// A missing `;` must not swallow the file: a new top-level statement ends it.
	src := "type A = B extends C\nexport function P({ onChange }) { return onChange(1); }"
	if got := jsParams(t, src); !hasName(got, "onChange") {
		t.Errorf("onChange lost after an unterminated type head: %v", got)
	}
}

// TestJSParamNamesLinearInLineLength pins review-round-4 finding 1. The opener
// retry loop re-derived the `function` opener on every iteration, so a
// `strings.Contains(s[from:], "function")` scan of the whole remaining line ran
// inside the loop — O(n) work per iteration, quadratic in line length. On a
// generated arrow-heavy line that was 4x per doubling and 156 ms at the 64 KiB
// capLine ceiling, against a ~12 ms budget. ParseUnifiedDiff does not cap at
// all, and the pre-commit path has no deferOnPanic deadline, so it could stall
// a commit outright.
//
// Asserts the SHAPE (growth ratio), not a wall-clock threshold, so it does not
// flake on a loaded machine.
func TestJSParamNamesLinearInLineLength(t *testing.T) {
	measure := func(n int) time.Duration {
		lines := TextToAddedLines(strings.Repeat("f0((x0)=>x0),", n))
		best := time.Hour
		for i := 0; i < 5; i++ { // min of several runs: resistant to scheduler noise
			st := time.Now()
			_ = JSParamNames(lines)
			if d := time.Since(st); d < best {
				best = d
			}
		}
		return best
	}
	small, large := measure(1500), measure(6000)
	// 4x the input. Linear predicts ~4x; the quadratic version was ~16x.
	if large > 8*small+2*time.Millisecond {
		t.Errorf("growth is superlinear: 1500 units %v, 6000 units %v (>8x)", small, large)
	}
}

// TestJSParamNamesNestedTypeBody pins review-round-4 finding 2. Exit from type
// position required a column-zero statement or a `;`, so an INDENTED closing
// brace — a type body inside a namespace, or a local type inside a function —
// satisfied neither and latched the extractor for the rest of the enclosing
// block. Nothing after it bound, reopening #302 for that whole region. Only
// top-level type bodies were covered before, which is why this was missed.
func TestJSParamNamesNestedTypeBody(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"interface in a namespace", `export namespace NS {
  interface Props {
    a: string
  }
  export function handler(cb) { return cb(1); }
}`, "cb"},
		{"local type alias in a function body", `function outer() {
  type Local = {
    a: string
  }
  const g = (aaa) => aaa;
}`, "aaa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsParams(t, tc.src); !hasName(got, tc.want) {
				t.Errorf("%q not bound (got %v): an indented type body latched "+
					"the extractor into type position", tc.want, got)
			}
		})
	}
	// The head that opens NO nesting must still need its `;` — round-3's case.
	if got := jsParams(t, "type A = B extends C\n  ? (x: D) => void\n  : never;"); len(got) > 0 {
		t.Errorf("bound %v from a conditional type", got)
	}
}

// TestJSParamNamesArrowGenericClause pins review-round-4 finding 3: the arrow
// form had no generic-clause guard, so a parameter name inside a NESTED generic
// leaked. `zzz` binds nothing at runtime, so folding it masks a hallucinated
// `zzz(...)` — the failure the doc says is excluded structurally.
func TestJSParamNamesArrowGenericClause(t *testing.T) {
	got := jsParams(t, `const f = <T extends Array<(zzz: number) => void>>(x: T) => x;`)
	if hasName(got, "zzz") {
		t.Errorf("bound `zzz` from inside an arrow's generic clause: %v", got)
	}
	if !hasName(got, "x") {
		t.Errorf("real parameter x not bound: %v", got)
	}
}

// TestJSParamNamesRoundFiveShapes pins review-round-5. Findings 1, 3 and 5 were
// one root cause — an early return that stopped scanning a line before its end —
// so they are grouped; the rest are independent.
func TestJSParamNamesRoundFiveShapes(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      []string
		absent    string
	}{
		// Finding 2 (which subsumes finding 1): a COMPLETE one-line conditional
		// type latched the extractor, because jsHeadIsUnfinished keyed on
		// "contains extends" alone. The `?` arm distinguishes finished from not.
		{"complete one-line conditional type", `function o() {
  type R = A extends B ? C : D;
  const run = (cb) => cb();
}`, []string{"cb"}, ""},
		{"conditional type, no semicolon", `function o() {
  type R = A extends any[] ? T[0] : T
  const pick = (row) => row
}`, []string{"row"}, ""},

		// Finding 3: a multi-line signature closing mid-line left the rest of
		// that line unscanned.
		{"code after a multi-line close", `function f(
  a,
) { const g = (bb) => bb(); }`, []string{"a", "bb"}, ""},
		{"multi-line curried arrow", `export const mw = (
  store,
) => (next) => next(store);`, []string{"store", "next"}, ""},

		// Finding 5: resuming past a whole group skipped arrows nested inside a
		// parenthesised expression.
		{"arrow nested in a group", `const q = await (async (tok) => tok);`, []string{"tok"}, ""},
		{"arrow nested in a return group", `function z() { return (foo.map((x) => x)); }`, []string{"x"}, ""},

		// Finding 4: the precomputed `function` opener was returned ahead of the
		// left-to-right scan, skipping any arrow earlier on the line.
		{"arrow before a function keyword", `const g = (aaa) => 0; function f(bbb) {}`, []string{"aaa", "bbb"}, ""},

		// Finding 7: `default` and a second `function` EXPRESSION.
		{"export default arrow", `export default (props) => props();`, []string{"props"}, ""},
		{"two function expressions", `foo(function (a) {}, function (b) {});`, []string{"a", "b"}, ""},

		// Finding 6: a space-padded generic must not split mid-type.
		{"space-padded generic", `function f(m: Map< string, Handler >, cb) {}`, []string{"m", "cb"}, "Handler"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := jsParams(t, tc.src)
			for _, w := range tc.want {
				if !hasName(got, w) {
					t.Errorf("%q not bound: %v", w, got)
				}
			}
			if tc.absent != "" && hasName(got, tc.absent) {
				t.Errorf("type name %q leaked: %v", tc.absent, got)
			}
		})
	}

	// The type-alias cases that motivated the ROUND-3 fixes must not regress:
	// the line carrying a type statement's `;` is the type's own last line, and
	// scanning it would bind a name from a pure type position.
	for _, src := range []string{
		"type H =\n  (e: MouseEvent) => void;",
		"export type Reducer =\n  (state: S, action: A) => S;",
	} {
		if got := jsParams(t, src); len(got) > 0 {
			t.Errorf("bound %v from a multi-line type alias", got)
		}
	}
}

// TestJSParamNamesNestedParenNotQuadratic pins review-round-6 finding 1. The
// round-5 change to resume INSIDE a group that bound nothing made
// jsConsumeParens re-walk the same region once per nesting level — 835 ms on a
// 40 KB line of bare nested parens, 1.88 s at 60 KB, both under capLine and so
// reachable, on a path pre-commit mode runs with no deadline.
//
// TestJSParamNamesLinearInLineLength cannot catch this: its input is the shape
// where every group BINDS, and a bound group is skipped whole. This one uses
// the shape where none bind.
func TestJSParamNamesNestedParenNotQuadratic(t *testing.T) {
	measure := func(depth int) time.Duration {
		lines := TextToAddedLines(strings.Repeat("(", depth) + strings.Repeat(")", depth) + ";")
		best := time.Hour
		for i := 0; i < 3; i++ {
			st := time.Now()
			_ = JSParamNames(lines)
			if d := time.Since(st); d < best {
				best = d
			}
		}
		return best
	}
	small, large := measure(5000), measure(20000)
	// 4x the depth. Linear predicts ~4x; the quadratic version was ~16x.
	if large > 8*small+2*time.Millisecond {
		t.Errorf("nested-paren scan is superlinear: depth 5000 %v, depth 20000 %v", small, large)
	}
}

// TestJSParamNamesNestedPatternGenericLeak pins review-round-6 finding 2.
// appendJSParams was made angle-aware, but jsBindingTargets' recursion into a
// nested pattern still split with splitTopLevelCommas — so the identical
// type-name leak survived one level down, in a destructured default value.
func TestJSParamNamesNestedPatternGenericLeak(t *testing.T) {
	for _, tc := range []struct{ src, leak string }{
		{`function f({ reg = new Map<string, TotallyMadeUp>() }) {}`, "TotallyMadeUp"},
		{`function f([reg = new Map<string, Bogus>()]) {}`, "Bogus"},
		{`function f({ a: { reg = mk<Alpha, Beta>() } }) {}`, "Beta"},
	} {
		got := jsParams(t, tc.src)
		if hasName(got, tc.leak) {
			t.Errorf("type name %q leaked from a nested pattern: %v", tc.leak, got)
		}
		if !hasName(got, "reg") {
			t.Errorf("real binding reg lost from %q: %v", tc.src, got)
		}
	}
}

// TestJSParamNamesSemiFreeUnionInBlock pins review-round-6 finding 3: a
// `semi: false` multi-line alias with no bracket body, declared inside a block,
// cleared on none of the three exits and latched the extractor until a
// column-zero statement. Same class as the round-4 nested-type-body finding,
// whose fix covered only the bracket arm.
func TestJSParamNamesSemiFreeUnionInBlock(t *testing.T) {
	src := `function f() {
  type H =
    | A
    | B
  const cb = (xxx) => xxx
  return cb(1)
}`
	if got := jsParams(t, src); !hasName(got, "xxx") {
		t.Errorf("xxx not bound (got %v): an indented semicolon-less union "+
			"latched the extractor for the rest of the block", got)
	}
	// The wrapped generic must still hold: its head opens a `<` nesting, which
	// jsTypeDelta counts even when the `<` ends the line.
	if got := jsParams(t, "type Reg = Record<\n  string,\n  (v: W) => void\n>;"); len(got) > 0 {
		t.Errorf("bound %v from a wrapped generic", got)
	}
}

// TestJSParamNamesRoundSevenShapes pins review-round-7.
func TestJSParamNamesRoundSevenShapes(t *testing.T) {
	// Finding 1: the latch depth was hardcoded to 1, ignoring parens already
	// open and unclosed on the signature's own opening line — so the first `)`
	// on a later line was read as the list's close. Both siblings (PyParamNames,
	// and this PR's LocallyBoundNames twin) already computed the real depth.
	if got := jsParams(t, "const handler = (a = foo(\n  1\n)) => a;"); !hasName(got, "a") {
		t.Errorf("a not bound (got %v): a nested unclosed call on the opening "+
			"line broke the continuation depth", got)
	}

	// Finding 2: ambient declarations and bodyless overload signatures are pure
	// type positions — their parameter names bind nothing at runtime, so binding
	// them masks a hallucinated call.
	for _, tc := range []struct{ src, leak string }{
		{`declare function bar(bX: number): void;`, "bX"},
		{`export function fmt(cX: number): string;`, "cX"},
	} {
		if got := jsParams(t, tc.src); hasName(got, tc.leak) {
			t.Errorf("bound %q from %q: %v", tc.leak, tc.src, got)
		}
	}
	// A real declaration with a body must still bind.
	if got := jsParams(t, `export function fmt(cX: number): string { return ""; }`); !hasName(got, "cX") {
		t.Errorf("real declaration lost its parameter: %v", got)
	}

	// Finding 4: `angle++` fired on any `<` after an identifier but `angle--`
	// only on a `>`, so an unspaced comparison latched the depth and the
	// `angle > 0` guard hid every later `(` on the line.
	if got := jsParams(t, `if (i<n) { list.forEach((row) => row()); }`); !hasName(got, "row") {
		t.Errorf("row lost to a latched generic depth: %v", got)
	}
	if got := jsParams(t, `function f(a = i<n, cb) {}`); !hasName(got, "cb") {
		t.Errorf("cb lost — the splitter latched on an unspaced comparison: %v", got)
	}
	// The generic clause it exists for must still be excluded.
	if got := jsParams(t, `const f = <T extends Array<(zzz: number) => void>>(x: T) => x;`); hasName(got, "zzz") {
		t.Errorf("generic-clause guard broke: %v", got)
	}
}

// TestJSParamNamesRoundEightShapes pins review-round-8.
func TestJSParamNamesRoundEightShapes(t *testing.T) {
	// Finding 1: jsAmbientOrOverload is a per-line check consulted BEFORE the
	// latch, so a WRAPPED ambient declaration or overload signature — exactly
	// the shape the multi-line accumulator exists to read — escaped it. Judged
	// at close time now, from the text after the `)`.
	for _, tc := range []struct{ src, leak string }{
		{"declare function fmtQ(\n  valq: number\n): string;", "valq"},
		{"export function fmtR(\n  valr: number\n): string;", "valr"},
	} {
		if got := jsParams(t, tc.src); hasName(got, tc.leak) {
			t.Errorf("bound %q from a wrapped ambient/overload declaration: %v", tc.leak, got)
		}
	}
	// A real wrapped declaration must still bind.
	if got := jsParams(t, "export function real(\n  keep: number\n) { return keep; }"); !hasName(got, "keep") {
		t.Errorf("real wrapped declaration lost its parameter: %v", got)
	}

	// Finding 2: `= (` latches, and a wrapped-JSX assignment then swallowed
	// every arrow inside it — the same hazard `return (` and `=> (` were
	// exempted for, arriving through the value-position path.
	if got := jsParams(t, "export const Panel = (\n  <List render={(rowq) => rowq()} />\n);"); !hasName(got, "rowq") {
		t.Errorf("rowq lost inside a wrapped-JSX assignment: %v", got)
	}
	// The real multi-line arrow signature must still bind.
	if got := jsParams(t, "const cb = (\n  err,\n  data\n) => data;"); !hasName(got, "data") {
		t.Errorf("multi-line arrow signature regressed: %v", got)
	}

	// Finding 3: the unparenthesized single-arg arrow has no `(` for the opener
	// scan to find. LocallyBoundNames covered it from the start; this extractor
	// did not, leaving #302 open for the concise form.
	for _, tc := range []struct{ src, want string }{
		{`export const wrap = cbq => cbq();`, "cbq"},
		{`items.forEach(itq => itq());`, "itq"},
		{`const curried = aq => bq => bq(aq);`, "bq"},
	} {
		if got := jsParams(t, tc.src); !hasName(got, tc.want) {
			t.Errorf("%q not bound from %q: %v", tc.want, tc.src, got)
		}
	}
	// ...but a RETURN TYPE before the arrow is not a parameter.
	if got := jsParams(t, `const f = (a): Foo => a;`); hasName(got, "Foo") {
		t.Errorf("return type Foo bound as a bare-arrow parameter: %v", got)
	}

	// Finding 4: the generic-depth guard needs the last `>` that is NOT the
	// arrow's, or an unspaced comparison/shift latches and hides every later `(`.
	for _, tc := range []struct{ src, want string }{
		{`const ok = compute(a.n<max).map((yyq) => yyq);`, "yyq"},
		{`const m = flags(v<<2).map((wwq) => wwq);`, "wwq"},
	} {
		if got := jsParams(t, tc.src); !hasName(got, tc.want) {
			t.Errorf("%q lost to a latched generic depth: %v", tc.want, got)
		}
	}
}
