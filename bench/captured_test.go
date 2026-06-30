package bench

import "testing"

// sampleCorpus illustrates the fixture schema and exercises the loader's
// validation + the stratified scorer. The REAL corpus (bench/captured/corpus.json,
// ~30 hand-labeled cases) is harvested in a working session and is not committed
// yet; when it lands, point the gate below at it and assert N>=20 +
// observed-floor compliance.
const sampleCorpus = "captured/corpus.sample.json"

// TestCapturedLoadsAndScores verifies the sample fixtures load (every record
// passes the strict consistency checks), then prints the stratified scorecard.
// It does NOT gate on catch-rate: with real captured data, misses (qualified
// calls, unexported Go, extraction misses) are expected findings, not failures.
func TestCapturedLoadsAndScores(t *testing.T) {
	cases, err := LoadCaptured(sampleCorpus)
	if err != nil {
		t.Fatalf("loading %s: %v", sampleCorpus, err)
	}
	if len(cases) == 0 {
		t.Fatalf("%s loaded zero cases", sampleCorpus)
	}
	report := ScoreCaptured(cases)
	t.Logf("\n%s", report.Format())

	// Both strata should be represented in the sample so the report shape is
	// exercised; the real corpus must additionally satisfy the observed floor.
	if report.ByStratum[Observed] == nil {
		t.Error("sample corpus has no observed (transcript:) cases")
	}
	if report.ByStratum[Elicited] == nil {
		t.Error("sample corpus has no elicited (elicited:) cases")
	}
}

// realCorpus is the harvested observed corpus: ~15 hand-verified hallucinations
// (and real references) mined from session transcripts, each backed by an
// in-session compiler/runtime error. All-observed, so it clears the floor.
const realCorpus = "captured/corpus.json"

// TestCapturedRealCorpus loads the harvested corpus, prints its scorecard
// (the actual finding), and asserts only that it loads cleanly and meets the
// observed-majority floor. Catch-rate is NOT gated: low catch-rate here is the
// finding (the guard's narrow positional scope), not a regression.
func TestCapturedRealCorpus(t *testing.T) {
	cases, err := LoadCaptured(realCorpus)
	if err != nil {
		t.Fatalf("loading %s: %v", realCorpus, err)
	}
	report := ScoreCaptured(cases)
	t.Logf("\n%s", report.Format())

	if got := report.observedFraction(); got < ObservedFloor {
		t.Errorf("observed fraction %.0f%% below floor %.0f%%", 100*got, 100*ObservedFloor)
	}
}

// TestCapturedRejectsMislabel pins the load-time consistency guard: a case
// labeled "real" whose symbol is absent from its own known_symbols is a labeling
// bug and must be rejected, not silently scored.
func TestCapturedRejectsMislabel(t *testing.T) {
	bad := CapturedCase{
		ID: "x", Lang: "go", SourceLine: "\t_ = Ghost(ctx)",
		ReferencedSymbol: "Ghost", Label: "real",
		KnownSymbols: []string{"ParseConfig"}, Provenance: "transcript:t",
	}
	if err := bad.validate(); err == nil {
		t.Error("expected validation error for real-labeled case missing from known_symbols")
	}
}
