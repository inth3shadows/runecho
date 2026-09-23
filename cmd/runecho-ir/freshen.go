package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/version"
)

// freshen keeps the INSTALLED binaries at the newest release (#375). It is the
// only code path that rebuilds them automatically, and it runs from exactly two
// places: the hourly periodic job (`repo reindex --all --prune --freshen=<dir>`)
// and an explicit `runecho-ir version-check --reinstall`. The git hooks only
// advise (`version-check --quiet`), so nothing on a checkout or merge executes
// anything.
//
// Trust statement. A rebuild runs install.sh from a commit that (a) origin
// currently serves as a vX.Y.Z tag and (b) is contained in origin's default
// branch. The tag list and the commit both come from origin over the network;
// local branches, worktrees and local refs/tags are never consulted, and the
// checked-out tree is never executed. That last property is what #373's
// HEAD-containment gate existed to approximate; here it holds by construction,
// which is why the gate is gone.
//
// Why `git archive` works after all: the objection recorded against it (#374)
// was that an archive has no .git, so install.sh's `git describe` would stamp
// every build "dev" and re-fire forever. That holds only when the version is
// unknown. Here it is the very thing learned from origin, and install.sh already
// honours RUNECHO_VERSION for exactly the no-.git case (the Dockerfile channel).
// An archive is preferable to a detached worktree: it mutates no worktree
// registry, fires none of the repo's own hooks, and shares no .git config with
// the build.
//
// Every path is fail-open (ExitOK) and writes exactly one line, timestamped, so
// the reindex log shows the job alive even when there is nothing to do — on a
// box where the job runs unattended, silence is what a dead job looks like.

const (
	lsRemoteTimeout = 30 * time.Second
	fetchTimeout    = 60 * time.Second
	exportTimeout   = 30 * time.Second
	// freshenInterval is the periodic job's cadence. One tick's worst case must
	// fit inside it, so two ticks can never overlap (no lock needed); pinned by
	// TestFreshen_TickBudgetBelowInterval.
	freshenInterval = time.Hour
)

// releaseTag matches a release tag exactly: no pre-release, no suffix. A tag
// that is not a release is not a reason to rebuild anything.
var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// Seams overridden in tests. Git itself stays real in tests (local-path remotes).
var (
	fzSelf     = os.Executable
	fzGoroot   = runtime.GOROOT
	fzLookPath = exec.LookPath
	fzNow      = time.Now
)

// goCandidateDirs are tried when neither PATH nor the building toolchain's
// GOROOT yields a `go`. A cron or launchd job gets a minimal PATH (/usr/bin:/bin),
// and install.sh needs `go` on it — without this, the periodic rebuild would fail
// on every tick on any box whose Go lives in Homebrew, linuxbrew or /usr/local/go.
var goCandidateDirs = []string{
	"/usr/local/go/bin",
	"/opt/homebrew/bin",
	"/usr/local/bin",
	"/home/linuxbrew/.linuxbrew/bin",
}

// resolveGoDir returns the directory holding a usable `go`, or "" if none is
// found: PATH first, then the toolchain that built this binary, then the fixed
// candidates. Pure over its three inputs so every branch is testable.
func resolveGoDir(lookPath func(string) (string, error), goroot string, exists func(string) bool) string {
	if p, err := lookPath("go"); err == nil {
		return filepath.Dir(p)
	}
	if goroot != "" {
		if dir := filepath.Join(goroot, "bin"); exists(filepath.Join(dir, "go")) {
			return dir
		}
	}
	for _, dir := range goCandidateDirs {
		if exists(filepath.Join(dir, "go")) {
			return dir
		}
	}
	return ""
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// newestReleaseTag picks the highest vX.Y.Z tag and its commit. ("", "") when
// the remote serves no release tag.
func newestReleaseTag(tags map[string]string) (tag, sha string) {
	for name, commit := range tags {
		if !releaseTag.MatchString(name) {
			continue
		}
		if tag == "" || semverLess(tag, name) {
			tag, sha = name, commit
		}
	}
	return tag, sha
}

// freshenLine writes one timestamped line.
func freshenLine(w io.Writer, format string, a ...any) {
	fmt.Fprintf(w, "%s freshen: %s\n", fzNow().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

// freshen brings the installed binaries up to origin's newest release, building
// from gitDir (a git common dir: `.bare` in a bare-worktree layout, `<root>/.git`
// in a plain clone). Always ExitOK — see the file comment.
func freshen(gitDir string, w io.Writer) int {
	if os.Getenv("RUNECHO_NO_AUTO_INSTALL") == "1" {
		freshenLine(w, "RUNECHO_NO_AUTO_INSTALL=1 — skipped")
		return ExitOK
	}
	if runtime.GOOS == "windows" {
		freshenLine(w, "skipped on Windows (a running binary cannot be replaced) — run install.sh by hand")
		return ExitOK
	}
	self, err := fzSelf()
	if err != nil {
		freshenLine(w, "cannot resolve own path, skipped: %v", err)
		return ExitOK
	}
	binDir := filepath.Dir(self)

	installed := semverCore(version.Version)
	if installed == "" {
		freshenLine(w, "installed build is unstamped (%s); not managed — run install.sh by hand", disp(version.Version))
		return ExitOK
	}

	ctx, cancel := context.WithTimeout(context.Background(), lsRemoteTimeout)
	tags, err := gitutil.RemoteTags(ctx, gitDir, "origin")
	cancel()
	if err != nil {
		freshenLine(w, "cannot list origin's tags from %s, skipped: %v", gitDir, err)
		return ExitOK
	}
	newest, sha := newestReleaseTag(tags)
	if newest == "" {
		freshenLine(w, "origin serves no vX.Y.Z tag; nothing to compare")
		return ExitOK
	}
	// A post-tag local build (vX.Y.Z-N-g…) has the same core as its tag, so it is
	// never behind it: a hand-built newer checkout is never downgraded.
	if !versionBehind(installed, newest) {
		freshenLine(w, "installed %s is up to date with origin's %s", installed, newest)
		return ExitOK
	}

	ctx, cancel = context.WithTimeout(context.Background(), fetchTimeout)
	err = gitutil.Fetch(ctx, gitDir, "origin")
	cancel()
	if err != nil {
		freshenLine(w, "installed %s is behind %s, but fetching origin failed, skipped: %v", installed, newest, err)
		return ExitOK
	}
	// Fail closed: a tag that is not on origin's default branch is not a release
	// this box takes, whoever pushed it.
	ref, err := gitutil.RemoteDefaultRef(gitDir, "origin")
	if err != nil {
		freshenLine(w, "cannot resolve origin's default branch, skipped: %v", err)
		return ExitOK
	}
	contained, err := gitutil.Contains(gitDir, sha, ref)
	if err != nil || !contained {
		freshenLine(w, "tag %s (%.12s) is not contained in %s; skipped (err=%v)", newest, sha, ref, err)
		return ExitOK
	}

	goDir := resolveGoDir(fzLookPath, fzGoroot(), fileExists)
	if goDir == "" {
		freshenLine(w, "installed %s is behind %s, but no go toolchain was found (PATH, GOROOT, or %v) — add PATH=… to the crontab/plist", installed, newest, goCandidateDirs)
		return ExitOK
	}

	// 0700 and an unpredictable name: the /tmp objection in reindexLogPath is
	// about FIXED names another user can pre-create.
	tmp, err := os.MkdirTemp("", "runecho-freshen-*")
	if err != nil {
		freshenLine(w, "cannot create a build dir, skipped: %v", err)
		return ExitOK
	}
	defer os.RemoveAll(tmp)
	ctx, cancel = context.WithTimeout(context.Background(), exportTimeout)
	err = gitutil.Export(ctx, gitDir, sha, tmp)
	cancel()
	if err != nil {
		freshenLine(w, "exporting %s failed, skipped: %v", newest, err)
		return ExitOK
	}
	if !isRunechoTree(tmp) {
		freshenLine(w, "the tree at %s is not the runecho source; refused", newest)
		return ExitOK
	}

	if err := vcRunInstall(tmp, binDir, newest, goDir); err != nil {
		freshenLine(w, "reinstall of %s FAILED: %v", newest, err)
		return ExitOK
	}
	// A zero exit is not proof the stamp moved; re-read the just-built binary.
	now := vcReadStamp(self)
	if now == "" || versionBehind(now, newest) {
		freshenLine(w, "reinstall reported success but the binary still says %s (want %s)", disp(now), newest)
		return ExitOK
	}
	freshenLine(w, "installed %s is behind origin's %s — reinstalled, now %s", installed, newest, now)
	return ExitOK
}
