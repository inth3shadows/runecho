package main

import (
	"encoding/xml"
	"os/exec"
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
