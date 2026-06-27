package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/version"
)

// TestVersionFlag verifies --version prints version.Version (the single source of
// truth shared with runecho-mcp) and exits 0, before any store resolution runs.
func TestVersionFlag(t *testing.T) {
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	code := runArgs([]string{"--version"})

	w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got := strings.TrimSpace(string(out)); got != version.Version {
		t.Errorf("output = %q, want %q", got, version.Version)
	}
}
