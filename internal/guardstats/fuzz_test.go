package guardstats

import (
	"strings"
	"testing"
)

// FuzzLoadReader asserts LoadReader never panics and never returns a non-nil
// error for any string input — its documented contract (guardstats.go:51-61)
// is fail-open: a malformed line, a bad timestamp, or an oversized line is
// skipped, not surfaced as an error, since the decision log is observability
// data and one bad line must never block a report over the rest of it. A
// strings.Reader never produces a genuine I/O error, so under this harness
// any non-nil error return is itself a contract violation.
// Run: go test -run=x -fuzz=FuzzLoadReader ./internal/guardstats
func FuzzLoadReader(f *testing.F) {
	seeds := []string{
		"",
		"\n\n",
		`{"v":1,"ts":"2026-06-01T10:00:00Z","mode":"hook","repo":"runecho","file":"a.go","lang":"go","decision":"ask","reason":"unresolved-symbol","symbols":["Foo","Bar"]}` + "\n",
		`{"v":1,"ts":"2026-06-01T10:00:00Z","mode":"hook","decision":"ask"}` + "\nnot valid json at all\n" + `{"v":1,"ts":"2026-06-01T10:05:00Z","mode":"hook","decision":"defer"}` + "\n",
		`{"v":1,"ts":"not-a-timestamp","mode":"hook","decision":"ask"}` + "\n" + `{"v":1,"ts":"2026-06-01T10:05:00Z","mode":"hook","decision":"defer"}` + "\n",
		`{"v":1` + "\n", // truncated JSON, no closing brace
		"not json\nnot json either",
		"\x00\x00\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data string) {
		_, err := LoadReader(strings.NewReader(data))
		if err != nil {
			t.Fatalf("LoadReader(%q) returned an error, want fail-open (nil): %v", data, err)
		}
	})
}
