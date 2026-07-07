package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestValidateClaims_DeterministicOrder pins finding #3: validate-claims builds
// its mismatch list from a map, so without an explicit sort the JSON output order
// is non-deterministic. With an empty IR every extracted ref is a mismatch, so the
// output must list them in stable, sorted order regardless of map iteration.
func TestValidateClaims_DeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	irPath := filepath.Join(dir, "ir.json")
	if err := os.WriteFile(irPath, []byte(`{"files":{}}`), 0o644); err != nil {
		t.Fatalf("write ir: %v", err)
	}
	txtPath := filepath.Join(dir, "msg.txt")
	// Refs given out of order in backticks; all unknown against the empty IR.
	if err := os.WriteFile(txtPath, []byte("uses `Zebra` then `Apple` and `Mango`\n"), 0o644); err != nil {
		t.Fatalf("write txt: %v", err)
	}

	var code int
	var stdout string
	withArgs([]string{"runecho-ir", "validate-claims", "--ir", irPath, "--text", txtPath}, func() {
		stdout, _ = captureOutput(func() { code = run() })
	})
	if code != ExitNoData {
		t.Fatalf("exit = %d, want ExitNoData (%d); stdout=%s", code, ExitNoData, stdout)
	}

	var out struct {
		Mismatches []struct {
			Ref string `json:"ref"`
		} `json:"mismatches"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode output %q: %v", stdout, err)
	}
	got := make([]string, len(out.Mismatches))
	for i, m := range out.Mismatches {
		got[i] = m.Ref
	}
	want := []string{"Apple", "Mango", "Zebra"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mismatch order = %v, want sorted %v", got, want)
	}
}

// TestValidateClaims_ImportNameRecognized pins review finding #3: the known-symbol
// set must span every indexed kind, not just function/class. A name bound only as
// an import (kind=import_name) genuinely exists, so it must not be reported as a
// hallucinated reference — parity with the MCP `locate` fix. A truly-absent name
// must still be flagged, proving the check itself is intact.
func TestValidateClaims_ImportNameRecognized(t *testing.T) {
	dir := t.TempDir()
	irPath := filepath.Join(dir, "ir.json")
	irJSON := `{"files":{"mod.js":{"hash":"h","imports":["fs"],"functions":[],"classes":[],"exports":[],"refs":[],"symbols":[{"name":"readFileSync","kind":"import_name"}]}}}`
	if err := os.WriteFile(irPath, []byte(irJSON), 0o644); err != nil {
		t.Fatalf("write ir: %v", err)
	}
	txtPath := filepath.Join(dir, "msg.txt")
	if err := os.WriteFile(txtPath, []byte("calls `readFileSync` and `totallyMadeUp`\n"), 0o644); err != nil {
		t.Fatalf("write txt: %v", err)
	}

	var code int
	var stdout string
	withArgs([]string{"runecho-ir", "validate-claims", "--ir", irPath, "--text", txtPath}, func() {
		stdout, _ = captureOutput(func() { code = run() })
	})

	var out struct {
		Mismatches []struct {
			Ref string `json:"ref"`
		} `json:"mismatches"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	// One genuine miss (totallyMadeUp) remains, so the check still reports it.
	if code != ExitNoData {
		t.Errorf("exit = %d, want ExitNoData (%d) for the one real miss; stdout=%s", code, ExitNoData, stdout)
	}
	for _, m := range out.Mismatches {
		if m.Ref == "readFileSync" {
			t.Errorf("import_name readFileSync wrongly flagged as a hallucination: %s", stdout)
		}
	}
	if len(out.Mismatches) != 1 || out.Mismatches[0].Ref != "totallyMadeUp" {
		t.Errorf("expected only totallyMadeUp flagged, got %v", out.Mismatches)
	}
}
