package main

import (
	"encoding/json"
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
	if !strings.Contains(d.Hook.PermissionReason, denyPrefix()) {
		t.Errorf("deny reason carries no deny remedy:\n%s", d.Hook.PermissionReason)
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
	// The model is told to stay in scope, and is never handed the means to drop
	// the user's guardrail itself (#540 review).
	if !strings.Contains(d.Hook.PermissionReason, "Only the user can change that scope") {
		t.Errorf("contract deny does not tell the model to stay in scope:\n%s", d.Hook.PermissionReason)
	}
	for _, banned := range []string{"contract deactivate", "widen it there"} {
		if strings.Contains(d.Hook.PermissionReason, banned) {
			t.Errorf("contract deny hands the model %q:\n%s", banned, d.Hook.PermissionReason)
		}
	}
	if strings.Contains(d.Hook.PermissionReason, "flagged name") {
		t.Errorf("contract-only deny carries a symbol remedy:\n%s", d.Hook.PermissionReason)
	}
}

// #540 review F1: a merged contract + symbol deny keeps BOTH findings — the
// contract section's own text must not swallow the symbol section after it.
func TestPosture_DenyMergedContractAndSymbolKeepsBoth(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	t.Setenv("RUNECHO_GUARD_POSTURE", "deny")
	top := contractRepo(t, "sess", inScopeBody)
	if err := os.WriteFile(filepath.Join(top, "go.mod"), []byte("module example.com/m\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, raw, d := runHook(t, contractPayload(t, "sess", filepath.Join(top, "a.go"), "package m\n\nfunc F() { HallucinatedFn() }\n"))
	assertDeny(t, d, raw)
	for _, want := range []string{"HallucinatedFn", "Only the user can change that scope", "define it first"} {
		if !strings.Contains(d.Hook.PermissionReason, want) {
			t.Errorf("merged deny lost %q:\n%s", want, d.Hook.PermissionReason)
		}
	}
}

func TestDenyTrailerPerCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		f    firedChecks
		want string
	}{
		"violations": {firedChecks{Violations: true}, "define it first"},
		"filescope":  {firedChecks{FileScope: true}, "import or qualify"},
		"qualified":  {firedChecks{Qualified: true}, "package actually declares"},
		"dangling":   {firedChecks{Dangling: true}, "Update the callers"},
		"dropped":    {firedChecks{Dropped: true}, "Keep the import"},
		"callshape":  {firedChecks{CallShape: true}, "match the signature"},
		"lint":       {firedChecks{Lint: true}, "the linter reports"},
		"deps":       {firedChecks{DepsGo: true}, "dependency actually exports"},
		"none":       {firedChecks{}, "Fix the findings"},
	} {
		got := denyTrailer(tc.f)
		if !strings.HasPrefix(got, denyPrefix()) || !strings.Contains(got, tc.want) {
			t.Errorf("%s: denyTrailer = %q, want prefix %q and %q", name, got, denyPrefix(), tc.want)
		}
		if name != "violations" && name != "none" && strings.Contains(got, "define it first") {
			t.Errorf("%s: denyTrailer carries the symbol remedy: %q", name, got)
		}
	}
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
	if d.Hook.PermissionDec != "ask" || !strings.Contains(d.Hook.PermissionReason, "Approve if") || strings.Contains(d.Hook.PermissionReason, denyPrefix()) {
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

// cursorPayload is payload as Cursor sends it through its Claude Code hook
// import (#543): the same shape plus cursor_version, which Claude Code never
// sends. Captured live from cursor-agent 2026.10.22.
func cursorPayload(t *testing.T, tool, filePath, content string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(payload(t, tool, filePath, "", content, nil)), &m); err != nil {
		t.Fatal(err)
	}
	m["cursor_version"] = "2026.10.22-27dcf5f"
	m["hook_event_name"] = "preToolUse"
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hookHostCursor = false })
	return string(b)
}

const halluSrc = "package m\n\nfunc F() { HallucinatedFn() }\n"

func halluRepo(t *testing.T) string {
	t.Helper()
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	goModRepo(t, repoRoot, []string{"KnownFunc"})
	return filepath.Join(repoRoot, "a.go")
}

// Cursor does not enforce ask for preToolUse, so with no posture set the guard
// denies there, and says why without naming a variable the user never set.
func TestPosture_CursorDefaultsToDeny(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "")
	_, raw, d := runHook(t, cursorPayload(t, "Write", halluRepo(t), halluSrc))
	assertDeny(t, d, raw)
	if !strings.Contains(d.Hook.PermissionReason, cursorDenyPrefix) || strings.Contains(d.Hook.PermissionReason, envDenyPrefix) {
		t.Errorf("Cursor default deny names the wrong cause:\n%s", d.Hook.PermissionReason)
	}
	if !strings.Contains(d.Hook.PermissionReason, "HallucinatedFn") {
		t.Errorf("deny reason lost the finding:\n%s", d.Hook.PermissionReason)
	}
}

// An explicit posture wins over the Cursor default, either way.
func TestPosture_CursorExplicitPostureWins(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "ask")
	_, _, d := runHook(t, cursorPayload(t, "Write", halluRepo(t), halluSrc))
	if d.Hook.PermissionDec != "ask" {
		t.Errorf("explicit ask under Cursor = %q, want ask", d.Hook.PermissionDec)
	}
	t.Setenv("RUNECHO_GUARD_POSTURE", "deny")
	_, raw, d := runHook(t, cursorPayload(t, "Write", halluRepo(t), halluSrc))
	assertDeny(t, d, raw)
	if !strings.Contains(d.Hook.PermissionReason, envDenyPrefix) {
		t.Errorf("explicit deny under Cursor does not name the variable:\n%s", d.Hook.PermissionReason)
	}
}

// The Cursor flag belongs to one fire: a Claude Code payload after it asks.
func TestPosture_CursorFlagResetsPerFire(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "")
	runHook(t, cursorPayload(t, "Write", halluRepo(t), halluSrc))
	_, _, d := runHook(t, payload(t, "Write", halluRepo(t), "", halluSrc, nil))
	if d.Hook.PermissionDec != "ask" {
		t.Errorf("Claude Code fire after a Cursor fire = %q, want ask", d.Hook.PermissionDec)
	}
	runHook(t, cursorPayload(t, "Write", halluRepo(t), halluSrc))
	runHook(t, "not json")
	if hookHostCursor {
		t.Error("a parse-fail fire kept the previous fire's Cursor flag")
	}
}

// A typo falls back to the host's default: deny under Cursor, ask elsewhere.
func TestPosture_CursorTypoFallsBackToDeny(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "block")
	hookHostCursor = true
	t.Cleanup(func() { hookHostCursor = false })
	if p, cd := postureSetting(); p != "deny" || !cd {
		t.Errorf("postureSetting() under Cursor with a typo = %q, %v; want deny, true", p, cd)
	}
}
