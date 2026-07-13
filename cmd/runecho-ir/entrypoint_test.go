package main

// Entrypoint tests for runecho-ir. Each test sets os.Args and RUNECHO_HOME,
// then calls run() directly — no subprocess, no process exit. Coverage locks
// the exit-code contract (issue #14) and the correctness-critical cross-repo
// diff refusal. Contract: ExitOK=0, ExitNoData=1, ExitError=2.
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

	"github.com/inth3shadows/runecho/internal/ir"

	"github.com/inth3shadows/runecho/internal/snapshot"
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
	if code != ExitError {
		t.Fatalf("unknown flag: got code %d, want %d (ExitError)", code, ExitError)
	}
	if !strings.Contains(stderr, "unknown flag") {
		t.Errorf("stderr %q does not mention \"unknown flag\"", stderr)
	}
}

// F4: a bare argument is a root-path by contract (`runecho-ir [root-path]`), so a
// mistyped subcommand (`runecho-ir snpashot`) lands in runIndex as a nonexistent
// path. It must fail fast (ExitError, no output written) — NOT warn-and-exit-0
// while creating a stray <arg>/.ai/ir.json, which is a typo masquerading as
// success. The fix validates root existence up front, without breaking the real
// `runecho-ir <dir>` contract (covered by TestBareRootPath_RealDirIndexes).
func TestBareRootPath_NonexistentErrors(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	typo := filepath.Join(work, "snpashot") // does not exist

	code, out, stderr := runWith(t, home, []string{"runecho-ir", typo})
	if code != ExitError {
		t.Fatalf("nonexistent root: got code %d, want %d (ExitError)", code, ExitError)
	}
	if strings.Contains(out, "Indexed") {
		t.Errorf("must not report a successful index for a nonexistent root; stdout=%q", out)
	}
	if !strings.Contains(stderr, "does not exist") {
		t.Errorf("stderr %q should explain the path does not exist", stderr)
	}
	// The stray-directory side effect must be gone: no <typo>/.ai created.
	if _, err := os.Stat(filepath.Join(typo, ".ai")); !os.IsNotExist(err) {
		t.Errorf("a nonexistent root must not create a stray %s/.ai directory", typo)
	}
}

// F4 contract preservation: a REAL directory argument must still index (exit 0),
// so the root-existence guard does not regress `runecho-ir <dir>`.
func TestBareRootPath_RealDirIndexes(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runWith(t, home, []string{"runecho-ir", dir})
	if code != 0 {
		t.Fatalf("real dir index: got code %d, want 0", code)
	}
	if !strings.Contains(out, "Indexed") {
		t.Errorf("stdout %q: expected an \"Indexed\" line for a real directory", out)
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
	// No repos enrolled — rm must exit ExitError (bad reference) and report "No repo named".
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "repo", "rm", "does-not-exist"})
	if code != ExitError {
		t.Fatalf("repo rm missing: got code %d, want %d (ExitError)", code, ExitError)
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

// poisonSymbolName loads the IR at irPath, renames the first symbol named
// oldName in file's entry to newName, and saves it back. Used to inject a
// distinguishable marker into an on-disk IR without touching the underlying
// source file, so a subsequent incremental reindex can be proven to have
// reused (not re-parsed) that file's entry.
func poisonSymbolName(t *testing.T, irPath, file, oldName, newName string) {
	t.Helper()
	doc, err := ir.Load(irPath)
	if err != nil {
		t.Fatalf("load %s to poison: %v", irPath, err)
	}
	fileIR, ok := doc.Files[file]
	if !ok {
		t.Fatalf("%s has no entry for %q (have: %v)", irPath, file, doc.Files)
	}
	found := false
	for i := range fileIR.Symbols {
		if fileIR.Symbols[i].Name == oldName {
			fileIR.Symbols[i].Name = newName
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no symbol named %q in %q to poison (symbols: %+v)", oldName, file, fileIR.Symbols)
	}
	doc.Files[file] = fileIR
	if err := doc.Save(irPath); err != nil {
		t.Fatalf("save poisoned %s: %v", irPath, err)
	}
}

// hasSymbolName reports whether irPath's entry for file contains a symbol
// with the given name.
func hasSymbolName(t *testing.T, irPath, file, name string) bool {
	t.Helper()
	doc, err := ir.Load(irPath)
	if err != nil {
		t.Fatalf("load %s: %v", irPath, err)
	}
	fileIR, ok := doc.Files[file]
	if !ok {
		return false
	}
	for _, s := range fileIR.Symbols {
		if s.Name == name {
			return true
		}
	}
	return false
}

// TestRepoReindex_Incremental proves doReindex/buildIR actually take the
// incremental Update() path (issue #92) when a prior .ai/ir.json is present,
// rather than always doing a full Generate(). Uses a "poison the cache"
// pattern: hand-edit the on-disk IR for an unchanged file so its entry
// carries a distinguishable marker, reindex without touching that file's
// source, and assert the marker survived (proves reuse-by-hash-match, not a
// fresh re-parse that would have recomputed the real symbol name). Then
// change the file's actual content and reindex again, asserting the marker
// is gone and the freshly parsed symbols are present (proves changed files
// still get re-parsed, so the incremental path isn't silently stale).
func TestRepoReindex_Incremental(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// "repo add" auto-reindexes once, so a prior .ai/ir.json already exists
	// on disk by the time we poison it.
	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "add", dir})
	if code != 0 {
		t.Fatalf("repo add: %d", code)
	}
	name := strings.Fields(out)[1]

	irPath := filepath.Join(dir, ".ai", "ir.json")
	poisonSymbolName(t, irPath, "stub.go", "Hello", "HelloPoisoned")

	// Reindex without touching stub.go: the incremental path must reuse the
	// unchanged file's entry verbatim, so the poisoned marker should survive.
	code, out, _ = runWith(t, home, []string{"runecho-ir", "repo", "reindex", name})
	if code != 0 {
		t.Fatalf("repo reindex (poisoned, unchanged source): got code %d, want 0", code)
	}
	if !strings.Contains(out, "Reindexed") {
		t.Errorf("stdout %q: expected \"Reindexed\"", out)
	}
	if !hasSymbolName(t, irPath, "stub.go", "HelloPoisoned") {
		t.Errorf("poisoned marker did not survive reindex: unchanged file was re-parsed instead of reused")
	}

	// Now actually change stub.go's content. A real edit must still be
	// re-parsed: the poisoned marker must NOT survive once the hash changes.
	newSrc := "package stub\n\nfunc Hello() {}\n\nfunc World() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "stub.go"), []byte(newSrc), 0o644); err != nil {
		t.Fatalf("rewrite stub.go: %v", err)
	}
	code, out, _ = runWith(t, home, []string{"runecho-ir", "repo", "reindex", name})
	if code != 0 {
		t.Fatalf("repo reindex (changed source): got code %d, want 0", code)
	}
	if hasSymbolName(t, irPath, "stub.go", "HelloPoisoned") {
		t.Errorf("poisoned marker survived after source changed: file was reused instead of re-parsed")
	}
	if !hasSymbolName(t, irPath, "stub.go", "Hello") {
		t.Errorf("expected fresh parse of changed stub.go to recover symbol \"Hello\"")
	}
	if !hasSymbolName(t, irPath, "stub.go", "World") {
		t.Errorf("expected fresh parse of changed stub.go to find new symbol \"World\"")
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

// diff --since with a matching snapshot must exit 0 (ExitOK).
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

// diff --since with no matching snapshot must exit ExitNoData (1).
func TestDiffSince_NoMatchingSnapshot_Exits1(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// Enroll with a snapshot under a different label.
	runWith(t, home, []string{"runecho-ir", "snapshot", "--label=other", dir})

	// --since=missing-label → no snapshot found → ExitNoData.
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "diff", "--since=missing-label", dir})
	if code != ExitNoData {
		t.Fatalf("diff --since no-match: got code %d, want %d (ExitNoData)", code, ExitNoData)
	}
	if !strings.Contains(stderr, "No snapshot found") {
		t.Errorf("stderr %q: expected \"No snapshot found\"", stderr)
	}
}

// diff --since when the repo is not enrolled must exit ExitNoData (1).
func TestDiffSince_UnenrolledRepo_Exits1(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// No enrollment, no snapshots.
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "diff", "--since=manual", dir})
	if code != ExitNoData {
		t.Fatalf("diff --since unenrolled: got code %d, want %d (ExitNoData)", code, ExitNoData)
	}
	if !strings.Contains(stderr, "not enrolled") {
		t.Errorf("stderr %q: expected \"not enrolled\"", stderr)
	}
}

// ---------------------------------------------------------------------------
// diff two-ID with missing snapshot (exits 1)
// ---------------------------------------------------------------------------

// diff <id-a> <id-b> where one ID doesn't exist must exit ExitError (2).
// An explicit snapshot ID that doesn't exist is a hard error (bad reference), not no-data.
func TestDiffTwoID_MissingSnapshot_Exits2(t *testing.T) {
	home := t.TempDir()
	// Non-existent snapshot IDs — no DB rows exist.
	code, _, stderr := runWith(t, home, []string{"runecho-ir", "diff", "9999", "9998"})
	if code != ExitError {
		t.Fatalf("diff two-ID missing: got code %d, want %d (ExitError)", code, ExitError)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("stderr %q: expected \"not found\"", stderr)
	}
}

// ---------------------------------------------------------------------------
// cross-repo diff refusal (correctness-critical)
// ---------------------------------------------------------------------------

// diff <id-a> <id-b> across different enrolled repos must exit ExitError (2) and print the
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
	if code != ExitError {
		t.Fatalf("cross-repo diff: got code %d, want %d (ExitError)", code, ExitError)
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

// verify when repo is not enrolled must exit ExitNoData (1) and report on stderr.
func TestVerify_NotEnrolled_Exits1(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	code, _, stderr := runWith(t, home, []string{"runecho-ir", "verify", dir})
	if code != ExitNoData {
		t.Fatalf("verify not-enrolled: got code %d, want %d (ExitNoData)", code, ExitNoData)
	}
	if !strings.Contains(stderr, "not enrolled") {
		t.Errorf("stderr %q: expected \"not enrolled\"", stderr)
	}
}

// verify with an enrolled repo but no session-start snapshot must exit ExitNoData (1).
func TestVerify_EnrolledNoSessionStart_Exits1(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	// Enroll the repo without creating a session-start snapshot.
	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "add", dir})
	if code != 0 {
		t.Fatalf("repo add: code %d, out=%q", code, out)
	}

	code, out, _ = runWith(t, home, []string{"runecho-ir", "verify", dir})
	if code != ExitNoData {
		t.Fatalf("verify enrolled no session-start: got code %d, want %d (ExitNoData)", code, ExitNoData)
	}
	if !strings.Contains(out, "No session-start snapshot found") {
		t.Errorf("stdout %q: expected \"No session-start snapshot found\"", out)
	}
}

// ---------------------------------------------------------------------------
// Worktree identity e2e tests (issue #7)
// ---------------------------------------------------------------------------

// irGitInitWithCommit creates a git repo with one commit so git worktree add works.
func irGitInitWithCommit(t *testing.T, dir string) {
	t.Helper()
	irGitInit(t, dir)
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-m", "init")
}

// gitCmd runs a git subcommand in dir and fatals on error.
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// gitWorktreeAdd creates a linked worktree at linkedDir with a new branch derived
// from the repo's current HEAD (avoids needing a ref to exist first).
func gitWorktreeAdd(t *testing.T, mainDir, linkedDir string) {
	t.Helper()
	gitCmd(t, mainDir, "worktree", "add", linkedDir, "-b", filepath.Base(linkedDir))
}

// enrolledRepoCount returns the number of data rows in `runecho-ir repo list` output.
func enrolledRepoCount(t *testing.T, home string) int {
	t.Helper()
	_, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "list"})
	count := 0
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "NAME") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "No repos") {
			continue
		}
		count++
	}
	return count
}

// TestWorktree_LinkedWorktreeParity proves snapshot/diff/verify all resolve the
// enrolled repo from a linked worktree without creating a duplicate enrollment.
func TestWorktree_LinkedWorktreeParity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	mainDir := t.TempDir()
	linkedDir := filepath.Join(t.TempDir(), "linked") // worktree add needs the leaf to not exist

	irGitInitWithCommit(t, mainDir)
	gitWorktreeAdd(t, mainDir, linkedDir)

	// Enroll from the main worktree.
	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "add", mainDir})
	if code != 0 {
		t.Fatalf("repo add from main: code %d, out=%q", code, out)
	}

	// snapshot from linked worktree — must resolve the same enrolled repo.
	code, out, _ = runWith(t, home, []string{"runecho-ir", "snapshot", "--label=session-start", linkedDir})
	if code != 0 {
		t.Fatalf("snapshot from linked worktree: code %d, out=%q", code, out)
	}
	if !strings.Contains(out, "Snapshot saved") {
		t.Errorf("snapshot output %q: expected \"Snapshot saved\"", out)
	}

	// diff --since from linked worktree — must work (exit 0, same repo's snapshots).
	code, _, _ = runWith(t, home, []string{"runecho-ir", "diff", "--since=session-start", linkedDir})
	if code != 0 {
		t.Fatalf("diff --since from linked worktree: got code %d, want 0", code)
	}

	// verify from linked worktree — must find the session-start snapshot.
	code, out, _ = runWith(t, home, []string{"runecho-ir", "verify", linkedDir})
	if code != 0 {
		t.Fatalf("verify from linked worktree: got code %d, want 0", code)
	}
	if !strings.Contains(out, "Verifying against snapshot") {
		t.Errorf("verify output %q: expected \"Verifying against snapshot\"", out)
	}

	// No duplicate enrollment — still exactly 1 repo in the store.
	if got := enrolledRepoCount(t, home); got != 1 {
		t.Errorf("enrolled repo count = %d, want 1 (no duplicate from linked-worktree snapshot)", got)
	}
}

// TestWorktree_NoDuplicateEnrollment verifies that auto-enroll from a linked
// worktree of an already-enrolled repo reuses the existing enrollment.
func TestWorktree_NoDuplicateEnrollment(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	mainDir := t.TempDir()
	linkedDir := filepath.Join(t.TempDir(), "linked2")

	irGitInitWithCommit(t, mainDir)
	gitWorktreeAdd(t, mainDir, linkedDir)

	// Enroll the main worktree explicitly.
	code, _, _ := runWith(t, home, []string{"runecho-ir", "repo", "add", mainDir})
	if code != 0 {
		t.Fatalf("repo add: code %d", code)
	}

	// snapshot from linked worktree triggers auto-enroll path — must reuse existing.
	code, out, _ := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=manual", linkedDir})
	if code != 0 {
		t.Fatalf("snapshot from linked worktree: code %d, out=%q", code, out)
	}

	if got := enrolledRepoCount(t, home); got != 1 {
		t.Errorf("enrolled repo count = %d after linked-worktree snapshot, want 1", got)
	}
}

// TestWorktree_AutoEnrollUsesTopLevel verifies that first-time auto-enroll from
// a subdir uses the git top-level path, not the literal cwd, as the enrolled path.
// This ensures future worktree lookups find the same enrollment.
func TestWorktree_AutoEnrollUsesTopLevel(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	sub := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// snapshot from a subdir — auto-enrolls at the git top-level, not sub.
	code, out, _ := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=s", sub})
	if code != 0 {
		t.Fatalf("snapshot from subdir: code %d, out=%q", code, out)
	}

	// The enrolled path must be the git top-level (dir), not the subdir.
	_, listOut, _ := runWith(t, home, []string{"runecho-ir", "repo", "list"})
	if strings.Contains(listOut, sub) {
		t.Errorf("repo list contains subdir %q — enrolled path should be the git top-level %q\nlist:\n%s", sub, dir, listOut)
	}
	if !strings.Contains(listOut, dir) {
		t.Errorf("repo list does not contain git top-level %q\nlist:\n%s", dir, listOut)
	}
}

// ---------------------------------------------------------------------------
// guard-stats
// ---------------------------------------------------------------------------

// TestGuardStats writes a decisions.jsonl fixture directly into a temp
// RUNECHO_HOME and asserts the report surfaces the expected repo/symbol names.
func TestGuardStats(t *testing.T) {
	home := t.TempDir()
	fixture := `{"v":1,"ts":"2026-06-01T10:00:00Z","mode":"hook","repo":"runecho","file":"a.go","lang":"go","decision":"ask","reason":"unresolved-symbol","symbols":["FooBar"]}
{"v":1,"ts":"2026-06-01T10:05:00Z","mode":"hook","repo":"runecho","file":"b.go","lang":"go","decision":"defer","reason":"parse-fail"}
{"v":1,"ts":"2026-06-01T10:10:00Z","mode":"hook","repo":"runecho","file":"a.go","lang":"go","decision":"outcome","reason":"approved"}
`
	if err := os.WriteFile(filepath.Join(home, "decisions.jsonl"), []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	code, out, _ := runWith(t, home, []string{"runecho-ir", "guard-stats", "--days=3650"})
	if code != ExitOK {
		t.Fatalf("guard-stats: got code %d, want %d (ExitOK)", code, ExitOK)
	}
	for _, want := range []string{"runecho", "FooBar", "parse-fail"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %q: expected to contain %q", out, want)
		}
	}
	if strings.Contains(out, "approved") {
		t.Errorf("stdout %q: outcome record's reason should not appear in the report", out)
	}
}

// TestGuardStats_NonPositiveDays verifies --days=0 and a negative --days are
// rejected as a usage error rather than silently producing an empty (but
// "successful") report — since(now) or since(future) would otherwise exclude
// every decision without signaling that the flag value itself was the cause.
func TestGuardStats_NonPositiveDays(t *testing.T) {
	home := t.TempDir()
	fixture := `{"v":1,"ts":"2026-06-01T10:00:00Z","mode":"hook","repo":"runecho","decision":"ask","reason":"unresolved-symbol","symbols":["FooBar"]}
`
	if err := os.WriteFile(filepath.Join(home, "decisions.jsonl"), []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	for _, days := range []string{"0", "-1"} {
		code, _, stderr := runWith(t, home, []string{"runecho-ir", "guard-stats", "--days=" + days})
		if code != ExitError {
			t.Errorf("guard-stats --days=%s: got code %d, want %d (ExitError)", days, code, ExitError)
		}
		if !strings.Contains(stderr, "must be positive") {
			t.Errorf("guard-stats --days=%s: stderr %q, expected a usage error mentioning \"must be positive\"", days, stderr)
		}
	}
}

// TestGuardStats_NoLog verifies the friendly no-data message and ExitNoData
// exit code when decisions.jsonl doesn't exist yet.
func TestGuardStats_NoLog(t *testing.T) {
	home := t.TempDir()

	code, _, stderr := runWith(t, home, []string{"runecho-ir", "guard-stats"})
	if code != ExitNoData {
		t.Fatalf("guard-stats no log: got code %d, want %d (ExitNoData)", code, ExitNoData)
	}
	if !strings.Contains(stderr, "No decisions recorded yet.") {
		t.Errorf("stderr %q: expected \"No decisions recorded yet.\"", stderr)
	}
}

// TestRepoAdd_RelativeSourceRootStoredAbsolute pins the F41/F98 fix: a relative
// --source-root was stored verbatim, so a later reindex resolved it against its
// own CWD and silently walked the wrong tree. Enroll must pin it absolute.
func TestRepoAdd_RelativeSourceRootStoredAbsolute(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	sub := filepath.Join(dir, "src")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	// Run with CWD = dir so the relative path is meaningful at enroll time.
	origWD, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWD)

	code, _, _ := runWith(t, home, []string{"runecho-ir", "repo", "add", "--name", "relsrc", "--source-root", "src", dir})
	if code != 0 {
		t.Fatalf("repo add: got code %d, want 0", code)
	}

	withHome(home, func() {
		db, err := snapshot.Open(filepath.Join(home, "history.db"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer db.Close()
		repo, err := db.GetRepoByName("relsrc")
		if err != nil || repo == nil {
			t.Fatalf("get repo: %v %v", repo, err)
		}
		if !filepath.IsAbs(repo.SourceRoot) {
			t.Errorf("stored source_root = %q, want absolute", repo.SourceRoot)
		}
	})
}

// TestRepoAdd_FailedInitialIndexExitsNonzero pins the F42 fix: repo add
// discarded doReindex's exit code, so an enroll whose initial index failed
// still exited 0 — scripts had no way to notice there is no snapshot behind
// the "Enrolled" line.
func TestRepoAdd_FailedInitialIndexExitsNonzero(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	missing := filepath.Join(dir, "does-not-exist")

	code, _, _ := runWith(t, home, []string{"runecho-ir", "repo", "add", "--name", "badsrc", "--no-hooks", "--source-root", missing, dir})
	if code == 0 {
		t.Fatalf("repo add with a failing initial index exited 0; want nonzero")
	}
}

// TestInstall_AllHooksSkippedExitsNonzeroAndSaysSo pins the F30/F33/F34 fix:
// when every hook is skipped (existing non-runecho hooks, no --force), install
// printed "Hooks installed" and exited 0 — success theater while the guard is
// not actually active. It must say no hooks were installed and exit soft-fail.
func TestInstall_AllHooksSkippedExitsNonzeroAndSaysSo(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)

	hooksDir := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"pre-commit", "post-commit", "post-merge", "post-checkout"} {
		if err := os.WriteFile(filepath.Join(hooksDir, h), []byte("#!/bin/sh\n# husky, not ours\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	code, out, _ := runWith(t, home, []string{"runecho-ir", "install", dir})
	if code == 0 {
		t.Fatalf("install with all hooks skipped exited 0; want nonzero (soft)")
	}
	if strings.Contains(out, "Hooks installed in") {
		t.Errorf("output still claims success: %q", out)
	}
	if !strings.Contains(out, "No hooks installed") {
		t.Errorf("output should state no hooks were installed: %q", out)
	}
}
