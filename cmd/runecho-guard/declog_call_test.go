package main

import (
	"strings"
	"testing"
)

// isolateDecisionCall gives a test the fresh-process state for the call tag.
func isolateDecisionCall(t *testing.T) {
	t.Helper()
	decisionCall.Store(nil)
	t.Cleanup(func() { decisionCall.Store(nil) })
}

func hookCallJSON(toolUseID string) string {
	s := `{"tool_name":"Write","tool_input":{"file_path":"/some/repo/notes.md","content":"x"},"session_id":"s1"`
	if toolUseID != "" {
		s += `,"tool_use_id":` + jsonQuote(toolUseID)
	}
	return s + `}`
}

// #467 step 1: every hook record carries a short hash of the payload's
// tool_use_id, never the raw id.
func TestLogDecision_StampsHashedCall(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	isolateDecisionCall(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	const id = "toolu_01ABCDEFGHIJKLMNOPQRSTUV"
	runHook(t, hookCallJSON(id))
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("no record written")
	}
	got, _ := rec["call"].(string)
	if want := contractSessionTag(id); got != want {
		t.Errorf("call = %q, want %q", got, want)
	}
	if strings.Contains(got, id) {
		t.Errorf("call %q carries the raw tool_use_id", got)
	}
}

func TestLogDecision_CallDoesNotLeakAcrossRuns(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	isolateDecisionCall(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	runHook(t, hookCallJSON("toolu_1"))
	runHook(t, hookCallJSON(""))
	if got, ok := readLastDecisionLog(t)["call"]; ok {
		t.Errorf("second run inherited call %v", got)
	}
	runHook(t, hookCallJSON("toolu_1"))
	runHook(t, "{not json")
	if got, ok := readLastDecisionLog(t)["call"]; ok {
		t.Errorf("parse-fail record inherited call %v", got)
	}
}

// The PostToolUse outcome record carries the call tag from its own payload, so
// it can later be joined to the PreToolUse ask of the same call.
func TestOutcomeMode_OutcomeRecordCarriesCall(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	isolateDecisionCall(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	file := "/some/repo/main.go"
	writeAskEntry(t, file)
	runOutcomeMode(strings.NewReader(`{"tool_name":"Edit","tool_use_id":"toolu_9","tool_input":{"file_path":` +
		jsonQuote(file) + `}}`))
	rec := readLastDecisionLog(t)
	if rec == nil || rec["decision"] != "outcome" {
		t.Fatalf("no outcome record: %v", rec)
	}
	if got, _ := rec["call"].(string); got != contractSessionTag("toolu_9") {
		t.Errorf("outcome call = %q, want %q", got, contractSessionTag("toolu_9"))
	}
}
