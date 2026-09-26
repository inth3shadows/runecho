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
	"slices"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/contract"
	"github.com/inth3shadows/runecho/internal/gitutil"
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

// TestChangedPaths_UnquotedNames pins #422: without -z git C-quotes any path
// with a byte >= 0x80, so café.py came back as `"caf\303\251.py"` and never
// matched a contract glob; and the old per-line TrimSpace stripped a name's own
// edge spaces. Both listing modes must return names exactly as on disk.
func TestChangedPaths_UnquotedNames(t *testing.T) {
	root := contractRepo(t, nil)
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tracked é.py")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")

	gitRun(t, root, "checkout", "-q", "-b", "work")
	write("branch café.py")
	write("trailing space.md ")
	write(`quote"and\\backslash.md`) // git C-quotes these even with core.quotepath=false
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "work")

	got, err := changedPaths(root, "main")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"branch café.py", `quote"and\\backslash.md`, "trailing space.md "}; !slices.Equal(got, want) {
		t.Errorf("--base: got %q, want %q", got, want)
	}

	if err := os.WriteFile(filepath.Join(root, "tracked é.py"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write("staged ü.go")
	gitRun(t, root, "add", "staged ü.go")
	write("untracked ñ.txt")
	got, err = changedPaths(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"staged ü.go", "tracked é.py", "untracked ñ.txt"}; !slices.Equal(got, want) {
		t.Errorf("working tree: got %q, want %q", got, want)
	}
}

// TestContractCheck_HostileNamesCannotForgeOutput pins the display half of
// #422. Reading git with -z hands changedPaths raw bytes, so a filename can
// carry an escape sequence or a newline that git's quoting used to neutralise.
// Printed raw, one would drive the terminal and the other would add a line
// that reads exactly like this command's own summary.
func TestContractCheck_HostileNamesCannotForgeOutput(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")
	forged := "x.py\n  Contract \"scoped\" — 0 out of scope"
	for _, name := range []string{"evil\x1b[2J.py", forged} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var code int
	stdout, _ := captureOutput(func() {
		code = runContractCheck([]string{"--dir", root, "--contract", "scoped"})
	})
	if code != ExitError {
		t.Fatalf("code = %d, want ExitError (both files are out of scope)", code)
	}
	if strings.ContainsRune(stdout, 0x1b) {
		t.Errorf("a raw ESC reached stdout:\n%q", stdout)
	}
	if n := strings.Count(stdout, "\n"); n != 3 {
		t.Errorf("stdout has %d lines, want 3 (summary + one per file) — a filename broke its line:\n%q", n, stdout)
	}
	if !strings.Contains(stdout, `  ! "evil\x1b[2J.py"`) {
		t.Errorf("hostile name not shown quoted:\n%q", stdout)
	}
}

func TestDisplayPath(t *testing.T) {
	for in, want := range map[string]string{
		"internal/a.go":      "internal/a.go",
		"café é.py":          "café é.py",
		"trailing sp ":       "trailing sp ",
		"a\nb":               `"a\nb"`,
		"esc\x1b[31m":        `"esc\x1b[31m"`,
		"nel\u0085x":         `"nel\u0085x"`,
		"ls\u2028x":          `"ls\u2028x"`,
		"bad\xffutf8":        `"bad\xffutf8"`,
		"src\u200b/x.py":     `"src\u200b/x.py"`,     // zero-width space: looks like src/x.py
		"ci/\u202elmy.dliub": `"ci/\u202elmy.dliub"`, // RLO: renders as ci/build.yml
		`quote"d.md`:         `"quote\"d.md"`,
		`back\slash.md`:      `"back\\slash.md"`,
	} {
		if got := displayPath(in); got != want {
			t.Errorf("displayPath(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestChangedPaths_RenameListsBothSides pins #427: a rename must list its
// source too, or moving a file out of an out-of-scope directory into scope
// reads as an in-scope change. Both listing modes that detect renames.
func TestChangedPaths_RenameListsBothSides(t *testing.T) {
	root := contractRepo(t, nil)
	if err := os.MkdirAll(filepath.Join(root, "legacy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"legacy/a.go", "legacy/b.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package x\n\nfunc F() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")

	gitRun(t, root, "checkout", "-q", "-b", "work")
	gitRun(t, root, "mv", "legacy/a.go", "internal/a.go")
	gitRun(t, root, "commit", "-q", "-m", "move a")
	got, err := changedPaths(root, "main")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"internal/a.go", "legacy/a.go"}; !slices.Equal(got, want) {
		t.Errorf("--base: got %q, want %q (the rename's source is missing)", got, want)
	}

	gitRun(t, root, "mv", "legacy/b.go", "internal/b.go")
	got, err = changedPaths(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"internal/b.go", "legacy/b.go"}; !slices.Equal(got, want) {
		t.Errorf("staged: got %q, want %q (the rename's source is missing)", got, want)
	}
}

// TestContractCheck_UnloadableHostileContractIsEscaped pins the load-warning
// half of #427: a contract file that fails to load is named in a warning, and
// that name is repo content.
func TestContractCheck_UnloadableHostileContractIsEscaped(t *testing.T) {
	root := contractRepo(t, map[string]string{"scoped.contract": scopedContract})
	big := filepath.Join(root, contract.Dir, "big\x1b[2J.contract")
	if err := os.WriteFile(big, make([]byte, contract.MaxContractBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr := captureOutput(func() {
		runContractCheck([]string{"--dir", root, "--contract", "scoped"})
	})
	if !strings.Contains(stderr, "Warning:") {
		t.Fatalf("precondition: the oversized contract should warn, stderr = %q", stderr)
	}
	if strings.ContainsRune(stderr, 0x1b) {
		t.Errorf("a raw ESC reached stderr:\n%q", stderr)
	}
}

// hostile is the contract text every display test below feeds through: a
// clear-screen, a terminal-title OSC and a colour sequence, in the three
// fields list and show print.
const hostileContract = "name: d\x1b[2J\ndescription: x\x1b]0;pwned\x07\nfoo\x1b[31m/**\n"

// TestContractListAndShow_EscapeContractText: list and show print a
// contract's name, description, path and globs — all repo content.
func TestContractListAndShow_EscapeContractText(t *testing.T) {
	root := contractRepo(t, map[string]string{"evil\x1b[2J.contract": hostileContract})
	for name, run := range map[string]func() int{
		"list": func() int { return runContractList([]string{"--dir", root}) },
		"show": func() int { return runContractShow([]string{"--dir", root, "d\x1b[2J"}) },
	} {
		var code int
		stdout, stderr := captureOutput(func() { code = run() })
		if code != ExitOK {
			t.Fatalf("%s: code = %d, stderr = %q", name, code, stderr)
		}
		if strings.ContainsAny(stdout+stderr, "\x1b\x07") {
			t.Errorf("%s: a raw control byte reached the terminal:\n%q", name, stdout+stderr)
		}
		if !strings.Contains(stdout, `d\x1b[2J`) {
			t.Errorf("%s: the name is not shown escaped:\n%q", name, stdout)
		}
	}
}

// TestContractCheck_SessionPathEscapesStoredPath pins the session half of
// #427: the stored contract path reaches stderr both when the file changed
// since activation and when it can no longer be loaded.
func TestContractCheck_SessionPathEscapesStoredPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	root := contractRepo(t, map[string]string{"evil\x1b[2J.contract": scopedContract})
	top, err := gitutil.TopLevel(root)
	if err != nil {
		t.Fatal(err)
	}
	enrollAt(t, home, "r", top)
	if _, stderr := captureOutput(func() {
		if code := runContractActivate([]string{"--dir", root, "--session", "s1", "scoped"}); code != ExitOK {
			t.Errorf("activate: code %d", code)
		}
	}); t.Failed() {
		t.Fatalf("activate failed: %q", stderr)
	}
	file := filepath.Join(root, contract.Dir, "evil\x1b[2J.contract")

	check := func(what string) {
		t.Helper()
		_, stderr := captureOutput(func() {
			runContractCheck([]string{"--dir", root, "--session", "s1"})
		})
		if !strings.Contains(stderr, `evil\x1b[2J.contract`) {
			t.Errorf("%s: stored path not shown escaped:\n%q", what, stderr)
		}
		if strings.ContainsRune(stderr, 0x1b) {
			t.Errorf("%s: a raw ESC reached stderr:\n%q", what, stderr)
		}
	}
	if err := os.WriteFile(file, []byte(scopedContract+"docs/**\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check("changed since activation")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	check("unloadable")
}

func TestDisplayText(t *testing.T) {
	for in, want := range map[string]string{
		"plain message":         "plain message",
		`C:\Users\e\a.contract`: `C:\Users\e\a.contract`,
		`say "hi"`:              `say "hi"`,
		"café":                  "café",
		"esc\x1b[2J":            `esc\x1b[2J`,
		"osc\x1b]0;t\x07":       `osc\x1b]0;t\a`,
		"nl\nx":                 `nl\nx`,
		"rlo\u202ex":            `rlo\u202ex`,
		"bad\xffx":              `bad\xffx`,
	} {
		if got := displayText(in); got != want {
			t.Errorf("displayText(%q) = %q, want %q", in, got, want)
		}
	}
}
