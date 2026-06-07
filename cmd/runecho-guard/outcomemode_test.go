package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeAskEntry writes a single ask decision record to decisions.jsonl in the
// current RUNECHO_HOME, with the given file and the current timestamp.
func writeAskEntry(t *testing.T, file string) {
	t.Helper()
	home := os.Getenv("RUNECHO_HOME")
	if home == "" {
		t.Fatal("RUNECHO_HOME not set")
	}
	rec := decisionRecord{
		V:        1,
		TS:       time.Now().UTC().Format(time.RFC3339),
		Mode:     "hook",
		File:     file,
		Decision: "ask",
		Reason:   "violations",
	}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "decisions.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// outcomeJSON returns a PostToolUse-format JSON payload for the given file.
func outcomeJSON(t *testing.T, filePath string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"tool_name":  "Edit",
		"tool_input": map[string]string{"file_path": filePath},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunOutcomeMode_LogsWhenRecentAskExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	file := "/some/repo/main.go"
	writeAskEntry(t, file)

	before := countDecisionLogLines(t)
	if code := runOutcomeMode(strings.NewReader(outcomeJSON(t, file))); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	after := countDecisionLogLines(t)
	if after != before+1 {
		t.Errorf("expected +1 log line (before=%d after=%d)", before, after)
	}
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("no record written")
	}
	if got, _ := rec["decision"].(string); got != "outcome" {
		t.Errorf("decision = %q, want %q", got, "outcome")
	}
	if got, _ := rec["reason"].(string); got != "approved" {
		t.Errorf("reason = %q, want %q", got, "approved")
	}
	if got, _ := rec["file"].(string); got != file {
		t.Errorf("file = %q, want %q", got, file)
	}
	if got, _ := rec["mode"].(string); got != "hook" {
		t.Errorf("mode = %q, want %q", got, "hook")
	}
}

func TestRunOutcomeMode_NoOpWhenNoAskExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	before := countDecisionLogLines(t)
	if code := runOutcomeMode(strings.NewReader(outcomeJSON(t, "/some/repo/main.go"))); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if after := countDecisionLogLines(t); after != before {
		t.Errorf("no ask: expected no new log entry (before=%d after=%d)", before, after)
	}
}

func TestRunOutcomeMode_NoOpForDifferentFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	writeAskEntry(t, "/other/file.go")

	before := countDecisionLogLines(t)
	if code := runOutcomeMode(strings.NewReader(outcomeJSON(t, "/some/repo/main.go"))); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if after := countDecisionLogLines(t); after != before {
		t.Errorf("different file: expected no new entry (before=%d after=%d)", before, after)
	}
}

func TestRunOutcomeMode_NoOpOnMalformedInput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	before := countDecisionLogLines(t)
	if code := runOutcomeMode(strings.NewReader("{not json")); code != 0 {
		t.Errorf("exit code = %d, want 0 (fail-open)", code)
	}
	if after := countDecisionLogLines(t); after != before {
		t.Errorf("malformed input: expected no entry (before=%d after=%d)", before, after)
	}
}

func TestRunOutcomeMode_NoOpOnEmptyFilePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	before := countDecisionLogLines(t)
	p, _ := json.Marshal(map[string]any{"tool_name": "Edit", "tool_input": map[string]string{}})
	if code := runOutcomeMode(strings.NewReader(string(p))); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if after := countDecisionLogLines(t); after != before {
		t.Errorf("empty path: expected no entry (before=%d after=%d)", before, after)
	}
}
