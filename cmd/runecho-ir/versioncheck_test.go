package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/version"
)

func TestSemverCore(t *testing.T) {
	cases := map[string]string{
		"v0.16.1":            "v0.16.1",
		"v0.16.1-3-gabc1234": "v0.16.1", // post-tag build suffix stripped
		"v0.16.1-dirty":      "v0.16.1",
		"runecho-ir v1.2.3":  "v1.2.3",
		"0.17.4":             "0.17.4", // goreleaser stamp (no leading v) still parses
		"runecho-ir 0.17.4":  "0.17.4", // goreleaser --version output
		"0.17.4-3-gabc1234":  "0.17.4", // goreleaser post-tag build
		"dev":                "",
		"":                   "",
	}
	for in, want := range cases {
		if got := semverCore(in); got != want {
			t.Errorf("semverCore(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVersionBehind(t *testing.T) {
	type c struct {
		installed, newest string
		want              bool
	}
	for _, tc := range []c{
		{"v0.16.0", "v0.16.1", true},  // patch behind
		{"v0.15.9", "v0.16.0", true},  // minor behind
		{"v0.16.1", "v0.16.1", false}, // equal
		{"v0.17.0", "v0.16.1", false}, // ahead (older branch) never downgrades
		{"v0.9.0", "v0.10.0", true},   // numeric, not lexical: 9 < 10
		{"0.16.0", "v0.17.0", true},   // mixed stamp formats (goreleaser vs install.sh)
		{"0.17.0", "0.17.0", false},   // both goreleaser-stamped, equal
		{"", "v0.16.1", false},        // unreadable installed → no rebuild
		{"v0.16.1", "", false},        // unreadable newest → no rebuild
		{"garbage", "v0.16.1", false},
	} {
		if got := versionBehind(tc.installed, tc.newest); got != tc.want {
			t.Errorf("versionBehind(%q,%q) = %v, want %v", tc.installed, tc.newest, got, tc.want)
		}
	}
}

func TestIsRunechoTree(t *testing.T) {
	// A dir with the runecho module line + install.sh is the tree; a foreign one
	// (right filename, wrong module) is not — this is the guard that stops the
	// hook running an unrelated project's install.sh forever.
	runecho := t.TempDir()
	os.WriteFile(filepath.Join(runecho, "go.mod"), []byte("module github.com/inth3shadows/runecho\n\ngo 1.25\n"), 0644)
	os.WriteFile(filepath.Join(runecho, "install.sh"), []byte("#!/usr/bin/env bash\n"), 0755)
	if !isRunechoTree(runecho) {
		t.Error("isRunechoTree false for the real tree")
	}

	foreign := t.TempDir()
	os.WriteFile(filepath.Join(foreign, "go.mod"), []byte("module example.com/other\n"), 0644)
	os.WriteFile(filepath.Join(foreign, "install.sh"), []byte("#!/usr/bin/env bash\n"), 0755)
	if isRunechoTree(foreign) {
		t.Error("isRunechoTree true for a foreign repo with an install.sh")
	}

	noInstall := t.TempDir()
	os.WriteFile(filepath.Join(noInstall, "go.mod"), []byte("module github.com/inth3shadows/runecho\n"), 0644)
	if isRunechoTree(noInstall) {
		t.Error("isRunechoTree true without an install.sh")
	}
}

// runechoRepo builds a temp git repo that isRunechoTree accepts, with one commit
// tagged at tag. Returns the repo path.
func runechoRepo(t *testing.T, tag string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/inth3shadows/runecho\n\ngo 1.25\n"), 0644)
	os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/usr/bin/env bash\n"), 0755)
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Pinned so the trust tests do not depend on the machine's init.defaultBranch
	// (RemoteDefaultRef's name fallback only knows main and master).
	git("-c", "init.defaultBranch=master", "init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "init")
	if tag != "" {
		git("tag", tag)
	}
	return dir
}

// withSeams pins the installed version and restores every rebuild seam after
// the test. Git itself is never stubbed: the repos are real, with local-path
// remotes, so gitutil runs as shipped.
func withSeams(t *testing.T, ver string) {
	t.Helper()
	origVer := version.Version
	origRun, origStamp, origLook := vcRunInstall, vcReadStamp, fzLookPath
	version.Version = ver
	// A deterministic toolchain answer, so no test depends on the machine's PATH.
	fzLookPath = func(string) (string, error) { return "/fake/go/bin/go", nil }
	vcRunInstall = func(top, binDir, ver, goDir string) error {
		t.Fatalf("unexpected rebuild (top=%s version=%s)", top, ver)
		return nil
	}
	t.Cleanup(func() {
		version.Version = origVer
		vcRunInstall, vcReadStamp, fzLookPath = origRun, origStamp, origLook
	})
}

func TestVersionCheck_OptOut(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	withSeams(t, "v0.16.1")
	t.Setenv("RUNECHO_NO_AUTO_INSTALL", "1")
	if code := runVersionCheck([]string{"--reinstall", clone}); code != ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}
}

func TestVersionCheck_ForeignTree_NoOp(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/other\n"), 0644)
	os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/usr/bin/env bash\n"), 0755)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, a := range [][]string{{"init", "-q"}, {"add", "."}} {
		exec.Command("git", append([]string{"-C", dir}, a...)...).Run()
	}
	cmd := exec.Command("git", "-C", dir, "commit", "-q", "-m", "x")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	cmd.Run()
	exec.Command("git", "-C", dir, "tag", "v9.9.9").Run()

	withSeams(t, "v0.0.1") // any rebuild fails the test
	if code := runVersionCheck([]string{"--reinstall", "--quiet", dir}); code != ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}
}

// TestVersionCheck_AdvisoryNeverExecutes is #373's chain re-pinned for the hook
// path (#375): a branch not in origin, carrying its own high tag and its own
// install.sh. The hooks run version-check WITHOUT --reinstall, and that mode
// must never build anything — whatever is checked out.
func TestVersionCheck_AdvisoryNeverExecutes(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.16.1")
	forkBranch(t, clone)
	withSeams(t, "v0.16.1") // any rebuild fails the test
	for _, args := range [][]string{{"--quiet", clone}, {clone}} {
		if code := runVersionCheck(args); code != ExitOK {
			t.Fatalf("exit = %d, want ExitOK", code)
		}
	}
}

// TestVersionCheck_ReinstallUsesOriginNotWorktree: --reinstall from a fork
// branch builds ORIGIN's newest release from an exported tree, never the
// checked-out worktree with its v99 tag and modified install.sh.
func TestVersionCheck_ReinstallUsesOriginNotWorktree(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	upstreamInstall := readFile(t, filepath.Join(clone, "install.sh"))
	forkBranch(t, clone)
	withSeams(t, "v0.16.1")
	ran := false
	vcRunInstall = func(top, binDir, ver, goDir string) error {
		ran = true
		if ver != "v0.17.0" {
			t.Errorf("built %s, want origin's v0.17.0", ver)
		}
		if got := readFile(t, filepath.Join(top, "install.sh")); got != upstreamInstall {
			t.Errorf("ran the worktree's install.sh, not origin's:\n%s", got)
		}
		return nil
	}
	vcReadStamp = func(string) string { return "v0.17.0" }
	if code := runVersionCheck([]string{"--reinstall", clone}); code != ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}
	if !ran {
		t.Error("--reinstall did not build while behind origin's release")
	}
}

func mustTop(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("show-toplevel: %v", err)
	}
	return filepath.Clean(string(out[:len(out)-1]))
}
