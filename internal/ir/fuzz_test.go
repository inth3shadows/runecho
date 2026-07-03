package ir

import (
	"testing"
	"unicode/utf8"
)

// FuzzNormalizePath asserts normalizePath never panics and is idempotent:
// applying it twice must equal applying it once. normalizePath produces the
// map key every file gets stored/looked-up under in the IR (generator.go:333,
// :364) — on every Generate and UpdateFile call, across every machine that
// indexes a repo. Idempotence matters because a caller that re-normalizes an
// already-normalized path (e.g. a value round-tripped through the IR and fed
// back in) must land on the exact same key, or the same physical file could
// silently split into two index entries. Also asserts the NFC step never
// turns valid UTF-8 into invalid UTF-8.
// Run: go test -run=x -fuzz=FuzzNormalizePath ./internal/ir
func FuzzNormalizePath(f *testing.F) {
	seeds := []string{
		"foo/bar.go",
		"./foo/bar.go",
		"././foo/bar.go", // doubled "./" prefix — TrimPrefix only strips once
		`foo\bar.go`,     // literal backslash, not a path separator on this OS
		"caf\u00e9.ts",   // NFC: café using a single precomposed codepoint
		"cafe\u0301.ts",  // NFD: café using e + combining acute accent
		"",
		".",
		"./",
		"\xff\xfe", // invalid UTF-8
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, relPath string) {
		once := normalizePath(relPath)
		twice := normalizePath(once)
		if once != twice {
			t.Fatalf("normalizePath not idempotent: normalizePath(%q) = %q, but normalizePath(%q) = %q", relPath, once, once, twice)
		}
		if utf8.ValidString(relPath) && !utf8.ValidString(once) {
			t.Fatalf("normalizePath(%q) produced invalid UTF-8 from valid input: %q", relPath, once)
		}
	})
}
