package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inth3shadows/runecho/internal/guardstats"
)

// writeAskEntryAt writes an ask record with an explicit timestamp and edit
// fingerprint, for tests that need to simulate an ask older than
// maxOutcomeAge (5 min) — the exact case #300 exists to fix.
func writeAskEntryAt(t *testing.T, file string, ts time.Time, editHash string, symbols []string) {
	t.Helper()
	logDecision(decisionRecord{
		TS:       ts.UTC().Format(time.RFC3339),
		Mode:     "hook",
		Repo:     "r",
		File:     file,
		Lang:     "go",
		Decision: "ask",
		Reason:   "violations",
		Symbols:  symbols,
		Edit:     editHash,
	})
}

// outcomeJSONWithEdit returns a PostToolUse-format payload whose tool_input
// carries real edit content, so runOutcomeMode computes a non-empty edit
// fingerprint the same way runHookMode would have for the matching ask.
func outcomeJSONWithEdit(t *testing.T, filePath, oldString, newString string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"tool_name": "Edit",
		"tool_input": map[string]string{
			"file_path":  filePath,
			"old_string": oldString,
			"new_string": newString,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRunOutcomeMode_RecordsOutcomeForAskOlderThanFiveMinutes is the #300
// regression pin: before the fingerprint join, an ask older than
// maxOutcomeAge (5 min) never got its outcome recorded at all, no matter how
// long the human actually took to approve it.
func TestRunOutcomeMode_RecordsOutcomeForAskOlderThanFiveMinutes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	file := "/some/repo/main.go"
	edit := hookEdit{ToolName: "Edit", OldString: "foo", NewString: "bar"}
	writeAskEntryAt(t, file, time.Now().Add(-2*time.Hour), editFingerprint(edit), []string{"bar"})

	if code := runOutcomeMode(strings.NewReader(outcomeJSONWithEdit(t, file, "foo", "bar"))); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	rec := readLastDecisionLog(t)
	if rec == nil || rec["decision"] != "outcome" {
		t.Fatalf("expected an outcome record for a 2h-old ask, got %v", rec)
	}
	if got, _ := rec["join"].(string); got != "edit" {
		t.Errorf("join = %q, want %q", got, "edit")
	}
}

// TestRecentUnrecordedAsk_FingerprintBeatsWindow: an old ask carrying the
// searched-for fingerprint must win over a newer, in-window ask for the same
// file that does NOT carry it — the hash track is precise, the window track
// is a guess, and the guess must never override a precise match.
func TestRecentUnrecordedAsk_FingerprintBeatsWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	file := "/some/repo/main.go"
	writeAskEntryAt(t, file, time.Now().Add(-10*time.Minute), "target-hash", []string{"Old"})
	writeAskEntryAt(t, file, time.Now().Add(-1*time.Minute), "other-hash", []string{"New"})

	rec, join, ok := recentUnrecordedAsk(filepath.Join(home, "decisions.jsonl"), file, "target-hash")
	if !ok {
		t.Fatal("expected a match via the fingerprint track")
	}
	if join != "edit" {
		t.Errorf("join = %q, want %q", join, "edit")
	}
	if len(rec.Symbols) != 1 || rec.Symbols[0] != "Old" {
		t.Errorf("symbols = %v, want the OLD ask's [Old], not the newer in-window ask", rec.Symbols)
	}
}

// TestRecentUnrecordedAsk_LegacyAskFallsBackToWindow pins backward
// compatibility: an ask with no Edit field (written by a pre-#300 guard, or
// looked up with no editHash) must still be found via the original
// (file, maxOutcomeAge) window — and still respect that window's boundary.
func TestRecentUnrecordedAsk_LegacyAskFallsBackToWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	inWindow := "/repo/in_window.go"
	writeAskEntryAt(t, inWindow, time.Now().Add(-2*time.Minute), "", []string{"Foo"})
	_, join, ok := recentUnrecordedAsk(filepath.Join(home, "decisions.jsonl"), inWindow, "some-new-hash")
	if !ok {
		t.Fatal("legacy ask within maxOutcomeAge should still be found via the window fallback")
	}
	if join != "window" {
		t.Errorf("join = %q, want %q", join, "window")
	}

	outOfWindow := "/repo/out_of_window.go"
	writeAskEntryAt(t, outOfWindow, time.Now().Add(-10*time.Minute), "", []string{"Bar"})
	if _, _, ok := recentUnrecordedAsk(filepath.Join(home, "decisions.jsonl"), outOfWindow, "some-new-hash"); ok {
		t.Error("legacy ask beyond maxOutcomeAge must not be found — the window fallback is unchanged by #300")
	}
}

// TestRunOutcomeMode_DedupesKeyedOutcome: two PostToolUse fires for the same
// edit (the documented plugin + hand-merged settings.json double-wiring, or a
// harness retry) must still record exactly one outcome, joined via the edit
// fingerprint rather than the file+window guess.
func TestRunOutcomeMode_DedupesKeyedOutcome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	file := "/some/repo/main.go"
	edit := hookEdit{ToolName: "Edit", OldString: "old", NewString: "new"}
	writeAskEntryAt(t, file, time.Now().Add(-90*time.Minute), editFingerprint(edit), []string{"Sym"})

	before := countDecisionLogLines(t)
	payload := outcomeJSONWithEdit(t, file, "old", "new")
	if code := runOutcomeMode(strings.NewReader(payload)); code != 0 {
		t.Fatalf("first fire: exit code = %d, want 0", code)
	}
	if code := runOutcomeMode(strings.NewReader(payload)); code != 0 {
		t.Fatalf("second fire: exit code = %d, want 0", code)
	}
	after := countDecisionLogLines(t)
	if after != before+1 {
		t.Errorf("expected exactly +1 outcome record for two fires of the same edit, got +%d", after-before)
	}
}

// TestRecentUnrecordedAsk_OversizedLineDoesNotAbortScan pins the
// bufio.Scanner hazard documented in
// docs/focus-hunt-2026-07-04-candidate-bugs.md: a single line longer than
// Scanner's fixed token cap makes Scan() return false forever, silently
// truncating every record after it. Widening maxOutcomeReadBytes to 1 MiB
// makes an oversized line 16x likelier to appear in the read window, so the
// switch to bufio.Reader.ReadString must be pinned in the same change.
func TestRecentUnrecordedAsk_OversizedLineDoesNotAbortScan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	// A pre-commit-shaped ask for a DIFFERENT file, carrying a symbols array
	// well past bufio.MaxScanTokenSize (64 KiB).
	hugeSymbols := make([]string, 20000)
	for i := range hugeSymbols {
		hugeSymbols[i] = "symbol_padding_entry_number_" + strings.Repeat("x", 40)
	}
	logDecision(decisionRecord{
		Mode: "precommit", File: "/unrelated/huge.go",
		Decision: "ask", Reason: "violations", Symbols: hugeSymbols,
	})

	file := "/some/repo/main.go"
	writeAskEntryAt(t, file, time.Now().Add(-1*time.Minute), "", []string{"Foo"})

	rec, _, ok := recentUnrecordedAsk(filepath.Join(home, "decisions.jsonl"), file, "")
	if !ok {
		t.Fatal("the real ask must still be found despite a preceding oversized line")
	}
	if len(rec.Symbols) != 1 || rec.Symbols[0] != "Foo" {
		t.Errorf("symbols = %v, want [Foo]", rec.Symbols)
	}
}

// TestRecentUnrecordedAsk_UnattributedOutcomeDoesNotCloseADifferentHash pins a
// bug found by review: the hash track used to accept ANY same-file outcome
// with an empty Edit as proof the currently hash-matched ask was answered.
// With two different-hash asks for the same file, an unrelated (or
// legacy/unattributable) empty-Edit outcome would falsely close whichever ask
// the hash track had matched — silently dropping its real, later approval.
func TestRecentUnrecordedAsk_UnattributedOutcomeDoesNotCloseADifferentHash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	file := "/some/repo/main.go"
	writeAskEntryAt(t, file, time.Now().Add(-30*time.Minute), "hash-a", []string{"A"})
	writeAskEntryAt(t, file, time.Now().Add(-20*time.Minute), "hash-b", []string{"B"})
	// An unattributed outcome (no Edit) for this file — e.g. a legacy guard, or
	// genuinely unrelated to either ask above.
	logDecision(decisionRecord{
		TS:   time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
		Mode: "hook", Repo: "r", File: file,
		Decision: "outcome", Reason: "approved",
	})

	rec, join, ok := recentUnrecordedAsk(filepath.Join(home, "decisions.jsonl"), file, "hash-b")
	if !ok {
		t.Fatal("ask B must still be findable — an unattributed outcome must not falsely close it")
	}
	if join != "edit" {
		t.Errorf("join = %q, want %q", join, "edit")
	}
	if len(rec.Symbols) != 1 || rec.Symbols[0] != "B" {
		t.Errorf("symbols = %v, want [B]", rec.Symbols)
	}
}

// TestDeclogWindowConstants_StayInSyncWithGuardstats pins the cross-package
// invariant the #300 fix depends on: fpreport joins on
// guardstats.KeyedOutcomeJoinWindow, the guard writes outcomes bounded by
// maxKeyedOutcomeAge, and if the two ever drift, fpreport would silently stop
// counting exactly the late approvals #300 exists to recover.
func TestDeclogWindowConstants_StayInSyncWithGuardstats(t *testing.T) {
	if maxKeyedOutcomeAge != guardstats.KeyedOutcomeJoinWindow {
		t.Errorf("maxKeyedOutcomeAge = %v, guardstats.KeyedOutcomeJoinWindow = %v — must match",
			maxKeyedOutcomeAge, guardstats.KeyedOutcomeJoinWindow)
	}
}
