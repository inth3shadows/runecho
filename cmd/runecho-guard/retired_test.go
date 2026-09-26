package main

import (
	"path/filepath"
	"testing"
)

// retired_test.go — #414 retired duplicate-symbol by tombstone: the name stays
// in checkOrder (protocol 1 promises every check exactly once), the check is
// gone. These pin the two halves of that bargain.

// TestRetiredCheckAlwaysSkipped: the protocol reports a retired check as
// skipped/retired on EVERY path — including the degraded store, whose sweep
// marks every other store-dependent check unknown, and a hand-built result
// that claims the check fired. A retired check reported unknown would tell a
// consumer coverage was lost; reported ok, that the code was checked.
func TestRetiredCheckAlwaysSkipped(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1") // the old gate must not revive it
	assertRetired := func(t *testing.T, doc protocolDoc) {
		t.Helper()
		for name := range retiredChecks {
			r := resultFor(t, doc, name)
			if r.Verdict != "skipped" || r.Reason != "retired" || len(r.Evidence) != 0 {
				t.Errorf("%s = %+v, want skipped/retired with no evidence", name, r)
			}
		}
	}

	t.Run("enrolled", func(t *testing.T) {
		repo := t.TempDir()
		gitInit(t, repo)
		enrollSnapshot(t, repo, map[string][]string{"a.go": {"Helper"}}, nil)
		_, doc, _ := runProtocol(t, writeReq(t, filepath.Join(repo, "b.go"), "package main\n\nfunc Helper() {}\n"))
		assertRetired(t, doc)
	})
	t.Run("degraded store", func(t *testing.T) {
		repo := t.TempDir()
		gitInit(t, repo)
		other := t.TempDir()
		gitInit(t, other)
		enrolledStore(t, other, []string{"KnownFunc"})
		_, doc, _ := runProtocol(t, writeReq(t, filepath.Join(repo, "main.go"), "package main\n\nfunc Helper() {}\n"))
		if r := resultFor(t, doc, "dangling"); r.Verdict != "unknown" {
			t.Fatalf("precondition: dangling = %q, want the degraded sweep's unknown", r.Verdict)
		}
		assertRetired(t, doc)
	})
	t.Run("unknown language", func(t *testing.T) {
		repo := t.TempDir()
		gitInit(t, repo)
		enrolledStore(t, repo, []string{"KnownFunc"})
		_, doc, _ := runProtocol(t, writeReq(t, filepath.Join(repo, "notes.txt"), "hello\n"))
		assertRetired(t, doc)
	})
	t.Run("hand-built violation", func(t *testing.T) {
		assertRetired(t, renderProtocol(verification{
			Edit: hookEdit{ToolName: "Write"}, Path: "/a/b.go",
			Results: []CheckResult{{Check: "duplicate-symbol", Verdict: VerdictViolation}},
		}))
	})
}

// TestDuplicateEnvIsInert: RUNECHO_GUARD_DUPLICATE=1 on the exact edit the
// check used to ask about (a Write defining a name another file already
// defines) is a clean defer, and the hook's decision record carries no
// duplicate-symbol key — the checks map reports what ran.
func TestDuplicateEnvIsInert(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	enrollSnapshot(t, repo, map[string][]string{"a.go": {"Helper"}}, nil)
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	code, raw, d := runHook(t, payload(t, "Write", filepath.Join(repo, "b.go"), "", "package main\n\nfunc Helper() {}\n", nil))
	if code != 0 || d.Hook.PermissionDec == "ask" {
		t.Fatalf("want a clean defer, got code=%d %s", code, raw)
	}
	rec := readLastDecisionLog(t)
	checks, _ := rec["checks"].(map[string]any)
	if checks == nil {
		t.Fatalf("record has no checks map: %v", rec)
	}
	for name := range retiredChecks {
		if st, ok := checks[name]; ok {
			t.Errorf("checks[%q] = %v, want the key absent", name, st)
		}
	}
}
