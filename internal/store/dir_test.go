package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunechoDir_EnvOverride: an absolute RUNECHO_HOME is returned as-is.
func TestRunechoDir_EnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "store")
	t.Setenv("RUNECHO_HOME", want)

	got, err := RunechoDir()
	if err != nil {
		t.Fatalf("RunechoDir: %v", err)
	}
	if got != want {
		t.Errorf("RunechoDir = %q, want %q", got, want)
	}
}

// TestRunechoDir_RelativeNormalized: a relative RUNECHO_HOME resolves to one
// stable absolute, cleaned path rather than differing per caller cwd.
func TestRunechoDir_RelativeNormalized(t *testing.T) {
	t.Setenv("RUNECHO_HOME", "./rel/../runecho-data")

	got, err := RunechoDir()
	if err != nil {
		t.Fatalf("RunechoDir: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("relative RUNECHO_HOME not made absolute: %q", got)
	}
	if got != filepath.Clean(got) {
		t.Errorf("RUNECHO_HOME not cleaned: %q", got)
	}
	wd, _ := os.Getwd()
	if want := filepath.Join(wd, "runecho-data"); got != want {
		t.Errorf("RunechoDir = %q, want %q", got, want)
	}
}

// TestRunechoDir_DefaultHome: unset RUNECHO_HOME falls back to ~/.runecho.
func TestRunechoDir_DefaultHome(t *testing.T) {
	t.Setenv("RUNECHO_HOME", "")

	got, err := RunechoDir()
	if err != nil {
		t.Fatalf("RunechoDir: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if want := filepath.Join(home, ".runecho"); got != want {
		t.Errorf("RunechoDir = %q, want %q", got, want)
	}
}
