package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pre-commit path is the guard's only entry point that blocks ON PURPOSE:
// it exits non-zero to stop a commit. That makes an unhandled panic there worse
// than anywhere else — Go exits 2, git reads any non-zero as "refuse", and the
// user cannot commit at all until the offending content changes. deferOnPanic
// covers the two stdio-hook modes; neverBlockOnPanic covers this one.
//
// Each test below inverts a behaviour rather than asserting its presence:
// delete the barrier and TestRunArgs_PreCommitPanicAllowsCommit panics the test
// binary; widen it to swallow every code and
// TestNeverBlockOnPanic_PassesCodeThrough fails.

func TestNeverBlockOnPanic_PassesCodeThrough(t *testing.T) {
	// A real violation must still block. The barrier is for panics, not for
	// turning the pre-commit check into a no-op.
	for _, want := range []int{0, 1, 2} {
		if got := neverBlockOnPanic(func() int { return want }); got != want {
			t.Fatalf("code = %d, want %d — the barrier must not rewrite a deliberate exit", got, want)
		}
	}
}

func TestNeverBlockOnPanic_PanicAllowsCommit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	if got := neverBlockOnPanic(func() int { panic("extraction blew up on staged content") }); got != 0 {
		t.Fatalf("code = %d, want 0 — a panic must allow the commit, not block it", got)
	}

	// Allowing the commit silently would trade a visible failure for an invisible
	// one: the user's next commits go unchecked with nothing to explain why.
	data, err := os.ReadFile(filepath.Join(home, "decisions.jsonl"))
	if err != nil {
		t.Fatalf("no decisions.jsonl written — the deferral left no diagnosable trace: %v", err)
	}
	var rec map[string]any
	line := strings.TrimSpace(string(data))
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("decisions.jsonl is not JSON: %v (%q)", err, line)
	}
	for field, want := range map[string]string{"mode": "precommit", "decision": "defer", "reason": "panic"} {
		if got, _ := rec[field].(string); got != want {
			t.Errorf("%s = %q, want %q", field, got, want)
		}
	}
}

// TestRunArgs_PreCommitPanicAllowsCommit is the wiring test: the two above prove
// neverBlockOnPanic works, this one proves runArgs actually routes through it.
// A barrier that exists but is not reached is the failure mode this repo calls
// presence-without-verification.
func TestRunArgs_PreCommitPanicAllowsCommit(t *testing.T) {
	t.Setenv("RUNECHO_HOME", t.TempDir())
	orig := runPreCommitBody
	t.Cleanup(func() { runPreCommitBody = orig })
	runPreCommitBody = func(bool, bool) int { panic("boom") }

	if got := runArgs(nil); got != 0 {
		t.Fatalf("runArgs = %d, want 0 — the pre-commit path is not behind the barrier", got)
	}
}

// TestRunArgs_PassesFlagsToPreCommit pins the seam itself. Extracting
// runPreCommit out of runArgs turned two *bool locals into value parameters;
// swapping or dropping either would silently disable --dry-run, which is the
// difference between reporting a violation and blocking a commit on it.
func TestRunArgs_PassesFlagsToPreCommit(t *testing.T) {
	orig := runPreCommitBody
	t.Cleanup(func() { runPreCommitBody = orig })

	for _, tc := range []struct {
		args            []string
		dryRun, verbose bool
	}{
		{nil, false, false},
		{[]string{"-dry-run"}, true, false},
		{[]string{"-verbose"}, false, true},
		{[]string{"-dry-run", "-verbose"}, true, true},
	} {
		var gotDry, gotVerbose bool
		runPreCommitBody = func(d, v bool) int { gotDry, gotVerbose = d, v; return 0 }
		if runArgs(tc.args); gotDry != tc.dryRun || gotVerbose != tc.verbose {
			t.Errorf("%v: dryRun=%v verbose=%v, want %v/%v", tc.args, gotDry, gotVerbose, tc.dryRun, tc.verbose)
		}
	}
}
