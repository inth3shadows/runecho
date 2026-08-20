package main

// precommit_test.go — exit-code contract tests for runArgs (the pre-commit,
// non-hook path). Complements hookmode_test.go and outcomemode_test.go, which
// cover --hook-mode and --outcome-mode respectively.
//
// Three of the four tests here cover the degraded branches reachable without a
// diff at all: SKIP env bypass, no history.db, and not-enrolled-repo. Each is a
// distinct early-return in runArgs and is not otherwise exercised.
//
// This header used to claim a real staged diff was impractical without spinning
// up a git server. That was wrong, and it cost coverage: runArgs reads
// `git diff --cached` from the process's working directory, so `git add` plus
// t.Chdir is the whole harness — see
// TestRunArgs_StagedDiff_AsksAndRecordsCheckReasons below, added once #363's
// review found the pre-commit ask record had nothing pinning it.

import (
	"os"
	"os/exec"
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

// TestRunArgs_StagedDiff_AsksAndRecordsCheckReasons is the first test here to
// drive runArgs through a REAL staged diff. The file header above calls that
// impractical, which was true before t.Chdir: the whole obstacle is that
// runArgs reads `git diff --cached` from the process's working directory, and
// staging needs nothing more than `git add`.
//
// It exists because deleting CheckReasons from the pre-commit ask record
// survived the entire suite (found by review of #363) — the pre-commit surface
// had no test that inspected its record at all, so nothing there was pinned.
func TestRunArgs_StagedDiff_AsksAndRecordsCheckReasons(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	// No go.mod anywhere above repoRoot: qualified is then Skipped for a reason
	// it RECORDS ("no-module-path"), which is the only reason the pre-commit
	// path can produce today — and therefore the only thing that can prove the
	// field is written here.
	enrolledStore(t, repoRoot, []string{"KnownFunc"})

	staged := filepath.Join(repoRoot, "main.go")
	if err := os.WriteFile(staged, []byte("package main\n\nfunc F() { HallucinatedFunc() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "main.go"}} {
		cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	t.Chdir(repoRoot)

	// A hallucinated symbol in a staged diff asks, which is exit 1 plus one
	// "ask" record — the only pre-commit record that carries a Checks map.
	if code := runArgs(nil); code != 1 {
		t.Fatalf("staged hallucination: exit = %d, want 1 (ask)", code)
	}
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("decisions.jsonl: no record written")
	}
	if got, _ := rec["mode"].(string); got != "precommit" {
		t.Fatalf("log mode = %q, want precommit: %v", got, rec)
	}
	checks, _ := rec["checks"].(map[string]any)
	if got, _ := checks["violations"].(string); got != "violation" {
		t.Errorf("checks[violations] = %q, want violation", got)
	}
	reasons, _ := rec["check_reasons"].(map[string]any)
	if got, _ := reasons["qualified"].(string); got != "no-module-path" {
		t.Errorf("check_reasons[qualified] = %q, want no-module-path (reasons=%v)", got, reasons)
	}
}
