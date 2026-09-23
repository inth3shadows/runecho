package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/version"
)

// installTimeout bounds one rebuild (three go builds, plus a possible
// GOTOOLCHAIN download on a mismatched Go). Rebuilds run from the periodic job
// and an explicit `--reinstall` only — never from a hook — but an unbounded one
// would still let a hung build overlap the next hourly tick. On timeout we fail
// open (one log line). Part of the per-tick budget pinned against
// freshenInterval.
var installTimeout = 5 * time.Minute // var only so a test can shrink it

// version-check keeps the INSTALLED runecho binaries in step with the source a
// worktree has checked out. It exists because on 2026-07-23 the installed guard
// went stale three times in one session while newer versions shipped, and two of
// that session's published quality numbers were fossils written by an old binary.
// "Reinstall after every merge" is not a fix — that habit had already failed
// three times.
//
// Two modes, split by #375:
//   - ADVISORY (no flag; what the post-merge/post-checkout hooks run with
//     --quiet): compares the installed stamp with the nearest tag reachable from
//     HEAD and prints one BEHIND line. It NEVER fetches and NEVER executes
//     anything, so a checkout — of anyone's branch — runs nothing.
//   - --reinstall: delegates to freshen, which builds origin's newest release
//     from an exported tree (see freshen.go for the trust statement). The
//     periodic job calls freshen directly on a timer.
//
// It never fails the operation it hooks — every exit path is ExitOK.

const runechoModuleLine = "module github.com/inth3shadows/runecho"

// semverCore, semverLess and parseSemver delegate to internal/version, the
// single shared implementation (lifted 2026-08-12, #331) — `runecho-ir doctor`
// needs the identical comparison and cannot import this `main` package, so the
// logic lives where both can reach it. Kept as package-local wrappers here
// (rather than rewriting every call site to `version.SemverCore`, etc.) to
// keep this diff to the move alone.
func semverCore(s string) string          { return version.SemverCore(s) }
func semverLess(a, b string) bool         { return version.SemverLess(a, b) }
func parseSemver(s string) ([3]int, bool) { return version.ParseSemver(s) }

// versionBehind reports whether the installed core is strictly older than the
// newest core — the one case that warrants a rebuild. Equal or ahead (an older
// branch checked out) is never behind, so a checkout can never downgrade.
func versionBehind(installed, newest string) bool {
	return semverLess(installed, newest)
}

// isRunechoTree reports whether top is the runecho source tree, by its go.mod
// module path plus a sibling install.sh. Hooks get installed into OTHER repos
// (the README tells users to run install.sh from the target repo); without this
// the hook would run a foreign project's install.sh on every merge and, because
// that never moves runecho's version, re-fire forever.
func isRunechoTree(top string) bool {
	if _, err := os.Stat(filepath.Join(top, "install.sh")); err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(top, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == runechoModuleLine {
			return true
		}
	}
	return false
}

// Seams overridden in tests. Real implementations shell out to git / install.sh.
var (
	// vcNewestTag returns the nearest tag by ancestry (git describe --abbrev=0).
	// On this repo's monotonic, linear release history (pre-push hook, #51) that
	// is also the highest version reachable; the direction is safe regardless
	// (under-reporting only ever skips a rebuild, never forces a downgrade).
	vcNewestTag = gitutil.DescribeTag
	// vcRunInstall runs install.sh from the exported tree at top, stamped
	// version, targeting binDir (where the currently-running binary lives) so a
	// custom-dir install refreshes in place rather than spraying a second copy
	// into ~/.local/bin, with goDir prepended to PATH (see resolveGoDir).
	vcRunInstall = defaultRunInstall
	// vcReadStamp returns the version the freshly-installed binary at path
	// reports (`<path> --version`) — read AFTER a reinstall to confirm the stamp
	// actually advanced, since a build can exit 0 without moving it.
	vcReadStamp = defaultReadStamp
)

func defaultRunInstall(top, binDir, version, goDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(top, "install.sh"))
	cmd.Dir = top
	killGroupOnCancel(cmd)
	// RUNECHO_VERSION: the exported tree has no .git, so install.sh's own
	// `git describe` would stamp "dev"; the version is known — it is the tag
	// freshen chose. Appended last so they win over any inherited value.
	cmd.Env = append(os.Environ(),
		"RUNECHO_BIN_DIR="+binDir,
		"RUNECHO_VERSION="+version,
		"PATH="+goDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("install.sh timed out after %s", installTimeout)
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func defaultReadStamp(binPath string) string {
	out, err := exec.Command(binPath, "--version").Output()
	if err != nil {
		return ""
	}
	return semverCore(string(out))
}

// runVersionCheck reports installed-vs-newest-reachable-tag and, with --reinstall,
// installs origin's newest release when behind (freshen). Always returns ExitOK:
// a freshness check must never fail the git operation that triggered it.
func runVersionCheck(args []string) int {
	fs := flag.NewFlagSet("version-check", flag.ContinueOnError)
	reinstall := fs.Bool("reinstall", false, "install origin's newest release when the installed binary is behind it (fetches; builds an exported tree, never the checkout)")
	quiet := fs.Bool("quiet", false, "print nothing when already up to date or not applicable (for hook use); with --reinstall, stays the offline advisory (legacy hook bodies)")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	// Opt-out: the hooks honour this so a user who wants to run a pinned build
	// is never overridden.
	if os.Getenv("RUNECHO_NO_AUTO_INSTALL") == "1" {
		return ExitOK
	}

	start := "."
	if fs.NArg() > 0 {
		start = fs.Arg(0)
	}
	top, err := gitutil.TopLevel(start)
	if err != nil {
		vcInfo(*quiet, "version-check: not inside a git working tree; nothing to check")
		return ExitOK
	}

	if !isRunechoTree(top) {
		vcInfo(*quiet, "version-check: %s is not the runecho source tree; nothing to do", top)
		return ExitOK
	}

	// --reinstall never builds THIS tree: it builds origin's newest release from
	// the repo this tree belongs to (#375). The checked-out revision only
	// identifies which repository to ask.
	//
	// `--reinstall --quiet` together is the body every hook written BEFORE #375
	// runs, and installed hooks are only rewritten by re-running `install`. So
	// that exact combination stays the offline advisory it has to be on a git
	// operation's latency path — no network, no build, silent when current —
	// rather than turning every checkout into a fetch and a possible 5-minute
	// build. A person asking for a rebuild does not pass --quiet.
	if *reinstall && !*quiet {
		gitDir, err := gitutil.CommonDir(top)
		if err != nil {
			fmt.Fprintf(os.Stderr, "version-check: cannot resolve the git dir of %s: %v\n", top, err)
			return ExitOK
		}
		fmt.Fprintln(os.Stderr, "version-check: asking origin for its newest release (may fetch and build — can take a few minutes)...")
		return freshen(gitDir, os.Stdout)
	}

	installed := semverCore(version.Version)
	newest := semverCore(func() string { t, _ := vcNewestTag(top); return t }())
	if newest == "" {
		vcInfo(*quiet, "version-check: no tag reachable from HEAD; nothing to compare")
		return ExitOK
	}

	if !versionBehind(installed, newest) {
		vcInfo(*quiet, "version-check: installed %s is up to date with %s", disp(installed), newest)
		return ExitOK
	}

	fmt.Printf("version-check: installed %s is BEHIND %s — run 'bash %s/install.sh' or 'runecho-ir version-check --reinstall' (installs origin's newest release)\n",
		disp(installed), newest, top)
	return ExitOK
}

// vcInfo prints an informational line unless quiet (hook) mode is on.
func vcInfo(quiet bool, format string, a ...any) {
	if quiet {
		return
	}
	fmt.Printf(format+"\n", a...)
}

// disp renders an empty/unreadable version as "unknown" rather than a blank.
func disp(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
