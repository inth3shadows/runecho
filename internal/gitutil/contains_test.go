package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
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

func repoWithCommit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	// Pinned, not inherited: a machine with init.defaultBranch=trunk would make
	// TestRemoteDefaultRef_PrefersSymrefThenFallsBack fail for an environment
	// reason, since the name fallback only knows main and master.
	git(t, "", "-c", "init.defaultBranch=master", "init", "-q", dir)
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

func TestContains_AncestryBothWays(t *testing.T) {
	dir := repoWithCommit(t)
	git(t, dir, "branch", "base")
	git(t, dir, "checkout", "-q", "-b", "ahead")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "extra")

	if ok, err := Contains(dir, "base", "ahead"); err != nil || !ok {
		t.Errorf("Contains(base, ahead) = %v, %v; base IS an ancestor of ahead", ok, err)
	}
	if ok, err := Contains(dir, "ahead", "base"); err != nil || ok {
		t.Errorf("Contains(ahead, base) = %v, %v; ahead is NOT an ancestor of base", ok, err)
	}
	// A commit contains itself — the case a `git pull` that changed nothing hits.
	if ok, err := Contains(dir, "base", "base"); err != nil || !ok {
		t.Errorf("Contains(base, base) = %v, %v; want true", ok, err)
	}
}

// TestContains_FailureIsNotAnAnswer is the one that matters for #373's caller.
// merge-base signals "not an ancestor" with exit 1 and a real failure with
// anything else. Folding the second into the first turns "git could not run"
// into a verdict — and since the caller fails CLOSED on error but merely skips
// on false, the two are only distinguishable here.
func TestContains_FailureIsNotAnAnswer(t *testing.T) {
	dir := repoWithCommit(t)

	ok, err := Contains(dir, "refs/heads/no-such-branch", "HEAD")
	if err == nil {
		t.Fatalf("Contains on an unresolvable rev returned (%v, nil) — a failed git is not evidence of ancestry", ok)
	}
	if ok {
		t.Error("Contains returned true alongside an error")
	}
	if !strings.Contains(err.Error(), "no-such-branch") {
		t.Errorf("error %q does not carry git's diagnostic", err)
	}
}

func TestRemoteDefaultRef_PrefersSymrefThenFallsBack(t *testing.T) {
	up := repoWithCommit(t)
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "-q", up, clone)

	ref, err := RemoteDefaultRef(clone, "origin")
	if err != nil {
		t.Fatalf("RemoteDefaultRef on a fresh clone: %v", err)
	}
	if !strings.HasPrefix(ref, "refs/remotes/origin/") {
		t.Errorf("ref = %q, want a refs/remotes/origin/* ref", ref)
	}

	// A clone made into an existing dir, or with --single-branch, can have working
	// remote-tracking branches and no origin/HEAD symref. Drop it and confirm the
	// name fallback still resolves rather than erroring — a caller that fails
	// closed would otherwise stop working for those users.
	//
	// `symbolic-ref -d`, NOT `update-ref -d`: the latter follows the symref and
	// deletes its TARGET, leaving origin/HEAD pointing at nothing. Written the
	// wrong way this test passes with the fallback deleted, because it never
	// reaches it — which is exactly how it was written first.
	git(t, clone, "symbolic-ref", "-d", "refs/remotes/origin/HEAD")
	ref2, err := RemoteDefaultRef(clone, "origin")
	if err != nil {
		t.Fatalf("RemoteDefaultRef with no origin/HEAD: %v", err)
	}
	if ref2 != ref {
		t.Errorf("fallback resolved %q, want %q", ref2, ref)
	}
}

// TestRemoteDefaultRef_DanglingSymrefFallsBack covers the other half of the same
// discovery: `git update-ref -d refs/remotes/origin/HEAD` removes the TARGET and
// leaves the symref, after which symbolic-ref still prints a ref that resolves
// to nothing. RemoteDefaultRef must skip it rather than hand that name back.
//
// A second valid fallback name is created first, so a failure means the dangling
// symref was trusted — not that there was no answer to find.
func TestRemoteDefaultRef_DanglingSymrefFallsBack(t *testing.T) {
	up := repoWithCommit(t)
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "-q", up, clone)

	ref, err := RemoteDefaultRef(clone, "origin")
	if err != nil {
		t.Fatal(err)
	}
	other := "refs/remotes/origin/master"
	if ref == other {
		other = "refs/remotes/origin/main"
	}
	git(t, clone, "update-ref", other, "HEAD")
	git(t, clone, "update-ref", "-d", ref) // the symref's target; the symref stays

	got, err := RemoteDefaultRef(clone, "origin")
	if err != nil {
		t.Fatalf("RemoteDefaultRef with a dangling symref: %v", err)
	}
	if got == ref {
		t.Fatalf("returned the dangling symref target %q instead of falling back", got)
	}
	if _, err := Contains(clone, "HEAD", got); err != nil {
		t.Errorf("RemoteDefaultRef returned %q, which git cannot resolve: %v", got, err)
	}
}

// TestRemoteDefaultRef_FallbackCoversMasterAndMain pins BOTH names. A fallback
// that only knows one of them silently stops working for every repo using the
// other, and the caller fails closed — so the symptom is a freshness check that
// quietly never fires, not an error.
func TestRemoteDefaultRef_FallbackCoversMasterAndMain(t *testing.T) {
	for _, branch := range []string{"main", "master"} {
		t.Run(branch, func(t *testing.T) {
			if _, err := exec.LookPath("git"); err != nil {
				t.Skip("git not available")
			}
			up := t.TempDir()
			git(t, "", "-c", "init.defaultBranch="+branch, "init", "-q", up)
			if err := os.WriteFile(filepath.Join(up, "f"), []byte("a\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git(t, up, "add", ".")
			git(t, up, "commit", "-qm", "init")

			clone := filepath.Join(t.TempDir(), "clone")
			git(t, "", "clone", "-q", up, clone)
			git(t, clone, "symbolic-ref", "-d", "refs/remotes/origin/HEAD")

			got, err := RemoteDefaultRef(clone, "origin")
			if err != nil {
				t.Fatalf("no origin/HEAD and default branch %q: %v", branch, err)
			}
			if want := "refs/remotes/origin/" + branch; got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestRemoteDefaultRef_ErrorsWithNoRemote(t *testing.T) {
	dir := repoWithCommit(t)
	if ref, err := RemoteDefaultRef(dir, "origin"); err == nil {
		t.Errorf("RemoteDefaultRef = %q, nil on a repo with no origin; want an error so callers fail closed", ref)
	}
}

// TestRemoteDefaultRef_RefusesToGuessBetweenMainAndMaster — picking by name when
// both exist is a coin flip, and the caller (#373) uses the answer as a trust
// anchor. Guessing wrong measures containment against the wrong branch and
// silently refuses a legitimate HEAD: a stale binary with no error, which is
// worse than the loud failure of refusing here.
func TestRemoteDefaultRef_RefusesToGuessBetweenMainAndMaster(t *testing.T) {
	up := repoWithCommit(t)
	clone := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "-q", up, clone)

	// Both candidates present, and no symref to say which is the default.
	git(t, clone, "update-ref", "refs/remotes/origin/main", "HEAD")
	git(t, clone, "update-ref", "refs/remotes/origin/master", "HEAD")
	git(t, clone, "symbolic-ref", "-d", "refs/remotes/origin/HEAD")

	ref, err := RemoteDefaultRef(clone, "origin")
	if err == nil {
		t.Fatalf("RemoteDefaultRef = %q, nil with both main and master present and no symref; want an error rather than a guess", ref)
	}
	if !strings.Contains(err.Error(), "remote set-head") {
		t.Errorf("error %q does not tell the user how to resolve it", err)
	}
}
