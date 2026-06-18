package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// storePath returns the learned-allow.json path inside the current RUNECHO_HOME.
func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("RUNECHO_HOME"), learnedAllowFile)
}

// --- store unit tests (no DB) ---------------------------------------------

func TestRecordApprovals_NoOpWhenDisabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_GUARD_LEARN", "") // isolate from inherited session env

	recordApprovals(home, "r", []string{"Foo"}, time.Now())

	if _, err := os.Stat(storePath(t)); !os.IsNotExist(err) {
		t.Fatalf("learned-allow.json should NOT exist while gate is off (err=%v)", err)
	}
	if set := learnedAllowedSet(home, "r", time.Now()); len(set) != 0 {
		t.Fatalf("learnedAllowedSet should be empty while gate off, got %v", set)
	}
}

func TestLearnedAllow_PromotesAtThreshold(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_GUARD_LEARN", "1") // default N=2

	now := time.Now()
	recordApprovals(home, "r", []string{"Foo"}, now)
	if _, ok := learnedAllowedSet(home, "r", now)["Foo"]; ok {
		t.Fatal("after 1 approval (N=2) Foo must NOT be allowed yet")
	}
	recordApprovals(home, "r", []string{"Foo"}, now)
	if _, ok := learnedAllowedSet(home, "r", now)["Foo"]; !ok {
		t.Fatal("after 2 approvals (N=2) Foo must be allowed")
	}
}

func TestLearnedAllow_ThresholdEnvOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_GUARD_LEARN", "1")
	t.Setenv("RUNECHO_GUARD_LEARN_N", "3")

	now := time.Now()
	recordApprovals(home, "r", []string{"Foo"}, now)
	recordApprovals(home, "r", []string{"Foo"}, now)
	if _, ok := learnedAllowedSet(home, "r", now)["Foo"]; ok {
		t.Fatal("after 2 approvals (N=3) Foo must NOT be allowed")
	}
	recordApprovals(home, "r", []string{"Foo"}, now)
	if _, ok := learnedAllowedSet(home, "r", now)["Foo"]; !ok {
		t.Fatal("after 3 approvals (N=3) Foo must be allowed")
	}
}

func TestLearnedAllow_DecayExcludesAndPrunes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_GUARD_LEARN", "1") // default TTL=14d

	now := time.Now()
	old := now.Add(-20 * 24 * time.Hour)

	// Seed a well-approved but STALE entry directly.
	la := learnedAllow{V: 1, Repos: map[string]map[string]learnedEntry{
		"r": {"Stale": {Count: 5, LastSeen: old.UTC().Format(time.RFC3339)}},
	}}
	saveLearnedAllow(home, la)

	// Read path filters it out without writing.
	if _, ok := learnedAllowedSet(home, "r", now)["Stale"]; ok {
		t.Fatal("stale entry (20d > 14d TTL) must be filtered from allowed set")
	}

	// A fresh approval of a DIFFERENT symbol triggers prune-on-write, removing Stale.
	recordApprovals(home, "r", []string{"Fresh"}, now)
	reloaded := loadLearnedAllow(home)
	if _, exists := reloaded.Repos["r"]["Stale"]; exists {
		t.Fatal("stale entry must be physically pruned on the next write")
	}
	if reloaded.Repos["r"]["Fresh"].Count != 1 {
		t.Fatalf("fresh entry count = %d, want 1", reloaded.Repos["r"]["Fresh"].Count)
	}
}

func TestLearnedAllow_TTLEnvOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_GUARD_LEARN", "1")
	t.Setenv("RUNECHO_GUARD_LEARN_TTL_DAYS", "1")

	now := time.Now()
	la := learnedAllow{V: 1, Repos: map[string]map[string]learnedEntry{
		"r": {"Foo": {Count: 5, LastSeen: now.Add(-2 * 24 * time.Hour).UTC().Format(time.RFC3339)}},
	}}
	saveLearnedAllow(home, la)

	if _, ok := learnedAllowedSet(home, "r", now)["Foo"]; ok {
		t.Fatal("entry 2d old with TTL=1d must be excluded")
	}
}

func TestLearnedAllow_FailOpenOnCorruptStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_GUARD_LEARN", "1")

	if err := os.WriteFile(storePath(t), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Must not panic; corrupt store reads as empty.
	if set := learnedAllowedSet(home, "r", time.Now()); len(set) != 0 {
		t.Fatalf("corrupt store should yield empty set, got %v", set)
	}
	// A subsequent write recovers the store rather than erroring.
	recordApprovals(home, "r", []string{"Foo"}, time.Now())
	if loadLearnedAllow(home).Repos["r"]["Foo"].Count != 1 {
		t.Fatal("recordApprovals should overwrite a corrupt store")
	}
}

func TestLearnedAllow_PerRepoIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_GUARD_LEARN", "1")

	now := time.Now()
	recordApprovals(home, "repoA", []string{"Foo"}, now)
	recordApprovals(home, "repoA", []string{"Foo"}, now)

	if _, ok := learnedAllowedSet(home, "repoA", now)["Foo"]; !ok {
		t.Fatal("Foo should be allowed in repoA")
	}
	if _, ok := learnedAllowedSet(home, "repoB", now)["Foo"]; ok {
		t.Fatal("Foo must NOT leak into repoB")
	}
}

// --- enrich correlation ----------------------------------------------------

func TestRecentAsk_ReturnsSymbolsAndRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	file := "/some/repo/main.go"
	logDecision(decisionRecord{
		Mode:     "hook",
		Repo:     "r",
		File:     file,
		Lang:     "go",
		Decision: "ask",
		Reason:   "violations",
		Symbols:  []string{"Foo", "Bar"},
	})

	rec, ok := recentAsk(filepath.Join(home, "decisions.jsonl"), file)
	if !ok {
		t.Fatal("recentAsk should find the ask record")
	}
	if rec.Repo != "r" {
		t.Errorf("repo = %q, want %q", rec.Repo, "r")
	}
	if strings.Join(rec.Symbols, ",") != "Foo,Bar" {
		t.Errorf("symbols = %v, want [Foo Bar]", rec.Symbols)
	}
}

func TestLogOutcomeForFile_EnrichesAndRecords(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_GUARD_LEARN", "1")

	file := "/some/repo/main.go"
	logDecision(decisionRecord{
		Mode: "hook", Repo: "r", File: file, Lang: "go",
		Decision: "ask", Reason: "violations", Symbols: []string{"Ghost"},
	})

	logOutcomeForFile(file)

	// Outcome row carries symbols + repo forwarded from the ask.
	rec := readLastDecisionLog(t)
	if rec == nil || rec["decision"] != "outcome" {
		t.Fatalf("expected an outcome record, got %v", rec)
	}
	syms, _ := rec["symbols"].([]any)
	if len(syms) != 1 || syms[0] != "Ghost" {
		t.Errorf("outcome symbols = %v, want [Ghost]", rec["symbols"])
	}
	// And the approval is folded into the learned-allow store.
	if loadLearnedAllow(home).Repos["r"]["Ghost"].Count != 1 {
		t.Error("logOutcomeForFile should record the approval in the learned-allow store")
	}
}

// --- hot-path integration --------------------------------------------------

func TestRunHookMode_LearnedAllow_SuppressesAsk(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"}) // sets RUNECHO_HOME, repo name "r"
	home := os.Getenv("RUNECHO_HOME")
	t.Setenv("RUNECHO_GUARD_LEARN", "1")

	goFile := filepath.Join(repoRoot, "main.go")
	now := time.Now()
	// Approve HallucinatedFunc to threshold (N=2) for repo "r".
	recordApprovals(home, "r", []string{"HallucinatedFunc"}, now)
	recordApprovals(home, "r", []string{"HallucinatedFunc"}, now)

	_, _, d := runHook(t, payload(t, "Edit", goFile, "y := HallucinatedFunc()", "", nil))
	if d.Hook.PermissionDec == "ask" {
		t.Fatal("learned-allowed symbol must NOT trigger an ask")
	}
	if rec := readLastDecisionLog(t); rec != nil && rec["reason"] == "violations" {
		t.Fatalf("expected no violations once learned-allowed, got %v", rec)
	}
}

func TestRunHookMode_LearnedAllow_DisabledStillAsks(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	home := os.Getenv("RUNECHO_HOME")

	goFile := filepath.Join(repoRoot, "main.go")
	now := time.Now()
	// Seed the store (needs the gate on), then turn the gate OFF for the edit.
	t.Setenv("RUNECHO_GUARD_LEARN", "1")
	recordApprovals(home, "r", []string{"HallucinatedFunc"}, now)
	recordApprovals(home, "r", []string{"HallucinatedFunc"}, now)
	t.Setenv("RUNECHO_GUARD_LEARN", "")

	_, _, d := runHook(t, payload(t, "Edit", goFile, "y := HallucinatedFunc()", "", nil))
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("with gate OFF the guard must still ask, got decision %q", d.Hook.PermissionDec)
	}
}
