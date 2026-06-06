package main

// Entrypoint tests for runecho-ir. Each test sets os.Args and RUNECHO_HOME,
// then calls run() directly — no subprocess, no process exit. Coverage targets
// the behaviors locked by issue #14 (exit-code inconsistencies deliberately
// preserved) plus the correctness-critical cross-repo diff refusal.
//
// Pattern mirrors cmd/runecho-guard/main_test.go: gitInit helper, TempDir for
// both the git repo and the central store, RUNECHO_HOME to redirect the store.

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// irGitInit creates a git repo in dir and writes a minimal Go source file so
// that buildIR has at least one indexable file (avoids empty-IR edge cases in
// snapshot assertions).
func irGitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	// A minimal Go file gives the IR generator something to index.
	src := "package stub\n\nfunc Hello() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "stub.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write stub.go: %v", err)
	}
}

// withArgs temporarily replaces os.Args for the duration of fn, then restores it.
func withArgs(args []string, fn func()) {
	orig := os.Args
	os.Args = args
	defer func() { os.Args = orig }()
	fn()
}

// withHome temporarily redirects RUNECHO_HOME to dir for the duration of fn.
func withHome(dir string, fn func()) {
	orig := os.Getenv("RUNECHO_HOME")
	os.Setenv("RUNECHO_HOME", dir)
	defer func() {
		if orig == "" {
			os.Unsetenv("RUNECHO_HOME")
		} else {
			os.Setenv("RUNECHO_HOME", orig)
		}
	}()
	fn()
}

// captureOutput redirects os.Stdout and os.Stderr during fn, returning what
// was written to each. Stdout/stderr are restored before captureOutput returns.
func captureOutput(fn func()) (stdout, stderr string) {
	// Capture stdout.
	origOut := os.Stdout
	rOut, wOut, _ := os.Pipe()
	os.Stdout = wOut

	// Capture stderr.
	origErr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	fn()

	wOut.Close()
	wErr.Close()
	os.Stdout = origOut
	os.Stderr = origErr

	var bufOut, bufErr bytes.Buffer
	io.Copy(&bufOut, rOut)
	io.Copy(&bufErr, rErr)
	return bufOut.String(), bufErr.String()
}

// runWith sets args and RUNECHO_HOME, calls run(), and returns
// (exitCode, stdout, stderr).
func runWith(t *testing.T, home string, args []string) (int, string, string) {
	t.Helper()
	var code int
	var stdout, stderr string
	withHome(home, func() {
		withArgs(args, func() {
			stdout, stderr = captureOutput(func() {
				code = run()
			})
		})
	})
	return code, stdout, stderr
}

// ---------------------------------------------------------------------------
// --version
// ---------------------------------------------------------------------------

func TestVersion(t *testing.T) {
	home := t.TempDir()
	code, out, _ := runWith(t, home, []string{"runecho-ir", "--version"})
	if code != 0 {
		t.Fatalf("--version: got code %d, want 0", code)
	}
	if !strings.Contains(out, "runecho-ir") {
		t.Errorf("--version stdout %q does not contain \"runecho-ir\"", out)
	}
}

// ---------------------------------------------------------------------------
// Unknown subcommand / flag
// ---------------------------------------------------------------------------

func TestUnknownFlag(t *testing.T) {
	home := t.TempDir()
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "--bogus-flag"})
	if code != 1 {
		t.Fatalf("unknown flag: got code %d, want 1", code)
	}
	if !strings.Contains(stderr, "unknown flag") {
		t.Errorf("stderr %q does not mention \"unknown flag\"", stderr)
	}
}

// ---------------------------------------------------------------------------
// repo add
// ---------------------------------------------------------------------------

func TestRepoAdd(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "add", dir})
	if code != 0 {
		t.Fatalf("repo add: got code %d, want 0", code)
	}
	if !strings.Contains(out, "Enrolled") {
		t.Errorf("stdout %q: expected \"Enrolled\"", out)
	}
}

// Re-adding the same path must be idempotent (exit 0) and print "Already enrolled".
func TestRepoAdd_Duplicate(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// First enrollment.
	code, _, _ := runWith(t, home, []string{"runecho-ir", "repo", "add", dir})
	if code != 0 {
		t.Fatalf("first repo add: got code %d, want 0", code)
	}

	// Second enrollment of the same path — must exit 0, not create a duplicate.
	code2, out2, _ := runWith(t, home, []string{"runecho-ir", "repo", "add", dir})
	if code2 != 0 {
		t.Fatalf("duplicate repo add: got code %d, want 0", code2)
	}
	if !strings.Contains(out2, "Already enrolled") {
		t.Errorf("stdout %q: expected \"Already enrolled\"", out2)
	}
}

// ---------------------------------------------------------------------------
// repo list
// ---------------------------------------------------------------------------

func TestRepoList_Empty(t *testing.T) {
	home := t.TempDir()
	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "list"})
	if code != 0 {
		t.Fatalf("repo list (empty): got code %d, want 0", code)
	}
	if !strings.Contains(out, "No repos enrolled") {
		t.Errorf("stdout %q: expected \"No repos enrolled\"", out)
	}
}

func TestRepoList_WithRepo(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	runWith(t, home, []string{"runecho-ir", "repo", "add", dir})

	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "list"})
	if code != 0 {
		t.Fatalf("repo list: got code %d, want 0", code)
	}
	if !strings.Contains(out, "NAME") {
		t.Errorf("stdout %q: expected table header \"NAME\"", out)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("stdout %q: expected enrolled path %q", out, dir)
	}
}

// ---------------------------------------------------------------------------
// repo rm
// ---------------------------------------------------------------------------

func TestRepoRemove_Existing(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// Enroll, then remove.
	runWith(t, home, []string{"runecho-ir", "repo", "add", dir})

	// Grab the auto-derived name from the list output so rm targets it exactly.
	_, listOut, _ := runWith(t, home, []string{"runecho-ir", "repo", "list"})
	// The name is the first column; extract from the first data row (after the header lines).
	var name string
	for _, line := range strings.Split(listOut, "\n") {
		if strings.Contains(line, dir) {
			name = strings.Fields(line)[0]
			break
		}
	}
	if name == "" {
		t.Fatal("could not parse repo name from list output")
	}

	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "rm", name})
	if code != 0 {
		t.Fatalf("repo rm: got code %d, want 0", code)
	}
	if !strings.Contains(out, "Removed") {
		t.Errorf("stdout %q: expected \"Removed\"", out)
	}
}

func TestRepoRemove_Missing(t *testing.T) {
	home := t.TempDir()
	// No repos enrolled — rm must exit 1 and report "No repo named".
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "repo", "rm", "does-not-exist"})
	if code != 1 {
		t.Fatalf("repo rm missing: got code %d, want 1", code)
	}
	if !strings.Contains(stderr, "No repo named") {
		t.Errorf("stderr %q: expected \"No repo named\"", stderr)
	}
}

// ---------------------------------------------------------------------------
// repo reindex
// ---------------------------------------------------------------------------

func TestRepoReindex(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// Enroll then reindex.
	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "add", dir})
	if code != 0 {
		t.Fatalf("repo add: %d", code)
	}
	// Parse name from enrollment output ("Enrolled <name> ...").
	name := strings.Fields(out)[1]

	code, out, _ = runWith(t, home, []string{"runecho-ir", "repo", "reindex", name})
	if code != 0 {
		t.Fatalf("repo reindex: got code %d, want 0", code)
	}
	if !strings.Contains(out, "Reindexed") {
		t.Errorf("stdout %q: expected \"Reindexed\"", out)
	}
}

// ---------------------------------------------------------------------------
// snapshot
// ---------------------------------------------------------------------------

func TestSnapshot(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	code, out, _ := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=session-start", dir})
	if code != 0 {
		t.Fatalf("snapshot: got code %d, want 0", code)
	}
	if !strings.Contains(out, "Snapshot saved") {
		t.Errorf("stdout %q: expected \"Snapshot saved\"", out)
	}
	if !strings.Contains(out, "session-start") {
		t.Errorf("stdout %q: expected label \"session-start\"", out)
	}
}

// ---------------------------------------------------------------------------
// diff --since happy path
// ---------------------------------------------------------------------------

// diff --since with a matching snapshot must exit 0 — this is one of the
// exit-code inconsistencies locked for issue #14 (no snapshot also exits 0).
func TestDiffSince_HappyPath(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// Create a baseline snapshot.
	code, _, _ := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=manual", dir})
	if code != 0 {
		t.Fatalf("snapshot: %d", code)
	}

	// diff --since against that label must succeed.
	code, _, _ = runWith(t, home, []string{"runecho-ir", "diff", "--since=manual", dir})
	if code != 0 {
		t.Fatalf("diff --since happy path: got code %d, want 0", code)
	}
}

// diff --since with no matching snapshot must exit 0.
// Issue #14 deferred: "no data" exits 0 in this path — lock it here.
func TestDiffSince_NoMatchingSnapshot_Exits0(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// Enroll with a snapshot under a different label.
	runWith(t, home, []string{"runecho-ir", "snapshot", "--label=other", dir})

	// --since=missing-label → no snapshot found → must exit 0 (not 1).
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "diff", "--since=missing-label", dir})
	if code != 0 {
		t.Fatalf("diff --since no-match: got code %d, want 0 (issue #14 deferred inconsistency)", code)
	}
	if !strings.Contains(stderr, "No snapshot found") {
		t.Errorf("stderr %q: expected \"No snapshot found\"", stderr)
	}
}

// diff --since when the repo is not enrolled must exit 0.
// Issue #14 deferred: "not enrolled" also exits 0 — lock it here.
func TestDiffSince_UnenrolledRepo_Exits0(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// No enrollment, no snapshots.
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "diff", "--since=manual", dir})
	if code != 0 {
		t.Fatalf("diff --since unenrolled: got code %d, want 0 (issue #14 deferred inconsistency)", code)
	}
	if !strings.Contains(stderr, "not enrolled") {
		t.Errorf("stderr %q: expected \"not enrolled\"", stderr)
	}
}

// ---------------------------------------------------------------------------
// diff two-ID with missing snapshot (exits 1)
// ---------------------------------------------------------------------------

// diff <id-a> <id-b> where one ID doesn't exist must exit 1.
// This is the correct non-zero side of the exit-code inconsistency: two-ID mode
// treats a missing snapshot as an error (unlike --since, which exits 0).
func TestDiffTwoID_MissingSnapshot_Exits1(t *testing.T) {
	home := t.TempDir()
	// Non-existent snapshot IDs — no DB rows exist.
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "diff", "9999", "9998"})
	if code != 1 {
		t.Fatalf("diff two-ID missing: got code %d, want 1", code)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("stderr %q: expected \"not found\"", stderr)
	}
}

// ---------------------------------------------------------------------------
// cross-repo diff refusal (correctness-critical)
// ---------------------------------------------------------------------------

// diff <id-a> <id-b> across different enrolled repos must exit 1 and print the
// refusal message. This is correctness-critical: cross-repo diffs are semantically
// invalid (main.go ~:482).
func TestDiffCrossRepo_Refused(t *testing.T) {
	home := t.TempDir()
	dirA := t.TempDir()
	dirB := t.TempDir()
	irGitInit(t, dirA)
	irGitInit(t, dirB)

	// Create a snapshot in repo A.
	code, out, _ := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=s", dirA})
	if code != 0 {
		t.Fatalf("snapshot A: code %d, out=%q", code, out)
	}

	// Create a snapshot in repo B.
	code, out, _ = runWith(t, home, []string{"runecho-ir", "snapshot", "--label=s", dirB})
	if code != 0 {
		t.Fatalf("snapshot B: code %d, out=%q", code, out)
	}

	// Retrieve the snapshot IDs via log.
	_, logA, _ := runWith(t, home, []string{"runecho-ir", "log", dirA})
	_, logB, _ := runWith(t, home, []string{"runecho-ir", "log", dirB})

	idA := parseFirstSnapshotID(logA)
	idB := parseFirstSnapshotID(logB)
	if idA == "" || idB == "" {
		t.Fatalf("could not parse snapshot IDs from log output: A=%q B=%q", logA, logB)
	}
	if idA == idB {
		t.Fatalf("both repos produced the same snapshot ID %q — test setup is wrong", idA)
	}

	code, _, stderr := runWith(t, home, []string{"runecho-ir", "diff", idA, idB})
	if code != 1 {
		t.Fatalf("cross-repo diff: got code %d, want 1", code)
	}
	if !strings.Contains(stderr, "Refusing cross-repo diff") {
		t.Errorf("stderr %q: expected \"Refusing cross-repo diff\"", stderr)
	}
}

// parseFirstSnapshotID returns the ID string from the first data row of a
// `runecho-ir log` output table (format: "ID  LABEL  SESSION  ...").
func parseFirstSnapshotID(logOut string) string {
	for _, line := range strings.Split(logOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "ID") || strings.HasPrefix(line, "-") {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			return fields[0]
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// verify basic flow
// ---------------------------------------------------------------------------

func TestVerify_HappyPath(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// Create a session-start snapshot.
	code, _, _ := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=session-start", dir})
	if code != 0 {
		t.Fatalf("snapshot: %d", code)
	}

	code, out, _ := runWith(t, home, []string{"runecho-ir", "verify", dir})
	if code != 0 {
		t.Fatalf("verify: got code %d, want 0", code)
	}
	if !strings.Contains(out, "Verifying against snapshot") {
		t.Errorf("stdout %q: expected \"Verifying against snapshot\"", out)
	}
}

// verify with no session-start snapshot must exit 0 and inform the user.
// Issue #14 deferred: "no snapshot" exits 0 — lock it.
func TestVerify_NoSnapshot_Exits0(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	code, out, _ := runWith(t, home, []string{"runecho-ir", "verify", dir})
	if code != 0 {
		t.Fatalf("verify no snapshot: got code %d, want 0 (issue #14 deferred inconsistency)", code)
	}
	if !strings.Contains(out, "No session-start snapshot found") {
		t.Errorf("stdout %q: expected \"No session-start snapshot found\"", out)
	}
}
