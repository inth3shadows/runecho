package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	t.Cleanup(func() { decisionModel.Store(nil) })
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
		s += `,"transcript_path":` + strconvQuote(transcript)
	}
	if agent != "" {
		s += `,"agent_id":` + strconvQuote(agent)
	}
	return s + `}`
}

func strconvQuote(s string) string {
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
		strconvQuote(p) + `}`))
	if m := decisionModel.Load(); m == nil || m.path != p {
		t.Errorf("runOutcomeMode did not set the model source from transcript_path: %+v", m)
	}
}
