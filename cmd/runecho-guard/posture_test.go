package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertDeny checks one hook run under RUNECHO_GUARD_POSTURE=deny: the call is
// denied, the reason carries the actionable hint and no "Approve if" trailer
// (the model cannot approve a blocked call), and the record says deny.
func assertDeny(t *testing.T, d decision, raw string) map[string]any {
	t.Helper()
	if d.Hook.PermissionDec != "deny" {
		t.Fatalf("permissionDecision = %q, want deny\n%s", d.Hook.PermissionDec, raw)
	}
	if strings.Contains(d.Hook.PermissionReason, "Approve if") {
		t.Errorf("deny reason still offers approval:\n%s", d.Hook.PermissionReason)
	}
	if !strings.HasSuffix(strings.TrimSpace(d.Hook.PermissionReason), denyHint) {
		t.Errorf("deny reason does not end with the hint:\n%s", d.Hook.PermissionReason)
	}
	rec := readLastDecisionLog(t)
	if rec == nil || rec["decision"] != "deny" {
		t.Fatalf("record decision = %v, want deny", rec)
	}
	if _, ok := rec["claim_symbols"].(map[string]any); !ok {
		t.Errorf("deny record's claim_symbols is not an object (absent and empty must stay distinguishable): %v", rec)
	}
	return rec
}

func TestPosture_DenySymbolAsk(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	goModRepo(t, repoRoot, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_POSTURE", "deny")
	_, raw, d := runHook(t, payload(t, "Write", filepath.Join(repoRoot, "a.go"), "", "package m\n\nfunc F() { HallucinatedFn() }\n", nil))
	assertDeny(t, d, raw)
	if !strings.Contains(d.Hook.PermissionReason, "HallucinatedFn") {
		t.Errorf("deny reason lost the finding:\n%s", d.Hook.PermissionReason)
	}
}

func TestPosture_DenyCallShapeAskWithoutIndex(t *testing.T) {
	const decl = "def fetch(url, timeout=10):\n    return url\n"
	enrolled := t.TempDir()
	gitInit(t, enrolled)
	enrolledStore(t, enrolled, []string{"KnownFunc"})
	py := filepath.Join(t.TempDir(), "client.py")
	if err := os.WriteFile(py, []byte(decl), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNECHO_GUARD_CALLSHAPE", "1")
	t.Setenv("RUNECHO_GUARD_POSTURE", "deny")
	_, raw, d := runHook(t, payload(t, "Write", py, "", decl+"\ndef go():\n    return fetch(\"u\", timeuot=5)\n", nil))
	assertDeny(t, d, raw)
}

func TestPosture_DenyContractOnly(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	t.Setenv("RUNECHO_GUARD_POSTURE", "deny")
	top := contractRepo(t, "sess", inScopeBody)
	_, raw, d := runHook(t, contractPayload(t, "sess", filepath.Join(top, "docs", "README.md"), "# hi\n"))
	assertDeny(t, d, raw)
}

// The default, and anything that is not "deny", keeps today's ask byte for byte.
func TestPosture_Parsing(t *testing.T) {
	for v, want := range map[string]string{
		"": "ask", "ask": "ask", "block": "ask", "deny": "deny", "Deny": "deny", " deny ": "deny",
	} {
		t.Setenv("RUNECHO_GUARD_POSTURE", v)
		if got := guardPosture(); got != want {
			t.Errorf("guardPosture() with %q = %q, want %q", v, got, want)
		}
	}
}

func TestPosture_DefaultAskUnchanged(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	goModRepo(t, repoRoot, []string{"KnownFunc"})
	_, _, d := runHook(t, payload(t, "Write", filepath.Join(repoRoot, "a.go"), "", "package m\n\nfunc F() { HallucinatedFn() }\n", nil))
	if d.Hook.PermissionDec != "ask" || !strings.Contains(d.Hook.PermissionReason, "Approve if") || strings.Contains(d.Hook.PermissionReason, denyHint) {
		t.Errorf("default posture changed:\n%q %q", d.Hook.PermissionDec, d.Hook.PermissionReason)
	}
	if rec := readLastDecisionLog(t); rec["decision"] != "ask" {
		t.Errorf("default record decision = %v, want ask", rec["decision"])
	}
}

// Pre-commit already blocks; the posture does not touch its records.
func TestPosture_PrecommitUnaffected(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "deny")
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")
	isolateDecisionModel(t)
	logDecision(decisionRecord{Mode: "precommit", Decision: "ask", Reason: "violations"})
	if rec := readLastDecisionLog(t); rec["decision"] != "ask" {
		t.Errorf("precommit record decision = %v, want ask", rec["decision"])
	}
}

// A deny is never joined: its tool call never ran. Admitting it would record a
// later edit's approval against it and train learned-allow on the symbols it
// blocked.
func TestPosture_DenyIsNeverJoinedToAnOutcome(t *testing.T) {
	isolateDecisionSession(t)
	isolateDecisionModel(t)
	t.Setenv("RUNECHO_HOME", t.TempDir())
	t.Setenv("RUNECHO_DEBUG", "")
	t.Setenv("RUNECHO_GUARD_POSTURE", "deny")
	file := "/some/repo/main.go"
	logDecision(decisionRecord{Mode: "hook", File: file, Decision: "ask", Reason: "violations",
		Symbols: []string{"Ghost"}, LearnSymbols: []string{"Ghost"}})
	before := countDecisionLogLines(t)
	runOutcomeMode(strings.NewReader(`{"tool_name":"Edit","tool_input":{"file_path":` + jsonQuote(file) + `}}`))
	if after := countDecisionLogLines(t); after != before {
		t.Errorf("an outcome was recorded against a deny: %v", readLastDecisionLog(t))
	}
}

func TestDenyReason(t *testing.T) {
	for in, want := range map[string]string{
		"x\nApprove if ok.\nmore": "x\n" + denyHint,
		"Approve if ok.":          denyHint,
		"no trailer":              "no trailer\n" + denyHint,
		"ends with newline\n":     "ends with newline\n" + denyHint,
		"":                        denyHint,
	} {
		if got := denyReason(in); got != want {
			t.Errorf("denyReason(%q) = %q, want %q", in, got, want)
		}
	}
}
