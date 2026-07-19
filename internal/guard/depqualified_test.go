package guard

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/depindex"
)

// stubIndex is a fixed answer table, so the gate tests below exercise the guard's
// abstain logic in isolation from how a real environment is discovered. Resolution
// correctness is depindex's own test's job.
type stubIndex map[string]depindex.PackageSymbols

func (s stubIndex) Lookup(module string) depindex.PackageSymbols {
	if ps, ok := s[module]; ok {
		return ps
	}
	return depindex.PackageSymbols{Res: depindex.Unknown, Reason: "not in stub"}
}

func resolvedPkg(names ...string) depindex.PackageSymbols {
	set := map[string]struct{}{}
	for _, n := range names {
		set[n] = struct{}{}
	}
	return depindex.PackageSymbols{Res: depindex.Resolved, Exports: set}
}

// polarsyStub mirrors the depindex fixture package: it has corr, not pearsonr.
var polarsyStub = stubIndex{"polarsy": resolvedPkg("corr", "cov", "DataFrame", "read_csv")}

func pyDepViolations(t *testing.T, src string, idx depindex.Index) []Violation {
	t.Helper()
	lines := TextToAddedLines(src)
	return PyDepQualifiedViolations(nil, lines, idx)
}

func symbolList(vs []Violation) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Symbol)
	}
	return out
}

func TestPyDepQualified_FlagsAbsentSymbol(t *testing.T) {
	src := "import polarsy as pl\n\nresult = pl.pearsonr(\"a\", \"b\")\n"
	got := pyDepViolations(t, src, polarsyStub)
	if len(got) != 1 || got[0].Symbol != "pl.pearsonr" {
		t.Fatalf("violations = %v, want exactly [pl.pearsonr]", symbolList(got))
	}
	if got[0].Lang != LangPython {
		t.Errorf("Lang = %q, want %q", got[0].Lang, LangPython)
	}
	if got[0].Line != 3 {
		t.Errorf("Line = %d, want 3", got[0].Line)
	}
	// The suggestion comes from the DEPENDENCY's export set, not the repo's —
	// "did you mean polarsy.corr" is the useful answer here.
	if got[0].Suggestion != "" && got[0].Suggestion != "corr" && got[0].Suggestion != "cov" {
		t.Errorf("Suggestion = %q, want a polarsy name or none", got[0].Suggestion)
	}
}

// Each of these must produce ZERO violations. They are the false-positive suite:
// every entry is valid code, and any regression that flags one of them breaks the
// invariant this whole check is built around.
func TestPyDepQualified_NeverFlags(t *testing.T) {
	tests := []struct {
		name string
		src  string
		idx  depindex.Index
	}{
		{
			"symbol exists in the package",
			"import polarsy as pl\nx = pl.corr(\"a\", \"b\")\n",
			polarsyStub,
		},
		{
			"symbol exists but is not in __all__",
			"import polarsy as pl\nx = pl.read_csv(\"f.csv\")\n",
			polarsyStub,
		},
		{
			// Gate 4: a lazily-populated module proves nothing by absence.
			"partial resolution",
			"import lazypkg as lp\nx = lp.whatever()\n",
			stubIndex{"lazypkg": {Res: depindex.Partial, Reason: "lazy"}},
		},
		{
			// Gate 4: not installed / no environment.
			"unknown resolution",
			"import numpy as np\nx = np.pearsonr(1, 2)\n",
			stubIndex{},
		},
		{
			// Gate 2: `pl` is used bare, so it may be a local rebinding and
			// `pl.pearsonr` may be an instance method call.
			"alias also used bare",
			"import polarsy as pl\nhelper(pl)\nx = pl.pearsonr(\"a\", \"b\")\n",
			polarsyStub,
		},
		{
			// Gate 2 again, the assignment form.
			"alias shadowed by assignment",
			"import polarsy as pl\npl = make_thing()\nx = pl.pearsonr(\"a\")\n",
			polarsyStub,
		},
		{
			// Gate 3: a monkey-patched module has attributes no index can know.
			"alias is monkey-patched",
			"import polarsy as pl\npl.pearsonr = my_impl\nx = pl.pearsonr(\"a\", \"b\")\n",
			polarsyStub,
		},
		{
			// Gate 1: an indented import is conditional and may bind a stub.
			"conditional import",
			"try:\n    import polarsy as pl\nexcept ImportError:\n    pl = None\nx = pl.pearsonr(\"a\")\n",
			polarsyStub,
		},
		{
			// `from x import y` binds y, not a qualifier — nothing for this check.
			"from-import binds no qualifier",
			"from polarsy import pearsonr\nx = pearsonr(\"a\", \"b\")\n",
			polarsyStub,
		},
		{
			// A deeper selector is an attribute of an attribute, not a module call.
			"deeper selector",
			"import polarsy as pl\nx = obj.pl.pearsonr(\"a\")\n",
			polarsyStub,
		},
		{
			// Submodule access resolves through the left-guard, never flagged.
			"submodule call",
			"import polarsy as pl\nx = pl.functions.pearsonr(\"a\")\n",
			polarsyStub,
		},
		{
			"inside a string literal",
			"import polarsy as pl\nx = \"pl.pearsonr(1)\"\n",
			polarsyStub,
		},
		{
			"inside a comment",
			"import polarsy as pl\n# x = pl.pearsonr(1)\n",
			polarsyStub,
		},
		{
			"inside a docstring",
			"import polarsy as pl\n\"\"\"\nusage: pl.pearsonr(a, b)\n\"\"\"\n",
			polarsyStub,
		},
		{
			"no imports at all",
			"x = pl.pearsonr(\"a\")\n",
			polarsyStub,
		},
		{
			"nil index",
			"import polarsy as pl\nx = pl.pearsonr(\"a\")\n",
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pyDepViolations(t, tt.src, tt.idx); len(got) != 0 {
				t.Fatalf("violations = %v, want none", symbolList(got))
			}
		})
	}
}

func TestPyDepQualified_UnaliasedImport(t *testing.T) {
	src := "import polarsy\nx = polarsy.pearsonr(\"a\", \"b\")\n"
	got := pyDepViolations(t, src, polarsyStub)
	if len(got) != 1 || got[0].Symbol != "polarsy.pearsonr" {
		t.Fatalf("violations = %v, want [polarsy.pearsonr]", symbolList(got))
	}
}

func TestPyDepQualified_DedupesRepeatedReference(t *testing.T) {
	src := "import polarsy as pl\na = pl.pearsonr(1)\nb = pl.pearsonr(2)\n"
	got := pyDepViolations(t, src, polarsyStub)
	if len(got) != 1 {
		t.Fatalf("violations = %v, want one deduped entry", symbolList(got))
	}
}

func TestPyDepQualified_ImportAddedInSameEdit(t *testing.T) {
	// The import arrives in the edited text while the pre-edit file is empty —
	// the gates must see the concatenation, not just the old file.
	added := TextToAddedLines("import polarsy as pl\nx = pl.pearsonr(\"a\")\n")
	got := PyDepQualifiedViolations(nil, added, polarsyStub)
	if len(got) != 1 {
		t.Fatalf("violations = %v, want one", symbolList(got))
	}
}

func TestPyDepQualified_ShadowInPreEditFileSuppresses(t *testing.T) {
	// The shadowing binding lives in the pre-edit file and the call in the edit.
	// Passing both is what lets gate 2 see the shadow.
	whole := TextToAddedLines("import polarsy as pl\npl = build()\n")
	added := TextToAddedLines("x = pl.pearsonr(\"a\")\n")
	if got := PyDepQualifiedViolations(whole, added, polarsyStub); len(got) != 0 {
		t.Fatalf("violations = %v, want none (shadow is in the pre-edit file)", symbolList(got))
	}
}

// TestPyDepQualified_EndToEndAgainstFixtureVenv wires the real resolver to the
// real guard against the vendored fixture environment — the one test that proves
// the two halves agree.
//
// It uses the SHAPE of captured-corpus case obs-py-004 (`pl.pearsonr(...)`, an
// invented scipy API on the polars module) but deliberately not the real polars:
// polars 1.42 defines a module-level __getattr__, so the real package resolves
// Partial and the guard correctly abstains on it. That is a true limitation, not
// a gap in this test — see the "reach" section of
// ~/.claude/plans/runecho-175-external-dep-symbol-index.md. The fixture models a
// static-__all__ package, which is the shape this check can actually validate.
func TestPyDepQualified_EndToEndAgainstFixtureVenv(t *testing.T) {
	abs, err := filepath.Abs(filepath.Join("..", "depindex", "testdata", "venv"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIRTUAL_ENV", abs)
	idx := depindex.NewPythonIndex(t.TempDir())

	hallucinated := "import polarsy as pl\nr = pl.pearsonr(\"p_mkt\", \"p_ml\")\n"
	got := PyDepQualifiedViolations(nil, TextToAddedLines(hallucinated), idx)
	if len(got) != 1 || !strings.HasSuffix(got[0].Symbol, ".pearsonr") {
		t.Fatalf("violations = %v, want [pl.pearsonr]", symbolList(got))
	}

	// The corrected call in the same shape must stay silent.
	real := "import polarsy as pl\nr = pl.corr(\"p_mkt\", \"p_ml\")\n"
	if got := PyDepQualifiedViolations(nil, TextToAddedLines(real), idx); len(got) != 0 {
		t.Fatalf("violations = %v on valid code, want none", symbolList(got))
	}
}

// TestPyDepQualified_PrivateSelectorAbstains pins the gate added after the
// ground-truth run: pandas exposes _pandas_parser_CAPI, a name its compiled
// extension injects at import time and that NO source-level index can see.
// Underscore-prefixed attributes are the Python analogue of Go's unexported
// identifiers — never flagged.
func TestPyDepQualified_PrivateSelectorAbstains(t *testing.T) {
	src := "import polarsy as pl\nx = pl._internal_capi()\n"
	if got := pyDepViolations(t, src, polarsyStub); len(got) != 0 {
		t.Fatalf("violations = %v, want none for a private selector", symbolList(got))
	}
}
