package guardstats

import (
	"testing"
	"time"
)

func ts(mins int) time.Time {
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	return base.Add(time.Duration(mins) * time.Minute)
}

func ask(reason, lang, repo, file string, mins int, syms ...string) Decision {
	return Decision{TS: ts(mins), Mode: "hook", Repo: repo, File: file, Lang: lang,
		Decision: "ask", Reason: reason, Symbols: syms}
}
func outcome(file string, mins int, syms ...string) Decision {
	return Decision{TS: ts(mins), Mode: "hook", File: file, Decision: "outcome",
		Reason: "approved", Symbols: syms}
}

func TestFPReport_SymbolExactJoin(t *testing.T) {
	decs := []Decision{
		ask("violations", "py", "r1", "a.py", 0, "foo"),
		outcome("a.py", 2, "foo"), // within window, same file+symbol → approved
		ask("violations", "py", "r1", "b.py", 10, "bar"),
		outcome("b.py", 20, "bar"), // 10 min later → OUTSIDE 5-min window → not approved
		ask("violations", "py", "r1", "c.py", 30, "baz"),
		outcome("c.py", 31, "different"), // within window but different symbol → not approved
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Asks != 3 {
		t.Fatalf("asks = %d, want 3", s.Window.Asks)
	}
	if s.Window.Approved != 1 {
		t.Fatalf("approved = %d, want 1 (only the in-window same-symbol join)", s.Window.Approved)
	}
	if got := s.ByReason["violations"].Rate(); got < 0.33 || got > 0.34 {
		t.Errorf("violations rate = %.3f, want ~0.333", got)
	}
}

func TestFPReport_OutcomeBeforeAskNotMatched(t *testing.T) {
	decs := []Decision{
		outcome("a.py", 0, "foo"),                      // outcome first
		ask("violations", "py", "r", "a.py", 5, "foo"), // ask later — must NOT pair backwards
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Approved != 0 {
		t.Errorf("approved = %d, want 0 (outcome preceded ask)", s.Window.Approved)
	}
	if s.UnmatchedOutcomes != 1 {
		t.Errorf("unmatched = %d, want 1", s.UnmatchedOutcomes)
	}
}

func TestFPReport_OneOutcomeConsumedOnce(t *testing.T) {
	// Two identical asks (same file+symbol), a single approval. Only ONE may be
	// counted approved — otherwise a single click inflates the FP rate.
	decs := []Decision{
		ask("violations", "py", "r", "a.py", 0, "foo"),
		ask("violations", "py", "r", "a.py", 1, "foo"),
		outcome("a.py", 2, "foo"),
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Asks != 2 || s.Window.Approved != 1 {
		t.Errorf("asks=%d approved=%d, want 2/1", s.Window.Asks, s.Window.Approved)
	}
}

func TestFPReport_PrecommitAsksExcluded(t *testing.T) {
	decs := []Decision{
		{TS: ts(0), Mode: "precommit", Repo: "r", File: "a.go", Decision: "ask", Reason: "violations", Symbols: []string{"x"}},
		ask("violations", "go", "r", "b.go", 5, "y"),
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Asks != 1 {
		t.Errorf("asks = %d, want 1 (pre-commit ask excluded)", s.Window.Asks)
	}
}

func TestFPReport_ByReasonAndLang(t *testing.T) {
	decs := []Decision{
		ask("violations", "py", "r", "a.py", 0, "foo"),
		outcome("a.py", 1, "foo"),
		ask("duplicate-symbol", "go", "r", "x.go", 10, "main"),
		// no outcome for the go one
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.ByReason["violations"].Approved != 1 || s.ByReason["duplicate-symbol"].Approved != 0 {
		t.Errorf("per-reason approved wrong: %+v", s.ByReason)
	}
	if s.ByLang["py"].Rate() != 1.0 || s.ByLang["go"].Rate() != 0.0 {
		t.Errorf("per-lang rate wrong: py=%.2f go=%.2f", s.ByLang["py"].Rate(), s.ByLang["go"].Rate())
	}
}

func TestFPReport_TopSymbolsAndRepos(t *testing.T) {
	decs := []Decision{
		ask("violations", "py", "loud", "a.py", 0, "main"),
		outcome("a.py", 1, "main"),
		ask("violations", "py", "loud", "b.py", 2, "main"),
		outcome("b.py", 3, "main"),
		ask("violations", "py", "quiet", "c.py", 4, "other"),
		outcome("c.py", 5, "other"),
	}
	s := FPReport(decs, ts(-1000), 10)
	if len(s.TopSymbols) == 0 || s.TopSymbols[0].Name != "main" || s.TopSymbols[0].Count != 2 {
		t.Errorf("top symbol should be main×2, got %+v", s.TopSymbols)
	}
	if len(s.LoudestRepos) == 0 || s.LoudestRepos[0].Repo != "loud" || s.LoudestRepos[0].Asks != 2 {
		t.Errorf("loudest repo should be loud×2, got %+v", s.LoudestRepos)
	}
}

func TestFPReport_WindowFilter(t *testing.T) {
	decs := []Decision{
		ask("violations", "py", "r", "old.py", -100, "foo"), // before since
		ask("violations", "py", "r", "new.py", 5, "bar"),
	}
	s := FPReport(decs, ts(0), 10) // since = ts(0), so the -100 ask is excluded
	if s.Window.Asks != 1 {
		t.Errorf("asks = %d, want 1 (old ask outside window)", s.Window.Asks)
	}
}

func TestFPBucket_Rate(t *testing.T) {
	if (FPBucket{Asks: 0}).Rate() != 0 {
		t.Error("zero asks must be 0 rate, not NaN")
	}
	if (FPBucket{Asks: 4, Approved: 3}).Rate() != 0.75 {
		t.Error("3/4 should be 0.75")
	}
}

// Findings from independent review, pinned as regression tests.

// Join reviewer #1: matching must not depend on input order. Two asks sharing a
// key, presented out of timestamp order, must both find their outcome.
func TestFPReport_AsksMatchedInTimeOrderNotInputOrder(t *testing.T) {
	// Input order puts the LATER ask first. Outcomes at t4 and t7.
	decs := []Decision{
		ask("violations", "py", "r", "a.py", 3, "foo"), // askB @ t3 (appears first)
		ask("violations", "py", "r", "a.py", 0, "foo"), // askA @ t0 (appears second)
		outcome("a.py", 4, "foo"),
		outcome("a.py", 7, "foo"),
	}
	s := FPReport(decs, ts(-1000), 10)
	// askA@0→out@4 (within 5), askB@3→out@7 (within 5). Both match.
	if s.Window.Approved != 2 {
		t.Fatalf("approved = %d, want 2 (both asks matched after time-sorting)", s.Window.Approved)
	}
}

// Join reviewer #2: the window is strict (< 5min), mirroring the recorder which
// never emits an outcome at exactly the edge.
func TestFPReport_WindowBoundaryStrict(t *testing.T) {
	exactly5 := []Decision{
		ask("violations", "py", "r", "a.py", 0, "foo"),
		outcome("a.py", 5, "foo"), // exactly 5 min later
	}
	if s := FPReport(exactly5, ts(-1000), 10); s.Window.Approved != 0 {
		t.Errorf("an outcome exactly 5min later must NOT match (strict window), got approved=%d", s.Window.Approved)
	}
	justUnder := []Decision{
		ask("violations", "py", "r", "b.py", 0, "foo"),
		Decision{TS: ts(0).Add(4*time.Minute + 59*time.Second), Mode: "hook", File: "b.py", Decision: "outcome", Reason: "approved", Symbols: []string{"foo"}},
	}
	if s := FPReport(justUnder, ts(-1000), 10); s.Window.Approved != 1 {
		t.Errorf("an outcome 4m59s later must match, got approved=%d", s.Window.Approved)
	}
}

// Adversarial #5: symbol-less records carry no join signal and must not collide.
func TestFPReport_EmptySymbolRecordsIgnored(t *testing.T) {
	decs := []Decision{
		{TS: ts(0), Mode: "hook", File: "", Decision: "ask", Reason: "violations", Symbols: nil},
		{TS: ts(1), Mode: "hook", File: "", Decision: "outcome", Reason: "approved", Symbols: nil},
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Asks != 0 {
		t.Errorf("symbol-less ask must be skipped, got asks=%d", s.Window.Asks)
	}
	if s.UnmatchedOutcomes != 0 {
		t.Errorf("symbol-less outcome must not enter the denominator, got unmatched=%d", s.UnmatchedOutcomes)
	}
}

// Adversarial #6: only "approved" outcomes count; a non-approved outcome must
// not inflate the rate (decisions.jsonl is user-writable).
func TestFPReport_NonApprovedOutcomeIgnored(t *testing.T) {
	decs := []Decision{
		ask("violations", "py", "r", "a.py", 0, "foo"),
		{TS: ts(1), Mode: "hook", File: "a.py", Decision: "outcome", Reason: "rejected", Symbols: []string{"foo"}},
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Approved != 0 {
		t.Errorf("a non-approved outcome must not count, got approved=%d", s.Window.Approved)
	}
}

// Adversarial #9: an empty window must not render an inverted [since, until].
func TestFPReport_EmptyWindowUntilNotInverted(t *testing.T) {
	s := FPReport(nil, ts(0), 10)
	if s.Until.Before(s.Since) {
		t.Errorf("Until (%v) must not precede Since (%v) on empty data", s.Until, s.Since)
	}
}
