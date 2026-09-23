package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These tests drive freshen against REAL git repos with local-path remotes, so
// gitutil.RemoteTags / Fetch / RemoteDefaultRef / Contains / Export all run as
// shipped. Only the rebuild itself (vcRunInstall), the stamp read, and the Go
// toolchain lookup are stubbed (withSeams).

// clonedRunechoRepo builds an "upstream" runecho repo tagged upstreamTag and a
// clone of it, and returns the clone. The clone has a real origin, so
// origin/HEAD resolves.
func clonedRunechoRepo(t *testing.T, upstreamTag string) (clone string) {
	t.Helper()
	up := runechoRepo(t, upstreamTag)
	clone = filepath.Join(t.TempDir(), "clone")
	trustGit(t, "", "clone", "-q", up, clone)
	return clone
}

// upstreamOf returns the clone's origin path.
func upstreamOf(t *testing.T, clone string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", clone, "remote", "get-url", "origin").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func trustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// forkBranch puts the clone on a branch origin does not have: a modified
// install.sh and a lightweight tag high enough to win any comparison — the #373
// attack shape. Both travel with an ordinary branch fetch.
func forkBranch(t *testing.T, clone string) {
	t.Helper()
	trustGit(t, clone, "checkout", "-q", "-b", "pr-branch")
	if err := os.WriteFile(filepath.Join(clone, "install.sh"), []byte("#!/usr/bin/env bash\n# owned\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	trustGit(t, clone, "commit", "-qam", "innocuous-looking change")
	trustGit(t, clone, "tag", "v99.0.0")
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gitDirOf(clone string) string { return filepath.Join(clone, ".git") }

// runFreshen runs freshen against the clone and returns what it wrote.
func runFreshen(t *testing.T, clone string) string {
	t.Helper()
	var out bytes.Buffer
	if code := freshen(gitDirOf(clone), &out); code != ExitOK {
		t.Fatalf("freshen exit = %d, want ExitOK — it must never fail the periodic tick", code)
	}
	return out.String()
}

func TestFreshen_InstallsNewestOriginTag(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	upstreamInstall := readFile(t, filepath.Join(clone, "install.sh"))
	withSeams(t, "v0.16.1")
	ran := false
	vcRunInstall = func(top, binDir, ver, goDir string) error {
		ran = true
		if ver != "v0.17.0" {
			t.Errorf("stamped %q, want v0.17.0", ver)
		}
		if !strings.HasPrefix(top, os.TempDir()) {
			t.Errorf("built in %s, want a private temp dir", top)
		}
		if _, err := os.Stat(filepath.Join(top, ".git")); !os.IsNotExist(err) {
			t.Errorf("exported tree has a .git (err=%v) — it must reach no repo config or hooks", err)
		}
		if got := readFile(t, filepath.Join(top, "install.sh")); got != upstreamInstall {
			t.Errorf("exported install.sh differs from upstream's")
		}
		if goDir != "/fake/go/bin" {
			t.Errorf("goDir = %q, want the resolved toolchain dir", goDir)
		}
		return nil
	}
	vcReadStamp = func(string) string { return "v0.17.0" }
	out := runFreshen(t, clone)
	if !ran {
		t.Fatalf("no rebuild while behind origin's release:\n%s", out)
	}
	if !strings.Contains(out, "now v0.17.0") {
		t.Errorf("output does not confirm the new stamp:\n%s", out)
	}
}

// The #375 blocker: the version signal must come from ORIGIN, not local refs.
// A fork fetch can plant tags locally (tags auto-follow any fetched commit), so
// the clone gets a high lightweight tag on a fork branch AND one on an upstream
// commit. Only origin's v0.17.0 may be built.
func TestFreshen_TagSourceIsOriginNotLocalRefs(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	upstreamInstall := readFile(t, filepath.Join(clone, "install.sh"))
	trustGit(t, clone, "tag", "v98.0.0", "origin/master") // auto-followed fork tag on a shared commit
	forkBranch(t, clone)                                  // v99.0.0 on an owned install.sh
	withSeams(t, "v0.16.1")
	ran := false
	vcRunInstall = func(top, binDir, ver, goDir string) error {
		ran = true
		if ver != "v0.17.0" {
			t.Errorf("built %s from local refs, want origin's v0.17.0", ver)
		}
		if readFile(t, filepath.Join(top, "install.sh")) != upstreamInstall {
			t.Error("ran a local branch's install.sh")
		}
		return nil
	}
	vcReadStamp = func(string) string { return "v0.17.0" }
	runFreshen(t, clone)
	if !ran {
		t.Error("no rebuild while behind origin's release")
	}
}

// A release tagged upstream AFTER the clone was made is not in local refs at
// all: freshen must learn it from origin and fetch its commit before exporting.
func TestFreshen_SeesTagPushedAfterClone(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	up := upstreamOf(t, clone)
	if err := os.WriteFile(filepath.Join(up, "NEW"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trustGit(t, up, "add", "NEW")
	trustGit(t, up, "commit", "-qm", "release")
	trustGit(t, up, "tag", "-a", "v0.18.0", "-m", "v0.18.0") // annotated, as CI cuts them
	withSeams(t, "v0.17.0")
	ran := false
	vcRunInstall = func(top, binDir, ver, goDir string) error {
		ran = true
		if ver != "v0.18.0" {
			t.Errorf("built %s, want v0.18.0", ver)
		}
		if _, err := os.Stat(filepath.Join(top, "NEW")); err != nil {
			t.Errorf("exported tree is not the v0.18.0 commit: %v", err)
		}
		return nil
	}
	vcReadStamp = func(string) string { return "v0.18.0" }
	runFreshen(t, clone)
	if !ran {
		t.Error("a release pushed after the clone was never built")
	}
}

// A vX.Y.Z tag that is not on origin's default branch is not a release this
// box takes — whoever pushed it. Fails closed.
func TestFreshen_TagOffDefaultBranchIsSkipped(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	up := upstreamOf(t, clone)
	trustGit(t, up, "checkout", "-q", "-b", "side")
	if err := os.WriteFile(filepath.Join(up, "SIDE"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trustGit(t, up, "add", "SIDE")
	trustGit(t, up, "commit", "-qm", "side")
	trustGit(t, up, "tag", "v0.18.0")
	trustGit(t, up, "checkout", "-q", "master")
	withSeams(t, "v0.17.0") // any rebuild fails the test
	if out := runFreshen(t, clone); !strings.Contains(out, "not contained") {
		t.Errorf("want a 'not contained' line:\n%s", out)
	}
}

func TestFreshen_NeverDowngradesOrRebuildsWhenCurrent(t *testing.T) {
	for _, installed := range []string{"v0.17.0", "v0.17.0-3-gabc1234", "v0.18.0"} {
		t.Run(installed, func(t *testing.T) {
			clone := clonedRunechoRepo(t, "v0.17.0")
			withSeams(t, installed) // any rebuild fails the test
			if out := runFreshen(t, clone); !strings.Contains(out, "up to date") {
				t.Errorf("want an 'up to date' line:\n%s", out)
			}
		})
	}
}

func TestFreshen_UnstampedInstallIsNotManaged(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	withSeams(t, "dev")
	if out := runFreshen(t, clone); !strings.Contains(out, "unstamped") {
		t.Errorf("want an 'unstamped' line:\n%s", out)
	}
}

func TestFreshen_OriginUnreachableFailsOpen(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	trustGit(t, clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "nonexistent"))
	withSeams(t, "v0.16.1")
	if out := runFreshen(t, clone); !strings.Contains(out, "cannot list origin's tags") {
		t.Errorf("want a 'cannot list' line:\n%s", out)
	}
}

// An origin whose tree is some other module must never have its install.sh run.
func TestFreshen_ForeignExportRefused(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	up := upstreamOf(t, clone)
	if err := os.WriteFile(filepath.Join(up, "go.mod"), []byte("module example.com/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	trustGit(t, up, "commit", "-qam", "swap module")
	trustGit(t, up, "tag", "v0.18.0")
	withSeams(t, "v0.17.0")
	if out := runFreshen(t, clone); !strings.Contains(out, "not the runecho source") {
		t.Errorf("want a refusal line:\n%s", out)
	}
}

func TestFreshen_OptOutEnv(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	withSeams(t, "v0.16.1")
	t.Setenv("RUNECHO_NO_AUTO_INSTALL", "1")
	if out := runFreshen(t, clone); !strings.Contains(out, "RUNECHO_NO_AUTO_INSTALL") {
		t.Errorf("want the opt-out named:\n%s", out)
	}
}

func TestFreshen_NoGoToolchainFailsOpen(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	withSeams(t, "v0.16.1")
	origGoroot, origCands := fzGoroot, goCandidateDirs
	fzLookPath = func(string) (string, error) { return "", errors.New("not found") }
	fzGoroot = func() string { return "" }
	goCandidateDirs = nil
	t.Cleanup(func() { fzGoroot, goCandidateDirs = origGoroot, origCands })
	if out := runFreshen(t, clone); !strings.Contains(out, "no go toolchain") {
		t.Errorf("want a 'no go toolchain' line naming the remedy:\n%s", out)
	}
}

func TestFreshen_ReinstallThatDidNotAdvanceIsReported(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	withSeams(t, "v0.16.1")
	vcRunInstall = func(top, binDir, ver, goDir string) error { return nil }
	vcReadStamp = func(string) string { return "v0.16.1" }
	if out := runFreshen(t, clone); !strings.Contains(out, "still says v0.16.1") {
		t.Errorf("a build that did not move the stamp must be reported:\n%s", out)
	}
}

// One tick's worst case must fit inside the interval, so two ticks never
// overlap — the reason freshen needs no lock.
func TestFreshen_TickBudgetBelowInterval(t *testing.T) {
	if budget := lsRemoteTimeout + fetchTimeout + exportTimeout + installTimeout; budget >= freshenInterval {
		t.Errorf("worst-case tick %s is not below the %s interval", budget, freshenInterval)
	}
}

// Exactly one timestamped line per run, even when there is nothing to do: on an
// unattended box, silence is what a dead job looks like.
func TestFreshen_PrintsExactlyOneLinePerRun(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	withSeams(t, "v0.17.0")
	out := runFreshen(t, clone)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want exactly one line, got %d:\n%s", len(lines), out)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ freshen: `).MatchString(lines[0]) {
		t.Errorf("line lacks the RFC3339 'freshen:' prefix: %q", lines[0])
	}
}

func TestNewestReleaseTag(t *testing.T) {
	tag, sha := newestReleaseTag(map[string]string{
		"v0.9.0": "a", "v0.10.0": "b", "v0.10.0-rc1": "c", "latest": "d", "v1.0": "e",
	})
	if tag != "v0.10.0" || sha != "b" {
		t.Errorf("newest = %s/%s, want v0.10.0/b (numeric, release-only)", tag, sha)
	}
	if tag, _ := newestReleaseTag(map[string]string{"nightly": "x"}); tag != "" {
		t.Errorf("no release tag should yield \"\", got %q", tag)
	}
}

func TestResolveGoDir_Order(t *testing.T) {
	found := func(string) (string, error) { return "/path/bin/go", nil }
	missing := func(string) (string, error) { return "", errors.New("nope") }
	has := func(set ...string) func(string) bool {
		return func(p string) bool {
			for _, s := range set {
				if p == s {
					return true
				}
			}
			return false
		}
	}
	if got := resolveGoDir(found, "/goroot", has("/goroot/bin/go")); got != "/path/bin" {
		t.Errorf("PATH must win: got %q", got)
	}
	if got := resolveGoDir(missing, "/goroot", has("/goroot/bin/go")); got != "/goroot/bin" {
		t.Errorf("GOROOT must come second: got %q", got)
	}
	if got := resolveGoDir(missing, "/goroot", has("/home/linuxbrew/.linuxbrew/bin/go")); got != "/home/linuxbrew/.linuxbrew/bin" {
		t.Errorf("fixed candidates come last: got %q", got)
	}
	if got := resolveGoDir(missing, "", has()); got != "" {
		t.Errorf("nothing found must be \"\", got %q", got)
	}
}

func TestCronEntry_Freshen(t *testing.T) {
	if e := cronEntry("/b/runecho-ir", "/l/r.log", ""); strings.Contains(e, "--freshen") {
		t.Errorf("no source must mean no flag: %s", e)
	}
	e := cronEntry("/b/runecho-ir", "/l/r.log", "/src/it's 100%/.bare")
	if !strings.Contains(e, `--freshen='/src/it'\''s 100\%/.bare'`) {
		t.Errorf("source not cron-quoted: %s", e)
	}
	if !strings.Contains(e, "--all --prune --freshen=") || !strings.HasSuffix(e, "# runecho") {
		t.Errorf("unexpected shape: %s", e)
	}
}

func TestLaunchdPlist_Freshen(t *testing.T) {
	if p := launchdPlist("/b/ir", "/o", "/e", ""); strings.Contains(p, "--freshen") {
		t.Errorf("no source must mean no argv element:\n%s", p)
	}
	p := launchdPlist("/b/ir", "/o", "/e", "/src/a&b/.bare")
	if !strings.Contains(p, "<string>--prune</string>\n\t\t<string>--freshen=/src/a&amp;b/.bare</string>\n\t</array>") {
		t.Errorf("freshen argv element missing or unescaped:\n%s", p)
	}
}

func TestDetectFreshenSource(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")
	gitDir, note := detectFreshenSource(clone)
	if want := mustCommonDir(t, clone); gitDir != want || note != "" {
		t.Errorf("inside a runecho tree: got (%q, %q), want (%q, \"\")", gitDir, note, want)
	}
	if gitDir, note := detectFreshenSource(t.TempDir()); gitDir != "" || note == "" {
		t.Errorf("outside any tree: got (%q, %q), want (\"\", a note)", gitDir, note)
	}
	foreign := t.TempDir()
	trustGit(t, foreign, "init", "-q")
	if err := os.WriteFile(filepath.Join(foreign, "go.mod"), []byte("module example.com/other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if gitDir, _ := detectFreshenSource(foreign); gitDir != "" {
		t.Errorf("a foreign tree must not be recorded, got %q", gitDir)
	}
}

func mustCommonDir(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(strings.TrimSpace(string(out)))
}

func TestRepoReindex_FreshenRequiresAll(t *testing.T) {
	if code := runRepoReindex([]string{"--freshen=/x", "somerepo"}); code != ExitError {
		t.Errorf("--freshen without --all: exit %d, want ExitError", code)
	}
}
