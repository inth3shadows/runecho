package guard

import (
	"testing"
)

// TestPyDeclaredNames pins the assignment-target extraction that feeds the
// additive known set. The must / mustNot split is the point: a real binding
// target must be admitted (else the FP it kills survives), while an annotation
// type, a keyword argument, or an attribute/subscript target must NOT be
// admitted (else a genuine hallucination of that name is masked — the worst
// class).
func TestPyDeclaredNames(t *testing.T) {
	cases := []struct {
		name    string
		src     []string
		must    []string
		mustNot []string
	}{
		{
			name: "computed-member callable",
			src:  []string{"handler = HANDLERS[key]"},
			must: []string{"handler"},
			// HANDLERS/key are on the RHS — references, never bound here.
			mustNot: []string{"HANDLERS", "key"},
		},
		{
			name: "tuple unpacking",
			src:  []string{"setup, teardown = pair"},
			must: []string{"setup", "teardown"},
		},
		{
			name: "parenthesized and nested tuple targets",
			src:  []string{"(a, b) = f()", "first, (g, h) = pair"},
			must: []string{"a", "b", "first", "g", "h"},
		},
		{
			name:    "annotated assignment drops the type",
			src:     []string{"svc: RouteContext = build()"},
			must:    []string{"svc"},
			mustNot: []string{"RouteContext", "build"},
		},
		{
			name:    "starred target",
			src:     []string{"first, *rest = items"},
			must:    []string{"first", "rest"},
			mustNot: []string{"items"},
		},
		{
			name: "single-line kwarg is not a binding",
			src:  []string{"result = configure(timeout=30)"},
			must: []string{"result"},
			// timeout is a keyword arg inside the call, not a target.
			mustNot: []string{"timeout", "configure"},
		},
		{
			name:    "wrapped-call kwarg across lines is not a binding",
			src:     []string{"result = configure(", "    timeout=30,", ")"},
			must:    []string{"result"},
			mustNot: []string{"timeout"},
		},
		{
			name:    "attribute and subscript targets bind nothing",
			src:     []string{"self.count = 0", "cache[key] = value"},
			mustNot: []string{"self", "count", "cache", "key", "value"},
		},
		{
			name:    "augmented assignment is not a new binding",
			src:     []string{"total += delta"},
			mustNot: []string{"delta"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PyDeclaredNames(lines(c.src...))
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

// TestRun_PyLocalCallable_NotFlagged is the end-to-end fix: a bare call to a
// locally-assigned callable is not a hallucination. The companion assertion
// guards the false-negative regression the precise extractor exists to avoid —
// a genuinely undefined call on the same diff MUST still be flagged.
func TestRun_PyLocalCallable_NotFlagged(t *testing.T) {
	diffs := []FileDiff{{
		Path: "dispatch.py",
		AddedLines: lines(
			"def dispatch(key, payload):",
			"    handler = HANDLERS[key]",
			"    handler(payload)",
			"    return doesNotExist(payload)",
		),
	}}
	v := Run(map[string]struct{}{"HANDLERS": {}}, "", diffs)
	names := violationSymbols(v)
	if containsStr(names, "handler") {
		t.Errorf("locally-assigned callable handler must not be flagged, got %v", names)
	}
	if !containsStr(names, "doesNotExist") {
		t.Errorf("genuine hallucination doesNotExist must still be flagged, got %v", names)
	}
}
