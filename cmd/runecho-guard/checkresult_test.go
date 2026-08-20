package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFiredChecksFrom_MatchesFieldByField pins #330's central risk-reduction
// move: askReason and firedChecks.anyNonViolation are UNCHANGED by this
// refactor (see checkresult.go's doc), so the only new logic that can drift
// the frozen decisionRecord.Reason format is firedChecksFrom's mapping. This
// walks every field firedChecks has (via dangling_test.go's firedCheckFields
// reflection helper, so a future field addition to firedChecks is covered
// automatically) and confirms a results slice with exactly that check set to
// VerdictViolation projects onto a firedChecks with exactly that field set —
// nothing more, nothing less.
func TestFiredChecksFrom_MatchesFieldByField(t *testing.T) {
	fieldToCheck := map[string]string{
		"Violations": "violations",
		"FileScope":  "file-scope",
		"Qualified":  "qualified",
		"DepsGo":     "deps-go",
		"Dangling":   "dangling",
		"Dropped":    "dropped-import",
		"Duplicate":  "duplicate-symbol",
		"CallShape":  "call-shape",
		"RecvMethod": "recv-method",
		"VarType":    "var-type",
		"Lint":       "lint",
	}
	fields := firedCheckFields(t)
	if len(fields) != len(fieldToCheck) {
		t.Fatalf("firedChecks has %d fields, this test's map has %d — a field was "+
			"added to firedChecks without a matching checkOrder entry here", len(fields), len(fieldToCheck))
	}
	for _, field := range fields {
		check, ok := fieldToCheck[field]
		if !ok {
			t.Fatalf("firedChecks.%s has no entry in fieldToCheck — firedChecksFrom "+
				"cannot possibly set it, so any check that fires on this field would "+
				"silently vanish from askReason's output", field)
		}
		results := []CheckResult{{Check: check, Verdict: VerdictViolation}}
		got := firedChecksFrom(results)
		want := firedOnly(t, field)
		if got != want {
			t.Errorf("firedChecksFrom([%s: Violation]) = %+v, want %+v", check, got, want)
		}
	}
}

// TestFiredChecksFrom_NonViolationVerdictsDoNotFire covers the other half:
// OK, Unknown, and Skipped must never set a firedChecks field, or a check
// that merely couldn't answer would read as a real finding.
func TestFiredChecksFrom_NonViolationVerdictsDoNotFire(t *testing.T) {
	for _, v := range []Verdict{VerdictOK, VerdictUnknown, VerdictSkipped} {
		results := []CheckResult{{Check: "violations", Verdict: v}, {Check: "dangling", Verdict: v}}
		if got := firedChecksFrom(results); got != (firedChecks{}) {
			t.Errorf("firedChecksFrom with verdict %d = %+v, want zero value", v, got)
		}
	}
}

// TestFiredChecksFrom_UnrecognizedCheckIsIgnored: a Check name outside
// checkOrder's eleven is a caller bug (see checkresult.go's doc) — it must not
// panic on every hook invocation, and it must not silently promote to some
// other field.
func TestFiredChecksFrom_UnrecognizedCheckIsIgnored(t *testing.T) {
	results := []CheckResult{{Check: "not-a-real-check", Verdict: VerdictViolation}}
	if got := firedChecksFrom(results); got != (firedChecks{}) {
		t.Errorf("firedChecksFrom with an unknown check name = %+v, want zero value", got)
	}
}

// TestAskReason_ViaFiredChecksFrom is the equivalence pin the #330 issue
// requires: askReason(firedChecksFrom(results)) must match the exact table
// dangling_test.go's TestAskReason already pins for hand-built firedChecks
// values, for every combination that table covers. Since askReason itself is
// untouched (see checkresult.go), this mainly proves firedChecksFrom's
// mapping preserves checkOrder's iteration order — a transposition there
// would still produce a valid-looking but WRONG '+'-joined string.
func TestAskReason_ViaFiredChecksFrom(t *testing.T) {
	combos := []struct {
		checks []string
		want   string
	}{
		{[]string{"violations"}, "violations"},
		{[]string{"dangling"}, "dangling"},
		{[]string{"dropped-import"}, "dropped-import"},
		{[]string{"duplicate-symbol"}, "duplicate-symbol"},
		{[]string{"call-shape"}, "call-shape"},
		{[]string{"file-scope"}, "file-scope"},
		{[]string{"qualified"}, "qualified"},
		{[]string{"deps-go"}, "deps-go"},
		{[]string{"violations", "dangling"}, "violations+dangling"},
		{[]string{"violations", "dropped-import"}, "violations+dropped-import"},
		{[]string{"violations", "duplicate-symbol"}, "violations+duplicate-symbol"},
		{[]string{"violations", "call-shape"}, "violations+call-shape"},
		{[]string{"dangling", "dropped-import"}, "dangling+dropped-import"},
		{
			[]string{"violations", "dangling", "dropped-import", "duplicate-symbol", "call-shape"},
			"violations+dangling+dropped-import+duplicate-symbol+call-shape",
		},
		{[]string{"violations", "file-scope", "dangling"}, "violations+file-scope+dangling"},
		{[]string{"file-scope", "qualified", "deps-go"}, "file-scope+qualified+deps-go"},
		{nil, "violations"}, // not called in practice when nothing fired
	}
	for _, c := range combos {
		var results []CheckResult
		for _, name := range c.checks {
			results = append(results, CheckResult{Check: name, Verdict: VerdictViolation})
		}
		if got := askReason(firedChecksFrom(results)); got != c.want {
			t.Errorf("askReason(firedChecksFrom(%v)) = %q, want %q", c.checks, got, c.want)
		}
	}
}

// TestClassifyResult covers the Violation > Unknown > OK precedence
// (checkresult.go's documented invariant) and the reason-carries-through
// case.
func TestClassifyResult(t *testing.T) {
	if got := classifyResult("x", true, "some-abstain-reason"); got.Verdict != VerdictViolation {
		t.Errorf("found=true with a reason still set = %v, want VerdictViolation (violation wins)", got.Verdict)
	}
	if got := classifyResult("x", true, "some-abstain-reason"); got.Reason != "" {
		t.Errorf("a Violation result carries a Reason = %q, want empty", got.Reason)
	}
	got := classifyResult("x", false, "go-work")
	if got.Verdict != VerdictUnknown || got.Reason != "go-work" {
		t.Errorf("found=false with a reason = %+v, want {Unknown, go-work}", got)
	}
	got = classifyResult("x", false, "")
	if got.Verdict != VerdictOK || got.Reason != "" {
		t.Errorf("found=false with no reason = %+v, want {OK, \"\"}", got)
	}
}

// TestVerdictString pins the four tokens decisionRecord.Checks and fpreport's
// CheckTally (#333) key on. A drift here breaks the switch in FPReport's
// CheckRuns tally silently (an unrecognized string just falls through every
// case), so this is worth its own test independent of checkStatusMap's.
func TestVerdictString(t *testing.T) {
	cases := map[Verdict]string{
		VerdictOK:        "ok",
		VerdictViolation: "violation",
		VerdictUnknown:   "unknown",
		VerdictSkipped:   "skipped",
	}
	for v, want := range cases {
		if got := v.String(); got != want {
			t.Errorf("Verdict(%d).String() = %q, want %q", v, got, want)
		}
	}
	// An out-of-range value (never produced by classifyResult, but not
	// prevented by the type) must not panic and must not silently key an
	// empty-string bucket in the persisted map.
	if got := Verdict(99).String(); got != "unknown" {
		t.Errorf("Verdict(99).String() = %q, want the unknown fallback", got)
	}
}

// TestCheckStatusMap pins #333's projection: every result becomes one
// check->verdict-string entry, and an empty/nil results slice returns nil
// (not an empty non-nil map) so decisionRecord's omitempty drops the field
// entirely rather than persisting "checks":{} on every early-return defer.
func TestCheckStatusMap(t *testing.T) {
	results := []CheckResult{
		{Check: "violations", Verdict: VerdictOK},
		{Check: "lint", Verdict: VerdictSkipped, Reason: "not-python"},
		{Check: "dangling", Verdict: VerdictViolation},
		{Check: "deps-go", Verdict: VerdictUnknown, Reason: "go.work workspace"},
	}
	got := checkStatusMap(results)
	want := map[string]string{
		"violations": "ok",
		"lint":       "skipped",
		"dangling":   "violation",
		"deps-go":    "unknown",
	}
	if len(got) != len(want) {
		t.Fatalf("checkStatusMap = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("checkStatusMap[%q] = %q, want %q", k, got[k], v)
		}
	}
	if checkStatusMap(nil) != nil {
		t.Errorf("checkStatusMap(nil) = %v, want nil", checkStatusMap(nil))
	}
}

// TestStoreQueryReason is a one-line invariant, pinned so a future refactor
// of the dangling/duplicate query-error plumbing can't silently invert it.
func TestStoreQueryReason(t *testing.T) {
	if got := storeQueryReason(0); got != "" {
		t.Errorf("storeQueryReason(0) = %q, want empty", got)
	}
	if got := storeQueryReason(1); got != "store-query-failed" {
		t.Errorf("storeQueryReason(1) = %q, want store-query-failed", got)
	}
}

// TestDepsGo_GoWorkAbstain_StrictAdvisory is #330's concrete before/after
// proof: with RUNECHO_GUARD_DEPS_GO=1 under a go.work workspace, EVERY
// depindex.GoIndex.Lookup returns Unknown (internal/depindex/golang.go's
// Lookup echoes idx.reason uniformly — see GoDepQualifiedViolationsWithReason's
// doc). Before this issue that abstain was invisible outside RUNECHO_DEBUG —
// the edit logged "clean" exactly like one where deps-go genuinely found
// nothing. After, RUNECHO_GUARD_STRICT=1 surfaces it as a check-degraded
// advisory, because "found nothing" and "could not check" are no longer the
// same in-process state.
func TestDepsGo_GoWorkAbstain_StrictAdvisory(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	if err := os.WriteFile(filepath.Join(top, "go.mod"), []byte("module example.com/x\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// A go.work overlay can redirect any module in the workspace to a local
	// directory go.mod knows nothing about — depindex refuses to index at all
	// rather than risk resolving the wrong package (internal/depindex/golang.go).
	if err := os.WriteFile(filepath.Join(top, "go.work"), []byte("go 1.24\n\nuse .\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNECHO_GUARD_DEPS_GO", "1")
	t.Setenv("RUNECHO_GUARD_STRICT", "1")

	file := filepath.Join(top, "known.go")
	// Pre-edit file already imports net/http; the edit adds a qualified call —
	// any external selector triggers the abstain path once go.work is present,
	// regardless of whether net/http itself would otherwise resolve cleanly.
	if err := os.WriteFile(file, []byte("package p\n\nimport \"net/http\"\n\nfunc DoThing() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "func DoThing() {}", "func DoThing() {\n\thttp.Get(\"x\")\n}", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("a go.work abstain must not ask, got: %s", d.Hook.PermissionReason)
	}
	if !strings.Contains(d.Hook.AdditionalContext, "could not run to completion") {
		t.Errorf("strict mode should surface the deps-go go.work abstain as an advisory, got context %q", d.Hook.AdditionalContext)
	}
	if rec := readLastDecisionLog(t); rec["reason"] != "check-degraded" {
		t.Errorf("decision log reason = %v, want check-degraded — the SAME reason string as any other "+
			"degraded check, proving decisionRecord.Reason's frozen format held", rec["reason"])
	}
}

// TestFileScope_NewFileWrite_NotDegraded is a code-review regression: a Write
// that creates a brand-new file makes readFileLines(filePath) return nil (the
// file doesn't exist yet), which is the SAME nil FileScopeViolationsWithReason
// sees for a genuinely oversized/unreadable file — both collapsed to
// "oversized-pre-edit-file" before this fix. Under RUNECHO_GUARD_STRICT=1
// that spuriously surfaced "coverage was incomplete" for an ordinary new-file
// Write, even though a nonexistent file's pre-edit state ("") is fully known,
// not degraded — the same distinction wholeFileText (duplicate.go) already
// makes for the deletion-side checks via os.IsNotExist.
func TestFileScope_NewFileWrite_NotDegraded(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_FILESCOPE", "1")
	t.Setenv("RUNECHO_GUARD_STRICT", "1")

	// brand_new.py does not exist on disk before this edit — that's the point.
	file := filepath.Join(top, "brand_new.py")
	in := payloadOld(t, "Write", file, "", "", "def go():\n    pass\n", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("a brand-new file must not ask, got: %s", d.Hook.PermissionReason)
	}
	if strings.Contains(d.Hook.AdditionalContext, "could not run to completion") {
		t.Errorf("a brand-new file was reported as degraded coverage, got context %q — "+
			"its pre-edit state (nonexistent) is fully known, not unknown", d.Hook.AdditionalContext)
	}
	if rec := readLastDecisionLog(t); rec != nil && rec["reason"] == "check-degraded" {
		t.Errorf("decision log reason = check-degraded for a brand-new file, want something else")
	}
}

// TestCountDegradedUnknown pins #359's class split: every Unknown is recorded
// in decisions.jsonl, but only the degraded ones — missing input or a failed
// query, not a check declining one candidate on its own gate — raise the
// strict-mode "coverage was incomplete" advisory.
func TestCountDegradedUnknown(t *testing.T) {
	results := []CheckResult{
		{Check: "violations", Verdict: VerdictOK},
		{Check: "dangling", Verdict: VerdictUnknown, Reason: "store-query-failed"},
		{Check: "var-type", Verdict: VerdictUnknown, Reason: "name-known-elsewhere"},
		{Check: "qualified", Verdict: VerdictUnknown, Reason: "unexported-selector"},
		{Check: "recv-method", Verdict: VerdictUnknown, Reason: "no-indexed-members"},
		{Check: "file-scope", Verdict: VerdictUnknown, Reason: "star-import"},
		{Check: "lint", Verdict: VerdictSkipped, Reason: "not-python"},
	}
	// checkStatusMap is what records the abstains; every one of the five here
	// persists as "unknown" regardless of class.
	if got := len(checkStatusMap(results)); got != len(results) {
		t.Errorf("checkStatusMap kept %d of %d results — every abstain is still recorded", got, len(results))
	}
	if got := countDegradedUnknown(results); got != 2 {
		t.Errorf("countDegradedUnknown = %d, want 2 (store-query-failed, star-import)", got)
	}
	// An unregistered reason counts as degraded: a new gate reason that forgets
	// to register itself must fail loudly (an advisory that fires too often),
	// never by silently dropping coverage from the count.
	novel := []CheckResult{{Check: "x", Verdict: VerdictUnknown, Reason: "brand-new-reason"}}
	if got := countDegradedUnknown(novel); got != 1 {
		t.Errorf("countDegradedUnknown(unregistered reason) = %d, want 1", got)
	}
	// A hand-built Unknown with no reason (runHookMode's oversized-pre-edit-file
	// arms) is degraded by nature.
	bare := []CheckResult{{Check: "x", Verdict: VerdictUnknown}}
	if got := countDegradedUnknown(bare); got != 1 {
		t.Errorf("countDegradedUnknown(no reason) = %d, want 1", got)
	}
	if got := countDegradedUnknown(nil); got != 0 {
		t.Errorf("countDegradedUnknown(nil) = %d, want 0", got)
	}
}

// TestCheckReasonMap pins the projection behind decisionRecord.CheckReasons:
// only checks that recorded a reason appear, and no reasons at all yields nil
// so omitempty drops the field entirely.
func TestCheckReasonMap(t *testing.T) {
	results := []CheckResult{
		{Check: "violations", Verdict: VerdictOK},
		{Check: "qualified", Verdict: VerdictSkipped, Reason: "no-module-path"},
		{Check: "var-type", Verdict: VerdictUnknown, Reason: "name-known-elsewhere"},
		{Check: "dangling", Verdict: VerdictViolation},
	}
	got := checkReasonMap(results)
	want := map[string]string{
		"qualified": "no-module-path",
		"var-type":  "name-known-elsewhere",
	}
	if len(got) != len(want) {
		t.Fatalf("checkReasonMap = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("checkReasonMap[%q] = %q, want %q", k, got[k], v)
		}
	}
	if checkReasonMap([]CheckResult{{Check: "violations", Verdict: VerdictOK}}) != nil {
		t.Error("checkReasonMap with no reasons must return nil, not an empty map")
	}
	if checkReasonMap(nil) != nil {
		t.Error("checkReasonMap(nil) must return nil")
	}
}

// TestFoldAbstainReason pins the precedence: the shared degraded pre-edit
// reason outranks a check's own gate reason, so a genuinely degraded edit still
// raises the strict advisory instead of being masked by a routine gate.
func TestFoldAbstainReason(t *testing.T) {
	if got := foldAbstainReason("name-known-elsewhere", "oversized-pre-edit-file"); got != "oversized-pre-edit-file" {
		t.Errorf("both set: got %q, want the degraded reason", got)
	}
	if got := foldAbstainReason("name-known-elsewhere", ""); got != "name-known-elsewhere" {
		t.Errorf("own only: got %q", got)
	}
	if got := foldAbstainReason("", "oversized-pre-edit-file"); got != "oversized-pre-edit-file" {
		t.Errorf("degraded only: got %q", got)
	}
	if got := foldAbstainReason("", ""); got != "" {
		t.Errorf("neither: got %q, want empty", got)
	}
}

// TestGateAbstainReasonsAreNotVerdictTokens guards a collision that would be
// silent: a reason string that happens to equal one of Verdict.String()'s four
// tokens would read as a verdict in any jq that greps both maps.
func TestGateAbstainReasonsAreNotVerdictTokens(t *testing.T) {
	tokens := map[string]struct{}{"ok": {}, "violation": {}, "unknown": {}, "skipped": {}}
	for reason := range gateAbstainReasons {
		if _, clash := tokens[reason]; clash {
			t.Errorf("gate reason %q collides with a verdict token", reason)
		}
	}
}
