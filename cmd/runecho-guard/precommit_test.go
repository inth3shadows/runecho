package main

// precommit_test.go — exit-code contract tests for runArgs (the pre-commit,
// non-hook path). Complements hookmode_test.go and outcomemode_test.go, which
// cover --hook-mode and --outcome-mode respectively.
//
// A real staged diff requires a live git commit — impractical to stage in a unit
// test without spinning up a git server and writing files. Instead we cover the
// three degraded branches that are fully reachable without a diff: SKIP env
// bypass, no history.db, and not-enrolled-repo. Each is a distinct early-return
// in runArgs and is not otherwise exercised by the existing suite.

import (
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/snapshot"
)

// TestRunArgs_SkipEnv_Exits0 verifies that RUNECHO_GUARD_SKIP=1 causes the
// pre-commit path to exit 0 immediately, bypassing store lookup, diff parsing,
// and every other check. This is the documented "escape hatch" contract.
func TestRunArgs_SkipEnv_Exits0(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_SKIP", "1")
	if code := runArgs(nil); code != 0 {
		t.Errorf("RUNECHO_GUARD_SKIP=1: exit = %d, want 0", code)
	}
}

// TestRunArgs_NoHistoryDB_Exits0 verifies that when the central store has
// never been created (runecho is not installed/configured on this machine),
// the guard exits 0 silently. This is the most common path on a machine where
// runecho has never run.
func TestRunArgs_NoHistoryDB_Exits0(t *testing.T) {
	home := t.TempDir() // no history.db in here
	t.Setenv("RUNECHO_HOME", home)
	if code := runArgs(nil); code != 0 {
		t.Errorf("no history.db: exit = %d, want 0", code)
	}
}

// TestRunArgs_NotEnrolled_Exits0 verifies the "repo not enrolled" branch:
// when history.db exists but the process's cwd is not enrolled, the guard
// exits 0 with an info message. "Not enrolled" is explicitly not a degraded
// state — RUNECHO_GUARD_STRICT=1 must NOT change this behaviour.
func TestRunArgs_NotEnrolled_Exits0(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	// Create a minimal store with no enrolled repos so the Stat check passes
	// and ResolveRepo finds nothing.
	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	db.Close()

	// Non-strict: not-enrolled → exit 0.
	t.Setenv("RUNECHO_GUARD_STRICT", "")
	if code := runArgs(nil); code != 0 {
		t.Errorf("not-enrolled (strict=off): exit = %d, want 0", code)
	}

	// Strict mode must also exit 0 for not-enrolled — it is not a degraded state.
	t.Setenv("RUNECHO_GUARD_STRICT", "1")
	if code := runArgs(nil); code != 0 {
		t.Errorf("not-enrolled (strict=1): exit = %d, want 0 (not-enrolled is never degraded)", code)
	}
}
