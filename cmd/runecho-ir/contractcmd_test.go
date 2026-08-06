// contractcmd_test.go — the `contract list|show|check` command paths.
//
// contract_test.go covers session-id resolution and the refusal paths, which is
// what the commands DECLINE to do. Nothing exercised what they do when they
// succeed: `runContractList`, `runContractShow`, `runContractCheck`,
// `loadContractByName`, `contractsDirFor`, `listContracts`, `changedPaths` and
// `shortHashDisplay` were all at 0.0% statement coverage while a contract test
// file existed, which reads as "covered" to anyone glancing at the tree.
//
// Everything here drives the real entry points against a real git repo and
// asserts on stdout and exit code — the two things a user actually sees. The
// `check` cases pass --contract, so they resolve without the snapshot DB and
// stay hermetic; the session-bound path through resolveCheckContract is
// deliberately left to the acceptance run recorded in the D2 plan, for the
// reason contract_test.go's header already gives.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/contract"
)

// contractRepo builds a git repo containing the given contract files, keyed by
// file name, and returns its root.
func contractRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if len(files) > 0 {
		cdir := filepath.Join(root, contract.Dir)
		if err := os.MkdirAll(cdir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(cdir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-07-10T12:00:00Z", "GIT_COMMITTER_DATE=2026-07-10T12:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

const scopedContract = `name: scoped
description: only the guts
internal/**
cmd/**
!internal/legacy/**
`

// scopedContractNamed is scopedContract under a different `name:` header, for
// the cases that need the contract name to diverge from its file name.
func scopedContractNamed(name string) string {
	return strings.Replace(scopedContract, "name: scoped", "name: "+name, 1)
}

// ---------------------------------------------------------------------------
// list
// ---------------------------------------------------------------------------

func TestContractList_Empty(t *testing.T) {
	root := contractRepo(t, nil)
	var code int
	stdout, _ := captureOutput(func() { code = runContractList([]string{"--dir", root}) })
	if code != ExitOK {
		t.Fatalf("code = %d, want ExitOK", code)
	}
	if !strings.Contains(stdout, "No contracts in") {
		t.Errorf("stdout = %q, want a no-contracts message", stdout)
	}
}

// Sorted by NAME, and each line carries the description and the pattern count —
// the count is what tells an author their globs actually parsed.
//
// The file names deliberately oppose the contract names: os.ReadDir yields
// a-file before z-file, so an unsorted listing prints zebra before apple and the
// sort is the only thing that can produce the asserted order. A fixture whose
// file order already matches its name order makes this assertion vacuous —
// deleting listContracts' sort.Slice then leaves the test green, which is the
// first version of this test and exactly the defect class this file exists to
// close.
func TestContractList_SortedByNameNotFileName(t *testing.T) {
	root := contractRepo(t, map[string]string{
		"a-file.contract": "name: zebra\ninternal/**\n",
		"z-file.contract": scopedContractNamed("apple"),
	})
	var code int
	stdout, _ := captureOutput(func() { code = runContractList([]string{"--dir", root}) })
	if code != ExitOK {
		t.Fatalf("code = %d, want ExitOK", code)
	}
	iApple := strings.Index(stdout, "apple")
	iZebra := strings.Index(stdout, "zebra")
	if iApple < 0 || iZebra < 0 {
		t.Fatalf("stdout missing a contract name: %q", stdout)
	}
	if iApple > iZebra {
		t.Errorf("listing is ordered by file name, not contract name:\n%s", stdout)
	}
	if !strings.Contains(stdout, "only the guts") {
		t.Errorf("description not shown: %q", stdout)
	}
	if !strings.Contains(stdout, "3 pattern(s)") {
		t.Errorf("pattern count wrong or missing (want 3): %q", stdout)
	}
}

// A dot-file in the contracts dir is skipped, not loaded as a contract.
func TestContractList_SkipsDotFiles(t *testing.T) {
	root := contractRepo(t, map[string]string{
		"real.contract": "name: real\ninternal/**\n",
		".hidden":       "name: hidden\ninternal/**\n",
	})
	var code int
	stdout, _ := captureOutput(func() { code = runContractList([]string{"--dir", root}) })
	if code != ExitOK {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(stdout, "hidden") {
		t.Errorf("dot-file was listed as a contract: %q", stdout)
	}
}

func TestContractList_NotAGitRepo(t *testing.T) {
	var code int
	_, stderr := captureOutput(func() { code = runContractList([]string{"--dir", t.TempDir()}) })
	if code == ExitOK {
		t.Fatal("listing outside a git repo returned ExitOK")
	}
	if !strings.Contains(stderr, "not a git repository") {
		t.Errorf("stderr does not say why: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// show
// ---------------------------------------------------------------------------

// Negated patterns must render differently from positive ones. They invert the
// meaning of a line, and a listing that showed both with the same prefix would
// be actively misleading about what is in scope.
func TestContractShow_RendersNegationDistinctly(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	var code int
	stdout, _ := captureOutput(func() { code = runContractShow([]string{"--dir", root, "scoped"}) })
	if code != ExitOK {
		t.Fatalf("code = %d, want ExitOK", code)
	}
	for _, want := range []string{"name:", "description:", "path:", "hash:", "+ internal/**", "- internal/legacy/**"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

// A contract is addressable by its file name as well as its `name:` header.
func TestContractShow_ResolvesByFileName(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	var code int
	stdout, _ := captureOutput(func() { code = runContractShow([]string{"--dir", root, "scoped.contract"}) })
	if code != ExitOK {
		t.Fatalf("code = %d, want ExitOK", code)
	}
	if !strings.Contains(stdout, "name:        scoped") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestContractShow_UnknownNameNamesIt(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	var code int
	_, stderr := captureOutput(func() { code = runContractShow([]string{"--dir", root, "nope"}) })
	if code == ExitOK {
		t.Fatal("unknown contract returned ExitOK")
	}
	if !strings.Contains(stderr, `"nope"`) {
		t.Errorf("stderr does not name the missing contract: %q", stderr)
	}
}

func TestContractShow_RequiresExactlyOneName(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	for _, args := range [][]string{{"--dir", root}, {"--dir", root, "a", "b"}} {
		var code int
		_, stderr := captureOutput(func() { code = runContractShow(args) })
		if code == ExitOK {
			t.Errorf("args %v returned ExitOK", args)
		}
		if !strings.Contains(stderr, "Usage:") {
			t.Errorf("args %v: stderr has no usage line: %q", args, stderr)
		}
	}
}

// ---------------------------------------------------------------------------
// check
// ---------------------------------------------------------------------------

// The working-tree case must see all three kinds of change. Missing any one of
// them turns `check` into a quiet pass on real work: staged-only changes are the
// normal state right before a commit, and a brand-new file is exactly the kind
// of scope drift the check exists to surface.
func TestContractCheck_WorkingTreeCoversModifiedStagedAndUntracked(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	for _, p := range []string{"internal/keep.go", "cmd/tool.go", "docs/notes.md"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte("one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")

	// modified (unstaged), in scope
	if err := os.WriteFile(filepath.Join(root, "internal/keep.go"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// staged, OUT of scope
	if err := os.WriteFile(filepath.Join(root, "docs/notes.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "docs/notes.md")
	// untracked, OUT of scope
	if err := os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var code int
	stdout, _ := captureOutput(func() {
		code = runContractCheck([]string{"--dir", root, "--contract", "scoped"})
	})
	if code != ExitError {
		t.Fatalf("code = %d, want ExitError (out-of-scope is a non-zero finding)", code)
	}
	if !strings.Contains(stdout, "3 changed file(s), 2 out of scope") {
		t.Errorf("summary line wrong — a missed change source would show here:\n%s", stdout)
	}
	for _, want := range []string{"! docs/notes.md", "! stray.txt"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "! internal/keep.go") {
		t.Errorf("an in-scope file was reported out of scope:\n%s", stdout)
	}
}

// Everything in scope exits zero, so the command composes into a hook.
//
// The contract file is committed first, deliberately. An UNCOMMITTED contract is
// itself an untracked path outside `internal/**`, so `check` reports it out of
// scope — correct (a contract is a real reviewed file in the repo, not a
// sidecar) but not the scenario this case is about.
func TestContractCheck_AllInScopeExitsZero(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "add contract")
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal/a.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var code int
	stdout, _ := captureOutput(func() {
		code = runContractCheck([]string{"--dir", root, "--contract", "scoped"})
	})
	if code != ExitOK {
		t.Fatalf("code = %d, want ExitOK", code)
	}
	if !strings.Contains(stdout, "0 out of scope") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestContractCheck_NoChangesSaysSo(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")

	var code int
	stdout, _ := captureOutput(func() {
		code = runContractCheck([]string{"--dir", root, "--contract", "scoped"})
	})
	if code != ExitOK {
		t.Fatalf("code = %d, want ExitOK", code)
	}
	if !strings.Contains(stdout, "No changed files.") {
		t.Errorf("stdout = %q", stdout)
	}
}

// --base uses a three-dot diff, so a file that landed on the base branch after
// this one was cut must NOT be reported as changed here. Two-dot would report
// it, and that noise is what trains a person to ignore the tool.
func TestContractCheck_BaseUsesMergeBaseNotBaseTip(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal/base.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")

	// Branch off, then advance main with a file this branch never touched.
	gitRun(t, root, "checkout", "-q", "-b", "work")
	gitRun(t, root, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(root, "unrelated-on-main.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "main moves on")

	// The branch's own out-of-scope change.
	gitRun(t, root, "checkout", "-q", "work")
	if err := os.WriteFile(filepath.Join(root, "mine.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "my work")

	var code int
	stdout, _ := captureOutput(func() {
		code = runContractCheck([]string{"--dir", root, "--contract", "scoped", "--base", "main"})
	})
	if code != ExitError {
		t.Fatalf("code = %d, want ExitError", code)
	}
	if !strings.Contains(stdout, "! mine.md") {
		t.Errorf("the branch's own out-of-scope file is missing:\n%s", stdout)
	}
	if strings.Contains(stdout, "unrelated-on-main.md") {
		t.Errorf("a file that only landed on the base branch was reported as changed — "+
			"this is the two-dot vs three-dot regression:\n%s", stdout)
	}
}

func TestContractCheck_UnknownContractFails(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	var code int
	_, stderr := captureOutput(func() {
		code = runContractCheck([]string{"--dir", root, "--contract", "nope"})
	})
	if code == ExitOK {
		t.Fatal("check against an unknown contract returned ExitOK")
	}
	if !strings.Contains(stderr, `"nope"`) {
		t.Errorf("stderr = %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// dispatch + helpers
// ---------------------------------------------------------------------------

func TestContractDispatch(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	var code int
	stdout, _ := captureOutput(func() { code = runContract([]string{"ls", "--dir", root}) })
	if code != ExitOK || !strings.Contains(stdout, "scoped") {
		t.Errorf("`contract ls` alias: code=%d stdout=%q", code, stdout)
	}
	for _, args := range [][]string{{}, {"bogus"}} {
		_, stderr := captureOutput(func() { code = runContract(args) })
		if code == ExitOK {
			t.Errorf("args %v returned ExitOK", args)
		}
		if !strings.Contains(stderr, "contract") {
			t.Errorf("args %v: unhelpful stderr %q", args, stderr)
		}
	}
}

// shortHashDisplay must not assume a length: the activation hash is read back
// from the database, and a hand-edited or truncated row would otherwise panic on
// a slice bound in the one command whose job is to explain what is happening.
func TestShortHashDisplay(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"abc":              "abc",
		"0123456789ab":     "0123456789ab",
		"0123456789abcdef": "0123456789ab",
	}
	for in, want := range cases {
		if got := shortHashDisplay(in); got != want {
			t.Errorf("shortHashDisplay(%q) = %q, want %q", in, got, want)
		}
	}
}
