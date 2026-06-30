package bench

import "testing"

// Fixed seed + size = a reproducible corpus. Same input, same score.
const (
	benchSeed = 1
	perPool   = 40 // 40 negatives + ~40 positives per language
)

// TestScorecard generates the synthetic corpus, scores the guard against it,
// prints the scorecard, and asserts the saturated baseline as a DETERMINISM
// REGRESSION TRIPWIRE.
//
// The synthetic corpus stays inside the guard's declared scope: in-scope,
// call-position references (exported for Go). There, the guard is exact —
// recall 100%, false-positive 0%. Both the corpus and the guard are fully
// deterministic, so this is a non-flaky tripwire: if a parser/guard change drops
// recall below 100% or raises false-positives above 0%, something regressed and
// this test fails with the exact delta.
//
// What a green run does NOT mean: that RunEcho catches all real-world
// hallucinations. The hard cases (qualified obj.Method calls, unexported Go,
// non-call positions) are out of synthetic scope by construction. The TRUE
// quality number against an observed LLM error distribution comes from the
// captured-LLM corpus (Phase 2), not this scaffold.
func TestScorecard(t *testing.T) {
	cases := Generate(benchSeed, perPool)
	if len(cases) == 0 {
		t.Fatal("empty corpus")
	}
	sc := Score(cases)
	t.Logf("\n%s", sc.Format())

	o := sc.Overall
	if o.TP+o.FN == 0 {
		t.Fatal("no hallucinated cases generated")
	}
	if o.FP+o.TN == 0 {
		t.Fatal("no real cases generated")
	}
	// Regression tripwire (safe to gate: deterministic corpus + deterministic guard).
	if o.recall() != 1.0 {
		t.Errorf("catch-rate regressed: want 100%%, got %.1f%% (FN=%d)", 100*o.recall(), o.FN)
	}
	if o.fpRate() != 0.0 {
		t.Errorf("false-positive regressed: want 0%%, got %.1f%% (FP=%d)", 100*o.fpRate(), o.FP)
	}
}
