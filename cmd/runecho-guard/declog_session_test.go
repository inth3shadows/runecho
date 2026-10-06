package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hookSessionJSON is a PreToolUse payload for a file the guard defers on
// (unknown-lang), so the record under test is written without a store.
func hookSessionJSON(session string) string {
	s := `{"tool_name":"Write","tool_input":{"file_path":"/some/repo/notes.md","content":"x"}`
	if session != "" {
		s += `,"session_id":"` + session + `"`
	}
	return s + `}`
}

// isolateDecisionSession gives a test the fresh-process state (no session ever
// set) and clears it again afterwards. decisionSession is process-wide, so
// without this a session-carrying test leaves its tag behind for whichever test
// logs next without going through a hook entry point.
func isolateDecisionSession(t *testing.T) {
	t.Helper()
	decisionSession.Store(nil)
	t.Cleanup(func() { decisionSession.Store(nil) })
}

func TestLogDecision_StampsHashedSession(t *testing.T) {
	isolateDecisionSession(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	const session = "11111111-2222-3333-4444-555555555555"
	runHook(t, hookSessionJSON(session))
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("no record written")
	}
	got, _ := rec["session"].(string)
	if want := contractSessionTag(session); got != want {
		t.Errorf("session = %q, want %q", got, want)
	}
	if strings.Contains(got, session) || got == session {
		t.Errorf("session %q carries the raw session id", got)
	}
}

// A hook process serves one tool call, but tests (and any future long-lived
// caller) run several in one process: a record must never carry the session of
// an earlier payload.
func TestLogDecision_SessionDoesNotLeakAcrossRuns(t *testing.T) {
	isolateDecisionSession(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	runHook(t, hookSessionJSON("session-a"))

	runHook(t, hookSessionJSON(""))
	if rec := readLastDecisionLog(t); rec == nil {
		t.Fatal("no record written")
	} else if _, ok := rec["session"]; ok {
		t.Errorf("payload without session_id wrote session = %v", rec["session"])
	}

	runHook(t, hookSessionJSON("session-a"))
	runHook(t, `{not json`)
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("no record written")
	}
	if got, _ := rec["reason"].(string); got != "parse-fail" {
		t.Fatalf("reason = %q, want parse-fail", got)
	}
	if _, ok := rec["session"]; ok {
		t.Errorf("parse-fail record inherited session = %v", rec["session"])
	}
}

func TestRunOutcomeMode_StampsSession(t *testing.T) {
	isolateDecisionSession(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	const file, session = "/some/repo/main.go", "session-b"
	writeAskEntry(t, file)
	payload := `{"tool_name":"Edit","session_id":"` + session + `","tool_input":{"file_path":"` + file + `"}}`
	if code := runOutcomeMode(strings.NewReader(payload)); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("no record written")
	}
	if got, _ := rec["decision"].(string); got != "outcome" {
		t.Fatalf("decision = %q, want outcome", got)
	}
	if got, want := rec["session"], contractSessionTag(session); got != want {
		t.Errorf("session = %v, want %q", got, want)
	}
}

// Pre-commit reads no hook payload, so its records carry no session. Driven
// through runArgs, so a session set in runArgs or in the panic barrier is
// caught. NOT covered: runPreCommit itself is stubbed out here, so a session
// set inside it, and the ask records it writes, are unpinned. The panic record
// is used because it is the pre-commit record that needs no enrolled repo to
// produce.
func TestRunArgs_PreCommitRecordHasNoSession(t *testing.T) {
	isolateDecisionSession(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")
	orig := runPreCommitBody
	t.Cleanup(func() { runPreCommitBody = orig })
	runPreCommitBody = func(bool, bool) int { panic("boom") }

	if got := runArgs(nil); got != 0 {
		t.Fatalf("runArgs = %d, want 0", got)
	}
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("no record written")
	}
	if got, _ := rec["mode"].(string); got != "precommit" {
		t.Fatalf("mode = %q, want precommit", got)
	}
	if _, ok := rec["session"]; ok {
		t.Errorf("pre-commit record carries session = %v", rec["session"])
	}
}

// The tag is contractSessionTag, shared with the #209 contract memo. Pinned to
// a literal so a change to that hash made for the memo's sake cannot re-key
// `session` on every record without a test failing.
func TestDecisionSessionTag_IsPinned(t *testing.T) {
	const session, want = "11111111-2222-3333-4444-555555555555", "666ff6ccaa5b"
	if got := contractSessionTag(session); got != want {
		t.Errorf("contractSessionTag(%q) = %q, want %q", session, got, want)
	}
}

// blockForever is a reader whose Read never returns: a hook body stalled before
// its payload is decoded.
type blockForever struct{}

func (blockForever) Read([]byte) (int, error) { select {} }

// A timeout that fires while the payload is still being read has no session to
// report, even when this process served another session's payload first.
func TestDeferOnPanic_TimeoutBeforeDecodeHasNoSession(t *testing.T) {
	isolateDecisionSession(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")
	old := guardTimeout
	guardTimeout = 300 * time.Millisecond
	t.Cleanup(func() { guardTimeout = old })

	setDecisionSession("session-a")
	var out bytes.Buffer
	deferOnPanic("hook-mode", &out, func(w io.Writer) int {
		return runHookMode(blockForever{}, w)
	})
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("no record written")
	}
	if got, _ := rec["reason"].(string); got != "timeout" {
		t.Fatalf("reason = %q, want timeout", got)
	}
	if _, ok := rec["session"]; ok {
		t.Errorf("pre-decode timeout record carries session = %v", rec["session"])
	}
}

// windowOutcome sends a PostToolUse for the edit x -> y. An askAs ask carries no
// fingerprint, so against those the join can only take the window track; an
// ask seeded with askWithEdit and that edit's fingerprint joins on the edit
// track.
func windowOutcome(t *testing.T, file, session string) {
	t.Helper()
	payload := `{"tool_name":"Edit","session_id":"` + session + `","tool_input":{"file_path":"` + file + `","old_string":"x","new_string":"y"}}`
	if code := runOutcomeMode(strings.NewReader(payload)); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// askAs logs an ask on file as session would (session "" writes none, like a
// guard older than #458), with NO edit fingerprint, like a guard older than
// #300. The window track is the only way such an ask can be joined, and since
// #461 the only kind of ask a fingerprinted outcome can window-join.
func askAs(t *testing.T, session, file string, symbols ...string) {
	t.Helper()
	askWithEdit(t, session, file, "", symbols...)
}

func askWithEdit(t *testing.T, session, file, editHash string, symbols ...string) {
	t.Helper()
	setDecisionSession(session)
	logDecision(decisionRecord{
		Mode: "hook", Repo: "r", File: file, Lang: "go", Decision: "ask",
		Reason: "violations", Symbols: symbols, LearnSymbols: symbols, Edit: editHash,
	})
}

// outcomesFor counts the outcome records logged for file.
func outcomesFor(t *testing.T, home, file string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "decisions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec decisionRecord
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Decision == "outcome" && rec.File == file {
			n++
		}
	}
	return n
}

// #459: another session's unrelated edit must not be recorded as the approval
// of this ask, and must not train learned-allow on it.
func TestRunOutcomeMode_WindowJoinDoesNotCrossSessions(t *testing.T) {
	isolateDecisionSession(t)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_DEBUG", "")
	t.Setenv("RUNECHO_GUARD_LEARN", "1")

	const file = "/some/repo/main.go"
	askAs(t, "session-a", file, "Ghost")
	before := countDecisionLogLines(t)
	windowOutcome(t, file, "session-b")

	if after := countDecisionLogLines(t); after != before {
		t.Errorf("log grew by %d record(s); want none: %v", after-before, readLastDecisionLog(t))
	}
	if la := loadLearnedAllow(home); len(la.Repos["r"]) != 0 {
		t.Errorf("learned-allow trained across sessions: %v", la.Repos["r"])
	}
}

// The filter must not cost the window track what it exists for: the same
// session's non-matching edit still joins, and so does a pair where either
// side has no session.
func TestRunOutcomeMode_WindowJoinWithinSessionAndLegacy(t *testing.T) {
	for _, tc := range []struct{ name, askSession, outcomeSession string }{
		{"same session", "session-a", "session-a"},
		{"ask without session", "", "session-a"},
		{"outcome without session", "session-a", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateDecisionSession(t)
			home := t.TempDir()
			t.Setenv("RUNECHO_HOME", home)
			t.Setenv("RUNECHO_DEBUG", "")
			t.Setenv("RUNECHO_GUARD_LEARN", "1")

			const file = "/some/repo/main.go"
			askAs(t, tc.askSession, file, "Ghost")
			windowOutcome(t, file, tc.outcomeSession)

			rec := readLastDecisionLog(t)
			if got, _ := rec["decision"].(string); got != "outcome" {
				t.Fatalf("decision = %q, want outcome", got)
			}
			if got, _ := rec["join"].(string); got != "window" {
				t.Errorf("join = %q, want window", got)
			}
			if _, ok := loadLearnedAllow(home).Repos["r"]["Ghost"]; !ok {
				t.Errorf("learned-allow has no entry for the approved symbol")
			}
		})
	}
}

// An ask with no session matches every session, so the ask filter alone cannot
// stop a second session joining it. What stops it is that ANY outcome on the
// file closes the ask: outcomes are not filtered by session. Filtering them
// approved this one ask once per session.
func TestRunOutcomeMode_SessionlessAskIsApprovedOnce(t *testing.T) {
	isolateDecisionSession(t)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_DEBUG", "")
	t.Setenv("RUNECHO_GUARD_LEARN", "1")

	const file = "/some/repo/main.go"
	askAs(t, "", file, "Ghost")
	windowOutcome(t, file, "session-a")
	windowOutcome(t, file, "session-b")

	if n := outcomesFor(t, home, file); n != 1 {
		t.Errorf("outcomes = %d, want 1", n)
	}
	if got := loadLearnedAllow(home).Repos["r"]["Ghost"].Count; got != 1 {
		t.Errorf("learned-allow count = %d, want 1", got)
	}
}

// The fingerprint track does not consult a session: another session's
// byte-identical edit joins this ask (join "edit"), and the outcome carries
// that other session. Pins that neither half of the fingerprint track filters
// on session: the cross-session join happens, and a repeat of the identical
// edit is deduped against it. The last step, the asker's different edit, is
// refused twice over since #461 (the fingerprints differ), so it no longer
// tests that an outcome closes the ask for the window track;
// TestRunOutcomeMode_SessionlessAskIsApprovedOnce does.
func TestRunOutcomeMode_FingerprintOutcomeFromOtherSessionClosesAsk(t *testing.T) {
	isolateDecisionSession(t)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_DEBUG", "")
	t.Setenv("RUNECHO_GUARD_LEARN", "1")

	const file = "/some/repo/main.go"
	fp := editFingerprint(hookEdit{ToolName: "Edit", OldString: "x", NewString: "y"})
	askWithEdit(t, "session-a", file, fp, "Ghost")

	windowOutcome(t, file, "session-b") // the identical x -> y edit
	rec := readLastDecisionLog(t)
	if rec["decision"] != "outcome" || rec["join"] != "edit" || rec["session"] != contractSessionTag("session-b") {
		t.Fatalf("identical edit from another session: want an edit-joined outcome tagged session-b, got %v", rec)
	}

	// A second fire of the same edit, from the asking session, is a duplicate
	// of an outcome already recorded for this fingerprint (#300 dedupe).
	windowOutcome(t, file, "session-a")
	if n := outcomesFor(t, home, file); n != 1 {
		t.Fatalf("after a repeat of the identical edit: outcomes = %d, want 1", n)
	}

	other := `{"tool_name":"Edit","session_id":"session-a","tool_input":{"file_path":"` + file + `","old_string":"m","new_string":"n"}}`
	if code := runOutcomeMode(strings.NewReader(other)); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if n := outcomesFor(t, home, file); n != 1 {
		t.Errorf("outcomes = %d, want 1", n)
	}
	if got := loadLearnedAllow(home).Repos["r"]["Ghost"].Count; got != 1 {
		t.Errorf("learned-allow count = %d, want 1", got)
	}
}

// A skipped foreign ask must leave the window track's state alone. If it reset
// the "already recorded" flag, as an admitted ask does, this session's ask
// would be approved again by its next edit.
func TestRunOutcomeMode_ForeignAskDoesNotReopenRecordedAsk(t *testing.T) {
	isolateDecisionSession(t)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_DEBUG", "")
	t.Setenv("RUNECHO_GUARD_LEARN", "1")

	const file = "/some/repo/main.go"
	askAs(t, "session-a", file, "GhostA")
	windowOutcome(t, file, "session-a")
	askAs(t, "session-b", file, "GhostB")
	windowOutcome(t, file, "session-a")

	if n := outcomesFor(t, home, file); n != 1 {
		t.Errorf("outcomes = %d, want 1", n)
	}
	if got := loadLearnedAllow(home).Repos["r"]["GhostA"].Count; got != 1 {
		t.Errorf("learned-allow count for GhostA = %d, want 1", got)
	}
}

// xyFingerprint is the fingerprint of windowOutcome's edit.
func xyFingerprint() string {
	return editFingerprint(hookEdit{ToolName: "Edit", OldString: "x", NewString: "y"})
}

// otherOutcome sends a PostToolUse for the edit m -> n: a different edit from
// windowOutcome's, with its own fingerprint.
func otherOutcome(t *testing.T, file, session string) {
	t.Helper()
	payload := `{"tool_name":"Edit","session_id":"` + session + `","tool_input":{"file_path":"` + file + `","old_string":"m","new_string":"n"}}`
	if code := runOutcomeMode(strings.NewReader(payload)); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// #461: when the ask and the outcome both carry a fingerprint and they differ,
// the outcome is a different edit. It must not be recorded as approving the
// ask, and the asked edit, when it does run, must be the ask's only approval.
func TestRunOutcomeMode_DifferentEditDoesNotApproveFingerprintedAsk(t *testing.T) {
	isolateDecisionSession(t)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_DEBUG", "")
	t.Setenv("RUNECHO_GUARD_LEARN", "1")

	const file = "/some/repo/main.go"
	askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")

	otherOutcome(t, file, "session-a")
	if n := outcomesFor(t, home, file); n != 0 {
		t.Fatalf("a different edit was recorded as an approval: outcomes = %d, want 0", n)
	}
	if got := loadLearnedAllow(home).Repos["r"]["Ghost"].Count; got != 0 {
		t.Fatalf("learned-allow trained by a different edit: count = %d, want 0", got)
	}

	windowOutcome(t, file, "session-a") // the asked edit itself
	if rec := readLastDecisionLog(t); rec["decision"] != "outcome" || rec["join"] != "edit" {
		t.Fatalf("the asked edit: want an edit-joined outcome, got %v", rec)
	}
	if n := outcomesFor(t, home, file); n != 1 {
		t.Errorf("outcomes = %d, want 1", n)
	}
	if got := loadLearnedAllow(home).Repos["r"]["Ghost"].Count; got != 1 {
		t.Errorf("learned-allow count = %d, want 1", got)
	}
}

// The window track is still the join when a fingerprint comparison is
// impossible because the OUTCOME has none (a payload with no tool_name): the
// fingerprinted ask is joined by window, as before #461.
func TestRunOutcomeMode_OutcomeWithoutFingerprintStillWindowJoins(t *testing.T) {
	isolateDecisionSession(t)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_DEBUG", "")

	const file = "/some/repo/main.go"
	askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
	noFingerprintOutcome(t, file, "session-a")
	rec := readLastDecisionLog(t)
	if rec["decision"] != "outcome" || rec["join"] != "window" {
		t.Fatalf("want a window-joined outcome, got %v", rec)
	}
	if _, has := rec["edit"]; has {
		t.Errorf("precondition: the outcome was meant to carry no fingerprint, got %v", rec["edit"])
	}
}

// noFingerprintOutcome sends a PostToolUse whose payload has no tool_name, so
// it carries no edit fingerprint and can only be joined by window.
func noFingerprintOutcome(t *testing.T, file, session string) {
	t.Helper()
	payload := `{"session_id":"` + session + `","tool_input":{"file_path":"` + file + `"}}`
	if code := runOutcomeMode(strings.NewReader(payload)); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// The one window path a current guard's ask can still take is an outcome with
// no fingerprint. The #459 session filter must hold on it.
func TestRunOutcomeMode_NoFingerprintOutcomeDoesNotCrossSessions(t *testing.T) {
	isolateDecisionSession(t)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_DEBUG", "")

	const file = "/some/repo/main.go"
	askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
	noFingerprintOutcome(t, file, "session-b")
	if n := outcomesFor(t, home, file); n != 0 {
		t.Errorf("outcomes = %d, want 0: %v", n, readLastDecisionLog(t))
	}
}

// An ask the window track passes over for its fingerprint must leave the
// track's state alone, exactly as a foreign-session ask must. If it reset the
// "already recorded" flag, the older fingerprint-less ask would be approved a
// second time by the next different edit.
func TestRunOutcomeMode_SkippedFingerprintedAskDoesNotReopenRecordedAsk(t *testing.T) {
	isolateDecisionSession(t)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_DEBUG", "")
	t.Setenv("RUNECHO_GUARD_LEARN", "1")

	const file = "/some/repo/main.go"
	askAs(t, "session-a", file, "Legacy")
	windowOutcome(t, file, "session-a")
	askWithEdit(t, "session-a", file, "gggggggggggg", "Current")
	otherOutcome(t, file, "session-a")

	if n := outcomesFor(t, home, file); n != 1 {
		t.Errorf("outcomes = %d, want 1", n)
	}
	if got := loadLearnedAllow(home).Repos["r"]["Legacy"].Count; got != 1 {
		t.Errorf("learned-allow count for Legacy = %d, want 1", got)
	}
}

// A fingerprinted ask the window track passes over does not hide an older ask
// with no fingerprint: a different edit is window-joined to that older ask.
// This pins the behaviour as it is, not as a goal. It needs an ask from a guard
// older than #300 within five minutes of a current one, and the join is still
// a guess about which edit ran.
func TestRunOutcomeMode_SkippedFingerprintedAskDoesNotShadowOlderAsk(t *testing.T) {
	isolateDecisionSession(t)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_DEBUG", "")

	const file = "/some/repo/main.go"
	askAs(t, "session-a", file, "Legacy")
	askWithEdit(t, "session-a", file, "gggggggggggg", "Current")
	otherOutcome(t, file, "session-a")

	rec := readLastDecisionLog(t)
	if rec["decision"] != "outcome" || rec["join"] != "window" {
		t.Fatalf("want a window-joined outcome, got %v", rec)
	}
	if syms, _ := rec["symbols"].([]any); len(syms) != 1 || syms[0] != "Legacy" {
		t.Errorf("symbols = %v, want [Legacy]", rec["symbols"])
	}
}
