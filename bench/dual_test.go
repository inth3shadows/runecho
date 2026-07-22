package bench

import "testing"

// TestDualCompatPinsLegacyCorpus is the load-bearing invariant of the Part A
// schema change (issue #171): adding the optional whole-file context fields and
// the qualified-check wiring must NOT move the numbers the positioning quotes.
// The legacy corpus carries none of the new fields, so BOTH configurations must
// reproduce the historical observed score exactly — 4/9 caught, 0/6 false
// positives. If this drifts, the schema change leaked into the baseline.
func TestDualCompatPinsLegacyCorpus(t *testing.T) {
	cases, err := LoadCaptured(realCorpus)
	if err != nil {
		t.Fatalf("loading %s: %v", realCorpus, err)
	}
	dual := ScoreCapturedDual(cases)
	t.Logf("\n%s", dual.Format())

	base := dual.Baseline.ByStratum[Observed]
	if base == nil {
		t.Fatal("no observed stratum in baseline")
	}
	if base.TP != 4 || base.TP+base.FN != 9 {
		t.Errorf("baseline observed caught = %d/%d, want 4/9 (schema change moved the baseline)", base.TP, base.TP+base.FN)
	}
	if base.FP != 0 || base.FP+base.TN != 6 {
		t.Errorf("baseline observed fp = %d/%d, want 0/6", base.FP, base.FP+base.TN)
	}

	// With no case carrying whole-file context, the qualified checks cannot fire,
	// so enhanced must equal baseline. Any divergence means a qualified check ran
	// on a case without opt-in context — a wiring bug.
	enh := dual.Enhanced.ByStratum[Observed]
	if enh.TP != base.TP || enh.FP != base.FP {
		t.Errorf("enhanced diverged from baseline on the legacy corpus (caught %d→%d, fp %d→%d); qualified checks fired without file_context",
			base.TP, enh.TP, base.FP, enh.FP)
	}
}

// TestDualEnhancedCatchesInternalQualified proves the wiring end to end: an
// internal-package qualified hallucination (internalpkg.NoSuchFunc — the #176
// case) is INVISIBLE to the baseline (guard.Run drops every dotted selector) and
// CAUGHT once the qualified check is enabled and the case supplies its file. This
// is the case the legacy corpus lacks entirely, and the reason Part A exists.
func TestDualEnhancedCatchesInternalQualified(t *testing.T) {
	cc := CapturedCase{
		ID:         "wire-go-internal-qualified",
		Lang:       "go",
		SourceLine: "\tresult := internalpkg.NoSuchFunc(ctx)",
		// Bare selector, matching how the legacy corpus labels referenced_symbol.
		ReferencedSymbol: "NoSuchFunc",
		Label:            "hallucinated",
		// Frozen repo symbols: NoSuchFunc is absent, so the guard may flag it.
		KnownSymbols: []string{"RealFunc", "OtherFunc"},
		Provenance:   "transcript:wire-test",
		Notes:        "position=qualified-call; synthetic wiring proof, not a mined case",
		ModulePath:   "example.com/repo",
		FileContext: []string{
			"package caller",
			"",
			"import (",
			"\t\"context\"",
			"\tinternalpkg \"example.com/repo/internalpkg\"",
			")",
			"",
			"func run(ctx context.Context) {",
			"\tresult := internalpkg.NoSuchFunc(ctx)",
			"\t_ = result",
			"}",
		},
	}
	if err := cc.validate(); err != nil {
		t.Fatalf("fixture invalid: %v", err)
	}
	c := cc.toCase()

	if guardFlags(c, baselineConfig) {
		t.Error("baseline flagged a qualified call — guard.Run should drop dotted selectors")
	}
	if !guardFlags(c, enhancedConfig) {
		t.Error("enhanced did NOT flag internalpkg.NoSuchFunc — qualified wiring is broken")
	}
}

// TestDualEnhancedNoFalsePositiveOnRealInternalQualified is the negative twin:
// a qualified call to a symbol that DOES exist in the repo must not be flagged
// even with the check on. Guards the FP invariant the whole tool rests on.
func TestDualEnhancedNoFalsePositiveOnRealInternalQualified(t *testing.T) {
	cc := CapturedCase{
		ID:               "wire-go-internal-qualified-real",
		Lang:             "go",
		SourceLine:       "\tresult := internalpkg.RealFunc(ctx)",
		ReferencedSymbol: "RealFunc",
		Label:            "real",
		KnownSymbols:     []string{"RealFunc", "OtherFunc"},
		Provenance:       "transcript:wire-test",
		Notes:            "position=qualified-call; real symbol, must not flag",
		ModulePath:       "example.com/repo",
		FileContext: []string{
			"package caller",
			"import internalpkg \"example.com/repo/internalpkg\"",
			"func run(ctx context.Context) {",
			"\tresult := internalpkg.RealFunc(ctx)",
			"\t_ = result",
			"}",
		},
	}
	if err := cc.validate(); err != nil {
		t.Fatalf("fixture invalid: %v", err)
	}
	c := cc.toCase()
	if guardFlags(c, enhancedConfig) {
		t.Error("enhanced flagged internalpkg.RealFunc — a real symbol; false positive")
	}
}

// TestDualEnhancedCatchesExternalDepQualified proves the #175 external-dependency
// path through the frozen index: a call to a symbol absent from an imported
// package's frozen export set is caught only when the check is on.
func TestDualEnhancedCatchesExternalDepQualified(t *testing.T) {
	cc := CapturedCase{
		ID:               "wire-go-dep-qualified",
		Lang:             "go",
		SourceLine:       "\tresp, err := http.Gett(url)",
		ReferencedSymbol: "Gett",
		Label:            "hallucinated",
		// Gett is absent from net/http; the frozen index below lists the real names.
		KnownSymbols: []string{"handler"},
		Provenance:   "transcript:wire-test",
		Notes:        "position=qualified-call; net/http has Get, not Gett",
		ModulePath:   "example.com/repo",
		DepExports: map[string][]string{
			"net/http": {"Get", "Post", "NewRequest", "Client", "Handler"},
		},
		FileContext: []string{
			"package caller",
			"import \"net/http\"",
			"func run(url string) {",
			"\tresp, err := http.Gett(url)",
			"\t_, _ = resp, err",
			"}",
		},
	}
	if err := cc.validate(); err != nil {
		t.Fatalf("fixture invalid: %v", err)
	}
	c := cc.toCase()
	if guardFlags(c, baselineConfig) {
		t.Error("baseline flagged a dep-qualified call — should be invisible without the check")
	}
	if !guardFlags(c, enhancedConfig) {
		t.Error("enhanced did NOT flag http.Gett — external-dep wiring is broken")
	}
}

// TestDualEnhancedFrozenIndexAbstainsOnRealDepSymbol pins the FP direction for
// #175: a real symbol on a resolved package must pass, and any package not in the
// frozen index must abstain (Unknown), never flag.
func TestDualEnhancedFrozenIndexAbstainsOnRealDepSymbol(t *testing.T) {
	real := CapturedCase{
		ID: "wire-go-dep-real", Lang: "go",
		SourceLine: "\tresp, err := http.Get(url)", ReferencedSymbol: "Get",
		Label: "real", KnownSymbols: []string{"handler"}, Provenance: "transcript:wire-test",
		Notes: "position=qualified-call; Get is real", ModulePath: "example.com/repo",
		DepExports: map[string][]string{"net/http": {"Get", "Post"}},
		FileContext: []string{
			"package caller", "import \"net/http\"",
			"func run(url string) { resp, err := http.Get(url); _, _ = resp, err }",
		},
	}
	if guardFlags(real.toCase(), enhancedConfig) {
		t.Error("enhanced flagged http.Get — a real symbol on a resolved package")
	}

	// Same shape, but the CALLED package is not in the frozen index → Unknown →
	// abstain, even though the symbol is a hallucination. A miss is correct here;
	// a flag on an unresolved package would be the dangerous direction.
	//
	// dep_exports is deliberately NON-empty (it lists net/http, which this file
	// does not import) so the external-dep check actually RUNS — an empty map would
	// short-circuit guardFlags before the check and make the assertion vacuous.
	// The index simply has no entry for the mystery package, so Lookup → Unknown.
	unresolved := real
	unresolved.SourceLine = "\tx := mystery.Nope(1)"
	unresolved.ReferencedSymbol = "Nope"
	unresolved.Label = "hallucinated"
	unresolved.DepExports = map[string][]string{"net/http": {"Get"}} // resolves net/http, NOT mystery
	unresolved.FileContext = []string{
		"package caller", "import \"example.com/external/mystery\"",
		"func run() { x := mystery.Nope(1); _ = x }",
	}
	unresolved.ModulePath = "example.com/repo"
	if guardFlags(unresolved.toCase(), enhancedConfig) {
		t.Error("enhanced flagged a call on a package absent from the frozen index — must abstain (Unknown)")
	}
}

// TestCapturedRejectsQualifiedFieldsOnNonGo pins the fixture-validation guard:
// module_path or dep_exports on a non-Go case is a mislabel (the qualified checks
// are Go-only), and file_context is required whenever they are set.
func TestCapturedRejectsQualifiedFieldsOnNonGo(t *testing.T) {
	nonGo := CapturedCase{
		ID: "x", Lang: "py", SourceLine: "x = pkg.foo()", ReferencedSymbol: "foo",
		Label: "hallucinated", KnownSymbols: []string{"bar"}, Provenance: "transcript:t",
		ModulePath: "example.com/repo", FileContext: []string{"import pkg"},
	}
	if err := nonGo.validate(); err == nil {
		t.Error("expected validation error for module_path on a non-go case")
	}

	noCtx := CapturedCase{
		ID: "y", Lang: "go", SourceLine: "\tx := pkg.Foo()", ReferencedSymbol: "Foo",
		Label: "hallucinated", KnownSymbols: []string{"Bar"}, Provenance: "transcript:t",
		ModulePath: "example.com/repo", // no FileContext
	}
	if err := noCtx.validate(); err == nil {
		t.Error("expected validation error for module_path without file_context")
	}
}
