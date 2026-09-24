package guard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseStagedDiff_RealRepo exercises the StdoutPipe/cap rewrite (F2) against
// a real git repo: a normal staged diff must still parse correctly and not be
// flagged partial. The 64 MiB truncation boundary is not exercised (a fixture
// that large is disproportionate); this pins the happy path the rewrite risked.
func TestParseStagedDiff_RealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc F() { Ghost() }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "a.go")

	diffs, partial, err := ParseStagedDiff(context.Background(), dir)
	if err != nil {
		t.Fatalf("ParseStagedDiff: %v", err)
	}
	if partial {
		t.Error("a small staged diff must not be reported partial")
	}
	found := false
	for _, d := range diffs {
		if strings.HasSuffix(d.Path, "a.go") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a.go in the staged diff, got %+v", diffs)
	}
}

func TestParseDiffOutput_SingleFile(t *testing.T) {
	raw := `diff --git a/foo.go b/foo.go
index abc..def 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,5 @@
 package main
+
+func Hello() {
+	fmt.Println("hello")
+}
`
	diffs, _, err := parseDiffOutput(raw)
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 FileDiff, got %d", len(diffs))
	}
	if diffs[0].Path != "foo.go" {
		t.Errorf("path = %q", diffs[0].Path)
	}
	if len(diffs[0].AddedLines) != 4 {
		t.Errorf("expected 4 added lines, got %d: %v", len(diffs[0].AddedLines), diffs[0].AddedLines)
	}
	// Line numbers: hunk @@ -1,3 +1,5 @@ → new starts at 1; context 'package main' advances to 2;
	// then '+' lines at 2,3,4,5.
	if diffs[0].AddedLines[0].LineNo != 2 {
		t.Errorf("first added line no = %d, want 2", diffs[0].AddedLines[0].LineNo)
	}
}

func TestParseDiffOutput_QuotedPath(t *testing.T) {
	// git emits a C-quoted path (octal-escaped UTF-8) when core.quotePath is on.
	// "café.js" → "caf\303\251.js". Without unquoting, the file is silently
	// skipped — a false-negative vector for the guard.
	raw := "diff --git \"a/caf\\303\\251.js\" \"b/caf\\303\\251.js\"\n" +
		"--- \"a/caf\\303\\251.js\"\n" +
		"+++ \"b/caf\\303\\251.js\"\n" +
		"@@ -0,0 +1 @@\n" +
		"+export function brew() {}\n"
	diffs, _, err := parseDiffOutput(raw)
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 FileDiff, got %d", len(diffs))
	}
	if diffs[0].Path != "café.js" {
		t.Errorf("path = %q, want %q", diffs[0].Path, "café.js")
	}
	if len(diffs[0].AddedLines) != 1 {
		t.Errorf("expected 1 added line, got %d", len(diffs[0].AddedLines))
	}
}

func TestParseDiffOutput_DeletionTargetIgnored(t *testing.T) {
	// A pure deletion targets /dev/null; it must not attach to the prior file.
	raw := `diff --git a/keep.go b/keep.go
--- a/keep.go
+++ b/keep.go
@@ -0,0 +1 @@
+func Keep() {}
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1 +0,0 @@
-func Gone() {}
`
	diffs, _, err := parseDiffOutput(raw)
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 FileDiff (deletion ignored), got %d: %+v", len(diffs), diffs)
	}
	if diffs[0].Path != "keep.go" {
		t.Errorf("path = %q, want keep.go", diffs[0].Path)
	}
}

func TestParseDiffOutput_MultiFile(t *testing.T) {
	raw := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -0,0 +1,2 @@
+package a
+func A() {}
diff --git a/b.py b/b.py
--- a/b.py
+++ b/b.py
@@ -0,0 +1 @@
+def b(): pass
`
	diffs, _, err := parseDiffOutput(raw)
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if len(diffs) != 2 {
		t.Fatalf("expected 2 FileDiffs, got %d", len(diffs))
	}
	if diffs[0].Path != "a.go" || diffs[1].Path != "b.py" {
		t.Errorf("paths = %q %q", diffs[0].Path, diffs[1].Path)
	}
	if len(diffs[0].AddedLines) != 2 {
		t.Errorf("a.go: expected 2 added lines, got %d", len(diffs[0].AddedLines))
	}
	if len(diffs[1].AddedLines) != 1 {
		t.Errorf("b.py: expected 1 added line, got %d", len(diffs[1].AddedLines))
	}
}

func TestParseDiffOutput_Empty(t *testing.T) {
	diffs, _, err := parseDiffOutput("")
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if len(diffs) != 0 {
		t.Errorf("expected empty result for empty diff, got %v", diffs)
	}
}

// An oversized diff line (over the 4 MB scanner cap) must set partial=true and
// return only the files that preceded it — never silently swallow the rest.
func TestParseDiffOutput_OversizedLineSetsPartial(t *testing.T) {
	// First file parses cleanly; then a single '+' line longer than the 4 MB cap
	// (a minified blob) trips bufio.ErrTooLong, so b.py after it is never seen.
	huge := strings.Repeat("x", 5*1024*1024)
	raw := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -0,0 +1,1 @@\n+package a\n" +
		"diff --git a/blob.js b/blob.js\n--- a/blob.js\n+++ b/blob.js\n@@ -0,0 +1,1 @@\n+" + huge + "\n" +
		"diff --git a/b.py b/b.py\n--- a/b.py\n+++ b/b.py\n@@ -0,0 +1 @@\n+def b(): pass\n"

	diffs, partial, err := parseDiffOutput(raw)
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if !partial {
		t.Fatal("expected partial=true after an oversized diff line")
	}
	// a.go preceded the blob and must be present; b.py followed it and must be absent.
	for _, d := range diffs {
		if d.Path == "b.py" {
			t.Errorf("b.py followed the oversized line and should not have been parsed, got %v", diffs)
		}
	}
	if len(diffs) == 0 || diffs[0].Path != "a.go" {
		t.Errorf("expected a.go (preceding the blob) to be parsed, got %v", diffs)
	}
}

// A normal diff must report partial=false.
func TestParseDiffOutput_NotPartial(t *testing.T) {
	raw := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -0,0 +1,1 @@\n+package a\n"
	_, partial, err := parseDiffOutput(raw)
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if partial {
		t.Error("a well-formed diff must not be flagged partial")
	}
}

func TestParseDiffOutput_RemovedLinesOnly(t *testing.T) {
	raw := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,3 +1,0 @@
-package main
-func Old() {}
-// comment
`
	diffs, _, err := parseDiffOutput(raw)
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 FileDiff")
	}
	if len(diffs[0].AddedLines) != 0 {
		t.Errorf("expected 0 added lines for remove-only diff, got %d", len(diffs[0].AddedLines))
	}
}

// TestParseDiffOutput_PlusPlusContentLineNotAHeader pins the F110 fix: an added
// line whose CONTENT starts with "++ " renders in the diff as "+++ ...", which
// the header check used to swallow — clearing cur and silently dropping every
// added line for the rest of that file (a guard false-negative vector, e.g. a
// markdown file quoting a diff, or C-style "++ i;" text). Inside a hunk, "+++ "
// must parse as an added line, not a file boundary.
func TestParseDiffOutput_PlusPlusContentLineNotAHeader(t *testing.T) {
	raw := `diff --git a/notes.md b/notes.md
index abc..def 100644
--- a/notes.md
+++ b/notes.md
@@ -0,0 +1,3 @@
+before
+++ quoted diff header
+CallAfter()
`
	diffs, _, err := parseDiffOutput(raw)
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 FileDiff, got %d: %+v", len(diffs), diffs)
	}
	if got := len(diffs[0].AddedLines); got != 3 {
		t.Fatalf("expected 3 added lines, got %d: %+v", got, diffs[0].AddedLines)
	}
	if diffs[0].AddedLines[1].Text != "++ quoted diff header" {
		t.Errorf("middle line text = %q, want the ++-content preserved", diffs[0].AddedLines[1].Text)
	}
	if diffs[0].AddedLines[2].Text != "CallAfter()" || diffs[0].AddedLines[2].LineNo != 3 {
		t.Errorf("line after ++-content = %+v, want CallAfter() at line 3", diffs[0].AddedLines[2])
	}
}

// stagedRepoWithMarkerProgram builds a repo with a.py staged, installs
// `prog` as an executable script that touches a marker file and then runs
// body, and applies configure (repo-local git config naming that script).
// It returns the repo and the marker path: the marker existing after
// ParseStagedDiff means git ran a config-defined program on the pre-commit
// path.
func stagedRepoWithMarkerProgram(t *testing.T, body string, configure func(git func(...string), prog, dir string)) (dir, marker string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir = t.TempDir()
	marker = filepath.Join(t.TempDir(), "ran")
	prog := filepath.Join(t.TempDir(), "prog.sh")
	script := "#!/bin/sh\ntouch '" + marker + "'\n" + body + "\n"
	if err := os.WriteFile(prog, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	configure(git, prog, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.py"), []byte("def f():\n    return ghost()\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "a.py")
	return dir, marker
}

// assertStagedDiffUntouched checks that ParseStagedDiff saw the real staged
// content and that the marker program never ran.
func assertStagedDiffUntouched(t *testing.T, dir, marker string) {
	t.Helper()
	diffs, _, err := ParseStagedDiff(context.Background(), dir)
	if err != nil {
		t.Fatalf("ParseStagedDiff: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("git ran a repo-configured program during the staged diff")
	}
	var got []string
	for _, d := range diffs {
		for _, l := range d.AddedLines {
			got = append(got, l.Text)
		}
	}
	if want := "    return ghost()"; len(got) != 2 || got[1] != want {
		t.Errorf("added lines = %q, want the raw staged content ending in %q", got, want)
	}
}

// TestParseStagedDiff_IgnoresExternalDiff: a repo-local diff.external must
// not run when the guard parses the staged diff. Plumbing diff-index never runs
// it, so this pins "plumbing, not porcelain" rather than the --no-ext-diff flag
// (which only restates the plumbing default).
func TestParseStagedDiff_IgnoresExternalDiff(t *testing.T) {
	dir, marker := stagedRepoWithMarkerProgram(t, "exit 0", func(git func(...string), prog, _ string) {
		git("config", "diff.external", prog)
	})
	assertStagedDiffUntouched(t, dir, marker)
}

// TestParseStagedDiff_IgnoresTextconv: a textconv driver must neither run nor
// rewrite the lines the checks see. Like the external-diff test, this pins the
// plumbing command, not the --no-textconv flag.
func TestParseStagedDiff_IgnoresTextconv(t *testing.T) {
	dir, marker := stagedRepoWithMarkerProgram(t, `tr a-z A-Z < "$1"`, func(git func(...string), prog, dir string) {
		git("config", "diff.evil.textconv", prog)
		if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.py diff=evil\n"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	assertStagedDiffUntouched(t, dir, marker)
}

// TestParseStagedDiff_IgnoresDriverCommand pins that an attribute-selected
// diff.<driver>.command (the per-path form of diff.external) never runs.
func TestParseStagedDiff_IgnoresDriverCommand(t *testing.T) {
	dir, marker := stagedRepoWithMarkerProgram(t, "exit 0", func(git func(...string), prog, dir string) {
		git("config", "diff.evil.command", prog)
		if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.py diff=evil\n"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	assertStagedDiffUntouched(t, dir, marker)
}

// TestParseStagedDiff_ImmuneToDiffUIConfig pins the switch to plumbing: each
// of these settings, on porcelain `git diff --cached`, reshapes the "+++ b/"
// header (or wraps it in escape codes) so no file parses and every pre-commit
// check silently passes on nothing.
func TestParseStagedDiff_ImmuneToDiffUIConfig(t *testing.T) {
	for _, kv := range [][2]string{
		{"diff.noprefix", "true"},
		{"diff.mnemonicPrefix", "true"},
		{"color.ui", "always"},
		{"color.diff", "always"},
	} {
		t.Run(kv[0], func(t *testing.T) {
			dir, marker := stagedRepoWithMarkerProgram(t, "exit 0", func(git func(...string), _, _ string) {
				git("config", kv[0], kv[1])
			})
			assertStagedDiffUntouched(t, dir, marker)
		})
	}
}

// TestParseStagedDiff_AgainstHEAD covers the non-unborn base: with a commit
// present only the newly staged line is reported, at its new line number, and
// a rename still contributes only its changed line (-M, porcelain's default).
func TestParseStagedDiff_AgainstHEAD(t *testing.T) {
	dir, _ := stagedRepoWithMarkerProgram(t, "exit 0", func(func(...string), string, string) {})
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("commit", "-q", "-m", "init")
	git("mv", "a.py", "b.py")
	f, err := os.OpenFile(filepath.Join(dir, "b.py"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("    x = phantom()\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	git("add", "-A")

	diffs, partial, err := ParseStagedDiff(context.Background(), dir)
	if err != nil || partial {
		t.Fatalf("ParseStagedDiff: err=%v partial=%v", err, partial)
	}
	if len(diffs) != 1 || diffs[0].Path != "b.py" {
		t.Fatalf("diffs = %+v, want exactly b.py", diffs)
	}
	if got := diffs[0].AddedLines; len(got) != 1 || got[0].LineNo != 3 || got[0].Text != "    x = phantom()" {
		t.Errorf("added lines = %+v, want only line 3 %q", got, "    x = phantom()")
	}
}
