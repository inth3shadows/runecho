package guardstats

import (
	"testing"
	"time"
)

// askCounts runs all three reports over one log and returns their ask counts:
// FPReport (rated + unrated), Audit, and Aggregate (guard-stats).
func askCounts(t *testing.T, decs []Decision) (fp, audit, agg int) {
	t.Helper()
	since := ts(-1000)
	s := FPReport(decs, since, 10)
	a := Audit(decs, since, &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}})
	g := Aggregate(decs, since, 10)
	return s.Window.Asks + s.Window.Unrated, a.Asks, g.TotalAsk
}

func at(d Decision, offset time.Duration) Decision {
	d.TS = d.TS.Add(offset)
	return d
}

// #493 case 1: a duplicate fire that straddles a second boundary is still one
// event. Before, FPReport's key truncated to the second: asks=2 approved=1.
func TestDedupe_SecondBoundaryStraddle(t *testing.T) {
	a := at(ask("violations", "py", "r1", "a.py", 0, "foo"), 900*time.Millisecond)
	decs := []Decision{a, at(a, 200*time.Millisecond), outcome("a.py", 1, "foo")}
	s := FPReport(decs, ts(-1000), 10)
	if s.Window.Asks != 1 || s.Window.Approved != 1 {
		t.Errorf("FPReport asks/approved = %d/%d, want 1/1", s.Window.Asks, s.Window.Approved)
	}
	if fp, audit, agg := askCounts(t, decs); fp != 1 || audit != 1 || agg != 1 {
		t.Errorf("asks fp/audit/agg = %d/%d/%d, want 1/1/1", fp, audit, agg)
	}
}

// Beyond the window, identical asks are two events: a real re-ask.
func TestDedupe_OutsideWindowStaysDistinct(t *testing.T) {
	a := ask("violations", "py", "r1", "a.py", 0, "foo")
	decs := []Decision{a, at(a, 6*time.Second)}
	if fp, audit, agg := askCounts(t, decs); fp != 2 || audit != 2 || agg != 2 {
		t.Errorf("asks fp/audit/agg = %d/%d/%d, want 2/2/2", fp, audit, agg)
	}
}

// The window is measured from the cluster's first fire, so re-asks 4s apart
// do not chain into one event.
func TestDedupe_WindowDoesNotChain(t *testing.T) {
	a := ask("violations", "py", "r1", "a.py", 0, "foo")
	decs := []Decision{a, at(a, 4*time.Second), at(a, 8*time.Second)}
	if fp, audit, agg := askCounts(t, decs); fp != 2 || audit != 2 || agg != 2 {
		t.Errorf("asks fp/audit/agg = %d/%d/%d, want 2/2/2", fp, audit, agg)
	}
}

// #493 case 2: parallel sessions interleave (A, B, A'), and parallel hook
// processes can write out of order. Audit compared only with the previous
// record, and required a non-negative delta.
func TestDedupe_InterleavedAndOutOfOrder(t *testing.T) {
	a := ask("violations", "py", "r1", "a.py", 0, "foo")
	a.Session = "s1"
	b := ask("violations", "py", "r1", "b.py", 0, "bar")
	b.Session = "s2"
	b = at(b, time.Second)
	interleaved := []Decision{a, b, at(a, 2*time.Second)}
	if fp, audit, agg := askCounts(t, interleaved); fp != 2 || audit != 2 || agg != 2 {
		t.Errorf("interleaved asks fp/audit/agg = %d/%d/%d, want 2/2/2", fp, audit, agg)
	}
	outOfOrder := []Decision{at(a, time.Second), a}
	if fp, audit, agg := askCounts(t, outOfOrder); fp != 1 || audit != 1 || agg != 1 {
		t.Errorf("out-of-order asks fp/audit/agg = %d/%d/%d, want 1/1/1", fp, audit, agg)
	}
}

// Fields that tell two asks apart keep them apart in every report.
func TestDedupe_DistinguishingFieldsInEveryReport(t *testing.T) {
	base := ask("violations", "py", "r1", "a.py", 0, "foo")
	base.Edit = "e1"
	vary := map[string]func(Decision) Decision{
		"file":   func(d Decision) Decision { d.File = "b.py"; return d },
		"reason": func(d Decision) Decision { d.Reason = "dangling"; return d },
		"symbol": func(d Decision) Decision { d.Symbols = []string{"bar"}; return d },
		"repo":   func(d Decision) Decision { d.Repo = "r2"; return d },
		"lang":   func(d Decision) Decision { d.Lang = "go"; return d },
		"gv":     func(d Decision) Decision { d.GV = "v9.9.9"; return d },
		"edit":   func(d Decision) Decision { d.Edit = "e2"; return d },
	}
	for name, mutate := range vary {
		t.Run(name, func(t *testing.T) {
			if fp, audit, agg := askCounts(t, []Decision{base, mutate(base)}); fp != 2 || audit != 2 || agg != 2 {
				t.Errorf("asks fp/audit/agg = %d/%d/%d, want 2/2/2", fp, audit, agg)
			}
		})
	}
}

// Pre-commit asks are never collapsed: they carry no file to group on.
func TestDedupe_PrecommitNotCollapsed(t *testing.T) {
	p := Decision{TS: ts(0), Mode: "precommit", Repo: "r1", Decision: "ask", Reason: "violations", Symbols: []string{"foo"}}
	g := Aggregate([]Decision{p, p}, ts(-1000), 10)
	if g.TotalAsk != 2 {
		t.Errorf("Aggregate TotalAsk = %d, want 2 for two pre-commit asks", g.TotalAsk)
	}
}

// A fire before the window does not swallow its duplicate inside it: every
// report sees only the window, so the in-window fire is the event.
func TestDedupe_WindowEdgeKeepsInWindowFire(t *testing.T) {
	since := ts(0)
	a := ask("violations", "py", "r1", "a.py", 0, "foo")
	decs := []Decision{at(a, -time.Second), at(a, time.Second)}
	s := FPReport(decs, since, 10)
	au := Audit(decs, since, &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}})
	g := Aggregate(decs, since, 10)
	if got := s.Window.Asks + s.Window.Unrated; got != 1 || au.Asks != 1 || g.TotalAsk != 1 {
		t.Errorf("asks fp/audit/agg = %d/%d/%d, want 1/1/1", got, au.Asks, g.TotalAsk)
	}
}

// #534 review F5: one wiring's fire can lack the session another's carries
// (TestAskSessions encodes it). Session is not part of the key, so they are
// still one event in every report.
func TestDedupe_FireWithoutSessionStillCollapses(t *testing.T) {
	a := ask("violations", "py", "r1", "a.py", 0, "foo")
	b := at(a, time.Second)
	b.Session = "s1"
	if fp, audit, agg := askCounts(t, []Decision{a, b}); fp != 1 || audit != 1 || agg != 1 {
		t.Errorf("asks fp/audit/agg = %d/%d/%d, want 1/1/1", fp, audit, agg)
	}
}

// A record past the window starts a new cluster, and that cluster collapses
// its own repeat fires.
func TestDedupe_NewClusterCollapsesItsDuplicates(t *testing.T) {
	a := ask("violations", "py", "r1", "a.py", 0, "foo")
	decs := []Decision{a, at(a, 6*time.Second), at(a, 7*time.Second)}
	if fp, audit, agg := askCounts(t, decs); fp != 2 || audit != 2 || agg != 2 {
		t.Errorf("asks fp/audit/agg = %d/%d/%d, want 2/2/2", fp, audit, agg)
	}
}
