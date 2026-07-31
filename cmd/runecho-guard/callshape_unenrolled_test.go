package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #261: call-shape needs no store row — it resolves a call against declarations
// in the file in front of it — but it was wired AFTER the `!res.OK` return, so
// on an unenrolled tree the guard deferred before the check ever ran. That is
// the common case for a hook installed globally, which is where an opted-in
// user would notice the flag meaning less than it says.
//
// The corpus fixtures in testdata/hookcorpus cannot reach this: runHookCase
// always enrolls a snapshot, so every one of them exercises the res.OK path.
// This is the one shape that has to be written by hand.
//
// The three cases are a set, and none of them is meaningful alone:
//
//	flag on  + wrong keyword -> ask   (the fix)
//	flag off + wrong keyword -> defer (the ask is call-shape's, not another check's)
//	flag on  + right keyword -> defer (the check compares, it does not fire on any kwarg)
//
// Dropping either control leaves a test that a blanket "ask on unenrolled
// Python" would satisfy.
func TestCallShape_UnenrolledTree(t *testing.T) {
	const decl = "def fetch(url, timeout=10):\n    return url\n"

	// A REAL store holding a real enrolled repo, and then an edit to a file in a
	// different tree entirely. That is what produces res.NoRepo — the store opens,
	// the lookup simply finds no row for this path. Pointing RUNECHO_HOME at an
	// empty directory instead lands in the store-degraded arm, which is a
	// different state and not the one the issue is about.
	setup := func(t *testing.T) string {
		t.Helper()
		enrolled := t.TempDir()
		gitInit(t, enrolled)
		enrolledStore(t, enrolled, []string{"KnownFunc"})

		py := filepath.Join(t.TempDir(), "client.py")
		if err := os.WriteFile(py, []byte(decl), 0o644); err != nil {
			t.Fatal(err)
		}
		return py
	}

	t.Run("wrong keyword asks with the flag on", func(t *testing.T) {
		py := setup(t)
		t.Setenv("RUNECHO_GUARD_CALLSHAPE", "1")

		_, raw, d := runHook(t, payload(t, "Write", py, "",
			decl+"\ndef go():\n    return fetch(\"u\", timeuot=5)\n", nil))

		if d.Hook.PermissionDec != "ask" {
			t.Fatalf("expected an ask on an unenrolled tree, got %q\n%s", d.Hook.PermissionDec, raw)
		}
		// The callee, not the keyword: the ask names the symbol whose declaration
		// disagrees, which is what guardstats and fpreport aggregate on.
		if !strings.Contains(d.Hook.PermissionReason, "fetch") || !strings.Contains(d.Hook.PermissionReason, "timeuot") {
			t.Fatalf("ask does not name the callee and the rejected keyword:\n%s", d.Hook.PermissionReason)
		}
		rec := readLastDecisionLog(t)
		if rec == nil {
			t.Fatal("no decision logged")
		}
		// "call-shape", not "violations". askReason falls back to "violations" when
		// no flag is set, and logging that here would file a resolves-but-misused
		// finding into the hallucination bucket the un-gating decision reads.
		if got := rec["reason"]; got != "call-shape" {
			t.Errorf("reason = %v, want call-shape", got)
		}
		if got := rec["decision"]; got != "ask" {
			t.Errorf("decision = %v, want ask", got)
		}
	})

	t.Run("flag off still defers", func(t *testing.T) {
		py := setup(t)

		_, _, d := runHook(t, payload(t, "Write", py, "",
			decl+"\ndef go():\n    return fetch(\"u\", timeuot=5)\n", nil))

		if d.Hook.PermissionDec == "ask" {
			t.Fatalf("flag-off asked (%q) — the ask above is not attributable to call-shape",
				d.Hook.PermissionReason)
		}
		if rec := readLastDecisionLog(t); rec != nil && rec["reason"] != "no-repo" {
			t.Errorf("reason = %v, want no-repo — the pre-gate must not change the "+
				"unenrolled defer when the check abstains", rec["reason"])
		}
	})

	t.Run("correct keyword defers with the flag on", func(t *testing.T) {
		py := setup(t)
		t.Setenv("RUNECHO_GUARD_CALLSHAPE", "1")

		_, _, d := runHook(t, payload(t, "Write", py, "",
			decl+"\ndef go():\n    return fetch(\"u\", timeout=5)\n", nil))

		if d.Hook.PermissionDec == "ask" {
			t.Fatalf("asked on a correctly spelled keyword (%q) — the check degenerated "+
				"to firing on any kwarg", d.Hook.PermissionReason)
		}
	})
}

// A schema-newer store means this binary cannot read the store at ALL, and that
// advisory is surfaced always — strict or not — because the fix is "reinstall"
// and nothing else the guard says matters until it happens. The call-shape
// pre-gate returns before the switch that emits it, so answering call-shape here
// would trade a loud "your binary is stale" for a quiet keyword finding, and log
// reason "call-shape" where "schema-newer" belongs. Code review caught this as a
// regression in the first cut of the pre-gate; this is what keeps it caught.
func TestCallShape_SchemaNewerKeepsItsAdvisory(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})

	// Bump user_version past what this binary understands, exactly as
	// TestRunHookMode_SchemaNewerSurfacesWarning does: OpenFast then returns
	// ErrSchemaNewer and lookupSymbolsFor reports res.Warn.
	raw, err := sql.Open("sqlite", filepath.Join(os.Getenv("RUNECHO_HOME"), "history.db"))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 9999"); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	raw.Close()

	t.Setenv("RUNECHO_GUARD_CALLSHAPE", "1")
	const decl = "def fetch(url, timeout=10):\n    return url\n"
	py := filepath.Join(repoRoot, "client.py")
	if err := os.WriteFile(py, []byte(decl), 0o644); err != nil {
		t.Fatal(err)
	}

	// A mismatch the check WOULD report if it ran — so a pass here is the
	// pre-gate abstaining, not the check finding nothing.
	_, out, d := runHook(t, payload(t, "Write", py, "",
		decl+"\ndef go():\n    return fetch(\"u\", timeuot=5)\n", nil))

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("call-shape asked over the schema-newer advisory — the stale-binary "+
			"signal is the one thing that must survive:\n%s", d.Hook.PermissionReason)
	}
	if !strings.Contains(d.Hook.AdditionalContext, "DISABLED") {
		t.Fatalf("schema-newer advisory did not survive the pre-gate, got %q", out)
	}
	if rec := readLastDecisionLog(t); rec == nil || rec["reason"] != "schema-newer" {
		t.Fatalf("reason = %v, want schema-newer", rec["reason"])
	}
}
