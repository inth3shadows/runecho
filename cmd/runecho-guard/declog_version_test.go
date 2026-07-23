package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/version"
)

// Every record must carry the writing binary's version. Without it, any
// aggregate over a window spanning two installs silently pools the behaviour of
// different programs — measured at 70% vs 19% on the same log (#207).
func TestLogDecision_StampsGuardVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	logDecision(decisionRecord{Mode: "hook", File: "a.py", Decision: "ask", Reason: "violations"})

	rec := lastDecisionRecord(t, home)
	if got := rec["gv"]; got != version.Version {
		t.Errorf("gv = %v, want %q", got, version.Version)
	}
	// v is the SCHEMA version and must stay 1 — the two are independent, and
	// conflating them is what made the log unreadable in the first place.
	if got := rec["v"]; got != float64(1) {
		t.Errorf("v = %v, want 1 (schema version, not the binary version)", got)
	}
}

// A caller cannot spoof the version: only the binary doing the writing knows
// which binary it is, so any supplied value would be wrong by construction.
func TestLogDecision_CallerSuppliedVersionIsOverwritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	logDecision(decisionRecord{GV: "v9.9.9-spoofed", Mode: "hook", Decision: "ask", Reason: "violations"})

	if got := lastDecisionRecord(t, home)["gv"]; got != version.Version {
		t.Errorf("gv = %v, want the real binary version %q", got, version.Version)
	}
}

// The outcome record is written through the same path, so it inherits the stamp
// — an ask and its approval always agree on version, which is what lets
// guardstats.FilterVersion drop a build without breaking joins.
func TestLogOutcome_StampsGuardVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	file := filepath.Join(t.TempDir(), "a.py")
	logDecision(decisionRecord{Mode: "hook", File: file, Lang: "py",
		Decision: "ask", Reason: "violations", Symbols: []string{"foo"}})
	logOutcomeForFile(file)

	rec := lastDecisionRecord(t, home)
	if rec["decision"] != "outcome" {
		t.Fatalf("last record is %v, want the outcome", rec["decision"])
	}
	if got := rec["gv"]; got != version.Version {
		t.Errorf("outcome gv = %v, want %q", got, version.Version)
	}
}

func lastDecisionRecord(t *testing.T, home string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "decisions.jsonl"))
	if err != nil {
		t.Fatalf("read decisions.jsonl: %v", err)
	}
	var last []byte
	for _, line := range splitNonEmptyLines(data) {
		last = line
	}
	if last == nil {
		t.Fatal("decisions.jsonl has no records")
	}
	var rec map[string]any
	if err := json.Unmarshal(last, &rec); err != nil {
		t.Fatalf("last line is not valid JSON: %v\n%s", err, last)
	}
	return rec
}

func splitNonEmptyLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			if line := data[start:i]; len(line) > 0 {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	return out
}
