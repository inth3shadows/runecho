package guardstats

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// gitRepo builds a throwaway repo and returns its root.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, root, msg string, when time.Time) string {
	t.Helper()
	stamp := when.Format(time.RFC3339)
	add := exec.Command("git", "-C", root, "add", "-A")
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	c := exec.Command("git", "-C", root, "commit", "-q", "--allow-empty", "-m", msg)
	c.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	rev, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(rev[:40])
}

// The scope split is the correctness core of this oracle. A local binding
// (`path = ...`, `from pathlib import Path`) in some OTHER file must not count
// as a definition — the first run of the audit searched bindings repo-wide and
// scored `fetch`, `Path`, `escape`, `port`, `name` and `path` as guard
// resolution bugs purely because those names are assigned somewhere in every
// Python repo.
func TestGitOracleBindingsAreFileScoped(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "other.py", "def unrelated():\n    path = compute()\n    from pathlib import Path\n    return Path(path)\n")
	write(t, root, "pkg/target.py", "def entry():\n    return 1\n")
	at := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	rev := commit(t, root, "one", at)

	g := GitOracle{Timeout: 20 * time.Second}
	target := "pkg/target.py"

	// `path` and `Path` are bound only in other.py, so they must NOT resolve for
	// an ask about pkg/target.py.
	for _, sym := range []string{"path", "Path"} {
		got, err := g.Defined(root, rev, "py", sym, target)
		if err != nil {
			t.Fatalf("Defined(%q): %v", sym, err)
		}
		if got {
			t.Errorf("Defined(%q, file=pkg/target.py) = true; a binding in another file must not resolve", sym)
		}
	}
	// A declaration, by contrast, IS importable and must resolve repo-wide.
	got, err := g.Defined(root, rev, "py", "unrelated", target)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("Defined(\"unrelated\") = false; a top-level def must resolve repo-wide")
	}
}

// The complement: a binding in the file the ask was about DOES resolve.
func TestGitOracleBindingInOwnFileResolves(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "pkg/target.py", "from pathlib import Path\n\ndef entry():\n    return Path('.')\n")
	rev := commit(t, root, "one", time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))

	g := GitOracle{Timeout: 20 * time.Second}
	got, err := g.Defined(root, rev, "py", "Path", "pkg/target.py")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("Defined(\"Path\") = false; an import in the asked-about file must resolve")
	}
}

// The multi-line `import { … }` member form — the shape the guard's own
// ExtractImports is blind to, and which the oracle must see or it would score
// that guard bug as a correct catch.
func TestGitOracleSeesMultilineJSImportMember(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "src/lib/db.ts",
		"import {\n  makeSeededRandom,\n  depthFor,\n} from './core';\n\nconst r = makeSeededRandom(1);\n")
	rev := commit(t, root, "one", time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))

	g := GitOracle{Timeout: 20 * time.Second}
	for _, sym := range []string{"makeSeededRandom", "depthFor"} {
		got, err := g.Defined(root, rev, "js", sym, "src/lib/db.ts")
		if err != nil {
			t.Fatalf("Defined(%q): %v", sym, err)
		}
		if !got {
			t.Errorf("Defined(%q) = false; a multi-line import member must resolve", sym)
		}
	}
}

// The dated half: the same symbol answers differently at two commits. This is
// the whole premise of the audit and the reason rev is in the memo key.
func TestGitOracleIsDated(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "pkg/a.py", "def caller():\n    return later()\n")
	early := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	revEarly := commit(t, root, "caller only", early)

	write(t, root, "pkg/b.py", "def later():\n    return 2\n")
	revLate := commit(t, root, "add callee", early.Add(2*time.Hour))

	g := GitOracle{Timeout: 20 * time.Second}
	file := "pkg/a.py"
	if got, err := g.Defined(root, revEarly, "py", "later", file); err != nil || got {
		t.Errorf("Defined at early rev = %v (err %v), want false", got, err)
	}
	if got, err := g.Defined(root, revLate, "py", "later", file); err != nil || !got {
		t.Errorf("Defined at late rev = %v (err %v), want true", got, err)
	}

	// RevAt must pick the commit that was current at the ask, not HEAD.
	rev, err := g.RevAt(root, early.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if rev != revEarly {
		t.Errorf("RevAt(mid) = %s, want the earlier commit %s", rev, revEarly)
	}
	if _, err := g.RevAt(root, early.Add(-time.Hour)); err == nil {
		t.Error("RevAt before the first commit returned no error; the audit must mark that unknown")
	}
}

// Symbol names reach this code from decisions.jsonl, which records names parsed
// out of repo files. Anything that is not a plain identifier is refused rather
// than escaped for git's ERE dialect.
func TestGitOracleRefusesNonIdentifierSymbols(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "a.py", "x = 1\n")
	rev := commit(t, root, "one", time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))

	g := GitOracle{Timeout: 20 * time.Second}
	for _, bad := range []string{"a b", "x)|(y", "..", "-e", "$(touch pwned)", "a\nb", ""} {
		_, err := g.Defined(root, rev, "py", bad, "a.py")
		if !errors.Is(err, ErrUnsafeSymbol) {
			t.Errorf("Defined(%q) err = %v, want ErrUnsafeSymbol", bad, err)
		}
	}
	// A qualified name is accepted; its final segment is what gets searched.
	if _, err := g.Defined(root, rev, "go", "http.Get", "a.py"); err != nil {
		t.Errorf("Defined(\"http.Get\") err = %v, want nil", err)
	}
}

// Worktrees from the claudew/codexw flow are deleted on session exit, so most of
// a 30-day window names directories that no longer exist. Falling back to a
// sibling worktree is what keeps those asks auditable at all.
func TestGitOracleFallsBackToSiblingWorktree(t *testing.T) {
	parent := t.TempDir()
	live := filepath.Join(parent, "main")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", live}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write(t, live, "a.py", "x = 1\n")
	commit(t, live, "one", time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))

	g := GitOracle{Timeout: 20 * time.Second}
	// A path inside a worktree that was deleted.
	gone := filepath.Join(parent, "claude-20260718-183238", "a.py")
	root, _, err := g.Worktree(gone)
	if err != nil {
		t.Fatalf("Worktree(%s) = %v; expected the sibling to be adopted", gone, err)
	}
	if filepath.Base(root) != "main" {
		t.Errorf("Worktree resolved to %s, want the sibling %s", root, live)
	}

	// A path with no surviving repo anywhere above it is an honest error, not a
	// silent wrong answer.
	if _, _, err := g.Worktree(filepath.Join(t.TempDir(), "nope", "a.py")); err == nil {
		t.Error("Worktree with no repo returned no error")
	}
	if _, _, err := g.Worktree(""); err == nil {
		t.Error("Worktree(\"\") returned no error")
	}
}

// A file at the REPO ROOT is the case the old two-segment pathspec heuristic got
// wrong: its parent segment is the worktree directory name, which by
// construction does not exist in the sibling being substituted. Worktree now
// splits at the surviving ancestor, so the repo-relative path is exact.
func TestGitOracleSiblingHandlesRepoRootFile(t *testing.T) {
	parent := t.TempDir()
	live := filepath.Join(parent, "main")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", live}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	write(t, live, "main.go", "package main\n\nfunc main() {}\n")
	commit(t, live, "one", time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))

	g := GitOracle{Timeout: 20 * time.Second}
	root, rel, err := g.Worktree(filepath.Join(parent, "claude-20260718-183238", "main.go"))
	if err != nil {
		t.Fatalf("repo-root file in a vanished worktree was refused: %v", err)
	}
	if root != live || rel != "main.go" {
		t.Errorf("Worktree = (%q, %q), want (%q, \"main.go\")", root, rel, live)
	}
}

// A worktree that still exists yields its own root and the exact relative path.
func TestGitOracleWorktreeRelForLivePath(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "pkg/target.py", "x = 1\n")
	commit(t, root, "one", time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))
	g := GitOracle{Timeout: 20 * time.Second}
	gotRoot, rel, err := g.Worktree(filepath.Join(root, "pkg", "target.py"))
	if err != nil {
		t.Fatal(err)
	}
	if gotRoot != root || rel != "pkg/target.py" {
		t.Errorf("Worktree = (%q, %q), want (%q, \"pkg/target.py\")", gotRoot, rel, root)
	}
}

// When a whole REPO directory is gone (not just one worktree) the surviving
// ancestor is the projects root, whose children are dozens of unrelated repos.
// Adopting one of those answers every dated question against a history that has
// never seen the file — a plausible verdict computed from the wrong repository,
// which is strictly worse than reporting the ask unauditable.
func TestGitOracleRefusesUnrelatedSiblingRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	parent := t.TempDir()
	for _, name := range []string{"aaa-unrelated", "zzz-other"} {
		r := filepath.Join(parent, name)
		if err := os.MkdirAll(r, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, a := range [][]string{
			{"init", "-q", "-b", "main"},
			{"config", "user.email", "t@example.com"},
			{"config", "user.name", "t"},
		} {
			if out, err := exec.Command("git", append([]string{"-C", r}, a...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", a, err, out)
			}
		}
		write(t, r, "x.py", "def unrelated_thing():\n    pass\n")
		commit(t, r, "one", time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))
	}

	g := GitOracle{Timeout: 20 * time.Second}
	// A file from a third repo that no longer exists anywhere.
	if root, _, err := g.Worktree(filepath.Join(parent, "deleted-repo", "src", "app.py")); err == nil {
		t.Errorf("adopted unrelated repo %q; want an error so the ask is marked unknown", root)
	}

	// The complement: a sibling that DOES hold the path is still adopted, so the
	// guard against wrong repos has not disabled the fallback it protects.
	live := filepath.Join(parent, "aaa-unrelated")
	write(t, live, "src/app.py", "def app():\n    pass\n")
	commit(t, live, "add app", time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC))
	root, _, err := g.Worktree(filepath.Join(parent, "deleted-worktree", "src", "app.py"))
	if err != nil {
		t.Fatalf("sibling holding the path was refused: %v", err)
	}
	if root != live {
		t.Errorf("adopted %q, want %q", root, live)
	}
}

// A file deleted from HEAD but present in history still identifies its repo —
// requiring it to exist right now would reject correct repos.
func TestGitOracleSiblingMatchUsesHistoryNotWorktree(t *testing.T) {
	parent := t.TempDir()
	live := filepath.Join(parent, "main")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", live}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	write(t, live, "src/old.py", "def gone():\n    pass\n")
	commit(t, live, "add", time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))
	if err := os.Remove(filepath.Join(live, "src", "old.py")); err != nil {
		t.Fatal(err)
	}
	commit(t, live, "delete", time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC))

	g := GitOracle{Timeout: 20 * time.Second}
	if _, _, err := g.Worktree(filepath.Join(parent, "gone-worktree", "src", "old.py")); err != nil {
		t.Errorf("refused a repo whose history holds the path: %v", err)
	}
}

// A repo MIGRATED from a plain clone to the bare-worktree layout keeps its old
// path: `~/personal_projects/quarry/context.py` still resolves to a surviving
// directory, which merely stopped being a worktree. There the whole remainder IS
// the repo-relative path, with no vanished component to drop. Assuming otherwise
// silently refused 28 real quarry asks that quarry/main could answer.
func TestGitOracleSiblingHandlesMigratedBareLayout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	container := filepath.Join(t.TempDir(), "quarry")
	live := filepath.Join(container, "main")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", live}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	write(t, live, "context.py", "def ctx():\n    pass\n")
	commit(t, live, "one", time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC))

	g := GitOracle{Timeout: 20 * time.Second}
	// The container survives but is not itself a worktree.
	root, rel, err := g.Worktree(filepath.Join(container, "context.py"))
	if err != nil {
		t.Fatalf("migrated-layout path was refused: %v", err)
	}
	if root != live || rel != "context.py" {
		t.Errorf("Worktree = (%q, %q), want (%q, \"context.py\")", root, rel, live)
	}
}
