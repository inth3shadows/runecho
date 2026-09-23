package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inth3shadows/runecho/internal/contract"
	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// --- store unit tests ---

func TestContractOnce_RecordThenApproved(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	dir := t.TempDir()
	now := time.Now()
	recordContractApproval(dir, "s1", "hash1", "/r/a.go", now)

	if !contractApproved(dir, "s1", "hash1", "/r/a.go", now) {
		t.Fatal("the exact key that was recorded must be approved")
	}
	// Each key component on its own must miss — a key that ignores any one of
	// them suppresses a question nobody answered.
	for name, k := range map[string][3]string{
		"other session": {"s2", "hash1", "/r/a.go"},
		"other hash":    {"s1", "hash2", "/r/a.go"},
		"other file":    {"s1", "hash1", "/r/b.go"},
		"empty session": {"", "hash1", "/r/a.go"},
	} {
		if contractApproved(dir, k[0], k[1], k[2], now) {
			t.Errorf("%s: must not be approved", name)
		}
	}
}

func TestContractOnce_EmptyKeyRecordsNothing(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	dir := t.TempDir()
	recordContractApproval(dir, "", "h", "/f", time.Now())
	recordContractApproval(dir, "s", "", "/f", time.Now())
	recordContractApproval(dir, "s", "h", "", time.Now())
	if _, err := os.Stat(filepath.Join(dir, contractApprovalsFile)); !os.IsNotExist(err) {
		t.Errorf("an incomplete key must write no store; stat err = %v", err)
	}
}

func TestContractOnce_DisabledNeverSuppressesOrWrites(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	recordContractApproval(dir, "s", "h", "/f", now) // seeded while enabled

	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "0")
	if contractApproved(dir, "s", "h", "/f", now) {
		t.Error("disabled: a seeded entry must not suppress")
	}
	other := t.TempDir()
	recordContractApproval(other, "s", "h", "/f", now)
	if _, err := os.Stat(filepath.Join(other, contractApprovalsFile)); !os.IsNotExist(err) {
		t.Errorf("disabled: no store may be written; stat err = %v", err)
	}
}

func TestContractOnce_TTLFiltersOnReadAndPrunesOnWrite(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	dir := t.TempDir()
	old := time.Now().Add(-contractApprovalTTL - time.Hour)
	recordContractApproval(dir, "s", "h", "/old", old)
	now := time.Now()
	if contractApproved(dir, "s", "h", "/old", now) {
		t.Error("an entry past the TTL must not suppress")
	}
	recordContractApproval(dir, "s", "h", "/new", now)
	ca := loadContractApprovals(dir)
	if len(ca.Entries) != 1 || ca.Entries[0].File != "/new" {
		t.Errorf("the write path must prune past-TTL entries; got %+v", ca.Entries)
	}
}

func TestContractOnce_RerecordDedupes(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	dir := t.TempDir()
	now := time.Now()
	recordContractApproval(dir, "s", "h", "/f", now)
	recordContractApproval(dir, "s", "h", "/f", now.Add(time.Minute))
	if n := len(loadContractApprovals(dir).Entries); n != 1 {
		t.Errorf("re-approving the same key must not duplicate it; %d entries", n)
	}
}

func TestContractOnce_CapEvictsOldest(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	var ca contractApprovals
	for i := 0; i < maxContractApprovals; i++ {
		ca.Entries = append(ca.Entries, contractApproval{
			Session: "s", Hash: "h", File: fmt.Sprintf("/f%d", i),
			At: base.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339),
		})
	}
	if err := saveContractApprovals(dir, ca); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	recordContractApproval(dir, "s", "h", "/newest", now)
	got := loadContractApprovals(dir)
	if len(got.Entries) != maxContractApprovals {
		t.Fatalf("cap not enforced: %d entries", len(got.Entries))
	}
	if contractApproved(dir, "s", "h", "/f0", now) {
		t.Error("the oldest entry must be the one evicted")
	}
	if !contractApproved(dir, "s", "h", "/newest", now) || !contractApproved(dir, "s", "h", "/f1", now) {
		t.Error("the newest and the second-oldest entries must survive")
	}
}

func TestContractOnce_CorruptOrOversizedStoreFailsSafe(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	now := time.Now()
	for name, content := range map[string][]byte{
		"corrupt":   []byte("{not json"),
		"oversized": bytes.Repeat([]byte(" "), maxContractApprovalBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, contractApprovalsFile)
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if contractApproved(dir, "s", "h", "/f", now) {
				t.Error("an unreadable store must suppress nothing")
			}
			recordContractApproval(dir, "s", "h", "/f", now)
			if !contractApproved(dir, "s", "h", "/f", now) {
				t.Error("the next approval must rewrite the store whole")
			}
		})
	}
}

// Two PostToolUse hooks can fire for one edit, and parallel sessions approve
// concurrently. Without the lock, the second save clobbers the first's entry.
func TestContractOnce_ConcurrentRecordsNoLostUpdate(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	dir := t.TempDir()
	now := time.Now()
	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			recordContractApproval(dir, "s", "h", fmt.Sprintf("/f%d", i), now)
		}(i)
	}
	wg.Wait()
	if got := len(loadContractApprovals(dir).Entries); got != n {
		t.Errorf("lost updates: %d of %d entries survived", got, n)
	}
}

func TestHumanApproval_ModeGate(t *testing.T) {
	for mode, want := range map[string]bool{
		"":                  true,
		"default":           true,
		"acceptEdits":       true,
		"plan":              true,
		"bypassPermissions": false,
		"dontAsk":           false,
	} {
		if got := humanApproval(mode); got != want {
			t.Errorf("humanApproval(%q) = %v, want %v", mode, got, want)
		}
	}
}

// --- end-to-end: PreToolUse ask → PostToolUse approval → PreToolUse repeat ---

// approveEdit feeds runOutcomeMode the PostToolUse payload for the same Write
// contractPayload built, so the #300 fingerprint join pairs it with the ask.
func approveEdit(t *testing.T, sessionID, permissionMode, filePath, content string) {
	t.Helper()
	m := map[string]any{
		"tool_name":  "Write",
		"tool_input": map[string]any{"file_path": filePath, "content": content},
	}
	if sessionID != "" {
		m["session_id"] = sessionID
	}
	if permissionMode != "" {
		m["permission_mode"] = permissionMode
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	runOutcomeMode(strings.NewReader(string(b)))
}

// contractOnceEnv pins both flags, so an ambient RUNECHO_GUARD_CONTRACT_ONCE=0
// on a dogfooding machine cannot silently turn these into tests of the off path.
func contractOnceEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
}

func mustAsk(t *testing.T, stdin, why string) {
	t.Helper()
	if _, raw, d := runHook(t, stdin); d.Hook.PermissionDec != "ask" {
		t.Fatalf("%s: expected an ask; got %q\n%s", why, d.Hook.PermissionDec, raw)
	}
}

func mustNotAsk(t *testing.T, stdin, why string) {
	t.Helper()
	if _, raw, d := runHook(t, stdin); d.Hook.PermissionDec == "ask" {
		t.Fatalf("%s: expected no ask; got one\n%s", why, raw)
	}
}

func suppressedOf(rec map[string]any) []string {
	raw, _ := rec["suppressed"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		out = append(out, s)
	}
	return out
}

// The feature in one test: the approved file goes quiet, the record still says
// what was silenced and under which binding, and a DIFFERENT out-of-scope file
// still asks — the memo is per-file, not a scope change.
func TestContract_ApprovedOnceIsSilentForThatFileOnly(t *testing.T) {
	contractOnceEnv(t)
	const sess = "sess-once"
	top := contractRepo(t, sess, inScopeBody)
	a := filepath.Join(top, "internal", "guard", "a.go")
	b := filepath.Join(top, "internal", "guard", "b.go")
	// No symbol references: runOutcomeMode's E6 refresh reindexes the repo from
	// disk, which drops the fixture's injected KnownFunc, so a body that called
	// it would ask on the repeat for a reason unrelated to the contract.
	body := "package guard\n\nvar X = 1\n"

	mustAsk(t, contractPayload(t, sess, a, body), "first out-of-scope edit")
	askHash, _ := readLastDecisionLog(t)["contract_hash"].(string)
	approveEdit(t, sess, "", a, body)

	body2 := body + "\nvar Y = 2\n"
	mustNotAsk(t, contractPayload(t, sess, a, body2), "repeat edit to the approved file")
	rec := readLastDecisionLog(t)
	if rec["decision"] != "defer" {
		t.Errorf("a suppressed clean edit should log a defer; got %v", rec["decision"])
	}
	if got := suppressedOf(rec); len(got) != 1 || got[0] != "contract" {
		t.Errorf(`suppressed = %v, want ["contract"]`, got)
	}
	if rec["contract"] != "scope" || rec["contract_hash"] != askHash {
		t.Errorf("the suppressed record must name its binding; got contract=%v hash=%v (ask hash %q)", rec["contract"], rec["contract_hash"], askHash)
	}

	mustAsk(t, contractPayload(t, sess, b, body), "a different out-of-scope file")
}

func TestContract_OutcomeWithoutSessionDoesNotSuppress(t *testing.T) {
	contractOnceEnv(t)
	const sess = "sess-nosid"
	top := contractRepo(t, sess, inScopeBody)
	a := filepath.Join(top, "internal", "a.go")
	body := "package x\n"
	mustAsk(t, contractPayload(t, sess, a, body), "first edit")
	approveEdit(t, "", "", a, body)
	mustAsk(t, contractPayload(t, sess, a, body+"\n"), "no session id on the outcome means no memo")
}

func TestContract_OtherSessionApprovalDoesNotSuppress(t *testing.T) {
	contractOnceEnv(t)
	const sess = "sess-mine"
	top := contractRepo(t, sess, inScopeBody)
	a := filepath.Join(top, "internal", "a.go")
	body := "package x\n"
	mustAsk(t, contractPayload(t, sess, a, body), "first edit")
	approveEdit(t, "sess-someone-else", "", a, body)
	mustAsk(t, contractPayload(t, sess, a, body+"\n"), "another session's approval answers nothing here")
}

func TestContract_BypassModeOutcomeDoesNotSuppress(t *testing.T) {
	contractOnceEnv(t)
	const sess = "sess-bypass"
	top := contractRepo(t, sess, inScopeBody)
	a := filepath.Join(top, "internal", "a.go")
	body := "package x\n"
	mustAsk(t, contractPayload(t, sess, a, body), "first edit")
	approveEdit(t, sess, "bypassPermissions", a, body)
	mustAsk(t, contractPayload(t, sess, a, body+"\n"), "an ask nobody was shown must not be remembered as answered")
}

func TestContract_OnceDisabledKeepsAsking(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT", "1")
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "0")
	const sess = "sess-off"
	top := contractRepo(t, sess, inScopeBody)
	a := filepath.Join(top, "internal", "a.go")
	body := "package x\n"
	mustAsk(t, contractPayload(t, sess, a, body), "first edit")
	approveEdit(t, sess, "", a, body)
	mustAsk(t, contractPayload(t, sess, a, body+"\n"), "RUNECHO_GUARD_CONTRACT_ONCE=0 must keep asking")
	if _, err := os.Stat(filepath.Join(os.Getenv("RUNECHO_HOME"), contractApprovalsFile)); !os.IsNotExist(err) {
		t.Errorf("disabled: no memo store may be written; stat err = %v", err)
	}
}

// reactivate re-binds sess to the contract file at top with the given body.
func reactivate(t *testing.T, top, sess, body string) {
	t.Helper()
	cpath := filepath.Join(top, contract.Dir, "scope")
	if err := os.WriteFile(cpath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := contract.Load(cpath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := snapshot.Open(filepath.Join(os.Getenv("RUNECHO_HOME"), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo, _, ok := db.ResolveRepo(top)
	if !ok {
		t.Fatal("repo did not resolve")
	}
	if err := db.ActivateContract(repo.ID, sess, c.Name, c.Path, c.Hash); err != nil {
		t.Fatal(err)
	}
}

// The key is the ACTIVATION hash: re-declaring the same text keeps the answer,
// re-declaring different text is a new commitment and asks again.
func TestContract_ReactivationSemantics(t *testing.T) {
	contractOnceEnv(t)
	const sess = "sess-react"
	top := contractRepo(t, sess, inScopeBody)
	a := filepath.Join(top, "internal", "a.go")
	body := "package x\n"
	mustAsk(t, contractPayload(t, sess, a, body), "first edit")
	approveEdit(t, sess, "", a, body)

	reactivate(t, top, sess, inScopeBody)
	mustNotAsk(t, contractPayload(t, sess, a, body+"\n"), "same text re-activated")

	reactivate(t, top, sess, inScopeBody+"docs/**\n")
	mustAsk(t, contractPayload(t, sess, a, body+"\n\n"), "edited text re-activated is a clean slate")
}

// A merged ask's approval answers the contract's path question too, so the next
// edit drops only the contract half: the fact half still asks, under its bare
// reason, and — now that no scope decision rides on the approval — offers its
// symbols to learned-allow again.
func TestContract_MergedAskApprovalSuppressesOnlyContract(t *testing.T) {
	contractOnceEnv(t)
	const sess = "sess-merged"
	top := contractRepo(t, sess, inScopeBody)
	a := filepath.Join(top, "internal", "y.go")
	body := "package main\n\nfunc F() { TotallyMadeUpSymbol() }\n"
	mustAsk(t, contractPayload(t, sess, a, body), "merged ask")
	if r := readLastDecisionLog(t)["reason"]; r != "contract+violations" {
		t.Fatalf("precondition: expected a merged ask, reason %v", r)
	}
	approveEdit(t, sess, "", a, body)

	body2 := body + "\nfunc G() { TotallyMadeUpSymbol() }\n"
	_, _, d := runHook(t, contractPayload(t, sess, a, body2))
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("the fact half must still ask; got %q", d.Hook.PermissionDec)
	}
	if strings.Contains(d.Hook.PermissionReason, "outside the scope") {
		t.Errorf("the contract section must be gone: %q", d.Hook.PermissionReason)
	}
	rec := readLastDecisionLog(t)
	if rec["reason"] != "violations" {
		t.Errorf("reason = %v, want the bare fact reason", rec["reason"])
	}
	if rec["learn_symbols"] == nil {
		t.Errorf("with the contract half suppressed, learn_symbols must be populated again: %v", rec)
	}
	if got := suppressedOf(rec); len(got) != 1 || got[0] != "contract" {
		t.Errorf(`suppressed = %v, want ["contract"]`, got)
	}
}

// Non-code files are where scope drift most often lands, and they reach the
// contract through a different arm (bailUnknownLang). The suppression must work
// — and be logged — there too.
func TestContract_SuppressedOnUnknownLangArm(t *testing.T) {
	contractOnceEnv(t)
	const sess = "sess-md"
	top := contractRepo(t, sess, inScopeBody)
	md := filepath.Join(top, "docs", "NOTES.md")
	mustAsk(t, contractPayload(t, sess, md, "# a\n"), "first doc edit")
	approveEdit(t, sess, "", md, "# a\n")
	mustNotAsk(t, contractPayload(t, sess, md, "# b\n"), "repeat doc edit")
	rec := readLastDecisionLog(t)
	if rec["reason"] != bailUnknownLang {
		t.Errorf("reason = %v, want %q", rec["reason"], bailUnknownLang)
	}
	if got := suppressedOf(rec); len(got) != 1 || got[0] != "contract" {
		t.Errorf(`suppressed = %v, want ["contract"]`, got)
	}
}

// A window-track join (an ask with no edit fingerprint, as older guards wrote)
// is a guess about WHICH edit was approved. A memo written from a guess would
// suppress an ask nobody answered, so only the fingerprint join writes one.
func TestContract_WindowJoinDoesNotWriteMemo(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	file := "/some/repo/internal/a.go"
	logDecision(decisionRecord{Mode: "hook", File: file, Decision: "ask", Reason: "contract", Contract: "scope", ContractHash: "abcdefabcdef"})
	approveEdit(t, "sess", "", file, "x\n")
	if rec := readLastDecisionLog(t); rec["decision"] != "outcome" || rec["join"] != "window" {
		t.Fatalf("precondition: expected a window-joined outcome; got %v", rec)
	}
	if _, err := os.Stat(filepath.Join(home, contractApprovalsFile)); !os.IsNotExist(err) {
		t.Errorf("a window join must write no memo; stat err = %v", err)
	}
}

func TestContract_AskTextPromisesOnce(t *testing.T) {
	cw := &contractWarning{Name: "scope", ContractPath: "/r/.runecho/contracts/scope", RepoRoot: "/r", SessionID: "s", RelPath: "x.go", Patterns: 1, ActivatedHash: "h", CurrentHash: "h"}
	const promise = "Approving records an answer for this file"
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	if !strings.Contains(cw.section(), promise) {
		t.Errorf("enabled: the ask must say what an approval buys:\n%s", cw.section())
	}
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "0")
	if strings.Contains(cw.section(), promise) {
		t.Errorf("disabled: the ask must not promise a suppression that will not happen:\n%s", cw.section())
	}
}

// The degraded-store arms write their own records; each must carry the marker.
func TestAnswerDegradedStore_StampsSuppressedContract(t *testing.T) {
	sc := &contractWarning{Name: "scope", ActivatedHash: "0123456789abcdef"}
	for name, res := range map[string]lookupResult{
		"schema-newer":   {Warn: "newer", RepoName: "r", ContractSuppressed: sc},
		"no-repo":        {NoRepo: true, ContractSuppressed: sc},
		"store-degraded": {RepoName: "r", ContractSuppressed: sc},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("RUNECHO_HOME", t.TempDir())
			var out bytes.Buffer
			if answerDegradedStore(&out, res, hookEdit{ToolName: "Edit", NewString: "x = 1\n"}, "/tmp/whatever/a.py", guard.LangPython, "") {
				t.Fatal("no finding: this arm must defer")
			}
			rec := readLastDecisionLog(t)
			if got := suppressedOf(rec); len(got) != 1 || got[0] != "contract" {
				t.Errorf(`suppressed = %v, want ["contract"]`, got)
			}
			if rec["contract_hash"] != "0123456789ab" {
				t.Errorf("contract_hash = %v, want the short activation hash", rec["contract_hash"])
			}
		})
	}
}

// The join pairs an outcome with the latest ask for the same edit, and a denied
// ask writes nothing — so a denied ask whose identical retry was answered by
// something else (a defer after deactivation, a hook timeout) would otherwise be
// remembered as approved. Found by adversarial review; each subtest is one way
// the edit can be answered after the ask, plus the two controls that must still
// record.
func TestContract_MemoOnlyWhenTheJoinedAskStillStands(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	const file = "/r/internal/a.go"
	ask := decisionRecord{Mode: "hook", File: file, Decision: "ask", Reason: "contract", Contract: "scope", ContractHash: "abcdefabcdef", Edit: "e1"}
	for name, tc := range map[string]struct {
		after []decisionRecord
		want  bool
	}{
		"no later record (control)":         {nil, true},
		"#252 re-fire of the same ask":      {[]decisionRecord{ask}, true},
		"later defer for the same file":     {[]decisionRecord{{Mode: "hook", File: file, Decision: "defer", Reason: "clean"}}, false},
		"later fileless timeout":            {[]decisionRecord{{Mode: "hook", Decision: "defer", Reason: "timeout"}}, false},
		"later ask for a different edit":    {[]decisionRecord{{Mode: "hook", File: file, Decision: "ask", Reason: "violations", Edit: "e2"}}, false},
		"later record for a different file": {[]decisionRecord{{Mode: "hook", File: "/r/other.go", Decision: "defer", Reason: "clean"}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("RUNECHO_HOME", home)
			logDecision(ask)
			for _, r := range tc.after {
				logDecision(r)
			}
			logOutcomeForFile(file, "e1", "sess", "acceptEdits")
			if got := contractApproved(home, "sess", "abcdefabcdef", file, time.Now()); got != tc.want {
				t.Errorf("memo written = %v, want %v", got, tc.want)
			}
		})
	}
}

// Every renderer arm that writes a record for a suppressed repeat must stamp it;
// the end-to-end tests reach only some of them. Driven directly so each arm is
// pinned on its own.
func TestRenderers_StampSuppressedOnEveryArm(t *testing.T) {
	sc := &contractWarning{Name: "scope", ActivatedHash: "0123456789abcdef"}
	check := func(t *testing.T) {
		t.Helper()
		rec := readLastDecisionLog(t)
		if got := suppressedOf(rec); len(got) != 1 || got[0] != "contract" {
			t.Errorf(`suppressed = %v, want ["contract"] on %v`, got, rec)
		}
	}
	for name, v := range map[string]verification{
		"empty-input":    {Bail: bailEmptyInput, Path: "/r/a.go", ContractSuppressed: sc},
		"unknown-lang":   {Bail: bailUnknownLang, Path: "/r/a.md", ContractSuppressed: sc},
		"clean":          {Path: "/r/a.go", Lang: guard.LangGo, ContractSuppressed: sc},
		"check-degraded": {Path: "/r/a.go", Lang: guard.LangGo, ContractSuppressed: sc, Results: []CheckResult{{Check: "additive", Verdict: VerdictUnknown, Reason: "store-query-failed"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("RUNECHO_HOME", t.TempDir())
			t.Setenv("RUNECHO_GUARD_STRICT", "1")
			var out bytes.Buffer
			renderHookDecision(&out, v)
			if name == "check-degraded" {
				if r := readLastDecisionLog(t)["reason"]; r != "check-degraded" {
					t.Fatalf("precondition: reason %v, want check-degraded", r)
				}
			}
			check(t)
		})
	}
	t.Run("degraded-store ask", func(t *testing.T) {
		t.Setenv("RUNECHO_HOME", t.TempDir())
		var out bytes.Buffer
		ms := []guard.CallShapeMismatch{{Callee: "f", Keyword: "k", LineNo: 1, DeclLine: 1}}
		if !askWithoutIndex(&out, nil, sc, ms, nil, "/r/a.py", guard.LangPython, "r", "", "e") {
			t.Fatal("a call-shape finding must ask")
		}
		check(t)
	})
}

// Both sides key on the CLEANED path. Every other fixture uses a clean path, so
// without this a write side that stored the raw path would pass everything and
// never match the read side's cleaned one.
func TestContract_UncleanPathStillSuppresses(t *testing.T) {
	contractOnceEnv(t)
	const sess = "sess-clean"
	top := contractRepo(t, sess, inScopeBody)
	contractPayload(t, sess, filepath.Join(top, "internal", "a.go"), "") // create the dir
	a := top + "/internal/./a.go"
	body := "package x\n"
	mustAsk(t, contractPayload(t, sess, a, body), "first edit")
	approveEdit(t, sess, "", a, body)
	mustNotAsk(t, contractPayload(t, sess, a, body+"\n"), "repeat edit through the same unclean path")
}
