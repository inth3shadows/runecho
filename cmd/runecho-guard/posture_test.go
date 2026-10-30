package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
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
	// The fire's posture is cleared once runHook returns, so the expected
	// prefix is derived from the environment: an explicit deny names the
	// variable, anything else here is Cursor's default.
	want := strings.TrimSuffix(cursorDenyPrefix, ").") // ")." or "; …)." follows
	if strings.EqualFold(strings.TrimSpace(os.Getenv("RUNECHO_GUARD_POSTURE")), "deny") {
		want = envDenyPrefix
	}
	if !strings.Contains(d.Hook.PermissionReason, want) {
		t.Errorf("deny reason lacks %q:\n%s", want, d.Hook.PermissionReason)
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
	return withField(t, payload(t, tool, filePath, "", content, nil), "cursor_version", "2026.10.22-27dcf5f")
}

func withField(t *testing.T, raw, key string, v any) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	m[key] = v
	m["hook_event_name"] = "preToolUse"
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
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
// denies there, says why without naming a variable the user never set, tells
// the model not to work around a false positive, names the user's way out,
// and stamps the record so these denies are not mistaken for chosen ones.
func TestPosture_CursorDefaultsToDeny(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "")
	_, raw, d := runHook(t, cursorPayload(t, "Write", halluRepo(t), halluSrc))
	rec := assertDeny(t, d, raw)
	r := d.Hook.PermissionReason
	if !strings.Contains(r, cursorDenyPrefix) || strings.Contains(r, envDenyPrefix) {
		t.Errorf("Cursor default deny names the wrong cause:\n%s", r)
	}
	for _, want := range []string{"HallucinatedFn", "do not rewrite working code", "RUNECHO_GUARD_POSTURE=ask", ".runechoguardignore"} {
		if !strings.Contains(r, want) {
			t.Errorf("Cursor deny reason lacks %q:\n%s", want, r)
		}
	}
	if rec["host"] != "cursor" {
		t.Errorf("record host = %v, want cursor", rec["host"])
	}
}

// .runechoguardignore reaches only the unresolved-symbol check (#267), so the
// way out names it only when that check fired.
func TestPosture_CursorWayOutNamesIgnoreOnlyForViolations(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "")
	hookPosture.Store(&postureState{posture: "deny", cursor: true, cursorDefault: true})
	t.Cleanup(func() { hookPosture.Store(nil) })
	if got := denyTrailer(firedChecks{Dropped: true}); strings.Contains(got, ".runechoguardignore") || !strings.Contains(got, "RUNECHO_GUARD_POSTURE=ask") {
		t.Errorf("dropped-import Cursor deny trailer = %q", got)
	}
}

// An explicit posture wins over the Cursor default, either way, and an
// explicit deny reads exactly as it does under Claude Code.
func TestPosture_CursorExplicitPostureWins(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "ask")
	_, _, d := runHook(t, cursorPayload(t, "Write", halluRepo(t), halluSrc))
	if d.Hook.PermissionDec != "ask" {
		t.Errorf("explicit ask under Cursor = %q, want ask", d.Hook.PermissionDec)
	}
	if rec := readLastDecisionLog(t); rec["decision"] != "ask" || rec["host"] != "cursor" {
		t.Errorf("explicit-ask Cursor record = %v", rec)
	}
	t.Setenv("RUNECHO_GUARD_POSTURE", "deny")
	_, raw, d := runHook(t, cursorPayload(t, "Write", halluRepo(t), halluSrc))
	assertDeny(t, d, raw)
	if r := d.Hook.PermissionReason; !strings.Contains(r, envDenyPrefix) || strings.Contains(r, "do not rewrite working code") {
		t.Errorf("explicit deny under Cursor differs from Claude Code's:\n%s", r)
	}
}

// The posture belongs to one fire: cleared when it returns, so a Claude Code
// payload after a Cursor one asks and carries no host, and a parse failure
// never runs under the previous fire's posture.
func TestPosture_CursorFlagResetsPerFire(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "")
	runHook(t, cursorPayload(t, "Write", halluRepo(t), halluSrc))
	if hookPosture.Load() != nil {
		t.Fatal("the fire's posture outlived runHookMode")
	}
	_, _, d := runHook(t, payload(t, "Write", halluRepo(t), "", halluSrc, nil))
	if d.Hook.PermissionDec != "ask" {
		t.Errorf("Claude Code fire after a Cursor fire = %q, want ask", d.Hook.PermissionDec)
	}
	if rec := readLastDecisionLog(t); rec["host"] != nil {
		t.Errorf("Claude Code record carries host %v", rec["host"])
	}
}

// A typo falls back to Cursor's default deny, and the reason names the
// ignored value instead of implying the user set nothing.
func TestPosture_CursorTypoNamedInReason(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "off")
	_, raw, d := runHook(t, cursorPayload(t, "Write", halluRepo(t), halluSrc))
	assertDeny(t, d, raw)
	if !strings.Contains(d.Hook.PermissionReason, `RUNECHO_GUARD_POSTURE="off" is not ask or deny`) {
		t.Errorf("typo under Cursor is hidden:\n%s", d.Hook.PermissionReason)
	}
}

// A cursor_version of an unexpected type still marks a Cursor payload instead
// of failing the decode, which would skip the guard entirely.
func TestPosture_CursorVersionNonString(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "")
	raw0 := withField(t, payload(t, "Write", halluRepo(t), "", halluSrc, nil), "cursor_version", 2026)
	_, raw, d := runHook(t, raw0)
	assertDeny(t, d, raw)
	for _, v := range []string{``, `null`, `""`, ` "" `} {
		if isCursorPayload(json.RawMessage(v)) {
			t.Errorf("isCursorPayload(%q) = true", v)
		}
	}
}

// Cursor sends every agent edit as a whole-file Write. A name already in the
// file that the guard cannot resolve must not block an edit elsewhere in it,
// or under the Cursor default no later edit to that file could land (#543 F1).
// The same Write from Claude Code is still checked whole, as before.
func TestPosture_CursorWriteChecksOnlyChangedLines(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "")
	path := halluRepo(t)
	const onDisk = "package m\n\nfunc F() { LegacyDynamic() }\n\nfunc G() {}\n"
	if err := os.WriteFile(path, []byte(onDisk), 0o644); err != nil {
		t.Fatal(err)
	}
	unrelated := strings.Replace(onDisk, "func G() {}", "func G() { _ = 1 }", 1)
	// An insertion right after the pre-existing line: its unique context is
	// that line, which must not be flagged (live-run finding).
	nextTo := strings.Replace(onDisk, "{ LegacyDynamic() }\n", "{ LegacyDynamic() }\n\nvar _ = 2\n", 1)
	if _, raw, d := runHook(t, cursorPayload(t, "Write", path, nextTo)); d.Hook.PermissionDec != "" {
		t.Errorf("an insertion next to a pre-existing name was blocked:\n%s", raw)
	}
	if _, raw, d := runHook(t, cursorPayload(t, "Write", path, unrelated)); d.Hook.PermissionDec != "" {
		t.Errorf("an unrelated Cursor edit was blocked by a pre-existing name:\n%s", raw)
	}
	added := strings.Replace(onDisk, "func G() {}", "func G() { HallucinatedNew() }", 1)
	_, raw, d := runHook(t, cursorPayload(t, "Write", path, added))
	assertDeny(t, d, raw)
	if r := d.Hook.PermissionReason; !strings.Contains(r, "HallucinatedNew") || strings.Contains(r, "LegacyDynamic") {
		t.Errorf("Cursor edit flagged the wrong names:\n%s", r)
	}
	if _, _, d := runHook(t, payload(t, "Write", path, "", unrelated, nil)); !strings.Contains(d.Hook.PermissionReason, "LegacyDynamic") {
		t.Errorf("a Claude Code Write is no longer checked whole: %q", d.Hook.PermissionReason)
	}
}

// Every check still scans the whole file, so a block that sits inside a
// multi-line string is masked as it is for a Claude Code Write, and only the
// changed lines can block (#544 round 2: the Edit conversion lost this).
func TestPosture_CursorWriteKeepsStringState(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "")
	path := halluRepo(t)
	const onDisk = "package m\n\nconst q = `\nSELECT a\nFROM t\n`\n\nfunc F() int { return 1 }\n"
	if err := os.WriteFile(path, []byte(onDisk), 0o644); err != nil {
		t.Fatal(err)
	}
	inString := strings.Replace(onDisk, "FROM t\n", "FROM t\nVALUES (1)\n", 1)
	if _, raw, d := runHook(t, cursorPayload(t, "Write", path, inString)); d.Hook.PermissionDec != "" {
		t.Errorf("an edit inside a raw string was blocked:\n%s", raw)
	}
	both := strings.Replace(inString, "return 1", "return HallucinatedZZ()", 1)
	_, raw, d := runHook(t, cursorPayload(t, "Write", path, both))
	assertDeny(t, d, raw)
	if r := d.Hook.PermissionReason; !strings.Contains(r, "HallucinatedZZ") || strings.Contains(r, "VALUES") {
		t.Errorf("string edit plus a bad call flagged the wrong names:\n%s", r)
	}
}

// A name that was only mentioned in the replaced text (a comment here) is
// still new when the edit calls it (#544 round 2: the identifier fold let it
// through).
func TestPosture_CursorWriteCommentNameStillNew(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_POSTURE", "")
	path := halluRepo(t)
	const onDisk = "package m\n\nfunc F() {\n\t// TODO: call SyncAllUsers here\n}\n"
	if err := os.WriteFile(path, []byte(onDisk), 0o644); err != nil {
		t.Fatal(err)
	}
	impl := strings.Replace(onDisk, "\t// TODO: call SyncAllUsers here\n", "\tSyncAllUsers()\n", 1)
	_, raw, d := runHook(t, cursorPayload(t, "Write", path, impl))
	assertDeny(t, d, raw)
	if !strings.Contains(d.Hook.PermissionReason, "SyncAllUsers") {
		t.Errorf("implementing a TODO's hallucinated name was not flagged:\n%s", d.Hook.PermissionReason)
	}
}

func TestCursorUnchangedLines(t *testing.T) {
	lines := func(s string) []guard.AddedLine { return guard.TextToAddedLines(s) }
	if cursorUnchangedLines(false, "Write", lines("a\n"), lines("b\n")) != nil ||
		cursorUnchangedLines(true, "Edit", lines("a\n"), lines("b\n")) != nil ||
		cursorUnchangedLines(true, "Write", nil, lines("b\n")) != nil {
		t.Fatal("a non-Cursor, non-Write or new-file fire filters findings")
	}
	for name, tc := range map[string]struct {
		old, new string
		changed  []int // 1-based lines of new that must be kept
		same     []int // lines that must be dropped
	}{
		"replace":    {"a\nb\nc\n", "a\nB\nc\n", []int{2}, []int{1, 3}},
		"insert":     {"a\nc\n", "a\nb\nc\n", []int{2}, []int{1, 3}},
		"insert-top": {"a\n", "z\na\n", []int{1}, []int{2}},
		"append-eof": {"a\nb", "a\nb\nc", []int{3}, []int{1, 2}},
		"repeated":   {"x\nx\nx\n", "x\nx\ny\nx\n", []int{3}, []int{1, 2, 4}},
		"all-new":    {"a\n", "b\n", []int{1}, nil},
		"delete":     {"a\nb\nc\n", "a\nc\n", nil, []int{1, 2}},
	} {
		u := cursorUnchangedLines(true, "Write", lines(tc.old), lines(tc.new))
		for _, n := range tc.changed {
			if u(n) {
				t.Errorf("%s: changed line %d dropped", name, n)
			}
		}
		for _, n := range tc.same {
			if !u(n) {
				t.Errorf("%s: unchanged line %d kept", name, n)
			}
		}
	}
}
