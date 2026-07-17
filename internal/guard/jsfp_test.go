package guard

import (
	"sort"
	"strings"
	"testing"
)

// TestJSDeclaredNames pins the declarator binding extraction that feeds the
// additive known set. The must/mustNot split is the whole point: a real binding
// target has to be admitted (else the FP it was meant to kill survives), while a
// TS type name, an object-destructure key, or an RHS reference must NOT be
// admitted (else a genuine hallucination of that name is masked — a false
// negative, the worst class).
func TestJSDeclaredNames(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		must    []string
		mustNot []string
	}{
		{
			name: "useState array destructure",
			src:  "const [text, setText] = useState('');",
			must: []string{"text", "setText"},
		},
		{
			name: "object destructure shorthand and rename",
			src:  "const { onClick, label: setLabel } = props;",
			must: []string{"onClick", "setLabel"},
			// `label` is the source key being renamed FROM — not a binding.
			mustNot: []string{"label", "props"},
		},
		{
			name: "computed-member callable",
			src:  "const fn = handlers[evt.type];",
			must: []string{"fn"},
			// handlers/evt are on the RHS — references, never bound here.
			mustNot: []string{"handlers", "evt", "type"},
		},
		{
			name:    "annotated bare declarator drops the type name",
			src:     "const svc: RouteContext = build();",
			must:    []string{"svc"},
			mustNot: []string{"RouteContext", "build"},
		},
		{
			name: "array element with default",
			src:  "const [a, b = fallback()] = tuple;",
			must: []string{"a", "b"},
			// fallback is a call in the default expression, not a binding.
			mustNot: []string{"fallback", "tuple"},
		},
		{
			name: "multi-declarator statement",
			src:  "const a = f(), Ulid = () => 1;",
			must: []string{"a", "Ulid"},
		},
		{
			name:    "object pattern with trailing type annotation",
			src:     "const { id }: Props = useProps();",
			must:    []string{"id"},
			mustNot: []string{"Props", "useProps"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := JSDeclaredNames(lines(c.src))
			set := make(map[string]struct{}, len(got))
			for _, n := range got {
				set[n] = struct{}{}
			}
			for _, m := range c.must {
				if _, ok := set[m]; !ok {
					t.Errorf("expected %q bound, got %v", m, sortedKeys(set))
				}
			}
			for _, m := range c.mustNot {
				if _, ok := set[m]; ok {
					t.Errorf("must NOT bind %q (would mask a real hallucination), got %v", m, sortedKeys(set))
				}
			}
		})
	}
}

// TestRun_JSDestructuredSetter_NotFlagged is the end-to-end fix for the largest
// real-world cluster: a bare call to a useState setter is not a hallucination.
// The companion assertion guards against the false-negative regression the
// precise extractor exists to avoid: a genuinely undefined call on the same
// diff MUST still be flagged.
func TestRun_JSDestructuredSetter_NotFlagged(t *testing.T) {
	diffs := []FileDiff{{
		Path: "Field.tsx",
		AddedLines: lines(
			"const [text, setText] = useState('');",
			"setText('hi');",
			"doesNotExist();",
		),
	}}
	// useState is imported in real code; supply it as a known IR symbol so only the
	// setter/hallucination behavior is under test.
	v := Run(map[string]struct{}{"useState": {}}, "", diffs)
	names := violationSymbols(v)
	if containsStr(names, "setText") {
		t.Errorf("destructured setter setText must not be flagged, got %v", names)
	}
	if !containsStr(names, "doesNotExist") {
		t.Errorf("genuine hallucination doesNotExist must still be flagged, got %v", names)
	}
}

// TestIsJSTestFile pins the test-file gate that scopes the runner-global
// allowlist.
func TestIsJSTestFile(t *testing.T) {
	yes := []string{
		"math.test.ts", "a/b/Comp.spec.tsx", "src/__tests__/thing.ts",
		"pkg/foo.test.js", "foo.spec.mjs",
	}
	no := []string{
		"math.ts", "Comp.tsx", "src/util.js", "testing.ts", "spec.ts",
		"main.go", "app.py", "notes.md",
	}
	for _, p := range yes {
		if !IsJSTestFile(p) {
			t.Errorf("IsJSTestFile(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if IsJSTestFile(p) {
			t.Errorf("IsJSTestFile(%q) = true, want false", p)
		}
	}
}

// TestRun_JSTestGlobals_GatedToTestFiles proves the allowlist is scoped: the
// ambient runner globals resolve inside a spec file but a bare describe() in
// product code is still flagged (precision preserved where it matters).
func TestRun_JSTestGlobals_GatedToTestFiles(t *testing.T) {
	spec := []FileDiff{{
		Path: "math.test.ts",
		AddedLines: lines(
			"describe('x', () => {",
			"  beforeEach(() => {});",
			"  it('adds', () => { expect(1).toBe(1); });",
			"});",
		),
	}}
	if v := Run(map[string]struct{}{}, "", spec); len(v) != 0 {
		t.Errorf("test-runner globals must resolve in a spec file, got %v", violationSymbols(v))
	}

	product := []FileDiff{{
		Path:       "math.ts",
		AddedLines: lines("export function check() { return describe(1); }"),
	}}
	if v := Run(map[string]struct{}{}, "", product); !containsStr(violationSymbols(v), "describe") {
		t.Errorf("bare describe() in product code must still be flagged, got %v", violationSymbols(v))
	}
}

func sortedKeys(m map[string]struct{}) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}
