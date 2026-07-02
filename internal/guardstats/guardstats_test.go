package guardstats

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// LoadReader
// ---------------------------------------------------------------------------

func TestLoadReader_ValidLines(t *testing.T) {
	input := `{"v":1,"ts":"2026-06-01T10:00:00Z","mode":"hook","repo":"runecho","file":"a.go","lang":"go","decision":"ask","reason":"unresolved-symbol","symbols":["Foo","Bar"]}
{"v":1,"ts":"2026-06-01T10:05:00Z","mode":"hook","repo":"runecho","file":"b.go","lang":"go","decision":"defer","reason":"parse-fail"}
`
	decisions, err := LoadReader(strings.NewReader(input))
	if err != nil {
		t.Fatalf("LoadReader: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("got %d decisions, want 2", len(decisions))
	}
	if decisions[0].Decision != "ask" || decisions[0].Repo != "runecho" || len(decisions[0].Symbols) != 2 {
		t.Errorf("decisions[0] = %+v, unexpected shape", decisions[0])
	}
	if decisions[1].Decision != "defer" || decisions[1].Reason != "parse-fail" {
		t.Errorf("decisions[1] = %+v, unexpected shape", decisions[1])
	}
	wantTS, _ := time.Parse(time.RFC3339, "2026-06-01T10:00:00Z")
	if !decisions[0].TS.Equal(wantTS) {
		t.Errorf("decisions[0].TS = %v, want %v", decisions[0].TS, wantTS)
	}
}

func TestLoadReader_MalformedLineTolerance(t *testing.T) {
	input := `{"v":1,"ts":"2026-06-01T10:00:00Z","mode":"hook","decision":"ask"}
not valid json at all
{"v":1,"ts":"2026-06-01T10:05:00Z","mode":"hook","decision":"defer"}
`
	decisions, err := LoadReader(strings.NewReader(input))
	if err != nil {
		t.Fatalf("LoadReader: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("got %d decisions, want 2 (malformed line should be skipped, not error)", len(decisions))
	}
}

// TestLoadReader_OversizedLineTolerance guards against a regression to
// bufio.Scanner, whose fixed ~64KB token cap makes Scan() fail (and, once
// failed, unable to resume) on a single line past that size — silently
// dropping every record after it instead of just the one oversized line. A
// large ask's symbols array can plausibly exceed 64KB. Unlike a malformed
// line, an oversized-but-valid line isn't skipped: it's correctly parsed
// (ReadString has no size cap), and parsing must still continue afterward.
func TestLoadReader_OversizedLineTolerance(t *testing.T) {
	huge := make([]string, 20000)
	for i := range huge {
		huge[i] = "VeryLongSymbolNameToInflateThisLineWellPast64KB"
	}
	oversizedLine := `{"v":1,"ts":"2026-06-01T10:00:00Z","mode":"hook","decision":"ask","symbols":["` +
		strings.Join(huge, `","`) + `"]}`
	if len(oversizedLine) < 65536 {
		t.Fatalf("test fixture line is only %d bytes, want > 64KB", len(oversizedLine))
	}
	input := oversizedLine + "\n" +
		`{"v":1,"ts":"2026-06-01T10:05:00Z","mode":"hook","decision":"defer","reason":"parse-fail"}` + "\n"

	decisions, err := LoadReader(strings.NewReader(input))
	if err != nil {
		t.Fatalf("LoadReader: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("got %d decisions, want 2 (oversized line parsed, later line still reached)", len(decisions))
	}
	if decisions[0].Decision != "ask" || len(decisions[0].Symbols) != len(huge) {
		t.Errorf("decisions[0] = %d symbols, want %d", len(decisions[0].Symbols), len(huge))
	}
	if decisions[1].Decision != "defer" || decisions[1].Reason != "parse-fail" {
		t.Errorf("decisions[1] = %+v, want the defer record", decisions[1])
	}
}

func TestLoadReader_BadTimestampTolerance(t *testing.T) {
	input := `{"v":1,"ts":"not-a-timestamp","mode":"hook","decision":"ask"}
{"v":1,"ts":"2026-06-01T10:05:00Z","mode":"hook","decision":"defer"}
`
	decisions, err := LoadReader(strings.NewReader(input))
	if err != nil {
		t.Fatalf("LoadReader: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("got %d decisions, want 1 (bad-ts line should be skipped)", len(decisions))
	}
	if decisions[0].Decision != "defer" {
		t.Errorf("surviving decision = %+v, want the defer record", decisions[0])
	}
}

func TestLoadReader_EmptyLinesSkipped(t *testing.T) {
	input := "\n\n{\"v\":1,\"ts\":\"2026-06-01T10:00:00Z\",\"mode\":\"hook\",\"decision\":\"ask\"}\n\n"
	decisions, err := LoadReader(strings.NewReader(input))
	if err != nil {
		t.Fatalf("LoadReader: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("got %d decisions, want 1", len(decisions))
	}
}

// ---------------------------------------------------------------------------
// Aggregate
// ---------------------------------------------------------------------------

func mustTS(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}

func TestAggregate_ByRepoAndLang(t *testing.T) {
	since := mustTS(t, "2026-06-01T00:00:00Z")
	decisions := []Decision{
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Repo: "runecho", Lang: "go", Decision: "ask", Symbols: []string{"Foo"}},
		{TS: mustTS(t, "2026-06-03T00:00:00Z"), Repo: "runecho", Lang: "go", Decision: "defer", Reason: "parse-fail"},
		{TS: mustTS(t, "2026-06-04T00:00:00Z"), Repo: "other-repo", Lang: "python", Decision: "ask", Symbols: []string{"Bar"}},
	}
	stats := Aggregate(decisions, since, 10)

	if stats.TotalAsk != 2 || stats.TotalDefer != 1 {
		t.Fatalf("totals = ask=%d defer=%d, want ask=2 defer=1", stats.TotalAsk, stats.TotalDefer)
	}
	if got := stats.ByRepo["runecho"]; got.Ask != 1 || got.Defer != 1 {
		t.Errorf("ByRepo[runecho] = %+v, want {Ask:1 Defer:1}", got)
	}
	if got := stats.ByRepo["other-repo"]; got.Ask != 1 || got.Defer != 0 {
		t.Errorf("ByRepo[other-repo] = %+v, want {Ask:1 Defer:0}", got)
	}
	if got := stats.ByLang["go"]; got.Ask != 1 || got.Defer != 1 {
		t.Errorf("ByLang[go] = %+v, want {Ask:1 Defer:1}", got)
	}
	if got := stats.ByLang["python"]; got.Ask != 1 {
		t.Errorf("ByLang[python] = %+v, want {Ask:1}", got)
	}
}

func TestAggregate_TopSymbolRankingAndTieBreak(t *testing.T) {
	since := mustTS(t, "2026-06-01T00:00:00Z")
	decisions := []Decision{
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Decision: "ask", Symbols: []string{"Zeta"}},
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Decision: "ask", Symbols: []string{"Zeta"}},
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Decision: "ask", Symbols: []string{"Alpha"}},
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Decision: "ask", Symbols: []string{"Beta"}},
	}
	stats := Aggregate(decisions, since, 10)

	if len(stats.TopSymbols) != 3 {
		t.Fatalf("got %d top symbols, want 3", len(stats.TopSymbols))
	}
	// Zeta has count 2 (highest); Alpha/Beta tie at count 1, broken alphabetically.
	want := []SymbolCount{
		{Name: "Zeta", Count: 2},
		{Name: "Alpha", Count: 1},
		{Name: "Beta", Count: 1},
	}
	for i, w := range want {
		if stats.TopSymbols[i] != w {
			t.Errorf("TopSymbols[%d] = %+v, want %+v", i, stats.TopSymbols[i], w)
		}
	}
}

func TestAggregate_TopN_Truncates(t *testing.T) {
	since := mustTS(t, "2026-06-01T00:00:00Z")
	decisions := []Decision{
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Decision: "ask", Symbols: []string{"A"}},
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Decision: "ask", Symbols: []string{"B"}},
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Decision: "ask", Symbols: []string{"C"}},
	}
	stats := Aggregate(decisions, since, 2)
	if len(stats.TopSymbols) != 2 {
		t.Fatalf("got %d top symbols, want 2 (topN=2 truncation)", len(stats.TopSymbols))
	}
}

func TestAggregate_TimeWindowFiltersStaleRecords(t *testing.T) {
	since := mustTS(t, "2026-06-15T00:00:00Z")
	decisions := []Decision{
		{TS: mustTS(t, "2026-06-01T00:00:00Z"), Decision: "ask", Symbols: []string{"Stale"}}, // before since
		{TS: mustTS(t, "2026-06-20T00:00:00Z"), Decision: "ask", Symbols: []string{"Fresh"}}, // after since
	}
	stats := Aggregate(decisions, since, 10)
	if stats.TotalAsk != 1 {
		t.Fatalf("TotalAsk = %d, want 1 (stale record must be excluded)", stats.TotalAsk)
	}
	if len(stats.TopSymbols) != 1 || stats.TopSymbols[0].Name != "Fresh" {
		t.Errorf("TopSymbols = %+v, want only Fresh", stats.TopSymbols)
	}
}

func TestAggregate_DeferReasonTally(t *testing.T) {
	since := mustTS(t, "2026-06-01T00:00:00Z")
	decisions := []Decision{
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Decision: "defer", Reason: "parse-fail"},
		{TS: mustTS(t, "2026-06-03T00:00:00Z"), Decision: "defer", Reason: "parse-fail"},
		{TS: mustTS(t, "2026-06-04T00:00:00Z"), Decision: "defer", Reason: "unknown-lang"},
		// ask records' reasons must NOT be tallied into DeferReasons.
		{TS: mustTS(t, "2026-06-05T00:00:00Z"), Decision: "ask", Reason: "unresolved-symbol", Symbols: []string{"X"}},
	}
	stats := Aggregate(decisions, since, 10)
	if stats.DeferReasons["parse-fail"] != 2 {
		t.Errorf("DeferReasons[parse-fail] = %d, want 2", stats.DeferReasons["parse-fail"])
	}
	if stats.DeferReasons["unknown-lang"] != 1 {
		t.Errorf("DeferReasons[unknown-lang] = %d, want 1", stats.DeferReasons["unknown-lang"])
	}
	if _, ok := stats.DeferReasons["unresolved-symbol"]; ok {
		t.Errorf("DeferReasons must not include ask reasons, got %+v", stats.DeferReasons)
	}
}

func TestAggregate_OutcomeAndRefreshExcluded(t *testing.T) {
	since := mustTS(t, "2026-06-01T00:00:00Z")
	decisions := []Decision{
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Decision: "ask", Repo: "r", Symbols: []string{"X"}},
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Decision: "outcome", Repo: "r", Reason: "approved"},
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Mode: "e6", Decision: "refresh", Reason: "refreshed"},
	}
	stats := Aggregate(decisions, since, 10)
	if stats.TotalAsk != 1 || stats.TotalDefer != 0 {
		t.Fatalf("totals = ask=%d defer=%d, want ask=1 defer=0 (outcome/refresh excluded)", stats.TotalAsk, stats.TotalDefer)
	}
	if got := stats.ByRepo["r"]; got.Ask != 1 || got.Defer != 0 {
		t.Errorf("ByRepo[r] = %+v, want {Ask:1 Defer:0} (outcome must not contribute)", got)
	}
}

// ---------------------------------------------------------------------------
// Format / Payload sanity
// ---------------------------------------------------------------------------

func TestFormat_ContainsExpectedSections(t *testing.T) {
	since := mustTS(t, "2026-06-01T00:00:00Z")
	decisions := []Decision{
		{TS: mustTS(t, "2026-06-02T00:00:00Z"), Repo: "runecho", Lang: "go", Decision: "ask", Symbols: []string{"Foo"}},
		{TS: mustTS(t, "2026-06-03T00:00:00Z"), Repo: "runecho", Lang: "go", Decision: "defer", Reason: "parse-fail"},
	}
	stats := Aggregate(decisions, since, 10)
	out := Format(stats)
	for _, want := range []string{"GUARD STATS", "runecho", "go", "Foo", "parse-fail"} {
		if !strings.Contains(out, want) {
			t.Errorf("Format output missing %q:\n%s", want, out)
		}
	}
}

func TestPayload_ArraysNeverNil(t *testing.T) {
	stats := Aggregate(nil, mustTS(t, "2026-06-01T00:00:00Z"), 10)
	p := Payload(stats)
	for _, key := range []string{"by_repo", "by_lang", "top_symbols", "defer_reasons"} {
		v, ok := p[key]
		if !ok {
			t.Fatalf("Payload missing key %q", key)
		}
		if v == nil {
			t.Errorf("Payload[%q] is nil, want empty slice", key)
		}
	}
}
