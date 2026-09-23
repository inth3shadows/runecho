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
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/version"
)

// freshen keeps the INSTALLED binaries at the newest release (#375). It is the
// only code path that rebuilds them automatically, and it runs from exactly two
// places: its own hourly schedule entry (`runecho-ir freshen <git-common-dir>`,
// written by `install --periodic` beside the reindex entry) and an explicit
// `runecho-ir version-check --reinstall`. The git hooks only advise
// (`version-check --quiet`), so nothing on a checkout or merge executes
// anything.
//
// Its OWN entry, not a flag on `repo reindex` (#375 review): a binary that
// predates freshen — e.g. `bash install.sh` from an old worktree — would reject
// an unknown reindex flag and skip the hourly reindex altogether, and every
// guard answer is computed from that index. Separately scheduled, such a binary
// fails only this line.
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
	// freshenInterval is the schedule's cadence. freshen's worst case fits inside
	// it (pinned by TestFreshen_TickBudgetBelowInterval; installTimeout is a hard
	// limit — see killGroupOnCancel), but an interactive --reinstall can still
	// coincide with a tick. So runs take freshenLockFile WITHOUT waiting: a
	// second run logs that one is already in progress and skips, rather than
	// building into the same bin dir at once or queueing behind a hung build.
	freshenInterval = time.Hour
	freshenLockFile = "freshen.lock"
	// noAutoInstallFile is the opt-out a scheduled job can actually see: cron
	// and launchd never read a shell profile, so RUNECHO_NO_AUTO_INSTALL
	// exported there would be invisible to the only automatic rebuilder left.
	noAutoInstallFile = "no-auto-install"
	// maxFreshenLine caps one log line. install.sh's failure output (a compile
	// error, a module download) runs to many lines; the log's contract is one
	// timestamped line per tick.
	maxFreshenLine = 1500
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
// found: the toolchain that built this binary first, then PATH, then the fixed
// candidates. GOROOT leads because it is known-good — it just built a working
// runecho — while cron's /usr/bin:/bin PATH can hold a distro `go` too old for
// go.mod (Debian ships GOTOOLCHAIN=local, so it fails rather than upgrading).
// Pure over its three inputs so every branch is testable.
func resolveGoDir(lookPath func(string) (string, error), goroot string, exists func(string) bool) string {
	if goroot != "" {
		if dir := filepath.Join(goroot, "bin"); exists(filepath.Join(dir, "go")) {
			return dir
		}
	}
	if p, err := lookPath("go"); err == nil {
		return filepath.Dir(p)
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

// freshenLine writes exactly one timestamped line: embedded newlines (install.sh
// output in an error) are folded and the message is capped at maxFreshenLine.
func freshenLine(w io.Writer, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	msg = strings.ReplaceAll(strings.ReplaceAll(msg, "\r\n", "\n"), "\n", " | ")
	if r := []rune(msg); len(r) > maxFreshenLine {
		msg = string(r[:maxFreshenLine]) + "…"
	}
	fmt.Fprintf(w, "%s freshen: %s\n", fzNow().UTC().Format(time.RFC3339), msg)
}

// freshen brings the installed binaries up to origin's newest release, building
// from gitDir (a git common dir: `.bare` in a bare-worktree layout, `<root>/.git`
// in a plain clone). Always ExitOK — see the file comment. Serialized across
// processes on $RUNECHO_HOME/freshen.lock (see freshenInterval); if the lock
// cannot be taken it runs unlocked, as every other lock in this codebase does.
func freshen(gitDir string, w io.Writer) int {
	if os.Getenv("RUNECHO_NO_AUTO_INSTALL") == "1" {
		freshenLine(w, "RUNECHO_NO_AUTO_INSTALL=1 — skipped")
		return ExitOK
	}
	dir, err := runechoDir()
	if err != nil {
		return freshenLocked(gitDir, w)
	}
	if _, err := os.Stat(filepath.Join(dir, noAutoInstallFile)); err == nil {
		freshenLine(w, "%s exists — skipped (remove it to resume automatic updates)", filepath.Join(dir, noAutoInstallFile))
		return ExitOK
	}
	release, ok := tryFreshenLock(dir)
	if !ok {
		freshenLine(w, "another freshen is still running (%s held) — skipped", filepath.Join(dir, freshenLockFile))
		return ExitOK
	}
	defer release()
	return freshenLocked(gitDir, w)
}

// runFreshenCmd is `runecho-ir freshen <git-common-dir>`, the scheduled entry.
// Exit 0 on every path once the argument is present — see freshen.
func runFreshenCmd(args []string) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir freshen <git-common-dir>   (normally run by the periodic job; by hand, use `version-check --reinstall`)")
		return ExitError
	}
	return freshen(args[0], os.Stdout)
}

func freshenLocked(gitDir string, w io.Writer) int {
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
