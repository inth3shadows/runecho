package depindex

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureVenv is a vendored miniature virtualenv so these tests never depend on
// what happens to be installed on the machine running them. Determinism here is
// not optional: the whole check is a claim about what a package does and does not
// export, and a test that consults the real environment would be a different
// assertion on every box.
const fixtureVenv = "testdata/venv"

func newFixtureIndex(t *testing.T) *PythonIndex {
	t.Helper()
	// VIRTUAL_ENV takes priority in FindSitePackages, and t.Setenv both isolates
	// and restores it — important because a developer running these tests inside
	// their own activated venv would otherwise resolve against that.
	abs, err := filepath.Abs(fixtureVenv)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIRTUAL_ENV", abs)
	idx := NewPythonIndex(t.TempDir())
	if idx.SitePackages() == "" {
		t.Fatalf("fixture venv did not resolve to a site-packages dir")
	}
	return idx
}

func TestLookup_StaticAllPackage(t *testing.T) {
	idx := newFixtureIndex(t)
	ps := idx.Lookup("polarsy")
	if ps.Res != Resolved {
		t.Fatalf("Res = %v (%s), want Resolved", ps.Res, ps.Reason)
	}
	// Declared in __all__, defined in the module, bound by import, private
	// top-level, and submodule names must ALL count as present: the export set is
	// deliberately an over-approximation so that no valid attribute access flags.
	for _, name := range []string{
		"corr",          // __all__ + imported binding
		"cov",           // __all__ + imported binding
		"DataFrame",     // __all__ + class def
		"Series",        // __all__ + class def
		"read_csv",      // defined but NOT in __all__
		"_PRIVATE_FLAG", // private top-level assignment
		"warnings",      // plain import binding
		"cs",            // aliased from-import binding
		"functions",     // submodule file
		"selectors",     // submodule file
		"sub",           // submodule package
	} {
		if !ps.Has(name) {
			t.Errorf("Has(%q) = false, want true", name)
		}
	}
	// The whole point: a name the package does not have.
	if ps.Has("pearsonr") {
		t.Errorf("Has(\"pearsonr\") = true, want false")
	}
}

func TestLookup_AbstainCases(t *testing.T) {
	idx := newFixtureIndex(t)
	tests := []struct {
		name   string
		module string
		want   Resolution
	}{
		// PEP 562 module-level __getattr__: attributes materialize on access, so
		// absence from the static surface proves nothing.
		{"lazy __getattr__", "lazypkg", Partial},
		// A star-import whose target is not installed: the chain cannot be
		// followed, so the importer must abstain rather than report a set that
		// silently omits whatever the target would have contributed.
		{"unfollowable star import", "starbadpkg", Partial},
		// The target exists but is itself non-enumerable; Partial propagates out.
		{"star import of a lazy module", "starlazypkg", Partial},
		// __all__ built by concatenating an imported list cannot be evaluated.
		{"computed __all__", "computedpkg", Partial},
		// Never installed: must be Unknown, NOT "no such symbol".
		{"absent package", "definitelynotinstalled", Unknown},
		// A directory with no __init__.py.
		{"namespace package", "nspkg", Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := idx.Lookup(tt.module)
			if ps.Res != tt.want {
				t.Fatalf("Res = %v, want %v (reason %q)", ps.Res, tt.want, ps.Reason)
			}
			if ps.Reason == "" {
				t.Errorf("non-Resolved result must carry a Reason for diagnosability")
			}
			// The load-bearing invariant: Has is false for every name on a
			// non-Resolved package, so a caller that skips the Res check still
			// cannot be told "this symbol exists".
			if ps.Has("anything") {
				t.Errorf("Has() must be false on a %v result", tt.want)
			}
		})
	}
}

func TestLookup_SingleFileModule(t *testing.T) {
	idx := newFixtureIndex(t)
	ps := idx.Lookup("singlemod")
	if ps.Res != Resolved {
		t.Fatalf("Res = %v (%s), want Resolved", ps.Res, ps.Reason)
	}
	if !ps.Has("only_func") || !ps.Has("CONST") {
		t.Errorf("single-file module exports not found: %v", ps.Exports)
	}
	if ps.Has("nope") {
		t.Errorf("Has(\"nope\") = true, want false")
	}
}

func TestLookup_NoVenvIsUnknown(t *testing.T) {
	// No VIRTUAL_ENV and a temp dir with no .venv anywhere above it. Every lookup
	// must abstain — a repo without an identifiable environment gets no external
	// checking rather than checking against the wrong interpreter.
	t.Setenv("VIRTUAL_ENV", "")
	idx := NewPythonIndex(t.TempDir())
	ps := idx.Lookup("polarsy")
	if ps.Res != Unknown {
		t.Fatalf("Res = %v, want Unknown without a venv", ps.Res)
	}
}

func TestLookup_MalformedModulePathIsUnknown(t *testing.T) {
	idx := newFixtureIndex(t)
	// A path-traversal-shaped qualifier must not escape site-packages.
	for _, mod := range []string{"..", "../etc", "a/b", ""} {
		if ps := idx.Lookup(mod); ps.Res != Unknown {
			t.Errorf("Lookup(%q).Res = %v, want Unknown", mod, ps.Res)
		}
	}
}

func TestLookup_CachesResult(t *testing.T) {
	idx := newFixtureIndex(t)
	first := idx.Lookup("polarsy")
	second := idx.Lookup("polarsy")
	if first.Res != second.Res || len(first.Exports) != len(second.Exports) {
		t.Fatalf("cached lookup diverged: %v vs %v", first, second)
	}
}

func TestExportsFromPythonModule_TruncatedIndexNeverResolves(t *testing.T) {
	// The crux from the issue's FP analysis: a partial enumeration must never be
	// presented as complete. Each of these sources is enumerable-looking but has a
	// construct that hides names, and each must degrade rather than resolve.
	sources := map[string]string{
		"module __getattr__":  "def __getattr__(name):\n    return 1\n",
		"globals() write":     "globals().update({'a': 1})\n",
		"setattr write":       "import sys\nsetattr(sys.modules[__name__], 'a', 1)\n",
		"importlib":           "import importlib\n",
		"__all__ mutated":     "__all__ = ['a']\n__all__.extend(other)\n",
		"__all__ from call":   "__all__ = list(other)\n",
		"__all__ concat name": "__all__ = ['a'] + other\n",
	}
	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			if ps, _ := exportsFromPythonModule(src); ps.Res == Resolved {
				t.Fatalf("Res = Resolved for a non-enumerable module; exports=%v", ps.Exports)
			}
		})
	}
}

func TestExportsFromPythonModule_MultiLineImportBindings(t *testing.T) {
	// A parenthesized multi-line from-import: its continuation lines are indented,
	// so without logical-line joining the names would be dropped — an under-count,
	// which is precisely the false-positive direction.
	src := "from mod import (\n    alpha,\n    beta as gamma,\n)\n"
	ps, _ := exportsFromPythonModule(src)
	if ps.Res != Resolved {
		t.Fatalf("Res = %v (%s), want Resolved", ps.Res, ps.Reason)
	}
	for _, name := range []string{"alpha", "gamma"} {
		if !ps.Has(name) {
			t.Errorf("Has(%q) = false; multi-line import binding was dropped", name)
		}
	}
	if ps.Has("beta") {
		t.Errorf("`beta as gamma` binds gamma, not beta")
	}
}

// TestLookup_BindingsRealWorldShapes pins the three defects that only surfaced
// when the resolver was first run against a real site-packages (polars, requests,
// pandas, urllib3 et al). Each was silently producing a TRUNCATED export set that
// still reported Resolved — the exact failure mode this package is built to
// prevent, and one the earlier hand-written fixtures happened not to trigger.
func TestLookup_BindingsRealWorldShapes(t *testing.T) {
	idx := newFixtureIndex(t)
	ps := idx.Lookup("parenpkg")
	if ps.Res != Resolved {
		t.Fatalf("Res = %v (%s), want Resolved", ps.Res, ps.Reason)
	}

	// Defect 1: `__all__ = (` has an RHS of just "(", and locating that RHS by
	// CONTENT found the first parenthesis anywhere in the module — parsing an
	// unrelated region and losing every declared name. Any module with a call or
	// a tuple above its __all__ (i.e. most of them) was affected: requests,
	// urllib3, and pandas all resolved to Partial or to a wrong set because of it.
	for _, name := range []string{"get", "post"} {
		if !ps.Has(name) {
			t.Errorf("Has(%q) = false; __all__ tuple was not parsed", name)
		}
	}

	// Defect 2: names bound inside a module-level try/except or if — `ssl` in
	// requests and urllib3, the CAPI guards in pandas — are indented, and the
	// original "column zero only" rule dropped them. They ARE module attributes,
	// so dropping them meant flagging valid code.
	for _, name := range []string{"ssl", "PLATFORM_FLAG", "_HOME", "_TABLE", "os"} {
		if !ps.Has(name) {
			t.Errorf("Has(%q) = false; module-level binding inside a block was dropped", name)
		}
	}

	// Defect 2's boundary: excluding def/class BODIES is still required, or a
	// function's locals would masquerade as module attributes and mask real
	// hallucinations.
	if ps.Has("local_only") {
		t.Errorf("a def-body local leaked into the module export set")
	}
}

// TestLookup_ResolveBudget pins the latency backstop: past maxResolvesPerRun
// distinct modules, further lookups abstain rather than spending unbounded time
// on the guard's edit-time path. Cached modules do not consume budget.
func TestLookup_ResolveBudget(t *testing.T) {
	idx := newFixtureIndex(t)
	for i := 0; i < maxResolvesPerRun; i++ {
		idx.Lookup(fmt.Sprintf("absent_module_%d", i))
	}
	// Budget spent on modules that do not exist; a real one now abstains too.
	ps := idx.Lookup("polarsy")
	if ps.Res != Unknown || !strings.Contains(ps.Reason, "budget") {
		t.Fatalf("Res = %v (%q), want Unknown with a budget reason", ps.Res, ps.Reason)
	}
	// A module already in the cache is still answered — no disk work needed.
	if again := idx.Lookup("absent_module_0"); again.Res != Unknown || strings.Contains(again.Reason, "budget") {
		t.Errorf("cached lookup should be served from cache, got %q", again.Reason)
	}
}

// TestLookup_FollowsStarImports pins the reach win from following
// `from X import *` one level instead of abstaining on sight. Star-imports are
// how httpx (`._api`) and PyYAML (`.error`) build their public surface, so
// treating them as automatically unknowable gave up real coverage for no safety
// gain: the target is a module sitting on disk that we can simply read.
func TestLookup_FollowsStarImports(t *testing.T) {
	idx := newFixtureIndex(t)
	ps := idx.Lookup("starpkg")
	if ps.Res != Resolved {
		t.Fatalf("Res = %v (%s), want Resolved", ps.Res, ps.Reason)
	}
	if !ps.Has("hidden") {
		t.Errorf("Has(\"hidden\") = false; the star-import target was not followed")
	}
	if !ps.Has("local") {
		t.Errorf("Has(\"local\") = false; the importer's own bindings were lost")
	}
	if ps.Has("definitely_absent") {
		t.Errorf("following a star must not make the set match everything")
	}
}

func TestResolveStarTarget(t *testing.T) {
	// PEP 328 relative-import arithmetic: a package is its own base, a plain
	// module resolves against its parent, and each dot beyond the first climbs one
	// level. Getting this wrong resolves to the WRONG module, which would union in
	// an unrelated export set — a silent correctness hole rather than an abstain.
	tests := []struct {
		module string
		isPkg  bool
		spec   string
		want   string
		ok     bool
	}{
		{"httpx", true, ".", "httpx", true},
		{"httpx", true, "._api", "httpx._api", true},
		{"yaml", true, ".error", "yaml.error", true},
		{"pkg.sub", true, ".mod", "pkg.sub.mod", true},
		{"pkg.sub", true, "..other", "pkg.other", true},
		{"pkg.mod", false, ".sibling", "pkg.sibling", true},
		{"pkg", true, "absolute.thing", "absolute.thing", true},
		{"pkg", true, "..too_high", "", false},
		{"pkg", true, "", "", false},
	}
	for _, tt := range tests {
		got, ok := resolveStarTarget(tt.module, tt.isPkg, tt.spec)
		if got != tt.want || ok != tt.ok {
			t.Errorf("resolveStarTarget(%q, %v, %q) = (%q, %v), want (%q, %v)",
				tt.module, tt.isPkg, tt.spec, got, ok, tt.want, tt.ok)
		}
	}
}
