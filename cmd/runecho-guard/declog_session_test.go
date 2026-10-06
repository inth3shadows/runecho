package main

import (
	"bytes"
	"io"
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
// through runArgs, not neverBlockOnPanic, so a session set anywhere on the real
// pre-commit entry path is caught. The panic record is used because it is the
// pre-commit record that needs no enrolled repo to produce.
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

// The outcome join never consults a session. When no ask carries the outcome's
// fingerprint it takes the newest ask on that file inside maxOutcomeAge, so a
// different edit from another session is joined to this ask and the outcome
// carries the OTHER session. Pinned because decisionRecord.Session documents
// it: an analysis must group by the ask's session.
func TestRunOutcomeMode_WindowJoinCrossesSessions(t *testing.T) {
	isolateDecisionSession(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	const file = "/some/repo/main.go"
	setDecisionSession("session-a")
	writeAskEntryAt(t, file, time.Now(), "aaaaaaaaaaaa", []string{"Ghost"})
	ask := readLastDecisionLog(t)
	if got, want := ask["session"], contractSessionTag("session-a"); got != want {
		t.Fatalf("ask session = %v, want %q", got, want)
	}

	payload := `{"tool_name":"Edit","session_id":"session-b","tool_input":{"file_path":"` + file + `","old_string":"x","new_string":"y"}}`
	if code := runOutcomeMode(strings.NewReader(payload)); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	rec := readLastDecisionLog(t)
	if got, _ := rec["decision"].(string); got != "outcome" {
		t.Fatalf("decision = %q, want outcome", got)
	}
	if got, _ := rec["join"].(string); got != "window" {
		t.Errorf("join = %q, want window", got)
	}
	if got, want := rec["session"], contractSessionTag("session-b"); got != want {
		t.Errorf("outcome session = %v, want %q (the PostToolUse session)", got, want)
	}
	if rec["session"] == ask["session"] {
		t.Errorf("outcome session %v equals the ask's; the join is expected to cross sessions", rec["session"])
	}
}
