package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeDecisions writes a decisions.jsonl into dir with the given raw JSONL lines.
func writeDecisions(t *testing.T, dir string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "decisions.jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339) }
func minAgo(m int) string {
	return time.Now().UTC().Add(-time.Duration(m) * time.Minute).Format(time.RFC3339)
}

func TestFPReport_NoLog_ExitNoData(t *testing.T) {
	dir := t.TempDir()
	var code int
	withHome(dir, func() { code = runFPReport(nil) })
	if code != ExitNoData {
		t.Errorf("missing log should be ExitNoData(%d), got %d", ExitNoData, code)
	}
}

func TestFPReport_BadDays_ExitError(t *testing.T) {
	dir := t.TempDir()
	var code int
	withHome(dir, func() { code = runFPReport([]string{"--days=0"}) })
	if code != ExitError {
		t.Errorf("--days=0 should be ExitError(%d), got %d", ExitError, code)
	}
}

func TestFPReport_JSONShape(t *testing.T) {
	dir := t.TempDir()
	ago := minAgo(3)
	now := nowRFC()
	writeDecisions(t, dir,
		`{"v":1,"ts":"`+ago+`","mode":"hook","repo":"r","file":"a.py","lang":"py","decision":"ask","reason":"violations","symbols":["foo"]}`,
		`{"v":1,"ts":"`+now+`","mode":"hook","file":"a.py","decision":"outcome","reason":"approved","symbols":["foo"]}`,
	)
	var out string
	var code int
	withHome(dir, func() {
		out, _ = captureOutput(func() { code = runFPReport([]string{"--json", "--days=1"}) })
	})
	if code != ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	overall, ok := payload["overall"].(map[string]any)
	if !ok || overall["asks"].(float64) != 1 || overall["approved"].(float64) != 1 {
		t.Errorf("overall should be 1 ask / 1 approved, got %v", payload["overall"])
	}
}

// The CI gate: an approval rate above --max-rate exits non-zero, but only once
// there are enough asks to be a rate rather than noise.
func TestFPReport_MaxRateGate(t *testing.T) {
	dir := t.TempDir()
	// 25 asks, all approved → 100% approval, over any threshold, above the 20-ask floor.
	var lines []string
	for i := 0; i < 25; i++ {
		a := minAgo(4)
		o := minAgo(2)
		f := "f" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ".py"
		lines = append(lines,
			`{"v":1,"ts":"`+a+`","mode":"hook","repo":"r","file":"`+f+`","lang":"py","decision":"ask","reason":"violations","symbols":["s`+string(rune('a'+i%26))+`"]}`,
			`{"v":1,"ts":"`+o+`","mode":"hook","file":"`+f+`","decision":"outcome","reason":"approved","symbols":["s`+string(rune('a'+i%26))+`"]}`,
		)
	}
	writeDecisions(t, dir, lines...)
	var code int
	withHome(dir, func() {
		_, _ = captureOutput(func() { code = runFPReport([]string{"--days=1", "--max-rate=0.5"}) })
	})
	// A tripped gate must be a DISTINCT non-zero from ExitNoData(1), so CI can
	// tell "rate exceeded — fail" from "no log — skip".
	if code != ExitError {
		t.Errorf("100%% approval over --max-rate=0.5 should trip the gate (ExitError=%d), got %d", ExitError, code)
	}
	if code == ExitNoData {
		t.Error("gate exit code must not collide with the no-data code")
	}

	// Below the 20-ask floor, the gate must NOT fire even at 100%.
	dir2 := t.TempDir()
	a, o := minAgo(4), minAgo(2)
	writeDecisions(t, dir2,
		`{"v":1,"ts":"`+a+`","mode":"hook","repo":"r","file":"x.py","lang":"py","decision":"ask","reason":"violations","symbols":["z"]}`,
		`{"v":1,"ts":"`+o+`","mode":"hook","file":"x.py","decision":"outcome","reason":"approved","symbols":["z"]}`,
	)
	var code2 int
	withHome(dir2, func() {
		_, _ = captureOutput(func() { code2 = runFPReport([]string{"--days=1", "--max-rate=0.5"}) })
	})
	if code2 != ExitOK {
		t.Errorf("below the 20-ask floor the gate must not fire, got %d", code2)
	}
}

// Flag validation added after adversarial review: bad values must error, not
// silently produce a misleading report or a gate that can never fire.
func TestFPReport_FlagValidation(t *testing.T) {
	dir := t.TempDir()
	writeDecisions(t, dir, `{"v":1,"ts":"`+minAgo(2)+`","mode":"hook","file":"a.py","decision":"outcome","reason":"approved","symbols":["x"]}`)
	cases := []struct {
		name string
		args []string
	}{
		{"days overflow", []string{"--days=200000"}},
		{"days zero", []string{"--days=0"}},
		{"top below one", []string{"--top=0"}},
		{"max-rate above one", []string{"--max-rate=5"}},   // user meant 5%, wrote a fraction that can never trip
		{"max-rate below zero", []string{"--max-rate=-2"}}, // not the -1 sentinel
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var code int
			withHome(dir, func() {
				_, _ = captureOutput(func() { code = runFPReport(tc.args) })
			})
			if code != ExitError {
				t.Errorf("%s should be ExitError(%d), got %d", tc.name, ExitError, code)
			}
		})
	}
}

// The --json gate result must be readable from the payload, not only the exit
// code — a `| jq` pipeline masks the exit status.
func TestFPReport_JSONCarriesGate(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 25; i++ {
		a, o := minAgo(4), minAgo(2)
		f := "g" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ".py"
		sym := "s" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		lines = append(lines,
			`{"v":1,"ts":"`+a+`","mode":"hook","repo":"r","file":"`+f+`","lang":"py","decision":"ask","reason":"violations","symbols":["`+sym+`"]}`,
			`{"v":1,"ts":"`+o+`","mode":"hook","file":"`+f+`","decision":"outcome","reason":"approved","symbols":["`+sym+`"]}`,
		)
	}
	writeDecisions(t, dir, lines...)
	var out string
	withHome(dir, func() {
		out, _ = captureOutput(func() { _ = runFPReport([]string{"--days=1", "--max-rate=0.5", "--json"}) })
	})
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	gate, ok := payload["gate"].(map[string]any)
	if !ok {
		t.Fatalf("gate object missing from --json output: %v", payload)
	}
	if gate["tripped"] != true || gate["evaluated"] != true {
		t.Errorf("gate should be evaluated+tripped at 100%% approval, got %v", gate)
	}
}
