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
		// #313: for-loop/comprehension/walrus/with/except binding forms. Each
		// case pins one form's target as bound (must) alongside a companion
		// name that must NOT bind — the false-negative regression check the
		// precision discipline exists to guard.
		{
			name:    "for-loop target",
			src:     []string{"for path_hook in sys.path_hooks:", "    importer = path_hook(path_item)"},
			must:    []string{"path_hook", "importer"},
			mustNot: []string{"sys", "path_hooks", "path_item"},
		},
		{
			name:    "comprehension target",
			src:     []string{"return [f(self.format) for f in funcs]"},
			must:    []string{"f"},
			mustNot: []string{"funcs", "self", "format"},
		},
		{
			name:    "walrus target",
			src:     []string{`if dealloc_warn := getattr(self, "_dealloc_warn", None):`, "    dealloc_warn(self)"},
			must:    []string{"dealloc_warn"},
			mustNot: []string{"getattr", "self"},
		},
		{
			name:    "with-as target",
			src:     []string{"with open(p) as fh:", "    fh()"},
			must:    []string{"fh"},
			mustNot: []string{"open", "p"},
		},
		{
			name:    "except-as target",
			src:     []string{"except SomeError as e:", "    e()"},
			must:    []string{"e"},
			mustNot: []string{"SomeError"},
		},
		{
			name: "chained assignment binds every target",
			src:  []string{"body['k'] = gnv = etype._generate_next_value_"},
			must: []string{"gnv"},
			// The subscript target `body['k']` binds nothing (unchanged
			// discipline); the RHS `etype`/`_generate_next_value_` are
			// references, never bound.
			mustNot: []string{"body", "etype", "_generate_next_value_"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PyDeclaredNames(lines(c.src...), nil)
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

// TestRun_PyBindingForms_NotFlagged is the #313 end-to-end fix, one form per
// line, exercising guard.Run's actual Pass-1 fold (validate.go, not just the
// PyDeclaredNames unit above) — each of the five stdlib-measured binding forms
// plus chained assignment must resolve its later bare call, and the companion
// genuinely-undefined call on the same diff must still be flagged (the
// false-negative regression this precision discipline exists to prevent).
func TestRun_PyBindingForms_NotFlagged(t *testing.T) {
	diffs := []FileDiff{{
		Path: "bindings.py",
		AddedLines: lines(
			"def use_all(sys, funcs, p, SomeError, etype):",
			"    for path_hook in sys.path_hooks:",
			"        importer = path_hook(path_item)",
			"    picked = [f(x) for f in funcs]",
			"    if dealloc_warn := probe():",
			"        dealloc_warn()",
			"    with open(p) as fh:",
			"        fh()",
			"    try:",
			"        pass",
			"    except SomeError as e:",
			"        e()",
			"    body = {}",
			"    body['k'] = gnv = etype._generate_next_value_",
			"    gnv()",
			"    return doesNotExist()",
		),
	}}
	v := Run(map[string]struct{}{"probe": {}}, "", diffs)
	names := violationSymbols(v)
	for _, bound := range []string{"path_hook", "f", "dealloc_warn", "fh", "e", "gnv"} {
		if containsStr(names, bound) {
			t.Errorf("#313 binding target %q must not be flagged, got %v", bound, names)
		}
	}
	if !containsStr(names, "doesNotExist") {
		t.Errorf("genuine hallucination doesNotExist must still be flagged, got %v", names)
	}
}
