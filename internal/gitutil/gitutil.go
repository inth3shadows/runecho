// Package gitutil holds the git invocations shared by the runecho commands.
// Keeping the git-common-dir resolution in one place is a correctness
// requirement, not just deduplication: the value is used as a repo lookup KEY
// (schema V4 common_dir). If enroll-time and guard-time computed it even
// slightly differently — relative vs absolute, trailing slash, un-cleaned —
// the keys would not match and the O(1) lookup would silently miss.
package gitutil

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Timeout bounds every git subprocess so a hung git can never block a
// PreToolUse hook.
const Timeout = 2 * time.Second

// runGit runs `git -C dir <args...>` under Timeout and returns its stdout.
// stderr is captured separately (not via CombinedOutput) so a warning git
// prints on success can't corrupt the stdout we parse, while error returns
// still carry git's diagnostic instead of a bare "exit status 128".
func runGit(dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
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
