package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubRuff writes a fake `ruff` (a bash script) to a temp bin dir and points
// PATH at it — bash's own directory must stay on PATH too, or the shebang
// can't resolve an interpreter and the stub never runs its body (the same
// pitfall TestGuardShimSurvivesStaleBinary documents). Returns a restore func.
func stubRuff(t *testing.T, script string) {
	t.Helper()
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not found: %v", err)
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "ruff"), []byte(script), 0o755); err != nil {
		t.Fatalf("write ruff stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+filepath.Dir(bashPath))
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
