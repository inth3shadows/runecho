package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordsFor returns the logged records for file with the given decision.
func recordsFor(t *testing.T, home, file, decision string) []decisionRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "decisions.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []decisionRecord
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var rec decisionRecord
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Decision == decision && rec.File == file {
			out = append(out, rec)
		}
	}
	return out
}

// editOutcome sends a PostToolUse for the edit oldStr -> newStr.
func editOutcome(t *testing.T, file, session, oldStr, newStr string) {
	t.Helper()
	payload := `{"tool_name":"Edit","session_id":"` + session + `","tool_input":{"file_path":"` + file + `","old_string":"` + oldStr + `","new_string":"` + newStr + `"}}`
	if code := runOutcomeMode(strings.NewReader(payload)); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// deferAs logs the record a PreToolUse writes when it lets an edit to file
// through without asking.
func deferAs(t *testing.T, session, file string) {
	t.Helper()
	setDecisionSession(session)
	logDecision(decisionRecord{Mode: "hook", Repo: "r", File: file, Lang: "go", Decision: "defer", Reason: "clean"})
}

func unjoinedEnv(t *testing.T) string {
	t.Helper()
	isolateDecisionSession(t)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_DEBUG", "")
	t.Setenv("RUNECHO_GUARD_LEARN", "1")
	return home
}

// #464: an outcome whose fingerprint differs from a pending ask's joins nothing
// (#461), and must leave a trace so that case can be counted. The trace carries
// no symbols and trains nothing.
func TestRunOutcomeMode_UnjoinedTraceForDifferingFingerprint(t *testing.T) {
	home := unjoinedEnv(t)
	const file = "/some/repo/main.go"
	askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
	otherOutcome(t, file, "session-a")

	got := recordsFor(t, home, file, "unjoined")
	if len(got) != 1 {
		t.Fatalf("unjoined records = %d, want 1", len(got))
	}
	rec := got[0]
	want := editFingerprint(hookEdit{ToolName: "Edit", OldString: "m", NewString: "n"})
	if rec.Mode != "outcome" || rec.Reason != "fingerprint-mismatch" || rec.Edit != want || rec.Repo != "r" || rec.Lang != "go" {
		t.Errorf("trace = %+v, want mode outcome, reason fingerprint-mismatch, edit %s, repo r, lang go", rec, want)
	}
	if rec.AskEdit != xyFingerprint() {
		t.Errorf("ask_edit = %q, want the passed-over ask's fingerprint %q", rec.AskEdit, xyFingerprint())
	}
	if rec.Session != contractSessionTag("session-a") {
		t.Errorf("session = %q, want the PostToolUse session's tag", rec.Session)
	}
	if len(rec.Symbols) != 0 || len(rec.LearnSymbols) != 0 || len(rec.ClaimSymbols) != 0 {
		t.Errorf("trace carries symbols: %+v", rec)
	}
	if n := outcomesFor(t, home, file); n != 0 {
		t.Errorf("outcomes = %d, want 0", n)
	}
	if n := loadLearnedAllow(home).Repos["r"]["Ghost"].Count; n != 0 {
		t.Errorf("learned-allow count = %d, want 0", n)
	}
}

// One trace per passed-over ask. The PostToolUse hook can be wired more than
// once, and every wiring fires; and an unanswered ask can be followed by any
// number of different edits inside the window. Neither adds a record. A new
// ask does: it is a new thing to account for.
func TestRunOutcomeMode_UnjoinedTraceOncePerAsk(t *testing.T) {
	home := unjoinedEnv(t)
	const file = "/some/repo/main.go"
	askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
	for i := 0; i < 3; i++ {
		otherOutcome(t, file, "session-a")
	}
	if n := len(recordsFor(t, home, file, "unjoined")); n != 1 {
		t.Fatalf("after three fires of one edit: unjoined records = %d, want 1", n)
	}
	editOutcome(t, file, "session-a", "p", "q")
	if n := len(recordsFor(t, home, file, "unjoined")); n != 1 {
		t.Fatalf("after a second, different edit: unjoined records = %d, want 1", n)
	}
	askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
	otherOutcome(t, file, "session-a")
	if n := len(recordsFor(t, home, file, "unjoined")); n != 2 {
		t.Errorf("after the ask was raised again: unjoined records = %d, want 2", n)
	}
}

// An outcome that carries a different fingerprint answered a different ask,
// here another session's, and says nothing about this one. If it closed the
// passed-over ask, a real mismatch on this ask would leave no trace whenever
// anything else on the file was approved first.
func TestRunOutcomeMode_UnjoinedTraceSurvivesAnotherAsksOutcome(t *testing.T) {
	home := unjoinedEnv(t)
	const file = "/some/repo/main.go"
	pq := editFingerprint(hookEdit{ToolName: "Edit", OldString: "p", NewString: "q"})
	askWithEdit(t, "session-a", file, xyFingerprint(), "GhostA")
	askWithEdit(t, "session-b", file, pq, "GhostB")
	editOutcome(t, file, "session-b", "p", "q")
	if out := recordsFor(t, home, file, "outcome"); len(out) != 1 || out[0].Join != "edit" {
		t.Fatalf("setup: session-b's own edit should join its ask by fingerprint, got %+v", out)
	}

	otherOutcome(t, file, "session-a")
	got := recordsFor(t, home, file, "unjoined")
	if len(got) != 1 || got[0].AskEdit != xyFingerprint() {
		t.Errorf("unjoined records = %+v, want one naming session-a's ask", got)
	}
}

// Concurrent fires of one PostToolUse must leave one trace, like the outcome
// dedupe they share a lock with. Repeated rounds because the window is small;
// see TestLogOutcomeForFile_ConcurrentFiresWriteOnce.
func TestLogOutcomeForFile_ConcurrentFiresWriteOneUnjoinedTrace(t *testing.T) {
	isolateDecisionSession(t)
	t.Setenv("RUNECHO_DEBUG", "")
	const rounds = 40
	for round := 0; round < rounds; round++ {
		home := t.TempDir()
		t.Setenv("RUNECHO_HOME", home)
		const file = "/some/repo/race.go"
		setDecisionSession("sess")
		logDecision(decisionRecord{Mode: "hook", Repo: "r", File: file, Lang: "go", Decision: "ask", Reason: "violations", Symbols: []string{"Foo"}, Edit: "e1"})

		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				logOutcomeForFile(file, "e2", "sess", "")
			}()
		}
		close(start)
		wg.Wait()

		if n := len(recordsFor(t, home, file, "unjoined")); n != 1 {
			t.Fatalf("round %d: unjoined records = %d, want 1", round, n)
		}
	}
}

// A trace is written only for an ask passed over because the fingerprints
// differ. Each case here is a neighbouring situation that must stay silent.
func TestRunOutcomeMode_NoUnjoinedTrace(t *testing.T) {
	const file = "/some/repo/main.go"
	for name, setup := range map[string]func(t *testing.T){
		"no ask on the file": func(t *testing.T) {
			otherOutcome(t, file, "session-a")
		},
		"the ask already has an outcome": func(t *testing.T) {
			askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
			windowOutcome(t, file, "session-a")
			otherOutcome(t, file, "session-a")
		},
		"the ask has no fingerprint": func(t *testing.T) {
			askAs(t, "session-a", file, "Ghost")
			otherOutcome(t, file, "session-a")
		},
		"the outcome has no fingerprint": func(t *testing.T) {
			askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
			noFingerprintOutcome(t, file, "session-a")
		},
		"the ask is another session's": func(t *testing.T) {
			askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
			otherOutcome(t, file, "session-b")
		},
		"the ask is older than the window": func(t *testing.T) {
			setDecisionSession("session-a")
			logDecision(decisionRecord{
				TS:   time.Now().Add(-2 * maxOutcomeAge).UTC().Format(time.RFC3339),
				Mode: "hook", Repo: "r", File: file, Lang: "go", Decision: "ask",
				Reason: "violations", Symbols: []string{"Ghost"}, Edit: xyFingerprint(),
			})
			otherOutcome(t, file, "session-a")
		},
		"the fingerprints match": func(t *testing.T) {
			askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
			windowOutcome(t, file, "session-a")
		},
		"the edit had a PreToolUse of its own (a later defer on the file)": func(t *testing.T) {
			askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
			deferAs(t, "session-a", file)
			otherOutcome(t, file, "session-a")
		},
		"a later ask with no fingerprint superseded it": func(t *testing.T) {
			askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
			askAs(t, "session-a", file, "Legacy")
			otherOutcome(t, file, "session-a")          // window-joins the later ask
			editOutcome(t, file, "session-a", "p", "q") // a further, different edit
		},
		"an outcome with no fingerprint already answered the ask": func(t *testing.T) {
			askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
			noFingerprintOutcome(t, file, "session-a")
			otherOutcome(t, file, "session-a")
		},
		"the outcome's fingerprint already has an outcome of its own": func(t *testing.T) {
			mn := editFingerprint(hookEdit{ToolName: "Edit", OldString: "m", NewString: "n"})
			askWithEdit(t, "session-a", file, mn, "Earlier")
			otherOutcome(t, file, "session-a") // joins that ask by fingerprint
			askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
			otherOutcome(t, file, "session-a") // the identical edit again
		},
	} {
		t.Run(name, func(t *testing.T) {
			home := unjoinedEnv(t)
			setup(t)
			if got := recordsFor(t, home, file, "unjoined"); len(got) != 0 {
				t.Errorf("unjoined records = %d, want 0: %+v", len(got), got)
			}
		})
	}
}

// The trace must not change what later fires conclude: the asked edit still
// joins its ask by fingerprint, exactly once.
func TestRunOutcomeMode_UnjoinedTraceDoesNotAffectLaterJoin(t *testing.T) {
	home := unjoinedEnv(t)
	const file = "/some/repo/main.go"
	askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
	otherOutcome(t, file, "session-a")
	windowOutcome(t, file, "session-a")

	out := recordsFor(t, home, file, "outcome")
	if len(out) != 1 || out[0].Join != "edit" {
		t.Fatalf("outcomes = %+v, want one edit-joined outcome", out)
	}
	if n := loadLearnedAllow(home).Repos["r"]["Ghost"].Count; n != 1 {
		t.Errorf("learned-allow count = %d, want 1", n)
	}
	if n := len(recordsFor(t, home, file, "unjoined")); n != 1 {
		t.Errorf("unjoined records = %d, want 1", n)
	}
}

// contractAskStillStands reads a later hook-mode record on the file as having
// answered the ask, which drops the #209 memo. The trace is written in
// outcome mode so that it is not such a record: a contract ask that is passed
// over once and then approved by its own edit still records its memo.
func TestContract_UnjoinedTraceDoesNotAnswerTheAsk(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_CONTRACT_ONCE", "")
	home := unjoinedEnv(t)
	const file = "/r/internal/a.go"
	setDecisionSession("sess")
	logDecision(decisionRecord{Mode: "hook", Repo: "r", File: file, Decision: "ask", Reason: "contract", Contract: "scope", ContractHash: "abcdefabcdef", ContractSession: contractSessionTag("sess"), Edit: "e1"})

	logOutcomeForFile(file, "e2", "sess", "acceptEdits")
	if n := len(recordsFor(t, home, file, "unjoined")); n != 1 {
		t.Fatalf("precondition: unjoined records = %d, want 1", n)
	}

	logOutcomeForFile(file, "e1", "sess", "acceptEdits")
	if !contractApproved(home, "sess", "abcdefabcdef", file, time.Now()) {
		t.Errorf("the memo was not written: the trace was read as answering the ask")
	}
}

// Another session's PreToolUse on the file says nothing about this session's
// tool calls, so it must not close this session's passed-over ask.
func TestRunOutcomeMode_UnjoinedTraceSurvivesAnotherSessionsDefer(t *testing.T) {
	home := unjoinedEnv(t)
	const file = "/some/repo/main.go"
	askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
	deferAs(t, "session-b", file)
	otherOutcome(t, file, "session-a")
	if got := recordsFor(t, home, file, "unjoined"); len(got) != 1 || got[0].AskEdit != xyFingerprint() {
		t.Errorf("unjoined records = %+v, want one naming session-a's ask", got)
	}
}

// A repeat fire of a PostToolUse whose first fire joined by WINDOW finds its
// own outcome already in the log. That is not a mismatch: no trace, whatever
// fingerprinted ask sits in the window. Trace or no trace must not depend on
// how many times the hook is wired.
func TestRunOutcomeMode_NoUnjoinedTraceOnRepeatFireOfWindowJoin(t *testing.T) {
	home := unjoinedEnv(t)
	const file = "/some/repo/main.go"
	askAs(t, "session-a", file, "Legacy")
	askWithEdit(t, "session-a", file, xyFingerprint(), "Ghost")
	otherOutcome(t, file, "session-a")
	otherOutcome(t, file, "session-a")

	if out := recordsFor(t, home, file, "outcome"); len(out) != 1 || out[0].Join != "window" {
		t.Fatalf("outcomes = %+v, want one window-joined outcome", out)
	}
	if got := recordsFor(t, home, file, "unjoined"); len(got) != 0 {
		t.Errorf("unjoined records = %+v, want none", got)
	}
}
