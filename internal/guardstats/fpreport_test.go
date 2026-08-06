package guardstats

import (
	"strings"
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
// A symbol-less record is never JOINED — but since #254 the ask half is still
// counted, in Unrated. The outcome half stays fully dropped.
func TestFPReport_EmptySymbolRecordsNotJoined(t *testing.T) {
	decs := []Decision{
		{TS: ts(0), Mode: "hook", File: "", Decision: "ask", Reason: "violations", Symbols: nil},
		{TS: ts(1), Mode: "hook", File: "", Decision: "outcome", Reason: "approved", Symbols: nil},
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Asks != 0 {
		t.Errorf("symbol-less ask must stay out of the rateable denominator, got asks=%d", s.Window.Asks)
	}
	if s.Window.Unrated != 1 {
		t.Errorf("symbol-less ask must be counted as unrated, got unrated=%d", s.Window.Unrated)
	}
	if s.Window.Approved != 0 {
		t.Errorf("a symbol-less ask must never join an outcome, got approved=%d", s.Window.Approved)
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

// TestFPReport_ByCheckSplitsCompoundReasons pins the #209 gate's instrument.
// askReason joins co-firing checks with "+", and contractReason prefixes
// "contract+", so ByReason alone spreads one check's rate across several
// buckets — and the decision on #209 needs the contract check's rate alone.
func TestFPReport_ByCheckSplitsCompoundReasons(t *testing.T) {
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	mk := func(off int, reason, file string, syms []string, dec string) Decision {
		return Decision{
			TS: base.Add(time.Duration(off) * time.Second), Mode: "hook",
			Decision: dec, Reason: reason, File: file, Symbols: syms, Lang: "go",
		}
	}
	decisions := []Decision{
		// A compound ask, approved: must count once for EACH of its two checks.
		mk(0, "contract+violations", "a.go", []string{"Alpha"}, "ask"),
		mk(1, "approved", "a.go", []string{"Alpha"}, "outcome"),
		// A contract-only ask, not approved.
		mk(10, "contract", "b.go", []string{"Beta"}, "ask"),
		// A violations-only ask, not approved.
		mk(20, "violations", "c.go", []string{"Gamma"}, "ask"),
	}

	s := FPReport(decisions, base.Add(-time.Hour), 5)

	// ByReason keeps the compound key intact — that co-firing signal is why both
	// maps exist.
	if got := s.ByReason["contract+violations"].Asks; got != 1 {
		t.Errorf("ByReason[contract+violations].Asks = %d, want 1", got)
	}
	if _, split := s.ByReason["contract"]; !split {
		t.Error("ByReason lost the contract-only bucket")
	}

	// ByCheck is the per-check view: contract fired twice (compound + solo),
	// violations twice (compound + solo), each with one approval from the
	// compound ask.
	for _, tc := range []struct {
		check          string
		asks, approved int
	}{
		{"contract", 2, 1},
		{"violations", 2, 1},
	} {
		b := s.ByCheck[tc.check]
		if b.Asks != tc.asks || b.Approved != tc.approved {
			t.Errorf("ByCheck[%s] = %d ask/%d approved, want %d/%d",
				tc.check, b.Asks, b.Approved, tc.asks, tc.approved)
		}
	}

	// The window total must stay 3: double-counting a compound ask into ByCheck
	// must not leak into the headline rate.
	if s.Window.Asks != 3 {
		t.Errorf("Window.Asks = %d, want 3 — ByCheck must not inflate the total", s.Window.Asks)
	}
}

// TestSplitChecks_DropsEmptyTerms: decisions.jsonl is user-writable, so a
// malformed reason must lose a term rather than invent a nameless check in a
// report someone is about to make a decision from.
func TestSplitChecks_DropsEmptyTerms(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 0}, {"violations", 1}, {"a+b+c", 3}, {"+violations+", 1}, {"a++b", 2},
	} {
		if got := len(splitChecks(tc.in)); got != tc.want {
			t.Errorf("splitChecks(%q) gave %d terms, want %d", tc.in, got, tc.want)
		}
	}
}

// #252 — the agent harness can run the PreToolUse hook several times for one
// Edit/Write, and the guard logs each run. Window.Asks++ fires per record while
// `consumed` caps approvals at one per outcome, so duplicates DEFLATE the rate:
// three copies of one approved ask would score 1/3 instead of 1/1.
func TestFPReport_CollapsesRepeatedAskRecords(t *testing.T) {
	repeated := ask("violations", "py", "r1", "a.py", 0, "foo")
	decs := []Decision{
		repeated, repeated, repeated, // one tool call, three hook invocations
		outcome("a.py", 2, "foo"), // approved once, because it happened once
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Asks != 1 {
		t.Fatalf("asks = %d, want 1 — three records describe one tool call", s.Window.Asks)
	}
	if s.Window.Approved != 1 {
		t.Fatalf("approved = %d, want 1", s.Window.Approved)
	}
	if got := s.Window.Rate(); got != 1 {
		t.Errorf("rate = %.3f, want 1.000; duplicate asks are understating the approval rate", got)
	}
}

// The collapse must not swallow genuinely distinct asks. Every field in the key
// gets its own case, or an over-eager key would silently erase real data — the
// failure mode that is worse than the bug being fixed.
func TestFPReport_KeepsDistinctAsks(t *testing.T) {
	base := ask("violations", "py", "r1", "a.py", 0, "foo")
	vary := map[string]func(Decision) Decision{
		// ONE SECOND, not ts(1) — ts counts minutes. A minute-resolution key would
		// survive a whole-minute delta and still collapse two distinct asks 30s
		// apart, so the mutation has to be as fine as the claim the doc comment makes.
		"different second": func(d Decision) Decision { d.TS = d.TS.Add(time.Second); return d },
		"different minute": func(d Decision) Decision { d.TS = ts(1); return d },
		"different repo":   func(d Decision) Decision { d.Repo = "r2"; return d },
		"different file":   func(d Decision) Decision { d.File = "b.py"; return d },
		"different lang":   func(d Decision) Decision { d.Lang = "go"; return d },
		"different reason": func(d Decision) Decision { d.Reason = "dangling"; return d },
		"different symbol": func(d Decision) Decision { d.Symbols = []string{"bar"}; return d },
		"different gv":     func(d Decision) Decision { d.GV = "v9.9.9"; return d },
	}
	for name, mutate := range vary {
		t.Run(name, func(t *testing.T) {
			s := FPReport([]Decision{base, mutate(base)}, ts(-1000), 10)
			if s.Window.Asks != 2 {
				t.Fatalf("asks = %d, want 2 — %s must not collapse", s.Window.Asks, name)
			}
		})
	}
}

// Symbol ORDER is not a distinguishing field: symbolKey sorts, so the same set
// logged in a different order is the same event.
func TestFPReport_CollapseIsSymbolOrderIndependent(t *testing.T) {
	a := ask("violations", "py", "r1", "a.py", 0, "foo", "bar")
	b := ask("violations", "py", "r1", "a.py", 0, "bar", "foo")
	s := FPReport([]Decision{a, b}, ts(-1000), 10)
	if s.Window.Asks != 1 {
		t.Fatalf("asks = %d, want 1 — symbol order must not create a second event", s.Window.Asks)
	}
}

// The collapse is deliberately NOT applied to outcome records: they measured
// 1.000 records per event, and cutting the numerator to fix the denominator
// would be the wrong trade. Without this test that decision is free to be
// reverted with no signal.
func TestFPReport_DoesNotCollapseOutcomes(t *testing.T) {
	o := outcome("a.py", 2, "foo")
	decs := []Decision{
		ask("violations", "py", "r1", "a.py", 0, "foo"),
		ask("violations", "py", "r1", "a.py", 1, "foo"), // distinct minute → distinct ask
		o, o, // two outcome records; both must remain claimable
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Asks != 2 {
		t.Fatalf("asks = %d, want 2", s.Window.Asks)
	}
	if s.Window.Approved != 2 {
		t.Fatalf("approved = %d, want 2 — outcome records must not be collapsed", s.Window.Approved)
	}
}

// Collapsing asks shrinks Window.Asks, which shrinks --max-rate gate ELIGIBILITY:
// a window sitting just above gateMinAsks can fall below it and skip the gate
// entirely. The skip is announced on stderr rather than silent, but the coverage
// loss is real and was not obvious from the rate change alone.
func TestFPReport_CollapseCanDropWindowBelowGateFloor(t *testing.T) {
	var decs []Decision
	// 19 distinct asks, plus one asked twice → 21 records, 20 events.
	for i := 0; i < 19; i++ {
		decs = append(decs, ask("violations", "py", "r1", "f"+string(rune('a'+i))+".py", i, "s"))
	}
	dupe := ask("violations", "py", "r1", "zz.py", 30, "s")
	decs = append(decs, dupe, dupe)

	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Asks != 20 {
		t.Fatalf("asks = %d, want 20 events from 21 records", s.Window.Asks)
	}
	// Same input read without the collapse would have reported 21 — one side of
	// the gate floor, then the other. Pin the boundary so a future key change that
	// over-collapses is visible as a gate-coverage change, not just a rate change.
	if s.Window.Asks < 20 {
		t.Errorf("collapse dropped the window below the gate floor of 20")
	}
}

// ---------------------------------------------------------------------------
// #254 — contract asks carry no symbols, so they had no join key and were
// DROPPED. `fpreport` then printed a contract rate over the ~14% of contract
// asks that happened to co-fire with a symbol-bearing check. These tests pin
// the fix: such asks are COUNTED (Unrated) but still never JOINED.
// ---------------------------------------------------------------------------

// contractAsk is a contract-check ask as the guard actually writes it: a claim
// about a PATH, with no symbols. Seconds, not minutes — the collapse key is
// second-resolution and a minutes-only helper would let a coarser key pass
// (the #253 lesson).
func contractAsk(file string, secs int) Decision {
	return Decision{TS: ts(0).Add(time.Duration(secs) * time.Second), Mode: "hook",
		Repo: "r1", File: file, Lang: "go", GV: "v0.17.18",
		Decision: "ask", Reason: "contract"}
}

// Restoring the `len(Symbols)==0` drop makes every Unrated assertion here read
// 0, so this test is what fails if the fix is reverted.
func TestFPReport_ContractOnlyAsksAreCountedNotDropped(t *testing.T) {
	decs := []Decision{
		contractAsk("a.go", 0),
		contractAsk("b.go", 60),
		ask("violations", "go", "r1", "c.go", 5, "foo"),
	}
	s := FPReport(decs, ts(-1000), 10)

	if s.Window.Asks != 1 || s.Window.Unrated != 2 {
		t.Fatalf("window = %d rated / %d unrated, want 1 / 2", s.Window.Asks, s.Window.Unrated)
	}
	if s.Window.Total() != 3 {
		t.Errorf("Total = %d, want 3 — every ask the guard raised", s.Window.Total())
	}
	// The contract check exists in the report at all, which it did not before.
	if got := s.ByCheck["contract"]; got.Unrated != 2 || got.Asks != 0 {
		t.Errorf("ByCheck[contract] = %+v, want 0 rated / 2 unrated", got)
	}
	if got := s.ByReason["contract"]; got.Unrated != 2 {
		t.Errorf("ByReason[contract].Unrated = %d, want 2", got.Unrated)
	}
	if got := s.ByLang["go"]; got.Unrated != 2 || got.Asks != 1 {
		t.Errorf("ByLang[go] = %+v, want 1 rated / 2 unrated", got)
	}
	if got := s.ByVersion["v0.17.18"]; got.Unrated != 2 {
		t.Errorf("ByVersion[v0.17.18].Unrated = %d, want 2", got.Unrated)
	}
	// Loudest-repos is a VOLUME ranking; answering it while discarding real asks
	// is the same defect at a different address.
	if len(s.LoudestRepos) != 1 || s.LoudestRepos[0].Total() != 3 {
		t.Fatalf("LoudestRepos = %+v, want one repo with 3 total asks", s.LoudestRepos)
	}
	// Split, not blended: "asks" must keep reconciling with overall.asks.
	if got := s.LoudestRepos[0]; got.Asks != 1 || got.Unrated != 2 {
		t.Errorf("LoudestRepos[0] = %+v, want 1 rated / 2 unrated", got)
	}
}

// The collision constraint: counting symbol-less asks must not create a join.
// Two independent symbol-less asks and a symbol-less approved outcome all sit
// on ONE file — the exact shape that would pair falsely if either side's guard
// were lifted without a real key.
func TestFPReport_SymbolLessRecordsOnOneFileNeverPair(t *testing.T) {
	decs := []Decision{
		contractAsk("same.go", 0),
		contractAsk("same.go", 30), // distinct event, same file, still no symbols
		{TS: ts(0).Add(45 * time.Second), Mode: "hook", File: "same.go",
			Decision: "outcome", Reason: "approved"}, // in-window, symbol-less
	}
	s := FPReport(decs, ts(-1000), 10)
	// Asserted before (and independently of) the counting claim: this is THE
	// collision constraint, and it must fail on its own terms if a future change
	// lifts either symbol-less guard without giving both sides a real join key.
	if s.Window.Approved != 0 {
		t.Errorf("approved = %d, want 0 — symbol-less records must never pair", s.Window.Approved)
	}
	if s.Window.Unrated != 2 {
		t.Errorf("unrated = %d, want 2 (two distinct symbol-less asks)", s.Window.Unrated)
	}
	if s.ByCheck["contract"].Approved != 0 {
		t.Errorf("contract bucket claimed an approval it cannot have")
	}
	if s.UnmatchedOutcomes != 0 {
		t.Errorf("a symbol-less outcome is dropped, not counted unmatched; got %d", s.UnmatchedOutcomes)
	}
}

// One Write producing three identical contract asks is on the reference log.
// #254 is what makes contract asks visible to fpreport at all, so it is also
// where that triplication would first inflate a published number.
func TestFPReport_ContractAskTriplicationCollapses(t *testing.T) {
	trip := []Decision{contractAsk("a.go", 0), contractAsk("a.go", 0), contractAsk("a.go", 0)}
	s := FPReport(trip, ts(-1000), 10)
	if s.Window.Unrated != 1 {
		t.Errorf("three records for one tool call = 1 unrated event, got %d", s.Window.Unrated)
	}

	// One second apart is a DIFFERENT event and must survive. A minute-resolution
	// key would pass the case above and fail here.
	distinct := []Decision{contractAsk("a.go", 0), contractAsk("a.go", 1)}
	s = FPReport(distinct, ts(-1000), 10)
	if s.Window.Unrated != 2 {
		t.Errorf("asks one second apart are distinct events, got unrated=%d, want 2", s.Window.Unrated)
	}

	// Same second, different file: also distinct.
	sameSec := []Decision{contractAsk("a.go", 0), contractAsk("b.go", 0)}
	s = FPReport(sameSec, ts(-1000), 10)
	if s.Window.Unrated != 2 {
		t.Errorf("asks on different files are distinct events, got unrated=%d, want 2", s.Window.Unrated)
	}
}

// Window.Asks is the --max-rate gate's denominator (cmd/runecho-ir/fpreport.go).
// Making unrated asks visible must not move it, or #254 would silently re-tune
// a gate while claiming to fix a report.
func TestFPReport_UnratedAsksDoNotMoveRateOrGateDenominator(t *testing.T) {
	rated := []Decision{
		ask("violations", "go", "r1", "a.go", 0, "foo"),
		outcome("a.go", 1, "foo"),
		ask("violations", "go", "r1", "b.go", 10, "bar"),
	}
	before := FPReport(rated, ts(-1000), 10)

	withUnrated := append(append([]Decision{}, rated...),
		contractAsk("c.go", 0), contractAsk("d.go", 90), contractAsk("e.go", 120))
	after := FPReport(withUnrated, ts(-1000), 10)

	if before.Window.Asks != after.Window.Asks {
		t.Errorf("gate denominator moved: %d → %d", before.Window.Asks, after.Window.Asks)
	}
	if before.Window.Approved != after.Window.Approved {
		t.Errorf("numerator moved: %d → %d", before.Window.Approved, after.Window.Approved)
	}
	if before.Window.Rate() != after.Window.Rate() {
		t.Errorf("headline rate moved: %.4f → %.4f", before.Window.Rate(), after.Window.Rate())
	}
	if after.Window.Coverage() >= 1 {
		t.Errorf("coverage must fall below 1 once unrated asks exist, got %.3f", after.Window.Coverage())
	}
}

// The exact #254 scenario: the contract bucket's rate is real but describes a
// small, non-random slice of the check — the asks that co-fired with a
// symbol-bearing check. Coverage is what makes that legible.
func TestFPReport_ContractBucketDisclosesItsCoverage(t *testing.T) {
	var decs []Decision
	// 3 contract+violations asks — these carry the violations half's symbols and
	// are the only contract asks that were ever visible.
	for i := 0; i < 3; i++ {
		f := string(rune('a'+i)) + ".go"
		decs = append(decs, ask("contract+violations", "go", "r1", f, i*10, "sym"))
		decs = append(decs, outcome(f, i*10+1, "sym"))
	}
	// 16 contract-only asks — invisible before #254.
	for i := 0; i < 16; i++ {
		decs = append(decs, contractAsk("scope"+string(rune('a'+i))+".go", i*7))
	}
	s := FPReport(decs, ts(-1000), 10)

	c := s.ByCheck["contract"]
	if c.Asks != 3 || c.Approved != 3 || c.Unrated != 16 {
		t.Fatalf("ByCheck[contract] = %+v, want 3 rated / 3 approved / 16 unrated", c)
	}
	if c.Rate() != 1 {
		t.Errorf("rate = %.3f, want 1.0 over the rateable subset", c.Rate())
	}
	if got := c.Coverage(); got < 0.157 || got > 0.159 {
		t.Errorf("coverage = %.4f, want ~0.158 (3 of 19)", got)
	}
	if c.Coverage() >= unratedCoverageFloor {
		t.Fatalf("this bucket must be below the callout floor")
	}

	out := FormatFP(s)
	if !strings.Contains(out, "+16 unrated") {
		t.Errorf("report hides the unrated contract asks:\n%s", out)
	}
	if !strings.Contains(out, "! +16 unrated") {
		t.Errorf("a bucket under the coverage floor must be marked !:\n%s", out)
	}
	if !strings.Contains(out, "carry no symbols") {
		t.Errorf("report lacks the headline caveat:\n%s", out)
	}
}

// A window of nothing but contract asks must not report itself as empty.
func TestFormatFP_WindowOfOnlyUnratedAsksIsNotEmpty(t *testing.T) {
	s := FPReport([]Decision{contractAsk("a.go", 0), contractAsk("b.go", 60)}, ts(-1000), 10)
	out := FormatFP(s)
	if strings.Contains(out, "No hook-mode asks in window") {
		t.Fatalf("2 real asks reported as an empty window:\n%s", out)
	}
	if !strings.Contains(out, "no rate (no ask here carries a join key)") {
		t.Errorf("a fully-unrated bucket must say so rather than print 0%%:\n%s", out)
	}
	if strings.Contains(out, "contract") == false {
		t.Errorf("the contract check is absent from the report:\n%s", out)
	}
}

// An empty window is still an empty window.
func TestFormatFP_TrulyEmptyWindowStillSaysSo(t *testing.T) {
	out := FormatFP(FPReport(nil, ts(0), 10))
	if !strings.Contains(out, "No hook-mode asks in window") {
		t.Errorf("empty window lost its message:\n%s", out)
	}
}

func TestFPBucket_TotalAndCoverage(t *testing.T) {
	cases := []struct {
		b            FPBucket
		total        int
		wantCoverage float64
	}{
		{FPBucket{Asks: 4, Approved: 2}, 4, 1},
		{FPBucket{Asks: 3, Approved: 3, Unrated: 16}, 19, 3.0 / 19.0},
		{FPBucket{Unrated: 5}, 5, 0},
		{FPBucket{}, 0, 1}, // nothing to under-cover
	}
	for _, c := range cases {
		if got := c.b.Total(); got != c.total {
			t.Errorf("%+v Total = %d, want %d", c.b, got, c.total)
		}
		if got := c.b.Coverage(); got != c.wantCoverage {
			t.Errorf("%+v Coverage = %.4f, want %.4f", c.b, got, c.wantCoverage)
		}
	}
}

// A consumer reading "rate" without "coverage" can read 1.0 off a 16% sample,
// so both must be present on every bucket the payload emits.
func TestPayloadFP_EveryBucketCarriesUnratedAndCoverage(t *testing.T) {
	decs := []Decision{
		ask("contract+violations", "go", "r1", "a.go", 0, "sym"),
		outcome("a.go", 1, "sym"),
		contractAsk("b.go", 120),
	}
	p := PayloadFP(FPReport(decs, ts(-1000), 10))

	overall, ok := p["overall"].(map[string]any)
	if !ok {
		t.Fatalf("overall missing")
	}
	for _, k := range []string{"asks", "approved", "rate", "unrated", "total", "coverage"} {
		if _, ok := overall[k]; !ok {
			t.Errorf("overall missing %q", k)
		}
	}
	if overall["unrated"] != 1 || overall["total"] != 2 {
		t.Errorf("overall = %+v, want unrated 1 / total 2", overall)
	}

	for _, group := range []string{"by_reason", "by_check", "by_lang", "by_version"} {
		rows, ok := p[group].([]map[string]any)
		if !ok || len(rows) == 0 {
			t.Fatalf("%s missing or empty", group)
		}
		for _, row := range rows {
			for _, k := range []string{"unrated", "total", "coverage"} {
				if _, ok := row[k]; !ok {
					t.Errorf("%s row %+v missing %q", group, row, k)
				}
			}
		}
	}

	// The contract check's own row must show the split.
	var contractRow map[string]any
	for _, row := range p["by_check"].([]map[string]any) {
		if row["check"] == "contract" {
			contractRow = row
		}
	}
	if contractRow == nil {
		t.Fatalf("by_check has no contract row: %+v", p["by_check"])
	}
	if contractRow["asks"] != 1 || contractRow["unrated"] != 1 {
		t.Errorf("contract row = %+v, want 1 rated / 1 unrated", contractRow)
	}
	if got := contractRow["coverage"].(float64); got != 0.5 {
		t.Errorf("contract coverage = %.3f, want 0.5", got)
	}
}

// Ordering by rateable asks alone would bury a check whose asks are ALL
// unrated at the bottom of the table, however loud it is.
func TestSortedFPKeys_OrdersByTotalNotRateableAsks(t *testing.T) {
	m := map[string]FPBucket{
		"violations": {Asks: 2, Approved: 1},
		"contract":   {Unrated: 9},
	}
	got := sortedFPKeys(m)
	if got[0] != "contract" {
		t.Errorf("order = %v, want the 9-ask contract bucket first", got)
	}
}

// ---------------------------------------------------------------------------
// #254 review follow-ups. Each pins a defect the review found in the first cut.
// ---------------------------------------------------------------------------

// Review finding 1. ByVersion buckets unrated asks too, so counting map KEYS
// would let one symbol-less ask from another build flip MixedVersions — and
// through cmd/runecho-ir's gateEligible, silently skip the --max-rate gate on a
// window whose rate is single-version.
func TestFPStats_MixedVersionsIgnoresUnratedOnlyVersions(t *testing.T) {
	var decs []Decision
	for i := 0; i < 25; i++ {
		d := ask("violations", "go", "r", "f.go", i, "s")
		d.GV = "v0.17.18"
		decs = append(decs, d)
	}
	if s := FPReport(decs, ts(-1000), 5); s.MixedVersions() {
		t.Fatalf("one build with rateable asks is not mixed")
	}

	stray := contractAsk("c.go", 3000)
	stray.GV = "v0.17.10" // a DIFFERENT build, contributing nothing rateable
	s := FPReport(append(decs, stray), ts(-1000), 5)

	if len(s.ByVersion) != 2 {
		t.Fatalf("the stray build must still appear in ByVersion, got %+v", s.ByVersion)
	}
	if s.MixedVersions() {
		t.Errorf("a version with 0 rateable asks cannot pool the rate; ByVersion=%+v", s.ByVersion)
	}
	if s.Window.Asks != 25 {
		t.Errorf("gate denominator = %d, want 25", s.Window.Asks)
	}
	// A pre-symbol-stamping record lands under UnknownVersion and must not gate
	// either — that shape is on every long window of the real log.
	old := Decision{TS: ts(3001), Mode: "hook", File: "old.go", Decision: "ask", Reason: "violations"}
	if s := FPReport(append(decs, old), ts(-1000), 5); s.MixedVersions() {
		t.Errorf("an unrated UnknownVersion record must not flip MixedVersions")
	}
}

// Review finding 2. fpRow refuses to print a rate over zero rateable asks; the
// headline was exempt from its own rule and printed "0% approval rate".
func TestFormatFP_HeadlineSuppressesRateWhenNothingIsRateable(t *testing.T) {
	out := FormatFP(FPReport([]Decision{contractAsk("a.go", 0), contractAsk("b.go", 60)}, ts(-1000), 10))
	if strings.Contains(out, "0% approval rate") {
		t.Errorf("headline printed a 0%% rate over zero rateable asks:\n%s", out)
	}
	if !strings.Contains(out, "no approval rate") {
		t.Errorf("headline must say there is no rate:\n%s", out)
	}
	// "N further asks" reads wrong when there were no prior asks to be further than.
	if strings.Contains(out, "further ask") {
		t.Errorf("caveat wording assumes prior rateable asks:\n%s", out)
	}
	if !strings.Contains(out, "2 of the 2 ask(s)") {
		t.Errorf("caveat must state the share of the window:\n%s", out)
	}
}

// Review finding 3. FormatFP suppresses the rate for a fully-unrated bucket;
// the JSON path must not hand a dashboard a plottable 0 for the same bucket.
func TestPayloadFP_FullyUnratedBucketHasNullRate(t *testing.T) {
	p := PayloadFP(FPReport([]Decision{
		contractAsk("a.go", 0),
		ask("violations", "go", "r1", "b.go", 10, "sym"),
	}, ts(-1000), 10))

	var contractRow map[string]any
	for _, row := range p["by_check"].([]map[string]any) {
		if row["check"] == "contract" {
			contractRow = row
		}
	}
	if contractRow == nil {
		t.Fatalf("no contract row: %+v", p["by_check"])
	}
	if contractRow["rate"] != nil {
		t.Errorf("fully-unrated bucket rate = %v, want null", contractRow["rate"])
	}
	if contractRow["coverage"] != float64(0) {
		t.Errorf("coverage = %v, want 0", contractRow["coverage"])
	}
	// A bucket that IS rateable still carries a number.
	for _, row := range p["by_check"].([]map[string]any) {
		if row["check"] == "violations" && row["rate"] == nil {
			t.Errorf("rateable bucket lost its rate: %+v", row)
		}
	}
}

// Review finding 5. %.0f turns 0.9978 into "100%", so a row would admit an
// unrated ask and claim full coverage in the same breath.
func TestFormatFP_CoverageNeverRoundsUpToFull(t *testing.T) {
	var decs []Decision
	for i := 0; i < 457; i++ {
		decs = append(decs, ask("violations", "go", "r", "f"+string(rune('a'+i%26))+".go", i, "s"))
	}
	decs = append(decs, Decision{TS: ts(900), Mode: "hook", Repo: "r", File: "x.go",
		Lang: "go", Decision: "ask", Reason: "violations"})
	s := FPReport(decs, ts(-1000), 10)

	if got := s.ByCheck["violations"]; got.Unrated != 1 || got.Asks < 400 {
		t.Fatalf("fixture wrong: %+v", got)
	}
	out := FormatFP(s)
	if strings.Contains(out, "+1 unrated (rate covers 100%)") {
		t.Errorf("a row cannot admit an unrated ask and claim 100%% coverage:\n%s", out)
	}
	if !strings.Contains(out, "+1 unrated (rate covers >99%)") {
		t.Errorf("want a >99%% form:\n%s", out)
	}
}

// Review finding 6. The worst-coverage row — 0% rated — was the one row with no
// ! marker, while the banner tells the reader to look for exactly that mark.
// And the "ask" column must mean the same quantity on every row.
func TestFormatFP_FullyUnratedRowIsMarkedAndColumnIsConsistent(t *testing.T) {
	decs := []Decision{
		ask("violations", "go", "r1", "a.go", 0, "sym"),
		outcome("a.go", 1, "sym"),
		contractAsk("b.go", 600),
		contractAsk("c.go", 660),
	}
	out := FormatFP(FPReport(decs, ts(-1000), 10))
	if !strings.Contains(out, "! +2 unrated") {
		t.Errorf("the 0%%-rated contract row must carry the ! the banner promises:\n%s", out)
	}
	// The contract check has 0 RATEABLE asks; the column shows rateable counts.
	if !strings.Contains(out, "contract                        0 ask") {
		t.Errorf("the ask column must stay the rateable count on every row:\n%s", out)
	}
}

// Review finding 7. loudest_repos[].asks and overall.asks must not carry two
// different denominators under one JSON name.
func TestPayloadFP_LoudestReposSplitsRatedAndUnrated(t *testing.T) {
	decs := []Decision{
		ask("violations", "go", "loud", "a.go", 0, "sym"),
		contractAsk("b.go", 600), // repo r1
		contractAsk("c.go", 660),
	}
	p := PayloadFP(FPReport(decs, ts(-1000), 10))
	repos := p["loudest_repos"].([]RepoCount)
	if len(repos) != 2 {
		t.Fatalf("repos = %+v, want 2", repos)
	}
	// Ranked by total volume: r1's 2 unrated asks beat loud's 1 rated ask.
	if repos[0].Repo != "r1" || repos[0].Total() != 2 || repos[0].Asks != 0 || repos[0].Unrated != 2 {
		t.Errorf("repos[0] = %+v, want r1 with 0 rated / 2 unrated", repos[0])
	}
	sumAsks := 0
	for _, r := range repos {
		sumAsks += r.Asks
	}
	if overall := p["overall"].(map[string]any); sumAsks != overall["asks"] {
		t.Errorf("sum(loudest_repos[].asks) = %d, must reconcile with overall.asks = %v",
			sumAsks, overall["asks"])
	}
}

// #316: friction's time-to-decision term.

func TestLatencyStats_Empty(t *testing.T) {
	got := latencyStats(nil)
	if got.N != 0 || got.MedianS != 0 {
		t.Errorf("empty input should be the zero value, got %+v", got)
	}
}

func TestLatencyStats_OddAndEvenMedian(t *testing.T) {
	odd := latencyStats([]time.Duration{1 * time.Minute, 3 * time.Minute, 2 * time.Minute})
	if odd.N != 3 || odd.MedianS != 120 {
		t.Errorf("odd-N median = %+v, want N=3 median=120s", odd)
	}
	if odd.MinS != 60 || odd.MaxS != 180 {
		t.Errorf("odd-N min/max = %+v, want 60/180", odd)
	}
	even := latencyStats([]time.Duration{1 * time.Minute, 2 * time.Minute, 3 * time.Minute, 4 * time.Minute})
	// (2m + 3m) / 2 = 150s
	if even.N != 4 || even.MedianS != 150 {
		t.Errorf("even-N median = %+v, want N=4 median=150s", even)
	}
	if even.MeanS != 150 {
		t.Errorf("even-N mean = %+v, want 150s", even)
	}
}

// The join key is (file, symbols) — latency must be measured from the ASK's
// own timestamp to the outcome's, not from some other pair's, even when
// several asks are in flight across different files at once.
func TestFPReport_LatencyMeasuresTheMatchedPair(t *testing.T) {
	decs := []Decision{
		ask("violations", "py", "r", "a.py", 0, "foo"),
		outcome("a.py", 4, "foo"), // 4 min after its own ask
		ask("violations", "py", "r", "b.py", 100, "bar"),
		outcome("b.py", 101, "bar"), // 1 min after ITS ask, not a.py's
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Latency.N != 2 {
		t.Fatalf("latency N = %d, want 2", s.Window.Latency.N)
	}
	// median of {4min, 1min} = 2.5min = 150s
	if s.Window.Latency.MedianS != 150 {
		t.Errorf("window median = %.0fs, want 150s (median of 240s and 60s)", s.Window.Latency.MedianS)
	}
	if s.Window.Latency.MinS != 60 || s.Window.Latency.MaxS != 240 {
		t.Errorf("window min/max = %+v, want 60/240", s.Window.Latency)
	}
}

// An ask with no matching approval must not silently borrow another ask's
// latency sample — it has none, and the bucket's N must reflect that.
func TestFPReport_UnmatchedAskContributesNoLatency(t *testing.T) {
	decs := []Decision{
		ask("violations", "py", "r", "a.py", 0, "foo"),
		outcome("a.py", 2, "foo"),
		ask("violations", "py", "r", "b.py", 10, "bar"), // never approved
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Asks != 2 || s.Window.Latency.N != 1 {
		t.Errorf("asks=%d latency.N=%d, want 2/1 (only the matched ask has a sample)",
			s.Window.Asks, s.Window.Latency.N)
	}
}

// Latency must be broken out per check, mirroring Approved — a slow
// duplicate-symbol decision must not blend into violations' number.
func TestFPReport_LatencyByCheck(t *testing.T) {
	decs := []Decision{
		ask("violations", "go", "r", "a.go", 0, "foo"),
		outcome("a.go", 1, "foo"), // 1 min
		ask("duplicate-symbol", "go", "r", "b.go", 0, "bar"),
		outcome("b.go", 4, "bar"), // 4 min — inside the 5-min join window
	}
	s := FPReport(decs, ts(-1000), 10)
	if got := s.ByCheck["violations"].Latency; got.N != 1 || got.MedianS != 60 {
		t.Errorf("violations latency = %+v, want N=1 median=60s", got)
	}
	if got := s.ByCheck["duplicate-symbol"].Latency; got.N != 1 || got.MedianS != 240 {
		t.Errorf("duplicate-symbol latency = %+v, want N=1 median=240s", got)
	}
}

func TestPayloadFP_LatencyIsNullNotZeroWhenUnmatched(t *testing.T) {
	decs := []Decision{
		ask("violations", "go", "r", "a.go", 0, "foo"), // never approved
	}
	p := PayloadFP(FPReport(decs, ts(-1000), 10))
	overall := p["overall"].(map[string]any)
	if overall["latency"] != nil {
		t.Errorf("latency = %v, want nil (no matched ask to measure)", overall["latency"])
	}
}

func TestPayloadFP_LatencyPresentWhenMatched(t *testing.T) {
	decs := []Decision{
		ask("violations", "go", "r", "a.go", 0, "foo"),
		outcome("a.go", 2, "foo"),
	}
	p := PayloadFP(FPReport(decs, ts(-1000), 10))
	overall := p["overall"].(map[string]any)
	lat, ok := overall["latency"].(map[string]any)
	if !ok {
		t.Fatalf("latency = %#v, want a populated map", overall["latency"])
	}
	if lat["n"] != 1 || lat["median_s"] != 120.0 {
		t.Errorf("latency = %+v, want n=1 median_s=120", lat)
	}
}

func TestFormatFP_PrintsMedianLatencyWhenMatched(t *testing.T) {
	decs := []Decision{
		ask("violations", "go", "r", "a.go", 0, "foo"),
		outcome("a.go", 2, "foo"),
	}
	out := FormatFP(FPReport(decs, ts(-1000), 10))
	if !strings.Contains(out, "median time to decision") {
		t.Errorf("overall line should report median latency:\n%s", out)
	}
	if !strings.Contains(out, "(median 2m0s)") {
		t.Errorf("per-check row should report median latency:\n%s", out)
	}
}

func TestFormatFP_OmitsLatencyWhenNothingMatched(t *testing.T) {
	decs := []Decision{
		ask("violations", "go", "r", "a.go", 0, "foo"), // never approved
	}
	out := FormatFP(FPReport(decs, ts(-1000), 10))
	if strings.Contains(out, "median time to decision") || strings.Contains(out, "median") {
		t.Errorf("no matched ask means no latency claim to print:\n%s", out)
	}
}
