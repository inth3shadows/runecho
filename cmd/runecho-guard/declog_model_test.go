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

// transcriptLine is one Claude Code transcript entry of the given type; an
// empty model omits message.model entirely.
func transcriptLine(typ, model string) string {
	msg := map[string]any{"role": typ}
	if model != "" {
		msg["model"] = model
	}
	b, _ := json.Marshal(map[string]any{"type": typ, "message": msg})
	return string(b)
}

func writeTranscript(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// isolateDecisionModel is isolateDecisionSession's counterpart for the model
// source, which is process-wide for the same reason.
func isolateDecisionModel(t *testing.T) {
	t.Helper()
	decisionModel.Store(nil)
	modelAbandoned.Store(false)
	t.Cleanup(func() {
		decisionModel.Store(nil)
		modelAbandoned.Store(false)
	})
}

func TestTranscriptModel_LatestAssistantWins(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, p,
		transcriptLine("assistant", "claude-opus-5-5"),
		transcriptLine("user", ""),
		transcriptLine("assistant", "claude-sonnet-5-5"),
		transcriptLine("user", ""),
	)
	if got := transcriptModel(p); got != "claude-sonnet-5-5" {
		t.Errorf("transcriptModel = %q, want the newest assistant's model", got)
	}
}

// Claude Code writes harness-generated assistant entries (an API error, an
// interrupt) with model "<synthetic>". No model issued those, so they must not
// shadow the real one behind them.
func TestTranscriptModel_SkipsSyntheticAndModelless(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, p,
		transcriptLine("assistant", "claude-haiku-5-5"),
		transcriptLine("assistant", "<synthetic>"),
		transcriptLine("assistant", ""),
	)
	if got := transcriptModel(p); got != "claude-haiku-5-5" {
		t.Errorf("transcriptModel = %q, want claude-haiku-5-5", got)
	}
}

// A user entry can quote an assistant entry's JSON verbatim (a pasted log, a
// tool result). Only the entry's own type counts.
func TestTranscriptModel_IgnoresNonAssistantEntryMentioningAssistant(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	quoted, _ := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"content": transcriptLine("assistant", "claude-fake-1"), "model": "claude-fake-1"},
	})
	writeTranscript(t, p, transcriptLine("assistant", "claude-opus-5-5"), string(quoted))
	if got := transcriptModel(p); got != "claude-opus-5-5" {
		t.Errorf("transcriptModel = %q, want claude-opus-5-5", got)
	}
}

func TestTranscriptModel_MissingOrEmptyPath(t *testing.T) {
	if got := transcriptModel(""); got != "" {
		t.Errorf(`transcriptModel("") = %q, want ""`, got)
	}
	if got := transcriptModel(filepath.Join(t.TempDir(), "absent.jsonl")); got != "" {
		t.Errorf("missing file = %q, want \"\"", got)
	}
	if got := transcriptModel(t.TempDir()); got != "" {
		t.Errorf("directory = %q, want \"\"", got)
	}
}

// Only the tail is read. The line the tail window cuts through is partial and
// must be dropped, not parsed — and a model that only exists before the window
// is not found, which is the accepted cost of a bounded read.
func TestTranscriptModel_ReadsOnlyTheTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	pad := `{"type":"user","message":{"content":"` + strings.Repeat("x", transcriptTailBytes) + `"}}`
	writeTranscript(t, p, transcriptLine("assistant", "claude-old-1"), pad)
	if got := transcriptModel(p); got != "" {
		t.Errorf("model beyond the tail window = %q, want \"\"", got)
	}

	// The cut line is itself an assistant entry, larger than the window: the
	// whole entries after it are still read.
	p2 := filepath.Join(t.TempDir(), "s2.jsonl")
	big := `{"type":"assistant","message":{"model":"claude-cut-1","content":"` + strings.Repeat("y", transcriptTailBytes) + `"}}`
	writeTranscript(t, p2, big, transcriptLine("assistant", "claude-tail-1"))
	if got := transcriptModel(p2); got != "claude-tail-1" {
		t.Errorf("transcriptModel = %q, want claude-tail-1", got)
	}
}

func TestModelTranscriptPath(t *testing.T) {
	main := "/p/proj/abc-123.jsonl"
	for _, tc := range []struct {
		name, path, agent, want string
	}{
		{"main thread", main, "", main},
		{"no transcript", "", "a1", ""},
		// A subagent's tool call carries the PARENT's transcript_path; its own
		// model is in its own transcript beside it.
		{"subagent from parent path", main, "a1", "/p/proj/abc-123/subagents/agent-a1.jsonl"},
		{"subagent path given directly", "/p/proj/abc-123/subagents/agent-a1.jsonl", "a1",
			"/p/proj/abc-123/subagents/agent-a1.jsonl"},
		// An agent id that is not a plain name records no model rather than
		// reading some other file.
		{"agent id with separator", main, "../x", ""},
		{"agent id dot-dot", main, "..", ""},
		// Glob metacharacters would let the workflows lookup match another
		// agent's transcript (#531 round 2).
		{"agent id star", main, "*", ""},
		{"agent id question", main, "a?", ""},
		{"agent id bracket", main, "[a]", ""},
	} {
		if got := modelTranscriptPath(tc.path, tc.agent); got != tc.want {
			t.Errorf("%s: modelTranscriptPath(%q, %q) = %q, want %q", tc.name, tc.path, tc.agent, got, tc.want)
		}
	}
}

// hookModelJSON is hookSessionJSON plus the transcript fields.
func hookModelJSON(transcript, agent string) string {
	s := `{"tool_name":"Write","tool_input":{"file_path":"/some/repo/notes.md","content":"x"},"session_id":"s1"`
	if transcript != "" {
		s += `,"transcript_path":` + jsonQuote(transcript)
	}
	if agent != "" {
		s += `,"agent_id":` + jsonQuote(agent)
	}
	return s + `}`
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestLogDecision_StampsModelFromTranscript(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	p := filepath.Join(t.TempDir(), "sess.jsonl")
	writeTranscript(t, p, transcriptLine("assistant", "claude-opus-5-5"))
	runHook(t, hookModelJSON(p, ""))
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("no record written")
	}
	if got, _ := rec["model"].(string); got != "claude-opus-5-5" {
		t.Errorf("model = %q, want claude-opus-5-5", got)
	}
}

// A subagent's edit is attributed to the subagent's model, never the parent's:
// #528's small-model arm is exactly a Haiku subagent under an Opus session.
func TestLogDecision_SubagentEditGetsSubagentModel(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	dir := t.TempDir()
	parent := filepath.Join(dir, "sess.jsonl")
	writeTranscript(t, parent, transcriptLine("assistant", "claude-opus-5-5"))
	writeTranscript(t, filepath.Join(dir, "sess", "subagents", "agent-a1.jsonl"),
		transcriptLine("assistant", "claude-haiku-5-5"))

	runHook(t, hookModelJSON(parent, "a1"))
	if got, _ := readLastDecisionLog(t)["model"].(string); got != "claude-haiku-5-5" {
		t.Errorf("model = %q, want claude-haiku-5-5", got)
	}

	// Subagent transcript missing: no model, NOT the parent's. A wrong label
	// would silently pool a small model's verdicts under a large one.
	runHook(t, hookModelJSON(parent, "a2"))
	if got, ok := readLastDecisionLog(t)["model"]; ok {
		t.Errorf("model = %v for a subagent with no transcript, want absent", got)
	}
}

func TestLogDecision_ModelDoesNotLeakAcrossRuns(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	p := filepath.Join(t.TempDir(), "sess.jsonl")
	writeTranscript(t, p, transcriptLine("assistant", "claude-opus-5-5"))
	runHook(t, hookModelJSON(p, ""))
	runHook(t, hookModelJSON("", ""))
	if got, ok := readLastDecisionLog(t)["model"]; ok {
		t.Errorf("second run inherited model %v", got)
	}
	// A parse-fail record is written before any payload field is known.
	runHook(t, hookModelJSON(p, ""))
	runHook(t, "{not json")
	if got, ok := readLastDecisionLog(t)["model"]; ok {
		t.Errorf("parse-fail record inherited model %v", got)
	}
}

// The outcome hook (PostToolUse) stamps the model too, from its own payload.
func TestOutcomeMode_StampsModel(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	p := filepath.Join(t.TempDir(), "sess.jsonl")
	writeTranscript(t, p, transcriptLine("assistant", "claude-sonnet-5-5"))
	setDecisionModel(p, "")
	logDecision(decisionRecord{Mode: "outcome", Decision: "approved"})
	if got, _ := readLastDecisionLog(t)["model"].(string); got != "claude-sonnet-5-5" {
		t.Errorf("model = %q, want claude-sonnet-5-5", got)
	}

	runOutcomeMode(strings.NewReader(`{"tool_name":"Write","session_id":"s1","transcript_path":` +
		jsonQuote(p) + `}`))
	if m := decisionModel.Load(); m == nil || m.transcriptPath != p {
		t.Errorf("runOutcomeMode did not set the model source from transcript_path: %+v", m)
	}
}

// F2 (#531 review): when the hook body is stuck resolving the model (a hung
// filesystem), the timeout branch must still return within its budget and
// write its record — it may not wait on the same resolution.
func TestDeferOnPanic_TimeoutDoesNotWaitOnModelRead(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")
	old := guardTimeout
	guardTimeout = 100 * time.Millisecond
	t.Cleanup(func() { guardTimeout = old })

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	m := &modelSource{transcriptPath: "/stuck"}
	started := make(chan struct{})
	go m.once.Do(func() { close(started); <-block }) // a read that never returns
	<-started
	decisionModel.Store(m)

	ret := make(chan int, 1)
	var out bytes.Buffer
	go func() {
		ret <- deferOnPanic("hook-mode", &out, func(w io.Writer) int {
			<-block
			return 0
		})
	}()
	select {
	case <-ret:
	case <-time.After(2 * time.Second):
		t.Fatal("deferOnPanic's timeout branch blocked on the in-flight model read")
	}
	rec := readLastDecisionLog(t)
	if rec == nil || rec["reason"] != "timeout" {
		t.Fatalf("timeout record not written: %v", rec)
	}
	if _, ok := rec["model"]; ok {
		t.Errorf("timeout record carries model %v from an unfinished read", rec["model"])
	}
}

// F7 (#531 review): the PostToolUse outcome record itself carries the model.
func TestOutcomeMode_OutcomeRecordCarriesModel(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	p := filepath.Join(t.TempDir(), "sess.jsonl")
	writeTranscript(t, p, transcriptLine("assistant", "claude-sonnet-5-5"))
	file := "/some/repo/main.go"
	writeAskEntry(t, file)
	b, _ := json.Marshal(map[string]any{
		"tool_name": "Edit", "transcript_path": p,
		"tool_input": map[string]string{"file_path": file},
	})
	runOutcomeMode(strings.NewReader(string(b)))
	rec := readLastDecisionLog(t)
	if rec == nil || rec["decision"] != "outcome" {
		t.Fatalf("no outcome record: %v", rec)
	}
	if got, _ := rec["model"].(string); got != "claude-sonnet-5-5" {
		t.Errorf("outcome model = %q, want claude-sonnet-5-5", got)
	}
}

// F8 (#531 review): workflow subagents write under
// subagents/workflows/<wf>/agent-<id>.jsonl, not directly under subagents/.
func TestLogDecision_WorkflowSubagentModel(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	dir := t.TempDir()
	parent := filepath.Join(dir, "sess.jsonl")
	writeTranscript(t, parent, transcriptLine("assistant", "claude-opus-5-5"))
	writeTranscript(t, filepath.Join(dir, "sess", "subagents", "workflows", "wf_1", "agent-w1.jsonl"),
		transcriptLine("assistant", "claude-haiku-5-5"))

	runHook(t, hookModelJSON(parent, "w1"))
	if got, _ := readLastDecisionLog(t)["model"].(string); got != "claude-haiku-5-5" {
		t.Errorf("workflow subagent model = %q, want claude-haiku-5-5", got)
	}
}

// #531 round 2: glob metacharacters in agent_id must not select another
// workflow subagent's transcript.
func TestLogDecision_GlobAgentIDRecordsNoModel(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	dir := t.TempDir()
	parent := filepath.Join(dir, "sess.jsonl")
	writeTranscript(t, parent, transcriptLine("assistant", "claude-opus-5-5"))
	writeTranscript(t, filepath.Join(dir, "sess", "subagents", "workflows", "wf_1", "agent-w1.jsonl"),
		transcriptLine("assistant", "claude-haiku-5-5"))
	for _, id := range []string{"*", "w?", "w[1]", "w1*"} {
		runHook(t, hookModelJSON(parent, id))
		if got, ok := readLastDecisionLog(t)["model"]; ok {
			t.Errorf("agent_id %q recorded model %v, want none", id, got)
		}
	}
}

// #531 round 2: setDecisionModel does no filesystem work — the subagent path
// is resolved inside the lazy read, after the decision is buffered. A
// transcript that appears only after setDecisionModel is still found.
func TestSetDecisionModel_ResolvesPathLazily(t *testing.T) {
	isolateDecisionModel(t)
	dir := t.TempDir()
	parent := filepath.Join(dir, "sess.jsonl")
	setDecisionModel(parent, "w1")
	writeTranscript(t, filepath.Join(dir, "sess", "subagents", "workflows", "wf_1", "agent-w1.jsonl"),
		transcriptLine("assistant", "claude-haiku-5-5"))
	if got := decisionModel.Load().get(); got != "claude-haiku-5-5" {
		t.Errorf("get() = %q, want claude-haiku-5-5 (path resolved eagerly at set time?)", got)
	}
}

// #531 round 2: after a timeout, the still-running hook body can store a fresh
// source (setDecisionModel) behind abandonDecisionModel's back. The timeout
// record must still not wait on that source's read.
func TestLogDecision_AbandonHoldsAgainstLaterStore(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")

	abandonDecisionModel()
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	m := &modelSource{transcriptPath: "/stuck"}
	started := make(chan struct{})
	go m.once.Do(func() { close(started); <-block })
	<-started
	decisionModel.Store(m)

	done := make(chan struct{})
	go func() {
		logDecision(decisionRecord{Mode: "hook", Decision: "defer", Reason: "timeout"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("logDecision waited on a read stored after abandonDecisionModel")
	}
}

// Two workflow transcripts for one agent id is ambiguous: record none.
func TestModelTranscriptPath_AmbiguousWorkflowRecordsNone(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "sess.jsonl")
	for _, wf := range []string{"wf_1", "wf_2"} {
		writeTranscript(t, filepath.Join(dir, "sess", "subagents", "workflows", wf, "agent-w1.jsonl"),
			transcriptLine("assistant", "claude-haiku-5-5"))
	}
	if got := modelTranscriptPath(parent, "w1"); got != "" {
		t.Errorf("modelTranscriptPath with two workflow matches = %q, want \"\"", got)
	}
}
