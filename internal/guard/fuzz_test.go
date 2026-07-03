package guard

import (
	"strings"
	"testing"
	"unicode/utf8"
)

var fuzzLangs = []Lang{LangGo, LangJS, LangPython, LangUnknown}

// FuzzStripLiteralsStateful asserts stripLiteralsStateful never panics and
// preserves two invariants: output length equals input length, and valid
// UTF-8 input never becomes invalid UTF-8 output. stripLiteralsStateful is on
// ExtractRefs's hot path (36 callers) — the core symbol-reference extraction
// used throughout the hallucination-detection guard — so a crash or a
// corrupted-rune bug here would corrupt every downstream match. The harness
// drives the same per-line loop as extract.go:435-450, threading `open`
// across lines, since that's where the real risk (multi-line strings/
// comments) lives — a single isolated call with open="" would never reach it.
// Run: go test -run=x -fuzz=FuzzStripLiteralsStateful ./internal/guard
func FuzzStripLiteralsStateful(f *testing.F) {
	seeds := []string{
		`q := "SELECT COUNT(*)" // trailing`,
		`x = f('a\'b') # note`,
		"const s = `tmpl ${x}` // c",
		`x = f"{Build(y)}"`,
		`x = f"{{esc}} {Call(z)}"`,
		`x = rf"\d+ {Match(p)}"`,
		"",
		`x = """unterminated triple-quote`,
		"/* unterminated block comment",
		"line one\x00\nline two\x00",
	}
	for _, s := range seeds {
		f.Add(uint8(0), s)
	}
	f.Fuzz(func(t *testing.T, langIdx uint8, text string) {
		lang := fuzzLangs[int(langIdx)%len(fuzzLangs)]
		open := ""
		for _, line := range strings.Split(text, "\n") {
			scan, newOpen := stripLiteralsStateful(lang, line, open)
			open = newOpen
			if len(scan) != len(line) {
				t.Fatalf("stripLiteralsStateful(%q, %q, open=%q) changed length: %d != %d", lang, line, open, len(scan), len(line))
			}
			if utf8.ValidString(line) && !utf8.ValidString(scan) {
				t.Fatalf("stripLiteralsStateful(%q, %q, open=%q) produced invalid UTF-8 from valid input: %q", lang, line, open, scan)
			}
		}
	})
}

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
