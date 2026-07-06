package main

// exitcode_test.go — exit-code contract tests for subcommands not covered by
// entrypoint_test.go: map, truth-trail, churn, log, diff (two-ID happy path),
// backup. All tests call run() via the runWith() helper from entrypoint_test.go
// so no subprocess is spawned and no os.Exit is called.
//
// Contract: ExitOK=0, ExitNoData=1, ExitError=2.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── runMap ──────────────────────────────────────────────────────────────────

// TestMap_BadFlag_Exits2 covers the parseSub (ContinueOnError) seam that
// mapcmd.go wires up: an unrecognised flag must return ExitError from run()
// without calling os.Exit.
func TestMap_BadFlag_Exits2(t *testing.T) {
	home := t.TempDir()
	code, _, _ := runWith(t, home, []string{"runecho-ir", "map", "--nope"})
	if code != ExitError {
		t.Fatalf("map bad flag: got code %d, want %d (ExitError)", code, ExitError)
	}
}

// TestMap_HappyPath_Exits0 verifies that map over a small git-init'd repo exits
// 0. The repo need not be enrolled (map builds a fresh IR and does not require
// snapshots).
func TestMap_HappyPath_Exits0(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	code, _, _ := runWith(t, home, []string{"runecho-ir", "map", dir})
	if code != ExitOK {
		t.Fatalf("map happy path: got code %d, want %d (ExitOK)", code, ExitOK)
	}
}

// ─── runTruthTrail ───────────────────────────────────────────────────────────

// TestTruthTrail_NotEnrolled_Exits1 exercises the first early-return in
// runTruthTrail: a repo not in the store → ExitNoData.
func TestTruthTrail_NotEnrolled_Exits1(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "truth-trail", dir})
	if code != ExitNoData {
		t.Fatalf("truth-trail not-enrolled: got code %d, want %d (ExitNoData)", code, ExitNoData)
	}
	if !strings.Contains(stderr, "not enrolled") {
		t.Errorf("stderr %q: expected \"not enrolled\"", stderr)
	}
}

// TestTruthTrail_NoBaselineSnapshot_Exits1 exercises the "no snapshot with
// the requested label" branch: the repo is enrolled but no session-start
// snapshot exists → ExitNoData.
func TestTruthTrail_NoBaselineSnapshot_Exits1(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// Enroll without creating any snapshot.
	code, _, _ := runWith(t, home, []string{"runecho-ir", "repo", "add", dir})
	if code != 0 {
		t.Fatalf("repo add: code %d", code)
	}

	code, _, stderr := runWith(t, home, []string{"runecho-ir", "truth-trail", dir})
	if code != ExitNoData {
		t.Fatalf("truth-trail no-snapshot: got code %d, want %d (ExitNoData)", code, ExitNoData)
	}
	if !strings.Contains(stderr, "No snapshot found") {
		t.Errorf("stderr %q: expected \"No snapshot found\"", stderr)
	}
}

// TestTruthTrail_BadTextPath_Exits2 exercises the os.ReadFile failure path:
// the repo is enrolled and has a baseline snapshot, but --text points to a
// file that does not exist → ExitError.
func TestTruthTrail_BadTextPath_Exits2(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// Create a session-start snapshot so we pass the enrollment/snapshot checks.
	code, _, _ := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=session-start", dir})
	if code != 0 {
		t.Fatalf("snapshot: code %d", code)
	}

	badText := filepath.Join(t.TempDir(), "does-not-exist.txt")
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "truth-trail", "--text", badText, dir})
	if code != ExitError {
		t.Fatalf("truth-trail bad-text: got code %d, want %d (ExitError)", code, ExitError)
	}
	if !strings.Contains(stderr, "cannot read text file") {
		t.Errorf("stderr %q: expected \"cannot read text file\"", stderr)
	}
}

// TestTruthTrail_HappyPath_Exits0 verifies the clean path: enrolled repo,
// session-start snapshot present, no --text → ExitOK.
func TestTruthTrail_HappyPath_Exits0(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	code, _, _ := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=session-start", dir})
	if code != 0 {
		t.Fatalf("snapshot: code %d", code)
	}

	code, _, _ = runWith(t, home, []string{"runecho-ir", "truth-trail", dir})
	if code != ExitOK {
		t.Fatalf("truth-trail happy path: got code %d, want %d (ExitOK)", code, ExitOK)
	}
}

// ─── runChurn ────────────────────────────────────────────────────────────────

// TestChurn_NotEnrolled_Exits1 verifies the early-return when the repo is not
// in the store.
func TestChurn_NotEnrolled_Exits1(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "churn", dir})
	if code != ExitNoData {
		t.Fatalf("churn not-enrolled: got code %d, want %d (ExitNoData)", code, ExitNoData)
	}
	if !strings.Contains(stderr, "not enrolled") {
		t.Errorf("stderr %q: expected \"not enrolled\"", stderr)
	}
}

// ─── runLog ──────────────────────────────────────────────────────────────────

// TestLog_NotEnrolled_Exits1 verifies that log exits ExitNoData and prints
// a helpful message when the repo is not enrolled.
func TestLog_NotEnrolled_Exits1(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "log", dir})
	if code != ExitNoData {
		t.Fatalf("log not-enrolled: got code %d, want %d (ExitNoData)", code, ExitNoData)
	}
	if !strings.Contains(stderr, "not enrolled") {
		t.Errorf("stderr %q: expected \"not enrolled\"", stderr)
	}
}

// TestLog_HappyPath_Exits0 verifies that log over an enrolled repo with at
// least one snapshot exits ExitOK and prints a populated table.
// (repo add auto-reindexes, so it always creates a snapshot — the "no
// snapshots" state is not reachable via the CLI, only via direct DB writes.)
func TestLog_HappyPath_Exits0(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// snapshot auto-enrolls and saves one snapshot.
	code, _, _ := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=test", dir})
	if code != 0 {
		t.Fatalf("snapshot: code %d", code)
	}

	code, out, _ := runWith(t, home, []string{"runecho-ir", "log", dir})
	if code != ExitOK {
		t.Fatalf("log happy path: got code %d, want %d (ExitOK)", code, ExitOK)
	}
	if !strings.Contains(out, "LABEL") {
		t.Errorf("stdout %q: expected table header with \"LABEL\"", out)
	}
	if !strings.Contains(out, "test") {
		t.Errorf("stdout %q: expected label \"test\" in output", out)
	}
}

// ─── runDiff two-ID happy path ────────────────────────────────────────────────

// TestDiffTwoID_HappyPath_Exits0 verifies that diff <id-a> <id-b> over two
// snapshots in the same enrolled repo exits ExitOK.
func TestDiffTwoID_HappyPath_Exits0(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// Take two snapshots under different labels so they have distinct IDs.
	code, _, _ := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=a", dir})
	if code != 0 {
		t.Fatalf("snapshot a: code %d", code)
	}
	code, _, _ = runWith(t, home, []string{"runecho-ir", "snapshot", "--label=b", dir})
	if code != 0 {
		t.Fatalf("snapshot b: code %d", code)
	}

	_, logOut, _ := runWith(t, home, []string{"runecho-ir", "log", dir})
	ids := parseAllSnapshotIDs(logOut)
	if len(ids) < 2 {
		t.Fatalf("need ≥2 snapshot IDs from log, got %d:\n%s", len(ids), logOut)
	}

	code, _, _ = runWith(t, home, []string{"runecho-ir", "diff", ids[0], ids[1]})
	if code != ExitOK {
		t.Fatalf("diff two-ID happy path: got code %d, want %d (ExitOK)", code, ExitOK)
	}
}

// TestDiff_SinceEmptyLabel_Reachable pins the fix for `--since=""` being
// indistinguishable from an absent flag. A snapshot may legitimately carry an
// empty label (only "auto" is reserved by SaveSnapshot), so `diff --since=` must
// reach it via the since-mode. Before the fix, keying the mode on `*since != ""`
// routed the empty string to the two-ID positional mode, making the empty-label
// snapshot permanently unreachable (usage error, ExitError).
func TestDiff_SinceEmptyLabel_Reachable(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// Store a snapshot under an explicitly empty label (snapshot auto-enrolls).
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=", dir})
	if code != 0 {
		t.Fatalf("snapshot --label=: got code %d, stderr %q", code, stderr)
	}

	// diff --since= must reach the empty-label snapshot (live vs snapshot), not
	// fall through to the two-ID positional usage error.
	code, _, stderr = runWith(t, home, []string{"runecho-ir", "diff", "--since=", dir})
	if code != ExitOK {
		t.Fatalf("diff --since= (empty label): got code %d, want %d (ExitOK); stderr %q", code, ExitOK, stderr)
	}
	if strings.Contains(stderr, "Usage:") || strings.Contains(stderr, "No snapshot found") {
		t.Errorf("empty-label snapshot should be reachable via --since=; stderr %q", stderr)
	}
}

// parseAllSnapshotIDs returns every snapshot ID string from `runecho-ir log`
// output, in the order they appear (newest first per the log table). It is
// intentionally distinct from the existing parseFirstSnapshotID helper.
func parseAllSnapshotIDs(logOut string) []string {
	var ids []string
	for _, line := range strings.Split(logOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "ID") || strings.HasPrefix(line, "-") {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			ids = append(ids, fields[0])
		}
	}
	return ids
}

// ─── runBackup ───────────────────────────────────────────────────────────────

// TestBackup_HappyPath_Exits0 verifies that backup writes a copy of the store
// to a new file and exits ExitOK.
func TestBackup_HappyPath_Exits0(t *testing.T) {
	home := t.TempDir()
	dest := filepath.Join(t.TempDir(), "backup.db")

	code, out, _ := runWith(t, home, []string{"runecho-ir", "backup", dest})
	if code != ExitOK {
		t.Fatalf("backup: got code %d, want %d (ExitOK)", code, ExitOK)
	}
	if !strings.Contains(out, "Backup written") {
		t.Errorf("stdout %q: expected \"Backup written\"", out)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("backup file does not exist: %v", err)
	}
}

// TestBackup_DestExists_Exits2 verifies that backup refuses to overwrite an
// existing file (VACUUM INTO requires a new file) and exits ExitError.
func TestBackup_DestExists_Exits2(t *testing.T) {
	home := t.TempDir()
	dest := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(dest, []byte{}, 0o644); err != nil {
		t.Fatalf("create dest: %v", err)
	}

	code, _, stderr := runWith(t, home, []string{"runecho-ir", "backup", dest})
	if code != ExitError {
		t.Fatalf("backup existing dest: got code %d, want %d (ExitError)", code, ExitError)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("stderr %q: expected \"already exists\"", stderr)
	}
}
