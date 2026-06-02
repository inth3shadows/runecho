// Package gitutil holds the git invocations shared by the runecho commands.
// Keeping the git-common-dir resolution in one place is a correctness
// requirement, not just deduplication: the value is used as a repo lookup KEY
// (schema V4 common_dir). If enroll-time and guard-time computed it even
// slightly differently — relative vs absolute, trailing slash, un-cleaned —
// the keys would not match and the O(1) lookup would silently miss.
package gitutil

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Timeout bounds every git subprocess so a hung git can never block a
// PreToolUse hook.
const Timeout = 2 * time.Second

// CommonDir returns the absolute, cleaned git-common-dir for the repo
// containing dir. This is the stable identity shared by every worktree of a
// repo (bare or not): the bare root for bare repos, <root>/.git for regular
// repos. It is NOT stripped to a working-tree root — the raw common-dir is the
// canonical key. dir must be absolute so a relative common-dir resolves
// deterministically.
func CommonDir(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	cd := strings.TrimSpace(string(out))
	if !filepath.IsAbs(cd) {
		cd = filepath.Join(dir, cd)
	}
	return filepath.Clean(cd), nil
}
