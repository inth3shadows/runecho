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
