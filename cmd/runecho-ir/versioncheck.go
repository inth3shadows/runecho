package main

import (
	"context"
	"flag"
	"fmt"
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

// installTimeout bounds the foreground rebuild. The hook runs synchronously, so
// git blocks on it — an unbounded `bash install.sh` (three go builds, plus a
// possible GOTOOLCHAIN download on a mismatched Go) could hang an interactive
// `git pull`/`git checkout` indefinitely. On timeout we fail open (advisory),
// which is strictly better than a hung git operation.
const installTimeout = 5 * time.Minute

// version-check keeps the INSTALLED runecho binaries in step with the source a
// worktree has checked out. It exists because on 2026-07-23 the installed guard
// went stale three times in one session while newer versions shipped, and two of
// that session's published quality numbers were fossils written by an old binary.
// "Reinstall after every merge" is not a fix — that habit had already failed
// three times. So the post-merge/post-checkout hooks call this automatically.
//
// This is the standalone, directly-testable form of that logic. It lives here —
// not only inside a generated hook body — because logic reachable only through a
// hook entry point is exactly the untested-surface gap #227 names.
//
// It NEVER fetches (that would tax every pull and worktree creation): it compares
// against tags already present locally, guaranteeing source/install parity, not
// freshness against the remote. And it never fails the operation it hooks — every
// exit path is ExitOK.

const runechoModuleLine = "module github.com/inth3shadows/runecho"

// semverCoreRE matches the leading vX.Y.Z of a version string, dropping any
// `-N-gsha`/`-dirty` build suffix. A post-tag build reports v0.16.1-3-gabc1234,
// and comparing full describe strings with sort -V orders such suffixes
// inconsistently — comparing only the core makes "ahead of the tag" read as
// "not behind".
var semverCoreRE = regexp.MustCompile(`v[0-9]+\.[0-9]+\.[0-9]+`)

// semverCore extracts the first vX.Y.Z occurrence from s, or "" if none.
func semverCore(s string) string {
	return semverCoreRE.FindString(s)
}

// semverLess reports whether core a is strictly semver-less than core b. Both
// must be vX.Y.Z cores (as returned by semverCore); a malformed or empty input
// yields false, so an unreadable version never triggers a downgrade or a rebuild.
func semverLess(a, b string) bool {
	ap, aok := parseSemver(a)
	bp, bok := parseSemver(b)
	if !aok || !bok {
		return false
	}
	for i := 0; i < 3; i++ {
		if ap[i] != bp[i] {
			return ap[i] < bp[i]
		}
	}
	return false
}

// parseSemver parses "vX.Y.Z" into [3]int. ok=false on any malformed input.
func parseSemver(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n := 0
		if p == "" {
			return out, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return out, false
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out, true
}

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
	// vcRunInstall rebuilds the binaries from the source tree at top, targeting
	// binDir (where the currently-running binary lives) so a custom-dir install
	// refreshes in place rather than spraying a second copy into ~/.local/bin.
	vcRunInstall = defaultRunInstall
	// vcReadStamp returns the version the freshly-installed binary at path
	// reports (`<path> --version`) — read AFTER a reinstall to confirm the stamp
	// actually advanced, since a build can exit 0 without moving it.
	vcReadStamp = defaultReadStamp
)

func defaultRunInstall(top, binDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(top, "install.sh"))
	cmd.Dir = top
	cmd.Env = append(os.Environ(), "RUNECHO_BIN_DIR="+binDir)
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
// rebuilds when the installed binary is behind. Always returns ExitOK: a freshness
// check must never fail the git operation that triggered it.
func runVersionCheck(args []string) int {
	fs := flag.NewFlagSet("version-check", flag.ContinueOnError)
	reinstall := fs.Bool("reinstall", false, "rebuild from source when the installed binary is behind the newest reachable tag")
	quiet := fs.Bool("quiet", false, "print nothing when already up to date or not applicable (for hook use)")
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

	// Behind. Report-only mode never touches the disk.
	if !*reinstall {
		fmt.Printf("version-check: installed %s is BEHIND %s — run 'bash %s/install.sh' or 'runecho-ir version-check --reinstall'\n",
			disp(installed), newest, top)
		return ExitOK
	}

	// Windows cannot replace a running .exe, so install.sh's rebuild of
	// runecho-ir.exe would fail on every checkout and the hook would spam a failed
	// build. Fall back to the advisory and let the user reinstall when no runecho
	// process holds the file — better than a self-inflicted error loop.
	if runtime.GOOS == "windows" {
		fmt.Printf("version-check: installed %s is BEHIND %s — run 'bash %s/install.sh' (auto-reinstall is skipped on Windows: a running binary can't be replaced)\n",
			disp(installed), newest, top)
		return ExitOK
	}

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "runecho: cannot resolve own path, skipping auto-reinstall: %v\n", err)
		return ExitOK
	}
	binDir := filepath.Dir(self)

	fmt.Printf("runecho: installed %s is behind %s — reinstalling...\n", disp(installed), newest)
	if err := vcRunInstall(top, binDir); err != nil {
		fmt.Fprintf(os.Stderr, "runecho: reinstall FAILED — run 'bash install.sh' from %s: %v\n", top, err)
		return ExitOK
	}

	// A zero exit is not proof the stamp moved (tags unfetched, wrong tree, a
	// stamping change). Re-read the just-built binary; reporting success from the
	// exit status alone would leave a stale binary re-firing on every checkout.
	now := vcReadStamp(self) // already a vX.Y.Z core (defaultReadStamp applies semverCore)
	if versionBehind(now, newest) || now == "" {
		fmt.Fprintf(os.Stderr, "runecho: reinstall reported success but the binary still says %s (want %s)\n",
			disp(now), newest)
	} else {
		fmt.Printf("runecho: now %s\n", now)
	}
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
