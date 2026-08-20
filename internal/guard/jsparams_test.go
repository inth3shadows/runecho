package guard

import (
	"sort"
	"strings"
	"testing"
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
