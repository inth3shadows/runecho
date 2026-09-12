// protocol_test.go — #394's stdin/stdout verdict protocol.
//
// The contract these pin is in TECHNICAL.md; the ones that matter most are the
// two a consumer would silently build a wrong policy on: every check appears
// exactly once (so "absent" is never something to interpret), and `unknown`
// never collapses into `ok` (so "no findings" cannot mean "could not look").
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
)

// runProtocol drives the real entry point and decodes its answer.
func runProtocol(t *testing.T, req string) (int, protocolDoc, string) {
	t.Helper()
	var out bytes.Buffer
	code := runProtocolMode(strings.NewReader(req), &out)
	raw := out.String()
	var doc protocolDoc
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			t.Fatalf("response is not JSON: %v\n%s", err, raw)
		}
	}
	return code, doc, raw
}

func resultFor(t *testing.T, doc protocolDoc, check string) protocolResult {
	t.Helper()
	for _, r := range doc.Results {
		if r.Check == check {
			return r
		}
	}
	t.Fatalf("no result for %q in %+v", check, doc.Results)
	return protocolResult{}
}

func writeReq(t *testing.T, path, content string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"protocol": 1, "path": path, "content": content})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// T1 — the structural promise. A consumer keys by check name and must never have
// to decide what a missing key meant; contrast decisions.jsonl, where a
// pre-commit record legitimately omits seven of the eleven.
func TestProtocolMode_EveryCheckPresentOnce(t *testing.T) {
	assertComplete := func(t *testing.T, doc protocolDoc) {
		t.Helper()
		if len(doc.Results) != len(checkOrder) {
			t.Fatalf("got %d results, want %d", len(doc.Results), len(checkOrder))
		}
		seen := map[string]int{}
		for i, r := range doc.Results {
			seen[r.Check]++
			if r.Check != checkOrder[i] {
				t.Errorf("result %d is %q, want %q — order must be checkOrder", i, r.Check, checkOrder[i])
			}
		}
		for _, name := range checkOrder {
			if seen[name] != 1 {
				t.Errorf("check %q appears %d times, want exactly 1", name, seen[name])
			}
		}
	}

	t.Run("enrolled", func(t *testing.T) {
		repo := t.TempDir()
		gitInit(t, repo)
		enrolledStore(t, repo, []string{"KnownFunc"})
		_, doc, _ := runProtocol(t, writeReq(t, filepath.Join(repo, "main.go"), "package main\n\nfunc x() { KnownFunc() }\n"))
		assertComplete(t, doc)
	})

	t.Run("unenrolled still answers for all eleven", func(t *testing.T) {
		repo := t.TempDir()
		gitInit(t, repo)
		other := t.TempDir()
		gitInit(t, other)
		enrolledStore(t, other, []string{"KnownFunc"})
		_, doc, _ := runProtocol(t, writeReq(t, filepath.Join(repo, "main.go"), "package main\n\nfunc x() { Whatever() }\n"))
		assertComplete(t, doc)
		// This is the arm where the hook says NOTHING. The protocol must still
		// distinguish "could not look" from "looked and found nothing".
		r := resultFor(t, doc, "violations")
		if r.Verdict != "unknown" {
			t.Errorf("violations on an unenrolled tree = %q, want unknown", r.Verdict)
		}
		if r.Reason != "no-repo" || r.Class != "degraded" {
			t.Errorf("got reason=%q class=%q, want no-repo/degraded", r.Reason, r.Class)
		}
	})
}

// The completeness guarantee must hold even when the CORE is incomplete. Today
// verifyEdit appends all eleven, so the fallback in renderProtocol is
// unreachable through the real entry point and a mutation that deletes it
// survives every end-to-end test. That is precisely the shape that rots: the
// day a twelfth check ships and forgets to append, the promise a consumer keys
// on would break silently. Asserted directly against renderProtocol instead.
func TestProtocolMode_CompletenessSurvivesAnIncompleteCore(t *testing.T) {
	v := verification{
		Edit: hookEdit{ToolName: "Write"},
		Path: "/a/b.go",
		Results: []CheckResult{
			{Check: "violations", Verdict: VerdictOK},
			{Check: "lint", Verdict: VerdictSkipped},
		},
	}
	doc := renderProtocol(v)
	if len(doc.Results) != len(checkOrder) {
		t.Fatalf("got %d results from a 2-result core, want all %d", len(doc.Results), len(checkOrder))
	}
	for i, r := range doc.Results {
		if r.Check != checkOrder[i] {
			t.Errorf("result %d is %q, want %q", i, r.Check, checkOrder[i])
		}
	}
	// The nine the core never reported render as skipped — never absent, and
	// never invented as ok.
	got := resultFor(t, doc, "dangling")
	if got.Verdict != "skipped" {
		t.Errorf("an unreported check rendered as %q, want skipped", got.Verdict)
	}
	if resultFor(t, doc, "violations").Verdict != "ok" {
		t.Error("a reported check was overwritten by the fallback")
	}
}

// T2 — the #359 invariant, stated where a consumer can rely on it. An oversized
// pre-edit file is real lost coverage, and it must never render as ok.
func TestProtocolMode_UnknownNeverCollapses(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	enrolledStore(t, repo, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_FILESCOPE", "1")

	big := filepath.Join(repo, "big.py")
	if err := os.WriteFile(big, bytes.Repeat([]byte("# filler\n"), 300000), 0o644); err != nil {
		t.Fatal(err)
	}
	req, err := json.Marshal(map[string]any{
		"protocol": 1, "path": big,
		"hunks": []map[string]string{{"old": "", "new": "x = KnownFunc()\n"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, doc, _ := runProtocol(t, string(req))

	r := resultFor(t, doc, "file-scope")
	if r.Verdict != "unknown" {
		t.Fatalf("file-scope on an oversized pre-edit file = %q, want unknown", r.Verdict)
	}
	if r.Reason != "oversized-pre-edit-file" {
		t.Errorf("reason = %q, want oversized-pre-edit-file", r.Reason)
	}
	if r.Class != "degraded" {
		t.Errorf("class = %q, want degraded — lost context is not a precision gate", r.Class)
	}
}

// T3 — the other half of the class split. A check that SAW a candidate and
// declined it on its own precision gate is not lost coverage, and a consumer
// that treats it as such would warn on roughly one Go edit in five.
func TestProtocolMode_GateClass(t *testing.T) {
	for reason := range gateAbstainReasons {
		if got := abstainClass(reason); got != "gate" {
			t.Errorf("abstainClass(%q) = %q, want gate", reason, got)
		}
	}
	for _, reason := range []string{"oversized-pre-edit-file", "store-query-failed", "no-repo", "brand-new-token"} {
		if got := abstainClass(reason); got != "degraded" {
			t.Errorf("abstainClass(%q) = %q, want degraded", reason, got)
		}
	}
}

// T4 — an edit must get the same verdicts here as through the hook, and tool
// name is what several code paths branch on.
func TestProtocolMode_InputMapping(t *testing.T) {
	cases := []struct {
		name, req string
		want      hookEdit
	}{
		{"content is a Write", `{"protocol":1,"path":"/a/b.py","content":"x=1\n"}`,
			hookEdit{ToolName: "Write", Content: "x=1\n"}},
		{"empty content is still a Write", `{"protocol":1,"path":"/a/b.py","content":""}`,
			hookEdit{ToolName: "Write", Content: ""}},
		{"one hunk is an Edit", `{"protocol":1,"path":"/a/b.py","hunks":[{"old":"o","new":"n"}]}`,
			hookEdit{ToolName: "Edit", OldString: "o", NewString: "n"}},
		{"three hunks is a MultiEdit", `{"protocol":1,"path":"/a/b.py","hunks":[{"old":"a","new":"b"},{"old":"c","new":"d"},{"old":"e","new":"f"}]}`,
			hookEdit{ToolName: "MultiEdit", Edits: []editOp{{OldString: "a", NewString: "b"}, {OldString: "c", NewString: "d"}, {OldString: "e", NewString: "f"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, path, errTok := decodeProtocolInput(strings.NewReader(tc.req))
			if errTok != "" {
				t.Fatalf("unexpected error token %q", errTok)
			}
			if path != "/a/b.py" {
				t.Errorf("path = %q", path)
			}
			if got.ToolName != tc.want.ToolName {
				t.Errorf("ToolName = %q, want %q", got.ToolName, tc.want.ToolName)
			}
			if got.Content != tc.want.Content || got.OldString != tc.want.OldString || got.NewString != tc.want.NewString {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
			if len(got.Edits) != len(tc.want.Edits) {
				t.Fatalf("Edits len = %d, want %d", len(got.Edits), len(tc.want.Edits))
			}
			for i := range got.Edits {
				if got.Edits[i] != tc.want.Edits[i] {
					t.Errorf("Edits[%d] = %+v, want %+v", i, got.Edits[i], tc.want.Edits[i])
				}
			}
		})
	}
}

// T5 — a malformed request gets an error DOCUMENT and exit 2, never a document
// of verdicts. The two are different answers and must not be confusable.
func TestProtocolMode_RejectsBadInput(t *testing.T) {
	cases := []struct{ name, req, want string }{
		{"no protocol field", `{"path":"/a/b.py","content":"x"}`, errUnsupportedProtocol},
		{"future protocol", `{"protocol":2,"path":"/a/b.py","content":"x"}`, errUnsupportedProtocol},
		{"not json", `{nope`, errMalformedInput},
		{"missing path", `{"protocol":1,"content":"x"}`, errMissingPath},
		{"relative path", `{"protocol":1,"path":"b.py","content":"x"}`, errRelativePath},
		{"nul in path", "{\"protocol\":1,\"path\":\"/a/\\u0000b.py\",\"content\":\"x\"}", errBadPath},
		{"both content and hunks", `{"protocol":1,"path":"/a/b.py","content":"x","hunks":[{"old":"o","new":"n"}]}`, errAmbiguousEdit},
		{"neither", `{"protocol":1,"path":"/a/b.py"}`, errMissingEdit},
		{"empty hunks", `{"protocol":1,"path":"/a/b.py","hunks":[]}`, errEmptyHunks},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, doc, raw := runProtocol(t, tc.req)
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if doc.Error != tc.want {
				t.Errorf("error = %q, want %q\n%s", doc.Error, tc.want, raw)
			}
			if len(doc.Results) != 0 {
				t.Errorf("an error document must carry no results, got %d", len(doc.Results))
			}
		})
	}
}

// T6 — the exit code says whether the guard could be ASKED, not what it thinks.
// A consumer that reads exit 0 as "clean" would ship every hallucination.
func TestProtocolMode_ExitCodeIsNotAVerdict(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	enrolledStore(t, repo, []string{"ProcessData"})

	code, doc, raw := runProtocol(t, writeReq(t, filepath.Join(repo, "main.go"),
		"package main\n\nfunc x() { ProcesData() }\n"))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 — a document full of violations is still a document\n%s", code, raw)
	}
	r := resultFor(t, doc, "violations")
	if r.Verdict != "violation" {
		t.Fatalf("violations = %q, want violation\n%s", r.Verdict, raw)
	}
	if len(r.Evidence) == 0 {
		t.Fatal("a violation with no evidence is not actionable")
	}
	if r.Evidence[0].Symbol != "ProcesData" {
		t.Errorf("evidence symbol = %q, want ProcesData", r.Evidence[0].Symbol)
	}
	if r.Evidence[0].LineSpace == "" {
		t.Error("line was reported without line_space — a consumer cannot place it")
	}
}

// T7 — every check's own finding shape survives onto the wire. This is the test
// that would catch a check being rendered as a bare verdict because its type did
// not fit guard.Violation, which is the exact failure the evidence design exists
// to avoid.
func TestProtocolMode_Evidence(t *testing.T) {
	base := verification{Edit: hookEdit{ToolName: "Edit"}}
	cases := []struct {
		name   string
		check  string
		setup  func(v *verification)
		assert func(t *testing.T, e protocolEvidence)
	}{
		{"dangling carries referrers", "dangling", func(v *verification) {
			v.Dangling = []danglingWarning{{Symbol: "Gone", Referrers: []string{"a/b.go"}}}
		}, func(t *testing.T, e protocolEvidence) {
			if e.Symbol != "Gone" || len(e.Referrers) != 1 || e.Referrers[0] != "a/b.go" {
				t.Errorf("got %+v", e)
			}
		}},
		{"duplicate carries locations", "duplicate-symbol", func(v *verification) {
			v.Duplicates = []duplicateWarning{{Symbol: "Dup", Locations: []string{"x/y.go"}}}
		}, func(t *testing.T, e protocolEvidence) {
			if e.Symbol != "Dup" || len(e.Locations) != 1 {
				t.Errorf("got %+v", e)
			}
		}},
		{"dropped-import carries a snippet line", "dropped-import", func(v *verification) {
			v.Dropped = []guard.DroppedImport{{Name: "path", LineNo: 4}}
		}, func(t *testing.T, e protocolEvidence) {
			if e.Symbol != "path" || e.Line != 4 || e.LineSpace != "snippet" {
				t.Errorf("got %+v", e)
			}
		}},
		{"call-shape carries keyword, accepted and its own decl space", "call-shape", func(v *verification) {
			v.CallShapes = []guard.CallShapeMismatch{{
				Callee: "fetch", Keyword: "timeuot", LineNo: 9,
				DeclLine: 2, DeclLineIsSnippet: true,
				Accepted: []string{"url", "timeout"}, Suggestions: []string{"timeout"},
			}}
		}, func(t *testing.T, e protocolEvidence) {
			if e.Symbol != "fetch" || e.Keyword != "timeuot" || len(e.Accepted) != 2 {
				t.Errorf("got %+v", e)
			}
			if e.DeclLine != 2 || e.DeclLineSpace != "snippet" {
				t.Errorf("decl space not carried: %+v", e)
			}
		}},
		{"lint is always file-spaced", "lint", func(v *verification) {
			v.Lint = []lintFinding{{Symbol: "helper", Line: 7, Rule: "F821", Message: "Undefined name `helper`"}}
		}, func(t *testing.T, e protocolEvidence) {
			if e.Rule != "F821" || e.Message == "" {
				t.Errorf("got %+v", e)
			}
			if e.LineSpace != "file" {
				t.Errorf("lint line_space = %q, want file — ruff reports against the proposed content", e.LineSpace)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := base
			tc.setup(&v)
			r := protocolResultFor(CheckResult{Check: tc.check, Verdict: VerdictViolation}, v, v.CallShapes, v.Lint)
			if len(r.Evidence) != 1 {
				t.Fatalf("got %d evidence objects, want 1: %+v", len(r.Evidence), r)
			}
			tc.assert(t, r.Evidence[0])
		})
	}
}

// A Write proposes the whole file, so its line numbers ARE file lines; an Edit
// carries only the hunk. Getting this backwards sends an agent to the wrong line
// in the one message whose job is to be precise.
func TestProtocolMode_LineSpaceFollowsTool(t *testing.T) {
	if got := lineSpaceFor("Write"); got != "file" {
		t.Errorf("Write = %q, want file", got)
	}
	for _, tool := range []string{"Edit", "MultiEdit"} {
		if got := lineSpaceFor(tool); got != "snippet" {
			t.Errorf("%s = %q, want snippet", tool, got)
		}
	}
}

// T9 — protocol mode must not pollute the dogfood stream. fpreport and fpaudit
// filter on mode=="hook", so a CI consumer replaying hundreds of edits would
// either be ignored or mis-bucketed; either way the un-gating decisions rest on
// that file and it is not this mode's to write.
func TestProtocolMode_DoesNotWriteDecisionLog(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	enrolledStore(t, repo, []string{"ProcessData"})
	home := os.Getenv("RUNECHO_HOME")

	if _, err := os.Stat(filepath.Join(home, "decisions.jsonl")); err == nil {
		t.Skip("decision log already exists from setup; cannot attribute")
	}
	runProtocol(t, writeReq(t, filepath.Join(repo, "main.go"), "package main\n\nfunc x() { ProcesData() }\n"))

	if _, err := os.Stat(filepath.Join(home, "decisions.jsonl")); err == nil {
		t.Error("protocol mode wrote to decisions.jsonl")
	}
}

// T10 — on panic the barrier must SAY so. deferOnPanic's "write nothing, exit 0"
// is right for a hook and catastrophic here: a consumer that asked a question
// and got empty stdout with a success code reads it as "no findings".
func TestProtocolMode_PanicYieldsErrorDoc(t *testing.T) {
	orig := protocolVerifyBody
	t.Cleanup(func() { protocolVerifyBody = orig })
	protocolVerifyBody = func(edit hookEdit, filePath, sessionID string) verification {
		panic("boom")
	}

	var out bytes.Buffer
	code := protocolPanicBarrier(&out, func(w io.Writer) int {
		return runProtocolMode(strings.NewReader(`{"protocol":1,"path":"/a/b.py","content":"x"}`), w)
	})
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	var doc protocolDoc
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &doc); err != nil {
		t.Fatalf("panic response is not JSON: %v\n%s", err, out.String())
	}
	if doc.Error != errPanic {
		t.Errorf("error = %q, want %q", doc.Error, errPanic)
	}
}

// protocolCheckName maps a fixture's `check` field onto the checkOrder name the
// protocol reports under. The two vocabularies were never the same — the corpus
// says "callshape" and "duplicate" where the results array says "call-shape" and
// "duplicate-symbol" — and a parity test that silently failed to find its check
// would pass by looking at nothing.
var protocolCheckName = map[string]string{
	"callshape":      "call-shape",
	"duplicate":      "duplicate-symbol",
	"dropped-import": "dropped-import",
	"dangling":       "dangling",
	"file-scope":     "file-scope",
	"qualified":      "qualified",
	"deps-go":        "deps-go",
	"lint":           "lint",
}

// protocolRequestFor converts a rendered PreToolUse payload into the equivalent
// protocol request. Going through the fixture's OWN hook body — rather than
// building a request from the fixture fields a second time — is what makes this
// a parity test: both surfaces are then demonstrably answering about one edit.
func protocolRequestFor(t *testing.T, hookBody, editedAbs string) string {
	t.Helper()
	var p struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			NewString string `json:"new_string"`
			OldString string `json:"old_string"`
			Content   string `json:"content"`
			Edits     []struct {
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
			} `json:"edits"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal([]byte(hookBody), &p); err != nil {
		t.Fatalf("fixture payload is not JSON: %v", err)
	}
	req := map[string]any{"protocol": 1, "path": editedAbs}
	switch p.ToolName {
	case "Write":
		req["content"] = p.ToolInput.Content
	case "MultiEdit":
		hunks := make([]map[string]string, 0, len(p.ToolInput.Edits))
		for _, e := range p.ToolInput.Edits {
			hunks = append(hunks, map[string]string{"old": e.OldString, "new": e.NewString})
		}
		req["hunks"] = hunks
	default:
		req["hunks"] = []map[string]string{{"old": p.ToolInput.OldString, "new": p.ToolInput.NewString}}
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// T11 — the document is not a lossy shadow of the ask, across all sixty
// fixtures. Every unit test above asserts a shape I chose; this asserts that on
// every input the corpus already believes in, the protocol reaches the same
// conclusion the hook does and names the same things.
//
// Deliberately a SEPARATE -run name from TestHookCorpus: bench/hookmutate scores
// that test, and adding assertions inside it would change what the mutation
// catalog measures.
func TestProtocolCorpusParity(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "hookcorpus", "*.json"))
	if err != nil {
		t.Fatalf("glob hook corpus: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no hook corpus fixtures found — this test would pass vacuously")
	}
	outOfScope := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var cases []hookCase
		if err := json.Unmarshal(data, &cases); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, c := range cases {
			c := c
			if c.Check == "contract" {
				// The edit-scope contract is bound to a Claude Code session and
				// is out of protocol v1. Filtered here rather than t.Skip'd
				// inside the subtest: .github/scripts/check-skips.sh matches
				// subtest names EXACTLY and fails in both directions, so a skip
				// would put every contract fixture's name in the allowlist and
				// break an unrelated PR the moment one is added or renamed.
				outOfScope++
				continue
			}
			t.Run(c.Name, func(t *testing.T) {
				if c.Check == "lint" {
					if _, err := exec.LookPath("ruff"); err != nil {
						t.Skip("ruff not on PATH — the lint check fails open without it")
					}
				}
				want, ok := protocolCheckName[c.Check]
				if !ok {
					t.Fatalf("fixture check %q has no protocol name — the map above went stale", c.Check)
				}

				_, edited, setFlags, body := setupHookCase(t, c)
				setFlags(true)
				_, doc, raw := runProtocol(t, protocolRequestFor(t, body, edited))

				if len(doc.Results) != len(checkOrder) {
					t.Fatalf("got %d results, want %d\n%s", len(doc.Results), len(checkOrder), raw)
				}
				if !c.ExpectAsk {
					// A fixture the hook stays silent on must produce no violation
					// anywhere. Checking every check, not just this fixture's, is
					// what would catch the protocol inventing a finding the ask
					// never made.
					for _, r := range doc.Results {
						if r.Verdict == "violation" {
							t.Errorf("silent fixture produced a %s violation: %+v", r.Check, r.Evidence)
						}
					}
					return
				}

				if c.AskWithoutFlag {
					// This fixture pins that the gated check adds NOTHING to an
					// ask that fires anyway — "python-firewall-leaves-invented-
					// symbols-alone" and "python-lint-additive-overlap-adds-
					// nothing" are the two. Demanding a violation from THIS check
					// would invert what the fixture asserts. What must hold is
					// that the document still reports the ask from somewhere.
					if !anyViolation(doc) {
						t.Errorf("the hook asks on this fixture but no check reports a violation\n%s", raw)
					}
					return
				}

				got := resultFor(t, doc, want)
				if got.Verdict != "violation" {
					t.Fatalf("%s = %q, want violation — the hook asks on this fixture\n%s", want, got.Verdict, raw)
				}
				if len(got.Evidence) == 0 {
					t.Fatal("violation carried no evidence")
				}
				// ExpectSyms is asserted by runHookCase as SUBSTRINGS of the ask
				// prose, so it mixes symbols with fragments of the guard's own
				// sentences ("not in this file's scope", "line 2"). Only the
				// token-shaped entries name something the evidence could carry;
				// the prose fragments are the ask's wording, which the protocol
				// deliberately does not reproduce.
				checked := 0
				for _, sym := range c.ExpectSyms {
					if strings.ContainsAny(sym, " \t'") {
						continue
					}
					checked++
					if !evidenceMentions(got.Evidence, sym) {
						t.Errorf("evidence does not mention %q, which the ask is pinned to name: %+v", sym, got.Evidence)
					}
				}
				if len(c.ExpectSyms) > 0 && checked == 0 {
					// Every entry was prose. Then this fixture pins no name the
					// protocol can be checked against, and saying so is better
					// than reporting a pass that examined nothing.
					t.Logf("note: expect_symbols for this fixture is all prose (%v); parity checked verdict only", c.ExpectSyms)
				}
			})
		}
	}
	if outOfScope > 0 {
		t.Logf("%d contract fixtures not replayed — out of protocol v1", outOfScope)
	}
}

// anyViolation reports whether the document carries a finding from any check.
func anyViolation(doc protocolDoc) bool {
	for _, r := range doc.Results {
		if r.Verdict == "violation" {
			return true
		}
	}
	return false
}

func evidenceMentions(es []protocolEvidence, s string) bool {
	for _, e := range es {
		if e.Symbol == s || e.Keyword == s || e.Rule == s {
			return true
		}
		if e.Message != "" && strings.Contains(e.Message, s) {
			return true
		}
		for _, g := range [][]string{e.Referrers, e.Locations, e.Accepted, e.Suggestions} {
			for _, v := range g {
				if v == s {
					return true
				}
			}
		}
	}
	return false
}

// T8 — the wire format itself, byte for byte. Everything above asserts a
// property; this asserts the bytes, so an accidental field rename or a dropped
// key fails loudly instead of quietly breaking every consumer. The compatibility
// rule says additive changes are legal — which is exactly why a golden is needed:
// "legal" must still mean "deliberate", and a golden diff in review is what makes
// it one.
//
//	RUNECHO_GOLDEN_UPDATE=1 go test ./cmd/runecho-guard/ -run TestProtocolMode_Golden
func TestProtocolMode_Golden(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{"enrolled-clean", func(t *testing.T) string {
			repo := t.TempDir()
			gitInit(t, repo)
			enrolledStore(t, repo, []string{"KnownFunc"})
			return writeReq(t, filepath.Join(repo, "main.go"), "package main\n\nfunc x() { KnownFunc() }\n")
		}},
		{"enrolled-violation-with-suggestions", func(t *testing.T) string {
			repo := t.TempDir()
			gitInit(t, repo)
			enrolledStore(t, repo, []string{"ProcessData"})
			return writeReq(t, filepath.Join(repo, "main.go"), "package main\n\nfunc x() { ProcesData() }\n")
		}},
		{"unenrolled-everything-unknown", func(t *testing.T) string {
			repo := t.TempDir()
			gitInit(t, repo)
			other := t.TempDir()
			gitInit(t, other)
			enrolledStore(t, other, []string{"KnownFunc"})
			return writeReq(t, filepath.Join(repo, "main.go"), "package main\n\nfunc x() { Whatever() }\n")
		}},
		{"error-document", func(t *testing.T) string {
			return `{"protocol":2,"path":"/a/b.go","content":"x"}`
		}},
	}

	var got bytes.Buffer
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.setup(t)
			var out bytes.Buffer
			code := runProtocolMode(strings.NewReader(req), &out)
			// The path and the snapshot timestamp are the only machine-varying
			// parts; scrubbing them keeps the rest byte-exact.
			body := scrubTimestamps(scrubAll(strings.TrimSpace(out.String()),
				map[string]string{os.Getenv("RUNECHO_HOME"): "<HOME>"}))
			body = scrubPaths(body)
			fmt.Fprintf(&got, "### %s\nexit=%d\n%s\n\n", tc.name, code, body)
		})
	}

	goldenPath := filepath.Join("testdata", "protocol.golden")
	if os.Getenv("RUNECHO_GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(goldenPath, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("golden rewritten: %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (regenerate with RUNECHO_GOLDEN_UPDATE=1): %v", err)
	}
	if got.String() != string(want) {
		t.Errorf("the protocol wire format changed.\n%s", firstDiff(string(want), got.String()))
	}
}

var reTempPath = regexp.MustCompile(`"/tmp/[^"]*"`)
var reSnapshotAt = regexp.MustCompile(`"snapshot_at":"[^"]*"`)

func scrubPaths(s string) string { return reTempPath.ReplaceAllString(s, `"<PATH>"`) }
func scrubTimestamps(s string) string {
	return reSnapshotAt.ReplaceAllString(s, `"snapshot_at":"<TS>"`)
}
