package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/ir"
)

// claimSymbolsOf pulls claim_symbols off a decision record as check -> names.
// The JSONL round-trip lands it as map[string]any of []any, which is tedious
// enough to unpack that every caller doing it inline would drift.
func claimSymbolsOf(t *testing.T, rec map[string]any) map[string][]string {
	t.Helper()
	raw, ok := rec["claim_symbols"].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string][]string{}
	for check, v := range raw {
		list, ok := v.([]any)
		if !ok {
			t.Fatalf("claim_symbols[%q] is %T, want a list", check, v)
		}
		for _, s := range list {
			out[check] = append(out[check], s.(string))
		}
	}
	return out
}

// A file-scope ask must record its flagged name as a rateable CLAIM while
// recording nothing for learned-allow. The two fields answer different
// questions (#393): approving a file-scope ask on `render` says the edit was
// fine, never that `render` resolves — folding it into learned-allow would
// blind the hallucination check to that name until the TTL expires.
func TestFileScopeAskClaimsWithoutTrainingLearnedAllow(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, map[string]ir.FileIR{
		"app/render.py": {Hash: "h1", Symbols: funcsToSymbols([]string{"render"})},
	})
	t.Setenv("RUNECHO_GUARD_FILESCOPE", "1")

	file := filepath.Join(top, "tests", "test_r.py")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	// The pre-edit file neither defines nor imports `render`.
	if err := os.WriteFile(file, []byte("import pytest\n\n\ndef test_it():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, raw, d := runHook(t, payloadOld(t, "Edit", file, "    pass", "    out = render(1)", "", nil))
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("an out-of-scope reference must ask; got %q\n%s", d.Hook.PermissionDec, raw)
	}

	rec := readLastDecisionLog(t)
	claims := claimSymbolsOf(t, rec)
	if got := claims["file-scope"]; len(got) != 1 || got[0] != "render" {
		t.Errorf("claim_symbols[file-scope] = %v, want [render] — without it fpaudit can never rate this check", got)
	}
	if _, present := rec["learn_symbols"]; present {
		t.Errorf("a file-scope ask must not offer symbols to learned-allow: %v", rec["learn_symbols"])
	}
}

// The coverage fix learn_symbols could never deliver. A contract-merged ask
// zeroes learn_symbols on purpose — an approval there answers the SCOPE
// question — but the hallucination check still made its resolution claim, and
// fpaudit's oracle consults no human judgement at all. These records read as
// "missing coverage" in the audit's own n/a note for 95 days.
func TestContractMergedAskStillClaimsItsViolations(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	top := contractRepo(t, "sess", inScopeBody)
	body := "package main\n\nfunc F() { TotallyMadeUpSymbol() }\n"

	_, _, d := runHook(t, contractPayload(t, "sess", filepath.Join(top, "internal", "y.go"), body))
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("expected an ask; got %q", d.Hook.PermissionDec)
	}
	rec := readLastDecisionLog(t)
	if _, present := rec["learn_symbols"]; present {
		t.Fatalf("precondition broken: a contract-merged ask must still not train learned-allow: %v", rec["learn_symbols"])
	}
	if got := claimSymbolsOf(t, rec)["violations"]; len(got) != 1 || got[0] != "TotallyMadeUpSymbol" {
		t.Errorf("claim_symbols[violations] = %v, want [TotallyMadeUpSymbol] — the check's claim does not depend on what an approval licenses", got)
	}
}

// A plain hallucination ask claims its own name, and claims it under
// "violations" specifically — the key is what selects the repo-wide oracle
// question in guardstats.claimScope.
func TestHallucinationAskClaimsUnderViolations(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))

	file := filepath.Join(top, "z.go")
	_, raw, d := runHook(t, payloadOld(t, "Write", file, "", "",
		"package main\n\nfunc F() { TotallyMadeUpSymbol() }\n", nil))
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("expected an ask; got %q\n%s", d.Hook.PermissionDec, raw)
	}
	if got := claimSymbolsOf(t, readLastDecisionLog(t))["violations"]; len(got) != 1 || got[0] != "TotallyMadeUpSymbol" {
		t.Errorf("claim_symbols[violations] = %v, want [TotallyMadeUpSymbol]", got)
	}
}

// call-shape must NEVER appear in claim_symbols. Its own ask header says "the
// symbol resolves but the call does not match it" — the callee is defined by
// construction, so a dated "was it defined" question answers yes for every
// correct catch and would report the whole check as false positives.
func TestCallShapeAskClaimsNothing(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, map[string]ir.FileIR{
		"a.py": {Hash: "h1", Symbols: funcsToSymbols([]string{"send"})},
	})
	t.Setenv("RUNECHO_GUARD_CALLSHAPE", "1")

	file := filepath.Join(top, "a.py")
	body := "def send(to, subject):\n    return (to, subject)\n\n\ndef go():\n    return send(to='x', body='y')\n"
	_, raw, d := runHook(t, payloadOld(t, "Write", file, "", "", body, nil))
	if d.Hook.PermissionDec != "ask" {
		t.Skipf("call-shape did not fire in this environment; got %q\n%s", d.Hook.PermissionDec, raw)
	}
	rec := readLastDecisionLog(t)
	// Not vacuous: the ask must actually be the call-shape one.
	if rec["reason"] != "call-shape" {
		t.Fatalf("decision reason = %v, want call-shape — this assertion is only meaningful on a call-shape ask", rec["reason"])
	}
	if got := claimSymbolsOf(t, rec)["call-shape"]; got != nil {
		t.Errorf("claim_symbols[call-shape] = %v, want nothing — the callee resolves by construction", got)
	}
}

// lintClaimSymbols is the F821/F811 split, unit-tested so it does not depend on
// ruff being installed. F811 asserts the symbol IS defined (twice) — the
// duplicate-symbol shape — and a rule-code fallback is not a name any oracle
// can look up.
func TestLintClaimSymbolsTakesOnlyResolvableF821(t *testing.T) {
	in := []lintFinding{
		{Line: 3, Rule: "F821", Symbol: "helper", Message: "Undefined name `helper`"},
		{Line: 7, Rule: "F811", Symbol: "fetch", Message: "Redefinition of unused `fetch` from line 1"},
		{Line: 9, Rule: "F821", Symbol: "", Message: "ruff reworded this message"},
	}
	got := lintClaimSymbols(in)
	if len(got) != 1 || got[0] != "helper" {
		t.Errorf("lintClaimSymbols = %v, want [helper] — F811 is the duplicate shape and a bare rule code is not an identifier", got)
	}
	if lintClaimSymbols(nil) != nil {
		t.Error("lintClaimSymbols(nil) must be nil so the check stays absent from claim_symbols")
	}
}
