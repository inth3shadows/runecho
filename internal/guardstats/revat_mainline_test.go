package guardstats

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func gitAt(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initRepoAt creates a repository at dir (which must not exist yet).
func initRepoAt(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, dir, "init", "-q", "-b", "master")
	gitAt(t, dir, "config", "user.email", "t@example.com")
	gitAt(t, dir, "config", "user.name", "t")
}

func violationAsk(ts time.Time, file, sym string) Decision {
	return Decision{TS: ts, Mode: "hook", Decision: "ask", File: file, Lang: "go",
		Reason: "violations", Symbols: []string{sym}, LearnSymbols: []string{sym},
		ClaimSymbols: map[string][]string{"violations": {sym}}}
}

// #494: the recorded worktree is gone and Worktree adopts a sibling whose own
// branch was cut before mainline defined the symbol. "Defined at ask time"
// must be asked of mainline at the ask, not of the sibling's stale branch —
// otherwise a real false positive is scored premature.
func TestAuditAdoptedSiblingWalksMainline(t *testing.T) {
	parent := t.TempDir()
	sib := filepath.Join(parent, "claude-20260901")
	initRepoAt(t, sib)
	write(t, sib, "a.go", "package a\n")
	c1 := commit(t, sib, "c1", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	write(t, sib, "a.go", "package a\n\nfunc Foo() {}\n")
	c2 := commit(t, sib, "c2", time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	gitAt(t, sib, "update-ref", "refs/remotes/origin/master", c2)
	gitAt(t, sib, "checkout", "-q", "-b", "claude-20260901", c1)

	gone := filepath.Join(parent, "claude-20260903-gone", "a.go")
	ask := violationAsk(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC), gone, "Foo")
	s := Audit([]Decision{ask}, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), GitOracle{Timeout: 20 * time.Second})
	if got := verdictOf(t, s, "Foo"); got != VerdictFP {
		t.Errorf("Foo = %q, want %q: mainline defined it before the ask", got, VerdictFP)
	}
}

// The recorded worktree still answers from its OWN branch: the agent saw that
// tree. A symbol its branch defined before the ask, never merged to mainline,
// is a false positive — walking mainline here would hide it.
func TestAuditRecordedWorktreeWalksItsOwnBranch(t *testing.T) {
	parent := t.TempDir()
	wt := filepath.Join(parent, "claude-20260901")
	initRepoAt(t, wt)
	write(t, wt, "a.go", "package a\n")
	c1 := commit(t, wt, "c1", time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	gitAt(t, wt, "update-ref", "refs/remotes/origin/master", c1)
	gitAt(t, wt, "checkout", "-q", "-b", "claude-20260901")
	write(t, wt, "a.go", "package a\n\nfunc Bar() {}\n")
	commit(t, wt, "c2", time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))

	ask := violationAsk(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC), filepath.Join(wt, "a.go"), "Bar")
	s := Audit([]Decision{ask}, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), GitOracle{Timeout: 20 * time.Second})
	if got := verdictOf(t, s, "Bar"); got != VerdictFP {
		t.Errorf("Bar = %q, want %q: the edited worktree's branch defined it before the ask", got, VerdictFP)
	}
}

// Audit walks HEAD for a file inside the worktree it resolved, and the Head
// commit for a file whose worktree was adopted (#494).
func TestAuditRevAtRefChoice(t *testing.T) {
	o := &fakeOracle{head: "MAINLINE", revAt: map[string]string{"": "OLD"}}
	in := []Decision{
		violationAsk(auditTS("2026-07-10T10:00:00Z"), "/wt/a.go", "Foo"),
		violationAsk(auditTS("2026-07-10T11:00:00Z"), "/gone/b.go", "Bar"),
	}
	Audit(in, auditTS("2026-07-01T00:00:00Z"), o)
	if len(o.refs) != 2 || o.refs[0] != "HEAD" || o.refs[1] != "MAINLINE" {
		t.Errorf("RevAt refs = %v, want [HEAD MAINLINE]", o.refs)
	}
}

func TestUnderRoot(t *testing.T) {
	for _, tc := range []struct {
		file, root string
		want       bool
	}{
		{"/r/wt/a.go", "/r/wt", true},
		{"/r/wt/sub/a.go", "/r/wt", true},
		{"/r/gone/a.go", "/r/wt", false},
		{"/r/wt2/a.go", "/r/wt", false},
		{"/r/..wt/a.go", "/r", true},
		{"/r", "/r/wt", false},
	} {
		if got := underRoot(tc.file, tc.root); got != tc.want {
			t.Errorf("underRoot(%q, %q) = %v, want %v", tc.file, tc.root, got, tc.want)
		}
	}
}

// Two asks at the same instant, one in the recorded worktree and one adopted,
// must each walk their own ref: the rev cache is keyed on the ref too.
func TestAuditRevCacheKeyedOnRef(t *testing.T) {
	o := &fakeOracle{head: "MAINLINE", revAt: map[string]string{"": "OLD"}}
	at := auditTS("2026-07-10T10:00:00Z")
	in := []Decision{violationAsk(at, "/wt/a.go", "Foo"), violationAsk(at, "/gone/b.go", "Bar")}
	Audit(in, auditTS("2026-07-01T00:00:00Z"), o)
	if len(o.refs) != 2 || o.refs[0] != "HEAD" || o.refs[1] != "MAINLINE" {
		t.Errorf("RevAt refs = %v, want [HEAD MAINLINE]", o.refs)
	}
}

// A ref from outside must never reach git as an option.
func TestGitOracleRevAtRejectsBadRef(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "a.py", "x = 1\n")
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	commit(t, root, "one", at)
	g := GitOracle{Timeout: 20 * time.Second}
	for _, ref := range []string{"", "--all", "-n"} {
		if _, err := g.RevAt(root, ref, at.Add(time.Hour)); err == nil {
			t.Errorf("RevAt(ref=%q) returned no error", ref)
		}
	}
}
