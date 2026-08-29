// Package gitutil holds the git invocations shared by the runecho commands.
// Keeping the git-common-dir resolution in one place is a correctness
// requirement, not just deduplication: the value is used as a repo lookup KEY
// (schema V4 common_dir). If enroll-time and guard-time computed it even
// slightly differently — relative vs absolute, trailing slash, un-cleaned —
// the keys would not match and the O(1) lookup would silently miss.
package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Timeout bounds every git subprocess so a hung git can never block a
// PreToolUse hook.
const Timeout = 2 * time.Second

// Command builds a hardened `git -C dir <args...>` bound to ctx, shared so every
// git invocation (gitutil here + the guard's staged-diff parser) gets the same
// config-injection defenses when run inside a possibly-untrusted repo:
//   - `-c core.fsmonitor=false` neutralizes a repo-local `core.fsmonitor = <prog>`
//     (a known RCE vector git runs on many commands); a command-line -c overrides
//     repo/global config.
//   - GIT_CONFIG_NOSYSTEM=1 ignores /etc/gitconfig.
//   - GIT_TERMINAL_PROMPT=0 prevents an interactive-credential hang.
//
// The commands runecho runs today (rev-parse, worktree list, diff --cached) don't
// invoke config-defined programs, so this is a standing guard rather than a fix
// for a live vector.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor=false", "-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	return cmd
}

// runGit runs `git -C dir <args...>` under Timeout and returns its stdout.
// stderr is captured separately (not via CombinedOutput) so a warning git
// prints on success can't corrupt the stdout we parse, while error returns
// still carry git's diagnostic instead of a bare "exit status 128".
func runGit(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	var stderr strings.Builder
	cmd := Command(ctx, dir, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}

// CommonDir returns the absolute, cleaned git-common-dir for the repo
// containing dir. This is the stable identity shared by every worktree of a
// repo (bare or not): the bare root for bare repos, <root>/.git for regular
// repos. It is NOT stripped to a working-tree root — the raw common-dir is the
// canonical key. dir must be absolute so a relative common-dir resolves
// deterministically.
func CommonDir(dir string) (string, error) {
	// dir must be absolute: a relative dir with a relative common-dir would join
	// to a relative (and cwd-dependent) key, silently breaking the V4 lookup. The
	// contract was documented but unenforced — make it a hard error.
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("gitutil.CommonDir: dir must be absolute, got %q", dir)
	}
	out, err := runGit(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	cd := strings.TrimSpace(string(out))
	if !filepath.IsAbs(cd) {
		cd = filepath.Join(dir, cd)
	}
	return filepath.Clean(cd), nil
}

// TopLevel returns the absolute, cleaned path of the git working tree containing
// dir (equivalent to git rev-parse --show-toplevel). Returns an error when dir is
// not inside a git working tree (e.g. a bare repository root or a non-git dir).
// The result is filepath.Clean'd for symmetry with CommonDir, so callers that
// compare or join the two never trip over an un-normalized separator.
func TopLevel(dir string) (string, error) {
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}

// AbsGitDir returns the absolute path to the git directory (common-dir) for the
// repo containing dir. This is the correct location for hook files: it is stable
// across all worktrees and is the path git itself reads hooks from.
func AbsGitDir(dir string) (string, error) {
	return CommonDir(dir)
}

// DescribeTag returns the nearest tag reachable from HEAD in the repo containing
// dir (equivalent to `git describe --tags --abbrev=0`). It reads only tags that
// already exist locally — it never fetches — so it answers "what release is this
// checkout at", not "what is the newest release on the remote". Returns an error
// when no tag is reachable (a shallow clone, or a repo with no tags).
func DescribeTag(dir string) (string, error) {
	out, err := runGit(dir, "describe", "--tags", "--abbrev=0")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// HooksPath returns the configured core.hooksPath for the repo containing dir, or
// "" when it is unset. A set hooksPath means git runs hooks from THERE, not from
// the common-dir's hooks/ — so a hook written to the common-dir would be silently
// ignored. `git config --get` exits non-zero when the key is absent; that is the
// expected unset case, reported as "" with no error.
func HooksPath(dir string) string {
	out, err := runGit(dir, "config", "--get", "core.hooksPath")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// WorktreePaths returns all working-tree paths registered for the git repo
// containing dir, parsed from `git worktree list --porcelain`. Returns nil on
// any error (non-git dir, git not available).
func WorktreePaths(dir string) []string {
	out, err := runGit(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if after, ok := strings.CutPrefix(line, "worktree "); ok {
			if p := strings.TrimSpace(after); p != "" {
				paths = append(paths, p)
			}
		}
	}
	return paths
}

// RemoteDefaultRef returns the remote-tracking ref for remote's default branch:
// refs/remotes/<remote>/HEAD when git knows it, otherwise whichever ONE of
// main/master exists.
//
// Why a fallback exists at all, stated accurately (an earlier version of this
// comment named two reasons that are false on modern git, measured on 2.55:
// `clone --single-branch`, `init` + `fetch`, and this repo's bare `repo-init`
// form ALL create the symref, because git 2.41 added "fetch: set remote/HEAD if
// it does not exist"). The two that hold: git < 2.41, and a symref that has been
// removed with `symbolic-ref -d` — which this package's own test does.
//
// Why it refuses when BOTH main and master exist: picking by name is a coin flip
// with no signal in it, and the caller (#373) uses the answer as a trust anchor.
// Guessing wrong measures containment against the wrong branch and silently
// refuses a legitimate HEAD — a stale binary with no error, which is worse than
// the loud failure of refusing here. Note "prefer whichever contains HEAD" is NOT
// an option: choosing the ref that contains HEAD makes the trust gate
// self-satisfying.
func RemoteDefaultRef(dir, remote string) (string, error) {
	if out, err := runGit(dir, "symbolic-ref", "--quiet", "refs/remotes/"+remote+"/HEAD"); err == nil {
		// Verify the target resolves. A symref can outlive what it points at:
		// `git update-ref -d refs/remotes/origin/HEAD` deletes the TARGET and
		// leaves the symref behind, after which symbolic-ref still happily prints
		// a ref that no longer exists. Returning it would hand the caller a name
		// that every later git command rejects — and a caller that fails closed
		// would read that as "not trusted" rather than "ask a different way".
		if ref := strings.TrimSpace(string(out)); ref != "" {
			if _, err := runGit(dir, "rev-parse", "--verify", "--quiet", ref); err == nil {
				return ref, nil
			}
		}
	}
	var found []string
	for _, branch := range []string{"main", "master"} {
		ref := "refs/remotes/" + remote + "/" + branch
		if _, err := runGit(dir, "rev-parse", "--verify", "--quiet", ref); err == nil {
			found = append(found, ref)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("no default branch for remote %q: neither %s/HEAD, %s/main, nor %s/master resolves", remote, remote, remote, remote)
	default:
		return "", fmt.Errorf("cannot tell which branch is the default for remote %q: %s/HEAD is unset and both %s exist — set it with `git remote set-head %s -a`",
			remote, remote, strings.Join(found, " and "), remote)
	}
}

// Contains reports whether rev is an ancestor of ref, or is ref itself.
//
// `git merge-base --is-ancestor` answers in the exit status: 0 yes, 1 no,
// anything else a real failure. The third case must NOT be folded into "no".
// Callers use this as a trust gate, and a git that failed to run is not evidence
// about ancestry — collapsing the two would turn "git is broken" into a verdict.
func Contains(dir, rev, ref string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	var stderr strings.Builder
	cmd := Command(ctx, dir, "merge-base", "--is-ancestor", rev, ref)
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return false, fmt.Errorf("%w: %s", err, msg)
	}
	return false, err
}
