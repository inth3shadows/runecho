package guard

import "testing"

// The file-scope check flags a reference that resolves in the REPO but not in the
// edited FILE ("real symbol, wrong scope"). Its entire safety rests on abstaining
// whenever the file's binding surface cannot be known, so the NEGATIVES come
// first and outnumber the positives: a miss is free, a false alarm is the
// adoption-killer.

// repoKnown is the repo-wide symbol set. Every name here "exists somewhere in the
// repo", which is the precondition for the check to consider a name at all.
func repoKnown(names ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

// linesOf (contiguous 1-based AddedLines) is shared with qualified_test.go.

// flaggedNames is the set of symbols the check raised, for terse assertions.
func flaggedNames(vs []Violation) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Symbol)
	}
	return out
}

// ---------------------------------------------------------------------------
// NEGATIVES — none of these may ever produce a violation.
// ---------------------------------------------------------------------------

func TestFileScope_Negatives(t *testing.T) {
	cases := []struct {
		name      string
		wholeFile []string
		added     []string
		known     []string
		why       string
	}{
		{
			name:      "imported at top of file, used in hunk",
			wholeFile: []string{"from delivery.renderer import render", "", "def go():", "    pass"},
			added:     []string{"    html = render(digest)"},
			known:     []string{"render"},
			why:       "the import binds it; the binding line sits outside the hunk",
		},
		{
			name:      "defined elsewhere in the same file",
			wholeFile: []string{"def helper(x):", "    return x", "", "def go():", "    pass"},
			added:     []string{"    y = helper(1)"},
			known:     []string{"helper"},
			why:       "a local def resolves it",
		},
		{
			name:      "forward reference — defined LATER in the file",
			wholeFile: []string{"def go():", "    pass", "", "def helper(x):", "    return x"},
			added:     []string{"    y = helper(1)"},
			known:     []string{"helper"},
			why:       "the whole-file scan is order-independent; temporal refs are not bugs",
		},
		{
			name:      "parameter of the enclosing def used as a callable",
			wholeFile: []string{"def pump(transform):", "    pass"},
			added:     []string{"    out = transform(line)"},
			known:     []string{"transform"},
			why:       "the signature binds it",
		},
		{
			name:      "locally assigned before use",
			wholeFile: []string{"def go():", "    handler = HANDLERS[key]", "    pass"},
			added:     []string{"    handler(payload)"},
			known:     []string{"handler"},
			why:       "assignment binds it",
		},
		{
			name:      "star import anywhere in the file abstains the WHOLE file",
			wholeFile: []string{"from helpers import *", "", "def go():", "    pass"},
			added:     []string{"    y = mystery_helper(1)"},
			known:     []string{"mystery_helper"},
			why:       "a star import binds an unknowable set — never flag under one",
		},
		{
			name:      "globals() present anywhere abstains the whole file",
			wholeFile: []string{"def wire():", "    globals()['injected'] = 1", "", "def go():", "    pass"},
			added:     []string{"    injected(1)"},
			known:     []string{"injected"},
			why:       "names may be injected at runtime",
		},
		{
			name:      "exec() present abstains the whole file",
			wholeFile: []string{"def wire(src):", "    exec(src)", "", "def go():", "    pass"},
			added:     []string{"    injected(1)"},
			known:     []string{"injected"},
			why:       "exec can bind arbitrary names",
		},
		{
			name:      "setattr() present abstains the whole file",
			wholeFile: []string{"def wire(m):", "    setattr(m, 'x', 1)", "", "def go():", "    pass"},
			added:     []string{"    injected(1)"},
			known:     []string{"injected"},
			why:       "dynamic attribute binding — abstain",
		},
		{
			name:      "importlib present abstains the whole file",
			wholeFile: []string{"import importlib", "", "def go():", "    pass"},
			added:     []string{"    injected(1)"},
			known:     []string{"injected"},
			why:       "dynamic import — abstain",
		},
		{
			name:      "global declaration binds the name",
			wholeFile: []string{"def wire():", "    global cache", "    cache = {}", "", "def go():", "    pass"},
			added:     []string{"    cache()"},
			known:     []string{"cache"},
			why:       "declared global",
		},
		{
			name:      "nonlocal declaration binds the name",
			wholeFile: []string{"def outer():", "    def inner():", "        nonlocal acc", "        pass"},
			added:     []string{"        acc()"},
			known:     []string{"acc"},
			why:       "declared nonlocal",
		},
		{
			name:      "FIREWALL: name absent from the repo is NOT this check's job",
			wholeFile: []string{"def go():", "    pass"},
			added:     []string{"    zzz_invented_thing(1)"},
			known:     []string{}, // not in repo at all
			why:       "an invented symbol belongs to the additive check; flagging here would double-report and widen surface",
		},
		{
			name:      "builtin call",
			wholeFile: []string{"def go():", "    pass"},
			added:     []string{"    print(len(x))"},
			known:     []string{"print", "len"},
			why:       "builtins are excluded upstream by the extractor",
		},
		{
			name:      "qualified call is never a bare-name reference",
			wholeFile: []string{"import transforms", "", "def go():", "    pass"},
			added:     []string{"    transforms.has_terse_marker(obj)"},
			known:     []string{"has_terse_marker"},
			why:       "qualified refs are skipped by the extractor; this is the CORRECT form",
		},
		{
			name:      "no whole-file context available",
			wholeFile: nil,
			added:     []string{"    render(x)"},
			known:     []string{"render"},
			why:       "without the file we cannot know its imports — must stay silent",
		},
		{
			name:      "conditional/try import still binds",
			wholeFile: []string{"try:", "    from fast import render", "except ImportError:", "    render = None", "", "def go():", "    pass"},
			added:     []string{"    render(x)"},
			known:     []string{"render"},
			why:       "the import line is present regardless of the guard",
		},
		{
			name:      "aliased import binds the alias",
			wholeFile: []string{"from mod import render as draw", "", "def go():", "    pass"},
			added:     []string{"    draw(x)"},
			known:     []string{"draw", "render"},
			why:       "ExtractImports binds the alias",
		},
		{
			name:      "multi-line import group binds every name",
			wholeFile: []string{"from mod import (", "    alpha,", "    beta,", ")", "", "def go():", "    pass"},
			added:     []string{"    beta(1)"},
			known:     []string{"alpha", "beta"},
			why:       "grouped imports bind all names",
		},
		{
			name:      "for-loop target binds",
			wholeFile: []string{"def go():", "    for cb in handlers:", "        pass"},
			added:     []string{"        cb(1)"},
			known:     []string{"cb"},
			why:       "loop target is a binding form",
		},
		{
			name:      "with-as target binds",
			wholeFile: []string{"def go():", "    with open(p) as fh:", "        pass"},
			added:     []string{"        fh()"},
			known:     []string{"fh"},
			why:       "with-as is a binding form",
		},
		{
			name:      "class defined in file",
			wholeFile: []string{"class Widget:", "    pass", "", "def go():", "    pass"},
			added:     []string{"    w = Widget()"},
			known:     []string{"Widget"},
			why:       "a class def binds the name",
		},
		{
			name:      "name introduced by the edit itself",
			wholeFile: []string{"def go():", "    pass"},
			added:     []string{"def fresh():", "    return 1", "", "x = fresh()"},
			known:     []string{"fresh"},
			why:       "the edit defines it; the hunk must be folded into scope too",
		},
		{
			name:      "reference inside a docstring is not code",
			wholeFile: []string{"def go():", "    pass"},
			added:     []string{"    \"\"\"call render(x) to draw\"\"\""},
			known:     []string{"render"},
			why:       "string content is masked by the extractor",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var whole []AddedLine
			if tc.wholeFile != nil {
				whole = linesOf(tc.wholeFile...)
			}
			got := FileScopeViolations(LangPython, whole, FileDiff{AddedLines: linesOf(tc.added...)}, repoKnown(tc.known...))
			if len(got) != 0 {
				t.Errorf("FALSE POSITIVE: flagged %v — %s", flaggedNames(got), tc.why)
			}
		})
	}
}

// TestFileScope_NonPythonIsSilent pins the v1 language scope. Go is
// package-qualified and compiler-checked; JS lands only after Python proves out.
func TestFileScope_NonPythonIsSilent(t *testing.T) {
	for _, lang := range []Lang{LangGo, LangJS, LangUnknown} {
		whole := linesOf("package main", "func go() {}")
		got := FileScopeViolations(lang, whole, FileDiff{AddedLines: linesOf("\tRender(x)")}, repoKnown("Render"))
		if len(got) != 0 {
			t.Errorf("lang %v: expected silence in v1, got %v", lang, flaggedNames(got))
		}
	}
}

// ---------------------------------------------------------------------------
// POSITIVES — the class the mining found, which the additive check misses.
// ---------------------------------------------------------------------------

func TestFileScope_CatchesRealSymbolUsedOutOfScope(t *testing.T) {
	cases := []struct {
		name      string
		wholeFile []string
		added     []string
		known     []string
		want      string
	}{
		{
			// competitive-intel: `render` is a real top-level function in
			// delivery/renderer.py, called in a test that never imported it.
			name:      "never-imported real symbol",
			wholeFile: []string{"import pytest", "", "def test_it():", "    pass"},
			added:     []string{"    out = render(digest, week)"},
			known:     []string{"render", "pytest"},
			want:      "render",
		},
		{
			// terse: real `transforms.has_terse_marker`, called bare (missing qualifier).
			name:      "missing module qualifier",
			wholeFile: []string{"import transforms", "", "def check(obj):", "    pass"},
			added:     []string{"    if has_terse_marker(obj):", "        return True"},
			known:     []string{"has_terse_marker", "transforms"},
			want:      "has_terse_marker",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FileScopeViolations(LangPython, linesOf(tc.wholeFile...),
				FileDiff{AddedLines: linesOf(tc.added...)}, repoKnown(tc.known...))
			if len(got) != 1 || got[0].Symbol != tc.want {
				t.Fatalf("want exactly [%s], got %v", tc.want, flaggedNames(got))
			}
			if got[0].Lang != LangPython {
				t.Errorf("Lang = %v, want LangPython", got[0].Lang)
			}
		})
	}
}

// TestFileScope_AbstainGatesAreLoadBearing proves each abstain gate actually
// suppresses something. Without this, every abstain negative could be passing
// vacuously — silent because the check would never have fired on that input
// anyway, not because the gate worked. Each case asserts the SAME input flags
// once the abstaining construct is removed.
func TestFileScope_AbstainGatesAreLoadBearing(t *testing.T) {
	// The control: no abstaining construct, name is real-but-out-of-scope → flags.
	control := []string{"import pytest", "", "def go():", "    pass"}
	added := linesOf("    injected(1)")
	known := repoKnown("injected", "pytest")

	if got := FileScopeViolations(LangPython, linesOf(control...), FileDiff{AddedLines: added}, known); len(got) != 1 {
		t.Fatalf("control must flag, else every abstain test below is vacuous; got %v", flaggedNames(got))
	}

	// Each gate, inserted into the same otherwise-flagging file, must silence it.
	gates := map[string]string{
		"star import": "from helpers import *",
		"globals()":   "    globals()['x'] = 1",
		"locals()":    "    locals()",
		"exec()":      "    exec(src)",
		"eval()":      "    eval(src)",
		"setattr()":   "    setattr(m, 'a', 1)",
		"vars()":      "    vars(m)",
		"__import__":  "    __import__('m')",
		"importlib":   "import importlib",
	}
	for name, line := range gates {
		t.Run(name, func(t *testing.T) {
			withGate := append([]string{line}, control...)
			got := FileScopeViolations(LangPython, linesOf(withGate...), FileDiff{AddedLines: added}, known)
			if len(got) != 0 {
				t.Errorf("gate %q did not suppress: got %v", name, flaggedNames(got))
			}
		})
	}

	// The firewall is load-bearing too: same input, name NOT in the repo → silent.
	t.Run("repo firewall", func(t *testing.T) {
		got := FileScopeViolations(LangPython, linesOf(control...), FileDiff{AddedLines: added}, repoKnown("pytest"))
		if len(got) != 0 {
			t.Errorf("firewall did not suppress an out-of-repo name: got %v", flaggedNames(got))
		}
	})

	// And the missing-whole-file gate.
	t.Run("no whole-file context", func(t *testing.T) {
		if got := FileScopeViolations(LangPython, nil, FileDiff{AddedLines: added}, known); len(got) != 0 {
			t.Errorf("missing-context gate did not suppress: got %v", flaggedNames(got))
		}
	})
}

// TestFileScope_DedupesByName pins that a name used repeatedly reports once.
func TestFileScope_DedupesByName(t *testing.T) {
	whole := linesOf("import pytest", "", "def test_it():", "    pass")
	added := linesOf("    a = render(1)", "    b = render(2)", "    c = render(3)")
	got := FileScopeViolations(LangPython, whole, FileDiff{AddedLines: added}, repoKnown("render"))
	if len(got) != 1 {
		t.Fatalf("want 1 violation, got %d: %v", len(got), flaggedNames(got))
	}
	if got[0].Line != 1 {
		t.Errorf("Line = %d, want first use (1)", got[0].Line)
	}
}

// TestFileScope_DocstringSeedIsHonored is the regression for the guard's largest
// historical false-positive class (#145 pre-commit, #178 hook path): an edit block
// that BEGINS inside a pre-existing docstring must be masked, not scanned as code.
// Prose reads as calls otherwise — the symbol below, `structure`, is one of the
// names that actually false-positived before #178 shipped.
//
// The control half proves the seed is load-bearing rather than incidental: the
// identical block WITHOUT the seed does flag, so this test cannot pass vacuously.
func TestFileScope_DocstringSeedIsHonored(t *testing.T) {
	whole := linesOf(
		`def go():`,
		`    """`,
		`    call structure(x) to draw`,
		`    """`,
	)
	block := []AddedLine{{LineNo: 1, Text: `    call structure(x) to draw`}}
	repo := repoKnown("structure")

	// Hook path: the caller's seed says this block starts inside a `"""` string.
	seeded := FileDiff{AddedLines: block, SeedByLine: map[int]string{1: `"""`}}
	if got := FileScopeViolations(LangPython, whole, seeded, repo); len(got) != 0 {
		t.Errorf("FALSE POSITIVE on docstring prose: %v", flaggedNames(got))
	}

	// Control: same block, no seed → the extractor cannot know it is inside a
	// string, so it flags. If this ever stops flagging, the test above is vacuous.
	unseeded := FileDiff{AddedLines: block}
	if got := FileScopeViolations(LangPython, whole, unseeded, repo); len(got) != 1 {
		t.Errorf("control must flag without a seed, else the seeded assertion is vacuous; got %v", flaggedNames(got))
	}
}
