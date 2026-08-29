package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/version"
)

// #373: reaching vcRunInstall runs `bash <top>/install.sh`, which compiles the
// whole checked-out tree — so it runs THIS revision's code. `git checkout` is
// not an act of trust, and on a public repo checking out a contributor's branch
// to read the diff is routine.
//
// These tests use the REAL defaultTrusted (no seam) against real git repos, so
// they exercise gitutil.RemoteDefaultRef and gitutil.Contains as shipped.

// clonedRunechoRepo builds an "upstream" runecho repo and a clone of it, and
// returns the clone. The clone has a real origin, so origin/HEAD resolves.
func clonedRunechoRepo(t *testing.T, upstreamTag string) (clone string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	up := runechoRepo(t, upstreamTag)
	clone = filepath.Join(t.TempDir(), "clone")
	trustGit(t, "", "clone", "-q", up, clone)
	return clone
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

// withRealTrust sets the installed version but leaves vcTrusted alone, so the
// gate runs for real. (withSeams stubs it to true, which is right for the tests
// that are about something else and wrong for these.)
func withRealTrust(t *testing.T, ver string) {
	t.Helper()
	origVer, origRun, origStamp := version.Version, vcRunInstall, vcReadStamp
	version.Version = ver
	t.Cleanup(func() { version.Version, vcRunInstall, vcReadStamp = origVer, origRun, origStamp })
}

// TestVersionCheck_ForkBranchDoesNotReinstall is #373's chain, end to end: a
// branch that is not in origin, carrying its own high tag, with its own
// install.sh. Before the containment gate this reinstalled from the branch.
func TestVersionCheck_ForkBranchDoesNotReinstall(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.16.1")

	// The attacker's branch: a modified install.sh, and a lightweight tag high
	// enough to satisfy the version comparison. Both travel with a branch fetch.
	trustGit(t, clone, "checkout", "-q", "-b", "pr-branch")
	if err := os.WriteFile(filepath.Join(clone, "install.sh"), []byte("#!/usr/bin/env bash\n# owned\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	trustGit(t, clone, "commit", "-qam", "innocuous-looking change")
	trustGit(t, clone, "tag", "v99.0.0")

	withRealTrust(t, "v0.16.1")
	vcRunInstall = func(top, binDir string) error {
		t.Fatalf("install.sh from an untrusted branch was executed (top=%s) — #373", top)
		return nil
	}

	if code := runVersionCheck([]string{"--reinstall", "--quiet", clone}); code != ExitOK {
		t.Fatalf("exit = %d, want ExitOK — the freshness check must never fail the git op", code)
	}
}

// TestVersionCheck_TrustedHeadStillReinstalls is the other half: the gate must
// not break the case #228 was written for. A refusal that also refuses the
// legitimate pull would be a silent stale binary, not a fix.
func TestVersionCheck_TrustedHeadStillReinstalls(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.17.0")

	withRealTrust(t, "v0.16.1") // behind the upstream tag
	ran := false
	vcRunInstall = func(top, binDir string) error { ran = true; return nil }
	vcReadStamp = func(string) string { return "v0.17.0" }

	if code := runVersionCheck([]string{"--reinstall", "--quiet", clone}); code != ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}
	if !ran {
		t.Error("reinstall did not run on a clean clone of origin's default branch — the gate is too tight")
	}
}

// TestVersionCheck_UnconfirmableTrustFailsClosed pins the direction of the
// failure. runechoRepo has no origin at all, so RemoteDefaultRef errors; "could
// not confirm" must not be read as permission.
func TestVersionCheck_UnconfirmableTrustFailsClosed(t *testing.T) {
	repo := runechoRepo(t, "v0.17.0")

	withRealTrust(t, "v0.16.1")
	vcRunInstall = func(top, binDir string) error {
		t.Fatal("reinstall ran although trust could not be established")
		return nil
	}

	if code := runVersionCheck([]string{"--reinstall", "--quiet", repo}); code != ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}
}

// TestDefaultTrusted_AnswersContainment checks the predicate directly, both
// ways, so a failure in the tests above can be localised.
func TestDefaultTrusted_AnswersContainment(t *testing.T) {
	clone := clonedRunechoRepo(t, "v0.16.1")

	ok, ref, err := defaultTrusted(mustTop(t, clone))
	if err != nil {
		t.Fatalf("defaultTrusted on a fresh clone: %v", err)
	}
	if !ok {
		t.Errorf("HEAD of a fresh clone reported as untrusted (ref=%s)", ref)
	}

	trustGit(t, clone, "checkout", "-q", "-b", "local-work")
	trustGit(t, clone, "commit", "-q", "--allow-empty", "-m", "not pushed")
	ok, _, err = defaultTrusted(mustTop(t, clone))
	if err != nil {
		t.Fatalf("defaultTrusted on a local branch: %v", err)
	}
	if ok {
		t.Error("a commit that exists only locally reported as contained in origin's default branch")
	}
}

// TestVersionCheck_TrustErrorHonoursQuiet — the hook always passes --quiet, and
// this path fires on every checkout and merge for anyone whose remote is not
// named `origin` (defaultTrusted hardcodes it). An unconditional write here is
// permanent noise in the terminal, which is the failure the Windows branch
// already exists to avoid.
func TestVersionCheck_TrustErrorHonoursQuiet(t *testing.T) {
	repo := runechoRepo(t, "v0.17.0") // no origin at all, so RemoteDefaultRef errors
	withRealTrust(t, "v0.16.1")
	vcRunInstall = func(string, string) error {
		t.Fatal("reinstall ran although trust could not be established")
		return nil
	}

	stdout, stderr := captureOutput(func() {
		if code := runVersionCheck([]string{"--reinstall", "--quiet", repo}); code != ExitOK {
			t.Errorf("exit = %d, want ExitOK", code)
		}
	})
	if stdout != "" || stderr != "" {
		t.Errorf("--quiet produced output:\nstdout: %q\nstderr: %q", stdout, stderr)
	}

	// Without --quiet it must still say why, or the user has a silently stale
	// binary and nothing to explain it.
	stdout, stderr = captureOutput(func() { runVersionCheck([]string{"--reinstall", repo}) })
	if !strings.Contains(stdout+stderr, "cannot confirm") {
		t.Errorf("non-quiet run said nothing about the skip:\nstdout: %q\nstderr: %q", stdout, stderr)
	}
}

// TestVersionCheck_TrustedTrueWithErrorSkips pins that fail-closed is a property
// of THIS gate, not of vcTrusted. Every implementation today returns false with
// its error, so without the forced `trusted = false` the gate still passes every
// other test — and a later (true, ref, err) would walk straight through.
func TestVersionCheck_TrustedTrueWithErrorSkips(t *testing.T) {
	repo := runechoRepo(t, "v0.17.0")
	withSeams(t, "v0.16.1") // stubs vcTrusted; overridden immediately below
	vcTrusted = func(string) (bool, string, error) {
		return true, "refs/remotes/origin/master", errors.New("git exploded")
	}
	vcRunInstall = func(string, string) error {
		t.Fatal("reinstall ran on (true, ref, err) — fail-closed is not enforced by the caller")
		return nil
	}

	if code := runVersionCheck([]string{"--reinstall", "--quiet", repo}); code != ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}
}
