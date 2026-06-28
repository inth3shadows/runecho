package guard

import (
	"testing"
	"unicode/utf8"
)

// FuzzGuardDiff asserts the unified-diff parser never panics on arbitrary input.
// parseDiffOutput is on the guard's hot path — it runs on every staged commit
// over text git produced from untrusted working-tree content — so the C2
// fail-safe (no crash on adversarial input) must hold here too. Run:
// go test -run=x -fuzz=FuzzGuardDiff ./internal/guard
func FuzzGuardDiff(f *testing.F) {
	seeds := []string{
		"diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -0,0 +1,2 @@\n+package p\n+func A() {}\n",
		"+++ b/with space.go\n@@ -1 +1 @@\n+x\n",
		"+++ \"b/quoted\\t.go\"\n@@ -0,0 +1 @@\n+y\n",
		"+++ /dev/null\n",
		"@@ -1 +1 @@\n+orphan line with no file header\n",
		"@@ malformed +-5 @@\n+x\n", // negative/garbage hunk start
		"", "+++ ", "+++ b/", "@@", "@@ @@", "+++ b/a\n@@ -0,0 +0,0 @@\n",
		"\x00\x00\n+++ b/\x00.go\n@@ -1 +1 @@\n+\x00\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		diffs, _, err := parseDiffOutput(raw) // must never panic
		if err != nil {
			return // an error return is acceptable; a panic is not
		}
		// Structural invariant: a parsed AddedLine's Text is the diff line minus
		// its leading '+', so it must stay valid UTF-8 if the source line was —
		// downstream consumers JSON-marshal it. (Invalid input bytes are allowed
		// to pass through; we only assert the parser doesn't corrupt valid runes.)
		for _, d := range diffs {
			for _, al := range d.AddedLines {
				if utf8.ValidString(raw) && !utf8.ValidString(al.Text) {
					t.Fatalf("AddedLine.Text became invalid UTF-8 from valid input: %q", al.Text)
				}
			}
		}
	})
}
