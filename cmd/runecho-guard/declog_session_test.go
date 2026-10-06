package main

import (
	"strings"
	"testing"
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

func TestLogDecision_StampsHashedSession(t *testing.T) {
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
