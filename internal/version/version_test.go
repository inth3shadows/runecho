package version

import "testing"

func TestCanonical(t *testing.T) {
	cases := map[string]string{
		"v0.17.4":            "v0.17.4",            // install.sh (git describe) — unchanged
		"0.17.4":             "v0.17.4",            // goreleaser ({{ .Version }}) — v prepended
		"0.17.4-3-gabc1234":  "v0.17.4-3-gabc1234", // goreleaser post-tag build
		"v0.17.4-3-gabc1234": "v0.17.4-3-gabc1234", // install.sh post-tag build — unchanged
		"dev":                "dev",                // unstamped — never invent a version
		"":                   "",                   // empty — unchanged
	}
	for in, want := range cases {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}
