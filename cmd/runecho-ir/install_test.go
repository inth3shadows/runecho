package main

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shellQuote must produce a token that a POSIX shell expands back to the exact
// original — so an install path containing $, `, ", or ' cannot trigger variable/
// command expansion inside the generated hook (which would target the wrong binary
// or run injected code). Proven by round-tripping through real bash.
func TestShellQuote_NeutralizesExpansion(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	cases := []string{
		"/usr/local/bin/runecho-guard",
		"/build/$JOB_ID/bin/rune", // $ must not expand
		"/x/`id`/rune",            // backtick must not run a command
		`/o'brien/bin/rune`,       // embedded single quote
		`/a"b/rune`,               // double quote
		"/p ath/with space/rune",  // whitespace
	}
	for _, in := range cases {
		script := "printf %s " + shellQuote(in)
		out, err := exec.Command("bash", "-c", script).Output()
		if err != nil {
			t.Fatalf("bash -c %q: %v", script, err)
		}
		if string(out) != in {
			t.Errorf("shellQuote round-trip: in=%q out=%q", in, string(out))
		}
	}
}

// cronQuote additionally backslash-escapes %, which cron converts to a newline
// before any shell parsing.
func TestCronQuote_EscapesPercent(t *testing.T) {
	if got, want := cronQuote("/a%b/rune"), `'/a\%b/rune'`; got != want {
		t.Errorf("cronQuote(%q) = %q, want %q", "/a%b/rune", got, want)
	}
}

// xmlEscape must produce XML that parses back to the original string — the
// launchd plist is XML, so a path containing &, <, >, or quotes must round-trip.
func TestXMLEscape_RoundTrips(t *testing.T) {
	cases := []string{
		"/usr/local/bin/runecho-ir",
		"/Users/a&b/bin/runecho-ir",    // ampersand
		"/Users/<weird>/runecho-ir",    // angle brackets
		`/Users/o'brien/runecho-ir`,    // apostrophe
		`/Users/"quoted"/runecho-ir`,   // double quote
		"/Users/tab\tspace/runecho-ir", // control whitespace
	}
	for _, in := range cases {
		doc := "<s>" + xmlEscape(in) + "</s>"
		var v struct {
			XMLName xml.Name `xml:"s"`
			Text    string   `xml:",chardata"`
		}
		if err := xml.Unmarshal([]byte(doc), &v); err != nil {
			t.Fatalf("escaped output is not valid XML for %q: %v (doc=%q)", in, err, doc)
		}
		if v.Text != in {
			t.Errorf("round-trip mismatch: in=%q out=%q (doc=%q)", in, v.Text, doc)
		}
	}
}

// installHooks must fold the #228 freshness check into the SAME post-merge/
// post-checkout hooks it already owns for the E6 reindex — never as a separate
// installer, and never at the cost of the reindex line (dropping the reindex is
// the worse staleness the feature exists to prevent). PR #226 shipped a third
// installer that collided here; this pins the merged design.
func TestInstallHooks_FreshnessFoldedIn(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	if _, err := installHooks(repo, true); err != nil {
		t.Fatalf("installHooks: %v", err)
	}
	hooksDir := filepath.Join(repo, ".git", "hooks")
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(hooksDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(b)
	}

	for _, name := range []string{"post-merge", "post-checkout"} {
		body := read(name)
		if !strings.Contains(body, "version-check --reinstall --quiet") {
			t.Errorf("%s missing the freshness check:\n%s", name, body)
		}
		if !strings.Contains(body, "repo reindex") {
			t.Errorf("%s lost the E6 reindex line:\n%s", name, body)
		}
	}
	// post-checkout must gate on a real branch switch ($3 == 1) so a file checkout
	// neither reindexes nor rebuilds.
	if body := read("post-checkout"); !strings.Contains(body, `"$3" = "1"`) {
		t.Errorf("post-checkout not gated on the branch-switch flag:\n%s", body)
	}
	// post-commit stays reindex-only: a local commit never advances past a tag
	// (releases are tagged server-side by CI), so a freshness check there is noise.
	if body := read("post-commit"); strings.Contains(body, "version-check") {
		t.Errorf("post-commit should not run the freshness check:\n%s", body)
	}
	if body := read("pre-commit"); !strings.Contains(body, "runecho-guard") {
		t.Errorf("pre-commit is not the guard hook:\n%s", body)
	}
}
