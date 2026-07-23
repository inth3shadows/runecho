package contract

import (
	"reflect"
	"testing"
)

const sample = `# Scope for the guard false-positive work.
name: guard-fp
description: Only the guard's FP paths, not its tests

internal/guard/**
cmd/runecho-guard/*.go
# Tests are out of scope for this pass.
!internal/guard/**/*_test.go
docs
`

func TestParse_HeadersAndPatterns(t *testing.T) {
	c := Parse([]byte(sample), "x", "fallback")
	if c.Name != "guard-fp" {
		t.Errorf("Name = %q, want guard-fp", c.Name)
	}
	if c.Description != "Only the guard's FP paths, not its tests" {
		t.Errorf("Description = %q", c.Description)
	}
	if len(c.Patterns) != 4 {
		t.Fatalf("got %d patterns, want 4: %+v", len(c.Patterns), c.Patterns)
	}
	if !c.Patterns[2].Negated {
		t.Error("third pattern should be negated")
	}
	if c.Hash == "" {
		t.Error("want a content hash")
	}
}

func TestInScope(t *testing.T) {
	c := Parse([]byte(sample), "x", "f")
	cases := map[string]bool{
		"internal/guard/extract.go":      true,
		"internal/guard/deep/nested.go":  true,
		"cmd/runecho-guard/main.go":      true,
		"cmd/runecho-guard/sub/other.go": false, // *.go is one segment only
		"internal/guard/extract_test.go": false, // negated
		"internal/guard/a/b_test.go":     false, // negated, nested
		"internal/snapshot/db.go":        false,
		"docs/competitive-landscape.md":  true, // bare dir = whole subtree
		"README.md":                      false,
	}
	for p, want := range cases {
		if got := c.InScope(p); got != want {
			t.Errorf("InScope(%q) = %v, want %v", p, got, want)
		}
	}
}

// An empty contract must put NOTHING in scope. Treating it as "allow all" would
// silently disable the check the author asked for.
func TestInScope_EmptyContractAllowsNothing(t *testing.T) {
	c := Parse([]byte("name: empty\n"), "x", "f")
	if c.InScope("anything.go") {
		t.Error("empty contract must not put paths in scope")
	}
}

// A glob containing a colon must not be swallowed as a `key: value` header, and
// a header appearing after globs is treated as a glob (headers are prefix-only).
func TestParse_ColonGlobIsNotAHeader(t *testing.T) {
	c := Parse([]byte("src:gen/**\nname: after\n"), "x", "fallback")
	if c.Name != "fallback" {
		t.Errorf("a header after a glob must not be honored; Name = %q", c.Name)
	}
	if len(c.Patterns) != 2 {
		t.Fatalf("want both lines as patterns, got %+v", c.Patterns)
	}
	if c.Patterns[0].Glob != "src:gen/**" {
		t.Errorf("colon glob mangled: %q", c.Patterns[0].Glob)
	}
}

func TestMatchGlob_DoubleStar(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"a/**", "a/b.go", true},
		{"a/**", "a/b/c/d.go", true},
		{"a/**", "a", false},
		{"a/**/c.go", "a/c.go", true}, // ** matches zero segments
		{"a/**/c.go", "a/b/c.go", true},
		{"a/*", "a/b.go", true},
		{"a/*", "a/b/c.go", false},
		{"**/x.go", "deep/nested/x.go", true},
		{"**", "anything/at/all", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.glob, c.path); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.glob, c.path, got, c.want)
		}
	}
}

func TestOutOfScope(t *testing.T) {
	c := Parse([]byte("internal/guard/**\n"), "x", "f")
	got := c.OutOfScope([]string{"internal/guard/a.go", "cmd/b.go", "internal/guard/c.go", "README.md"})
	want := []string{"cmd/b.go", "README.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OutOfScope = %q, want %q", got, want)
	}
}

// The hash must track the file's exact bytes: a contract edited mid-session
// changes what is being enforced, and the stored hash is what makes an ask
// reproducible against the text that produced it.
func TestHashTracksExactBytes(t *testing.T) {
	a := Parse([]byte("internal/**\n"), "x", "f")
	b := Parse([]byte("internal/**\n"), "x", "f")
	d := Parse([]byte("internal/**\n# added a comment\n"), "x", "f")
	if a.Hash != b.Hash {
		t.Error("identical bytes must hash identically")
	}
	if a.Hash == d.Hash {
		t.Error("a comment-only edit must change the hash")
	}
}

// A glob with a wildcard in the FILE segment must still match only that
// directory level — `internal/snapshot/contracts*.go` covers contracts.go and
// contracts_test.go but not a nested file.
func TestInScope_WildcardFileSegment(t *testing.T) {
	c := Parse([]byte("internal/snapshot/contracts*.go\n"), "x", "f")
	cases := map[string]bool{
		"internal/snapshot/contracts.go":      true,
		"internal/snapshot/contracts_test.go": true,
		"internal/snapshot/db.go":             false,
		"internal/snapshot/sub/contracts.go":  false,
	}
	for p, want := range cases {
		if got := c.InScope(p); got != want {
			t.Errorf("InScope(%q) = %v, want %v", p, got, want)
		}
	}
}
