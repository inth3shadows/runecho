package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #554: the scheduled reindex rotates its own log.

// logFixture creates a 0700 logs dir holding reindex.log with size bytes, and
// opens it for append the way cron's `>>` and launchd's StandardOutPath do.
func logFixture(t *testing.T, size int) (logPath string, out *os.File) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	logPath = filepath.Join(dir, "reindex.log")
	if err := os.WriteFile(logPath, bytes.Repeat([]byte("x"), size), 0600); err != nil {
		t.Fatal(err)
	}
	out = openAppend(t, logPath)
	return logPath, out
}

func openAppend(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func mustNotExist(t *testing.T, path, why string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%s exists (lstat err=%v): %s", path, err, why)
	}
}

func slurp(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

const testCap = 100

func TestRotateJobLog_AboveCapRotates(t *testing.T) {
	logPath, out := logFixture(t, testCap+1)
	var warn bytes.Buffer
	if got := rotateJobLog(logPath, out, testCap, &warn); got != rotated {
		t.Fatalf("outcome = %v, want rotated", got)
	}
	mustNotExist(t, logPath, "the scheduler, not runecho, creates the next log")
	if got := slurp(t, logPath+".1"); got != strings.Repeat("x", testCap+1) {
		t.Errorf(".1 holds %d bytes, want the original %d", len(got), testCap+1)
	}
	// The fd the scheduler opened follows the inode: this run's output lands
	// in .1, which is what makes renaming at start safe.
	if _, err := out.WriteString("after"); err != nil {
		t.Fatal(err)
	}
	if got := slurp(t, logPath+".1"); !strings.HasSuffix(got, "after") {
		t.Error("a write after the rename did not land in reindex.log.1")
	}
	if warn.Len() != 0 {
		t.Errorf("success printed %q; it should be silent", warn.String())
	}
}

func TestRotateJobLog_AtCapDoesNotRotate(t *testing.T) {
	logPath, out := logFixture(t, testCap)
	if got := rotateJobLog(logPath, out, testCap, &bytes.Buffer{}); got != rotateBelowCap {
		t.Errorf("outcome = %v, want rotateBelowCap at exactly the cap", got)
	}
	mustNotExist(t, logPath+".1", "nothing should rotate at the cap")
}

func TestRotateJobLog_BelowCapDoesNotRotate(t *testing.T) {
	logPath, out := logFixture(t, testCap-1)
	if got := rotateJobLog(logPath, out, testCap, &bytes.Buffer{}); got != rotateBelowCap {
		t.Errorf("outcome = %v, want rotateBelowCap", got)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("log gone below the cap: %v", err)
	}
}

func TestRotateJobLog_ReplacesOlderGeneration(t *testing.T) {
	logPath, out := logFixture(t, testCap+1)
	if err := os.WriteFile(logPath+".1", []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := rotateJobLog(logPath, out, testCap, &bytes.Buffer{}); got != rotated {
		t.Fatalf("outcome = %v, want rotated", got)
	}
	if got := slurp(t, logPath+".1"); got == "old" {
		t.Error(".1 still holds the older generation")
	}
	mustNotExist(t, logPath+".2", "only one generation is kept")
}

func TestRotateJobLog_SymlinkLogRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere.log")
	if err := os.WriteFile(target, bytes.Repeat([]byte("x"), testCap+1), 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "reindex.log")
	if err := os.Symlink(target, logPath); err != nil {
		t.Skipf("symlink: %v", err)
	}
	out := openAppend(t, logPath) // opened through the link, as a scheduler would
	var warn bytes.Buffer
	if got := rotateJobLog(logPath, out, testCap, &warn); got != rotateRefusedSymlink {
		t.Fatalf("outcome = %v, want rotateRefusedSymlink", got)
	}
	if fi, err := os.Lstat(logPath); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the log symlink was disturbed (err=%v)", err)
	}
	if got := slurp(t, target); len(got) != testCap+1 {
		t.Errorf("symlink target changed: %d bytes", len(got))
	}
	mustNotExist(t, logPath+".1", "a symlinked log must not be rotated")
	if !strings.Contains(warn.String(), "symlink") {
		t.Errorf("warning %q should say why", warn.String())
	}
}

func TestRotateJobLog_SymlinkRotatedRefused(t *testing.T) {
	logPath, out := logFixture(t, testCap+1)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, logPath+".1"); err != nil {
		t.Skipf("symlink: %v", err)
	}
	var warn bytes.Buffer
	if got := rotateJobLog(logPath, out, testCap, &warn); got != rotateRefusedSymlink {
		t.Fatalf("outcome = %v, want rotateRefusedSymlink", got)
	}
	if fi, err := os.Lstat(logPath + ".1"); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf(".1 symlink was replaced (err=%v)", err)
	}
	if got := slurp(t, victim); got != "keep me" {
		t.Errorf("victim changed: %q", got)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("log was moved despite the refusal: %v", err)
	}
	if !strings.Contains(warn.String(), "symlink") {
		t.Errorf("warning %q should say why", warn.String())
	}
}

func TestRotateJobLog_StdoutNotTheLog(t *testing.T) {
	logPath, _ := logFixture(t, testCap+1)
	other := filepath.Join(t.TempDir(), "other.log")
	if err := os.WriteFile(other, nil, 0600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	for name, out := range map[string]*os.File{"other file": openAppend(t, other), "pipe": w} {
		var warn bytes.Buffer
		if got := rotateJobLog(logPath, out, testCap, &warn); got != rotateNotOurLog {
			t.Errorf("%s: outcome = %v, want rotateNotOurLog", name, got)
		}
		if warn.Len() != 0 {
			t.Errorf("%s: printed %q; an interactive run must be silent", name, warn.String())
		}
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("log was rotated although stdout was not it: %v", err)
	}
}

func TestRotateJobLog_LogAbsent(t *testing.T) {
	dir := t.TempDir()
	out := openAppend(t, func() string {
		p := filepath.Join(dir, "stdout")
		os.WriteFile(p, nil, 0600)
		return p
	}())
	logPath := filepath.Join(dir, "logs", "reindex.log")
	if got := rotateJobLog(logPath, out, testCap, &bytes.Buffer{}); got != rotateNotOurLog {
		t.Errorf("outcome = %v, want rotateNotOurLog", got)
	}
	mustNotExist(t, logPath, "rotation must never create the log")
	mustNotExist(t, logPath+".1", "rotation must never create .1 from nothing")
}

// Two runs passing their checks before either renames: the loser's re-check
// right before its rename sees the log is no longer its stdout and backs off,
// and .1 keeps exactly the original bytes.
func TestRotateJobLog_RacingRotators(t *testing.T) {
	logPath, outA := logFixture(t, testCap+1)
	outB := openAppend(t, logPath)
	orig := rotateHook
	t.Cleanup(func() { rotateHook = orig })
	var bGot rotateOutcome
	fired := false
	rotateHook = func() {
		if fired {
			return
		}
		fired = true
		bGot = rotateJobLog(logPath, outB, testCap, &bytes.Buffer{})
	}
	aGot := rotateJobLog(logPath, outA, testCap, &bytes.Buffer{})
	if bGot != rotated || aGot != rotateNotOurLog {
		t.Errorf("outcomes: B=%v A=%v, want B rotated and A backing off (not our log any more)", bGot, aGot)
	}
	if got := slurp(t, logPath+".1"); got != strings.Repeat("x", testCap+1) {
		t.Errorf(".1 holds %d bytes, want exactly the original %d", len(got), testCap+1)
	}
	mustNotExist(t, logPath, "nothing recreates the log but the scheduler")
}

func TestRotateJobLog_RenameFailureIsFailOpen(t *testing.T) {
	logPath, out := logFixture(t, testCap+1)
	if err := os.MkdirAll(filepath.Join(logPath+".1", "blocker"), 0700); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	if got := rotateJobLog(logPath, out, testCap, &warn); got != rotateRenameFailed {
		t.Fatalf("outcome = %v, want rotateRenameFailed", got)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("log lost on a failed rename: %v", err)
	}
	if !strings.Contains(warn.String(), "could not rotate") {
		t.Errorf("warning %q should report the failure", warn.String())
	}
}

func TestRotateJobLog_WindowsSkipped(t *testing.T) {
	logPath, out := logFixture(t, testCap+1)
	orig := rotateGOOS
	t.Cleanup(func() { rotateGOOS = orig })
	rotateGOOS = "windows"
	if got := rotateJobLog(logPath, out, testCap, &bytes.Buffer{}); got != rotateUnsupportedOS {
		t.Errorf("outcome = %v, want rotateUnsupportedOS", got)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("log rotated on windows: %v", err)
	}
}

// runReindexAllIntoLog runs `repo reindex --all` with stdout and stderr pointed
// at home's scheduled log, as cron and launchd do, and returns the exit code.
func runReindexAllIntoLog(t *testing.T, home string) int {
	t.Helper()
	logPath := reindexLogFile(home)
	out := openAppend(t, logPath)
	origOut, origErr, origCap := os.Stdout, os.Stderr, reindexLogCap
	os.Stdout, os.Stderr, reindexLogCap = out, out, testCap
	defer func() { os.Stdout, os.Stderr, reindexLogCap = origOut, origErr, origCap }()
	var code int
	withHome(home, func() {
		withArgs([]string{"runecho-ir", "repo", "reindex", "--all"}, func() { code = run() })
	})
	return code
}

func scheduledLogHome(t *testing.T, prefill string) string {
	t.Helper()
	home := t.TempDir()
	root := t.TempDir()
	irGitInit(t, root)
	enrollAt(t, home, "alive", root)
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reindexLogFile(home), []byte(prefill), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestReindexAll_RotatesScheduledLog(t *testing.T) {
	prefill := strings.Repeat("old line\n", 20) // > testCap
	home := scheduledLogHome(t, prefill)
	if code := runReindexAllIntoLog(t, home); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	logPath := reindexLogFile(home)
	mustNotExist(t, logPath, "the run should have rotated it and written into .1")
	got := slurp(t, logPath+".1")
	if !strings.HasPrefix(got, prefill) || !strings.Contains(got, "alive") {
		t.Errorf(".1 = %q, want the prefill followed by this run's line for alive", got)
	}
}

func TestReindexAll_RotationFailureStillReindexes(t *testing.T) {
	home := scheduledLogHome(t, strings.Repeat("old line\n", 20))
	logPath := reindexLogFile(home)
	if err := os.MkdirAll(filepath.Join(logPath+".1", "blocker"), 0700); err != nil {
		t.Fatal(err)
	}
	if code := runReindexAllIntoLog(t, home); code != 0 {
		t.Fatalf("exit %d, want 0 — a failed rotation must not fail the job", code)
	}
	if got := slurp(t, logPath); !strings.Contains(got, "alive") {
		t.Errorf("log = %q, want this run's line for alive", got)
	}
}

// F2 (#557 review): if a scheduler spawn creates a fresh log after another run
// rotated, a run whose checks passed earlier must not rename that fresh file
// over .1 — that would throw away the generation just rotated.
func TestRotateJobLog_RecreatedLogIsNotRenamedOver1(t *testing.T) {
	logPath, out := logFixture(t, testCap+1)
	orig := rotateHook
	t.Cleanup(func() { rotateHook = orig })
	rotateHook = func() {
		// Another run rotates, then the next spawn's `>>` creates a fresh log.
		if err := os.Rename(logPath, logPath+".1"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(logPath, []byte("fresh"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if got := rotateJobLog(logPath, out, testCap, &bytes.Buffer{}); got != rotateNotOurLog {
		t.Errorf("outcome = %v, want rotateNotOurLog", got)
	}
	if got := slurp(t, logPath+".1"); len(got) != testCap+1 {
		t.Errorf(".1 holds %d bytes (%q...), want the rotated generation's %d", len(got), got[:min(len(got), 10)], testCap+1)
	}
	if got := slurp(t, logPath); got != "fresh" {
		t.Errorf("the fresh log was moved: %q", got)
	}
}

// F5 (#557 review): a small symlinked log is below the cap, so there is nothing
// to refuse yet; it must not print a warning into the log every hour.
func TestRotateJobLog_SmallSymlinkLogIsSilent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere.log")
	if err := os.WriteFile(target, []byte("small"), 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "reindex.log")
	if err := os.Symlink(target, logPath); err != nil {
		t.Skipf("symlink: %v", err)
	}
	var warn bytes.Buffer
	if got := rotateJobLog(logPath, openAppend(t, logPath), testCap, &warn); got != rotateBelowCap {
		t.Errorf("outcome = %v, want rotateBelowCap", got)
	}
	if warn.Len() != 0 {
		t.Errorf("printed %q for a small symlinked log; it should be silent until there is something to refuse", warn.String())
	}
}
