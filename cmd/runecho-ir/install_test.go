package main

import (
	"encoding/xml"
	"testing"
)

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
