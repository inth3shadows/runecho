package guardstats

import (
	"strings"
	"testing"
	"time"
)

// #528 deny posture: a deny is an ask EVENT (the guard flagged the edit) but
// not a rateable one — no human could override it and no outcome follows. So
// every report counts it once, and FPReport keeps it out of the approval rate.
func TestDenyCountsOnceAndStaysOutOfTheRate(t *testing.T) {
	deny := ask("violations", "py", "r1", "a.py", 0, "ghost")
	deny.Decision = "deny"
	dupDeny := at(deny, time.Second) // a second hook wiring's fire
	a := ask("violations", "py", "r1", "b.py", 1, "real")
	out := outcome("b.py", 2, "real")
	decs := []Decision{deny, dupDeny, a, out}
	since := ts(-1000)

	if got := len(duplicateAskFires(decs, since)); got != 1 {
		t.Errorf("duplicateAskFires = %d, want 1 (the deny's second fire)", got)
	}
	au := Audit(decs, since, &fakeOracle{head: "HEAD", revAt: map[string]string{"": "OLD"}})
	if au.Asks != 2 {
		t.Errorf("Audit.Asks = %d, want 2 (one deny, one ask)", au.Asks)
	}
	if g := Aggregate(decs, since, 10); g.TotalAsk != 2 {
		t.Errorf("Aggregate.TotalAsk = %d, want 2", g.TotalAsk)
	}
	s := FPReport(decs, since, 10)
	if s.Window.Asks != 1 || s.Window.Approved != 1 || s.Window.Rate() != 1 {
		t.Errorf("FPReport asks/approved/rate = %d/%d/%.2f, want 1/1/1.00", s.Window.Asks, s.Window.Approved, s.Window.Rate())
	}
	if s.Window.Denied != 1 || s.ByReason["violations"].Denied != 1 {
		t.Errorf("FPReport Denied window/violations = %d/%d, want 1/1", s.Window.Denied, s.ByReason["violations"].Denied)
	}
	if !strings.Contains(FormatFP(s), "denied") {
		t.Errorf("FormatFP does not report denies:\n%s", FormatFP(s))
	}
}

// A deny and an ask of the same edit are different events, never collapsed.
func TestDenyAndAskDoNotCollapse(t *testing.T) {
	a := ask("violations", "py", "r1", "a.py", 0, "ghost")
	d := at(a, time.Second)
	d.Decision = "deny"
	if got := len(duplicateAskFires([]Decision{a, d}, ts(-1000))); got != 0 {
		t.Errorf("an ask and a deny collapsed into one event")
	}
}

// #540 review F3: a window of only denies (the #528 deny arm) is not empty —
// its per-check rows and its JSON carry the denies.
func TestDenyOnlyWindowIsReported(t *testing.T) {
	var decs []Decision
	for m := 0; m < 3; m++ {
		d := ask("violations", "go", "r1", "a.go", m, "ghost")
		d.Decision = "deny"
		decs = append(decs, d)
	}
	s := FPReport(decs, ts(-1000), 10)
	out := FormatFP(s)
	if strings.Contains(out, "No hook-mode asks in window.") {
		t.Errorf("a deny-only window reported itself as empty:\n%s", out)
	}
	if !strings.Contains(out, "+3 denied") {
		t.Errorf("per-bucket rows do not show the denies:\n%s", out)
	}
	p := PayloadFP(s)
	ov, _ := p["overall"].(map[string]any)
	if ov["denied"] != 3 {
		t.Errorf("JSON overall.denied = %v, want 3", ov["denied"])
	}
}
