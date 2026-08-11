package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubRuff writes a fake `ruff` (a bash script) to a temp bin dir and PREPENDS
// that dir to PATH, so the stub shadows any real ruff while everything else the
// guard shells out to still resolves.
//
// Prepending rather than replacing is load-bearing. An earlier cut set PATH to
// just the stub dir plus bash's dir, which happens to work on Debian/Ubuntu
// only because bash and git share /usr/bin. Anywhere they don't (macOS: /bin/bash
// vs /usr/bin/git or /opt/homebrew/bin/git) that strips `git`, so gitutil fails,
// lookupSymbolsFor returns !OK, and a test that means to pin the lint timeout
// path silently exercises answerDegradedStore instead — passing or failing for
// a reason unrelated to what it claims to test. t.Setenv restores PATH at test
// end, so no explicit restore func is needed.
func stubRuff(t *testing.T, script string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not found: %v", err)
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "ruff"), []byte(script), 0o755); err != nil {
		t.Fatalf("write ruff stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestLintFindingsWithReason_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("ruff"); err != nil {
		t.Skip("ruff not on PATH")
	}
	t.Run("undefined name", func(t *testing.T) {
		content := "def go():\n    return undefined_thing(1)\n"
		findings, reason := lintFindingsWithReason("client.py", content)
		if reason != "" {
			t.Fatalf("reason = %q, want empty (ran to completion)", reason)
		}
		if len(findings) != 1 {
			t.Fatalf("findings = %v, want exactly 1", findings)
		}
		f := findings[0]
		if f.Rule != "F821" || f.Line != 2 || !strings.Contains(f.Message, "undefined_thing") {
			t.Fatalf("finding = %+v, want {Line:2 Rule:F821 Message:contains undefined_thing}", f)
		}
	})
	t.Run("clean", func(t *testing.T) {
		findings, reason := lintFindingsWithReason("client.py", "def go():\n    return 1\n")
		if reason != "" || len(findings) != 0 {
			t.Fatalf("findings=%v reason=%q, want no findings and no reason", findings, reason)
		}
	})
}

// TestLintFindingsWithReason_RuffAbsent covers a caller that skipped its own
// LookPath gate (or hit the TOCTOU window where ruff vanishes between the
// caller's check and this one) — exec.CommandContext must still surface a
// real abstain, not silently read as "ran clean".
func TestLintFindingsWithReason_RuffAbsent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	findings, reason := lintFindingsWithReason("client.py", "def go():\n    return undefined_thing(1)\n")
	if findings != nil || reason != "lint-exec-failed" {
		t.Fatalf("findings=%v reason=%q, want nil/%q", findings, reason, "lint-exec-failed")
	}
}

func TestLintFindingsWithReason_Timeout(t *testing.T) {
	stubRuff(t, "#!/usr/bin/env bash\nexec sleep 5\n")
	orig := lintTimeout
	lintTimeout = 50 * time.Millisecond
	defer func() { lintTimeout = orig }()

	_, reason := lintFindingsWithReason("client.py", "x = 1\n")
	if reason != "timeout" {
		t.Fatalf("reason = %q, want %q", reason, "timeout")
	}
}

func TestLintFindingsWithReason_UnparseableOutput(t *testing.T) {
	stubRuff(t, "#!/usr/bin/env bash\necho 'not json'\nexit 0\n")
	_, reason := lintFindingsWithReason("client.py", "x = 1\n")
	if reason != "lint-unparseable-output" {
		t.Fatalf("reason = %q, want %q", reason, "lint-unparseable-output")
	}
}

// TestLintFindingsWithReason_SyntaxErrorNotReported pins that ruff's
// unconditional `invalid-syntax` diagnostics are dropped. --select does NOT
// suppress them (verified against ruff 0.16.1), so without the explicit
// filter the ask header would claim "(F821/F811)" over a parser error and
// lintSymbolFromMessage would put punctuation — ")" out of "Expected `)`,
// found newline" — into decisionRecord.Symbols, the field guardstats buckets
// on and fpaudit dedups on.
func TestLintFindingsWithReason_SyntaxErrorNotReported(t *testing.T) {
	if _, err := exec.LookPath("ruff"); err != nil {
		t.Skip("ruff not on PATH")
	}
	findings, reason := lintFindingsWithReason("client.py", "def go(:\n    return 1\n")
	if reason != "" {
		t.Fatalf("reason = %q, want empty (ruff ran fine; the input is what's broken)", reason)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none — invalid-syntax is not one of this check's two rules", findings)
	}
}

// TestRunHookMode_Lint_NoDoubleReport pins the firewall against the additive
// check. A genuine hallucination trips BOTH checks (F821 and "not found in
// the indexed code" are the same question), and before the firewall the ask
// named it under two headers while decisionRecord.Symbols carried it twice —
// double-counting one finding in the telemetry the un-gating decision reads.
// Every hookcorpus lint fixture dodges this by enrolling the symbol
// elsewhere, so the overlap is only reachable from here.
func TestRunHookMode_Lint_NoDoubleReport(t *testing.T) {
	if _, err := exec.LookPath("ruff"); err != nil {
		t.Skip("ruff not on PATH")
	}
	root := t.TempDir()
	gitInit(t, root)
	enrolledStore(t, root, []string{"KnownFunc"})
	py := filepath.Join(root, "client.py")

	t.Setenv("RUNECHO_GUARD_LINT", "1")
	// totally_made_up resolves nowhere: not in the snapshot, not in the file.
	_, raw, d := runHook(t, payload(t, "Write", py, "", "def go():\n    return totally_made_up(1)\n", nil))
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("expected an ask, got %q\n%s", d.Hook.PermissionDec, raw)
	}
	if n := strings.Count(d.Hook.PermissionReason, "totally_made_up"); n != 1 {
		t.Errorf("ask names totally_made_up %d times, want exactly 1 — lint must not restate the additive check's finding:\n%s",
			n, d.Hook.PermissionReason)
	}
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("no decision logged")
	}
	syms, _ := rec["symbols"].([]any)
	if len(syms) != 1 || syms[0] != "totally_made_up" {
		t.Errorf("decision-log symbols = %v, want exactly [\"totally_made_up\"] — a duplicate double-counts one finding", rec["symbols"])
	}
	// The additive check owns this finding, so the bucket must stay
	// "violations" — attributing it to lint would move a default-on check's
	// finding into a gated check's false-positive rate.
	if got := rec["reason"]; got != "violations" {
		t.Errorf("decision-log reason = %v, want \"violations\" — the additive check found it first", got)
	}
}

// TestLint_UnenrolledTree mirrors TestCallShape_UnenrolledTree (#261): lint
// resolves entirely from the Write payload, so it must answer on a tree the
// store knows nothing about — the common case for a globally installed hook,
// and the one TECHNICAL.md's "needs no index" claim is about. Before this it
// sat after the !res.OK bail and silently did nothing there.
//
// The three cases are a set: flag-on asks, flag-off defers (so the ask is
// attributable to lint and not to some other always-on check), and clean
// content defers (so the check compares rather than firing on any Python).
func TestLint_UnenrolledTree(t *testing.T) {
	if _, err := exec.LookPath("ruff"); err != nil {
		t.Skip("ruff not on PATH")
	}
	// A REAL store with a real enrolled repo, then an edit to a file in a
	// DIFFERENT tree — that is what produces res.NoRepo (the store opens, the
	// lookup finds no row). Pointing RUNECHO_HOME at an empty dir instead
	// lands in the store-degraded arm, a different state.
	setup := func(t *testing.T) string {
		t.Helper()
		enrolled := t.TempDir()
		gitInit(t, enrolled)
		enrolledStore(t, enrolled, []string{"KnownFunc"})
		return filepath.Join(t.TempDir(), "client.py")
	}
	// Same-file redefinition (F811): isolates lint even where an index exists.
	const dirty = "def fetch():\n    return 1\n\ndef fetch():\n    return 2\n"

	t.Run("flag on asks on an unenrolled tree", func(t *testing.T) {
		py := setup(t)
		t.Setenv("RUNECHO_GUARD_LINT", "1")
		_, raw, d := runHook(t, payload(t, "Write", py, "", dirty, nil))
		if d.Hook.PermissionDec != "ask" {
			t.Fatalf("expected an ask on an unenrolled tree, got %q\n%s", d.Hook.PermissionDec, raw)
		}
		if !strings.Contains(d.Hook.PermissionReason, "F811") {
			t.Fatalf("ask does not name the rule:\n%s", d.Hook.PermissionReason)
		}
		rec := readLastDecisionLog(t)
		if rec == nil {
			t.Fatal("no decision logged")
		}
		// "lint", not "violations": askReason falls back to "violations" when
		// no flag is set, which would file this into the hallucination bucket
		// the un-gating decision reads.
		if got := rec["reason"]; got != "lint" {
			t.Errorf("reason = %v, want lint", got)
		}
		syms, _ := rec["symbols"].([]any)
		if len(syms) != 1 || syms[0] != "fetch" {
			t.Errorf("symbols = %v, want [\"fetch\"]", rec["symbols"])
		}
	})

	t.Run("flag off still defers", func(t *testing.T) {
		py := setup(t)
		_, _, d := runHook(t, payload(t, "Write", py, "", dirty, nil))
		if d.Hook.PermissionDec == "ask" {
			t.Fatalf("flag-off asked (%q) — the ask above is not attributable to lint", d.Hook.PermissionReason)
		}
		if rec := readLastDecisionLog(t); rec != nil && rec["reason"] != "no-repo" {
			t.Errorf("reason = %v, want no-repo — the pre-gate must not change the unenrolled defer when the check abstains", rec["reason"])
		}
	})

	t.Run("clean content defers with the flag on", func(t *testing.T) {
		py := setup(t)
		t.Setenv("RUNECHO_GUARD_LINT", "1")
		_, _, d := runHook(t, payload(t, "Write", py, "", "def fetch():\n    return 1\n", nil))
		if d.Hook.PermissionDec == "ask" {
			t.Fatalf("asked on clean content (%q) — the check degenerated to firing on any Python", d.Hook.PermissionReason)
		}
	})
}

// TestRunHookMode_Lint covers the gating shape (flag/tool/lang) and the true
// file line number end to end. The gating-shape cases duplicate what
// testdata/hookcorpus/lint.json's isolation probe already proves via
// mutation, but the true-line-number assertion can't live there — every
// other hook-mode check reports a hunk-relative "snippet line N", and lint's
// contract is that it does NOT, which nothing in the corpus harness checks
// for by itself.
func TestRunHookMode_Lint(t *testing.T) {
	if _, err := exec.LookPath("ruff"); err != nil {
		t.Skip("ruff not on PATH")
	}
	// F811 (redefinition), not F821: `fetch` is defined twice in the SAME
	// file, so addInFileDefs folds both into the known set and the additive
	// hallucination check stays silent — the fixture isolates lint the same
	// way callshape's own fixtures isolate call-shape (see callshape.json's
	// "unaccepted-keyword" case). The second def sits on line 4, so an ask
	// reporting "snippet line" (relative to a hunk that doesn't exist under
	// Write) or any line other than 4 would prove the wrong number is read.
	const content = "def fetch():\n    return 1\n\ndef fetch():\n    return 2\n"

	setup := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		gitInit(t, root)
		enrolledStore(t, root, []string{"KnownFunc"})
		return filepath.Join(root, "client.py")
	}

	t.Run("flag on asks with a true file line number", func(t *testing.T) {
		py := setup(t)
		t.Setenv("RUNECHO_GUARD_LINT", "1")
		_, raw, d := runHook(t, payload(t, "Write", py, "", content, nil))
		if d.Hook.PermissionDec != "ask" {
			t.Fatalf("expected an ask, got %q\n%s", d.Hook.PermissionDec, raw)
		}
		if !strings.Contains(d.Hook.PermissionReason, "line 4:") {
			t.Fatalf("ask does not report the true file line (4):\n%s", d.Hook.PermissionReason)
		}
		if strings.Contains(d.Hook.PermissionReason, "snippet line") {
			t.Fatalf("ask uses hunk-relative \"snippet line\" wording — lint reports true file lines:\n%s", d.Hook.PermissionReason)
		}
		rec := readLastDecisionLog(t)
		if rec == nil || rec["reason"] != "lint" {
			t.Errorf("decision-log reason = %v, want %q", rec["reason"], "lint")
		}
		// decisionRecord.Symbols must carry the flagged identifier ("fetch"),
		// not ruff's rule code ("F811") -- guardstats' per-symbol frequency
		// report and fpaudit's duplicate-fire dedup both key on this field,
		// and every OTHER check in this file appends the real name here.
		syms, _ := rec["symbols"].([]any)
		if len(syms) != 1 || syms[0] != "fetch" {
			t.Fatalf("decision-log symbols = %v, want [\"fetch\"] -- not the rule code", rec["symbols"])
		}
	})

	t.Run("flag off defers", func(t *testing.T) {
		py := setup(t)
		_, _, d := runHook(t, payload(t, "Write", py, "", content, nil))
		if d.Hook.PermissionDec == "ask" {
			t.Fatalf("flag-off asked (%q) — the ask above is not attributable to lint", d.Hook.PermissionReason)
		}
	})

	t.Run("Edit tool defers", func(t *testing.T) {
		py := setup(t)
		if err := os.WriteFile(py, []byte("def fetch():\n    return 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("RUNECHO_GUARD_LINT", "1")
		// Same isolating shape as the Write case above (a same-file
		// redefinition), replayed as an Edit hunk that appends the second
		// `fetch` def — proves lint stays silent for the tool it does not
		// support, not merely that this particular content is clean.
		_, _, d := runHook(t, payloadOld(t, "Edit", py, "    return 1\n", "    return 1\n\ndef fetch():\n    return 2\n", "", nil))
		if d.Hook.PermissionDec == "ask" {
			t.Fatalf("Edit tool produced an ask (%q) — lint is Write-only in v1", d.Hook.PermissionReason)
		}
	})

	t.Run("non-Python file defers", func(t *testing.T) {
		root := t.TempDir()
		gitInit(t, root)
		enrolledStore(t, root, []string{"KnownFunc"})
		t.Setenv("RUNECHO_GUARD_LINT", "1")
		goFile := filepath.Join(root, "client.go")
		_, _, d := runHook(t, payload(t, "Write", goFile, "", "package main\n\nfunc fetch() int {\n\treturn 1\n}\n", nil))
		if d.Hook.PermissionDec == "ask" {
			t.Fatalf("non-Python file produced an ask (%q) — lint only runs on Python", d.Hook.PermissionReason)
		}
	})
}

// TestRunHookMode_Lint_Degraded pins the VerdictUnknown path end to end: a
// hung/malformed ruff must abstain (surfaced only under RUNECHO_GUARD_STRICT,
// same as every other degraded-coverage state), never silently ask or block.
func TestRunHookMode_Lint_Degraded(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	enrolledStore(t, root, []string{"KnownFunc"})
	py := filepath.Join(root, "client.py")

	t.Setenv("RUNECHO_GUARD_LINT", "1")
	t.Setenv("RUNECHO_GUARD_STRICT", "1")

	t.Run("timeout surfaces as degraded, not an ask", func(t *testing.T) {
		stubRuff(t, "#!/usr/bin/env bash\nexec sleep 5\n")
		orig := lintTimeout
		lintTimeout = 50 * time.Millisecond
		defer func() { lintTimeout = orig }()

		_, raw, d := runHook(t, payload(t, "Write", py, "", "def go():\n    return 1\n", nil))
		if d.Hook.PermissionDec == "ask" {
			t.Fatalf("timeout produced an ask instead of an abstain:\n%s", raw)
		}
		if !strings.Contains(d.Hook.AdditionalContext, "could not run to completion") {
			t.Fatalf("strict mode should surface a degraded-coverage advisory, got %q", d.Hook.AdditionalContext)
		}
		if rec := readLastDecisionLog(t); rec == nil || rec["reason"] != "check-degraded" {
			t.Errorf("decision-log reason = %v, want %q", rec["reason"], "check-degraded")
		}
	})
}
