package guardstats

import (
	"testing"
)

// auditClaimAsk is auditAsk with claim_symbols set — the #393 record shape.
func auditClaimAsk(at, file, reason string, syms []string, learn []string, claims map[string][]string) Decision {
	d := auditAsk(at, file, reason, syms, learn)
	d.ClaimSymbols = claims
	return d
}

// THE regression for #393's central finding. A file-scope ask flags a name that
// IS declared elsewhere in the repo — that is the premise of the finding, not
// evidence against it. Answering it with the repo-wide question reports every
// correct file-scope catch as a resolver false positive, which is the same bug
// VerdictNA was created to fix for duplicate-symbol (27 fp, 0 stands on the
// audit's first run).
func TestAuditFileScopeAsksTheFileScopedQuestion(t *testing.T) {
	o := &fakeOracle{
		head:  "HEAD",
		revAt: map[string]string{"": "R0"},
		// `render` is declared repo-wide at both commits, and never reachable in
		// the edited file. A correct file-scope catch.
		scopedDefined: map[[3]string]bool{
			{"R0", "render", "repo"}:   true,
			{"HEAD", "render", "repo"}: true,
			{"R0", "render", "file"}:   false,
			{"HEAD", "render", "file"}: false,
		},
	}
	d := auditClaimAsk("2026-09-01T10:00:00Z", "/wt/tests/test_r.py", "file-scope",
		[]string{"render"}, nil, map[string][]string{"file-scope": {"render"}})

	s := Audit([]Decision{d}, auditTS("2026-08-01T00:00:00Z"), o)
	if got := s.Findings[0].Verdict; got != VerdictStands {
		t.Fatalf("file-scope verdict = %q, want %q — the repo-wide question would say %q",
			got, VerdictStands, VerdictFP)
	}
	if got := s.Findings[0].Scope; got != ScopeFile {
		t.Errorf("finding scope = %q, want %q", got, ScopeFile)
	}
}

// The premature reading has to survive the scope split too: the agent referenced
// a name the file could not see, then added the import. Repo-wide this is
// invisible (defined at both commits); file-scoped it is the correct verdict,
// and it is the verdict that actually tells the project what to fix.
func TestAuditFileScopePrematureIsTheImportArrivingLater(t *testing.T) {
	o := &fakeOracle{
		head:  "HEAD",
		revAt: map[string]string{"": "R0"},
		scopedDefined: map[[3]string]bool{
			{"R0", "render", "repo"}:   true,
			{"HEAD", "render", "repo"}: true,
			{"R0", "render", "file"}:   false,
			{"HEAD", "render", "file"}: true,
		},
	}
	d := auditClaimAsk("2026-09-01T10:00:00Z", "/wt/tests/test_r.py", "file-scope",
		[]string{"render"}, nil, map[string][]string{"file-scope": {"render"}})

	s := Audit([]Decision{d}, auditTS("2026-08-01T00:00:00Z"), o)
	if got := s.Findings[0].Verdict; got != VerdictPremature {
		t.Fatalf("file-scope verdict = %q, want %q", got, VerdictPremature)
	}
}

// The memo key must carry the scope. Without it the first (repo-scoped) answer
// for a symbol is served to the next file-scoped question for the same symbol at
// the same commit — silently, with no error and no way to notice from the output.
func TestAuditMemoDoesNotServeARepoAnswerToAFileQuestion(t *testing.T) {
	o := &fakeOracle{
		head:  "HEAD",
		revAt: map[string]string{"": "R0"},
		scopedDefined: map[[3]string]bool{
			{"R0", "render", "repo"}:   true,
			{"HEAD", "render", "repo"}: true,
			{"R0", "render", "file"}:   false,
			{"HEAD", "render", "file"}: false,
		},
	}
	// Same file, same symbol, same commit — one repo-scoped ask (violations),
	// then one file-scoped ask. The violations ask warms the memo.
	ds := []Decision{
		auditClaimAsk("2026-09-01T10:00:00Z", "/wt/tests/test_r.py", "violations",
			[]string{"render"}, []string{"render"}, map[string][]string{"violations": {"render"}}),
		auditClaimAsk("2026-09-01T11:00:00Z", "/wt/tests/test_r.py", "file-scope",
			[]string{"render"}, nil, map[string][]string{"file-scope": {"render"}}),
	}
	s := Audit(ds, auditTS("2026-08-01T00:00:00Z"), o)
	if len(s.Findings) != 2 {
		t.Fatalf("want 2 findings, got %d", len(s.Findings))
	}
	if s.Findings[0].Verdict != VerdictFP {
		t.Errorf("violations verdict = %q, want %q (defined repo-wide at ask time)", s.Findings[0].Verdict, VerdictFP)
	}
	if s.Findings[1].Verdict != VerdictStands {
		t.Errorf("file-scope verdict = %q, want %q — a memo without scope in its key would serve the repo answer here",
			s.Findings[1].Verdict, VerdictStands)
	}
}

// call-shape's own ask header says "the symbol resolves but the call does not
// match it". Its callee is defined by construction, so it must never become
// rateable — and its n/a must say WHICH kind, because "the question does not
// apply" and "missing coverage" call for opposite responses.
func TestAuditCallShapeStaysNAWithTheCorrectReason(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "R0"}}
	d := auditClaimAsk("2026-09-01T10:00:00Z", "/wt/a.py", "call-shape",
		[]string{"fetch_page"}, nil, map[string][]string{})

	s := Audit([]Decision{d}, auditTS("2026-08-01T00:00:00Z"), o)
	f := s.Findings[0]
	if f.Verdict != VerdictNA {
		t.Fatalf("call-shape verdict = %q, want %q", f.Verdict, VerdictNA)
	}
	if f.Note != NAReasonNotResolutionClaim {
		t.Errorf("na reason = %q, want %q", f.Note, NAReasonNotResolutionClaim)
	}
	if o.calls != 0 {
		t.Errorf("oracle consulted %d times for an n/a pair; want 0", o.calls)
	}
}

// qualified IS a resolution claim; this oracle just cannot ask it (it strips the
// qualifier and searches tree-wide). That is missing coverage, and pooling it
// with call-shape's genuine n/a is what let #393 sit unnoticed for 95 days.
func TestAuditQualifiedNAReadsAsMissingCoverage(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "R0"}}
	d := auditClaimAsk("2026-09-01T10:00:00Z", "/wt/a.go", "qualified",
		[]string{"pkg.Helper"}, nil, map[string][]string{})

	s := Audit([]Decision{d}, auditTS("2026-08-01T00:00:00Z"), o)
	if got := s.Findings[0].Note; got != NAReasonNoOracleQuestion {
		t.Fatalf("na reason = %q, want %q", got, NAReasonNoOracleQuestion)
	}
	if s.NAReasons[NAReasonNoOracleQuestion] != 1 {
		t.Errorf("NAReasons = %v, want one %s", s.NAReasons, NAReasonNoOracleQuestion)
	}
}

// The coverage fix that learn_symbols could never deliver: a contract-merged ask
// zeroes learn_symbols on purpose (an approval there answers the scope question,
// not the resolution one) — but the CHECK still made its resolution claim, and
// the oracle consults no human judgement at all. These records were n/a for
// 95 days for a reason that never applied to them.
func TestAuditContractMergedViolationsAreRated(t *testing.T) {
	o := &fakeOracle{
		head:    "HEAD",
		revAt:   map[string]string{"": "R0"},
		defined: map[[2]string]bool{},
	}
	d := auditClaimAsk("2026-09-01T10:00:00Z", "/wt/a.py", "contract+violations",
		[]string{"Ghost"}, nil, map[string][]string{"violations": {"Ghost"}})
	d.LearnSymbols = []string{} // what the guard writes on a merged ask

	s := Audit([]Decision{d}, auditTS("2026-08-01T00:00:00Z"), o)
	if got := s.Findings[0].Verdict; got != VerdictStands {
		t.Fatalf("contract-merged violations verdict = %q, want %q (rated, not n/a)", got, VerdictStands)
	}
}

// A check name the guard recorded but claimScope does not know must be DROPPED,
// not defaulted to the repo-wide question. Defaulting is how a check with the
// wrong question silently starts emitting confident, wrong verdicts — the exact
// failure mode this whole type exists to prevent.
func TestAuditUnknownCheckInClaimSymbolsIsNotDefaulted(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "R0"}}
	d := auditClaimAsk("2026-09-01T10:00:00Z", "/wt/a.py", "some-future-check",
		[]string{"Whatever"}, nil, map[string][]string{"some-future-check": {"Whatever"}})

	s := Audit([]Decision{d}, auditTS("2026-08-01T00:00:00Z"), o)
	if got := s.Findings[0].Verdict; got != VerdictNA {
		t.Fatalf("unknown check verdict = %q, want %q", got, VerdictNA)
	}
	if o.calls != 0 {
		t.Errorf("oracle consulted %d times for an unknown check; want 0", o.calls)
	}
}

// Records written before claim_symbols existed keep their old meaning, and are
// labelled as legacy rather than as a check that chose not to claim — the two
// shrink for different reasons and only one of them is actionable.
func TestAuditLegacyRecordFallbackAndReason(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "R0"}}

	// learn_symbols present, claim_symbols absent: still rated, repo-scoped.
	rated := auditAsk("2026-09-01T10:00:00Z", "/wt/a.py", "violations",
		[]string{"Ghost"}, []string{"Ghost"})
	// Neither field, and a reason that cannot be split.
	legacy := auditAsk("2026-09-01T11:00:00Z", "/wt/b.py", "violations+dangling",
		[]string{"Other"}, nil)

	s := Audit([]Decision{rated, legacy}, auditTS("2026-08-01T00:00:00Z"), o)
	if s.Findings[0].Verdict != VerdictStands || s.Findings[0].Scope != ScopeRepo {
		t.Errorf("legacy learn_symbols record: verdict %q scope %q, want %q/%q",
			s.Findings[0].Verdict, s.Findings[0].Scope, VerdictStands, ScopeRepo)
	}
	if s.Findings[1].Note != NAReasonLegacyRecord {
		t.Errorf("pre-learn_symbols record na reason = %q, want %q",
			s.Findings[1].Note, NAReasonLegacyRecord)
	}
}

// A merged reason naming both kinds of unrateable check attributes to neither.
// no-oracle-question is the actionable "missing coverage" number; letting a
// duplicate-symbol pair inflate it would make the one figure this split exists
// to expose the least trustworthy one in the table.
func TestAuditMixedUnrateableReasonIsNotAttributed(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "R0"}}
	d := auditClaimAsk("2026-09-01T10:00:00Z", "/wt/a.go", "qualified+duplicate-symbol",
		[]string{"pkg.Helper", "Dupe"}, nil, map[string][]string{})

	s := Audit([]Decision{d}, auditTS("2026-08-01T00:00:00Z"), o)
	for _, f := range s.Findings {
		if f.Note != NAReasonMixedReason {
			t.Errorf("na reason for %q = %q, want %q", f.Symbol, f.Note, NAReasonMixedReason)
		}
	}
	if s.NAReasons[NAReasonNoOracleQuestion] != 0 {
		t.Errorf("no-oracle-question = %d, want 0 — a mixed reason must not inflate the missing-coverage figure",
			s.NAReasons[NAReasonNoOracleQuestion])
	}
}

// The rateable half of a merged ask is still rated: only the symbols the record
// does not attribute fall through to the reason-string heuristic.
func TestAuditMergedAskRatesItsClaimedHalf(t *testing.T) {
	o := &fakeOracle{head: "HEAD", revAt: map[string]string{"": "R0"}, defined: map[[2]string]bool{}}
	d := auditClaimAsk("2026-09-01T10:00:00Z", "/wt/a.py", "violations+dangling",
		[]string{"Ghost", "Removed"}, []string{"Ghost"}, map[string][]string{"violations": {"Ghost"}})

	s := Audit([]Decision{d}, auditTS("2026-08-01T00:00:00Z"), o)
	byName := map[string]AuditFinding{}
	for _, f := range s.Findings {
		byName[f.Symbol] = f
	}
	if got := byName["Ghost"].Verdict; got != VerdictStands {
		t.Errorf("Ghost verdict = %q, want %q", got, VerdictStands)
	}
	if got := byName["Removed"]; got.Verdict != VerdictNA || got.Note != NAReasonNotResolutionClaim {
		t.Errorf("Removed = %q/%q, want %q/%q", got.Verdict, got.Note, VerdictNA, NAReasonNotResolutionClaim)
	}
}
