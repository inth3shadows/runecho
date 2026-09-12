package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryCheckHasARemedy is the #267 regression that a twelfth check cannot
// slip past. Every name in checkOrder must land in exactly one bucket: it has an
// env gate (guardGates), or it is the additive check whose remedy is
// .runechoguardignore. A check in neither would fire, raise an ask, and offer
// the user nothing but RUNECHO_GUARD_SKIP=1 — which is the whole guard, not that
// check. A check in BOTH would be offered the ignore file it does not consume,
// which is the defect this file exists to fix.
func TestEveryCheckHasARemedy(t *testing.T) {
	for _, name := range checkOrder {
		_, gated := guardGates[name]
		additive := name == "violations"
		switch {
		case gated && additive:
			t.Errorf("check %q is both gated and the additive check — pick one remedy", name)
		case !gated && !additive:
			t.Errorf("check %q has no remedy: add its RUNECHO_GUARD_*=0 setting to guardGates, "+
				"or make it the additive check .runechoguardignore covers", name)
		}
	}
	// And nothing in guardGates that checkOrder does not know about — a stale
	// entry would name a gate for a check that can never fire.
	known := make(map[string]struct{}, len(checkOrder))
	for _, n := range checkOrder {
		known[n] = struct{}{}
	}
	for name := range guardGates {
		if _, ok := known[name]; !ok {
			t.Errorf("guardGates has %q, which is not in checkOrder", name)
		}
	}
}

// TestTrailers_AdditiveOnly_ByteIdentical pins the modal case to the exact text
// that has shipped since #243. The dogfood metric this guard is tuned by is a
// rate of approvals against this string; a silent reword makes before/after
// windows incomparable, so a change here must be deliberate.
func TestTrailers_AdditiveOnly_ByteIdentical(t *testing.T) {
	f := firedChecks{Violations: true}
	const wantAsk = "Approve if these are legitimate (new/local/dynamic, or an intended removal). Silence repeats via .runechoguardignore, or RUNECHO_GUARD_SKIP=1 to disable."
	if got := askTrailer(f); got != wantAsk {
		t.Errorf("askTrailer(additive only):\n got %q\nwant %q", got, wantAsk)
	}
	const wantPre = "Add false positives to .runechoguardignore, or bypass with RUNECHO_GUARD_SKIP=1."
	if got := precommitRemedyLine(f); got != wantPre {
		t.Errorf("precommitRemedyLine(additive only):\n got %q\nwant %q", got, wantPre)
	}
}

// TestNoIgnoreRemedyWithoutTheAdditiveCheck is #267's core assertion, run over
// every check rather than the one that prompted the issue: if the additive check
// did not fire, neither surface may mention .runechoguardignore, and each must
// name the gate that actually silences what DID fire.
//
// Enumerates firedChecks by REFLECTION (firedOnly/firedCheckFields, dangling_test.go)
// rather than ranging over guardGates. Two reasons, both learned from the helper's
// own doc comment: a hand-written list is a habit rather than a pin, and ranging
// over the map under test would make a gate DELETED from guardGates silently drop
// its subtest instead of failing one. firedNames is the bridge from the Go field
// name to the check name — the same production mapping the trailer uses, so no
// third list exists to drift.
func TestNoIgnoreRemedyWithoutTheAdditiveCheck(t *testing.T) {
	for _, field := range firedCheckFields(t) {
		f := firedOnly(t, field)
		names := f.firedNames()
		if len(names) != 1 {
			t.Fatalf("firedOnly(%s) produced %d check names, want exactly 1 — "+
				"firedNames and firedChecks have drifted", field, len(names))
		}
		name := names[0]
		t.Run(name, func(t *testing.T) {
			gate, gated := guardGates[name]
			for surface, got := range map[string]string{
				"ask":       askTrailer(f),
				"precommit": precommitRemedyLine(f),
			} {
				if !strings.Contains(got, "RUNECHO_GUARD_SKIP=1") {
					t.Errorf("%s: dropped the unconditional RUNECHO_GUARD_SKIP=1 remedy: %q", surface, got)
				}
				if !gated {
					// The additive check: the ignore file is its real remedy.
					if !strings.Contains(got, ".runechoguardignore") {
						t.Errorf("%s: %s is the one check .runechoguardignore reaches, but it is not offered: %q", surface, name, got)
					}
					continue
				}
				if strings.Contains(got, ".runechoguardignore") {
					t.Errorf("%s: offers .runechoguardignore for a %s-only finding, which it cannot silence: %q", surface, name, got)
				}
				if !strings.Contains(got, gate) {
					t.Errorf("%s: does not name %s, the only remedy for a %s finding: %q", surface, gate, name, got)
				}
			}
		})
	}
}

// gateFuncs binds each gated check to the predicate its RUNECHO_GUARD_* variable
// controls. This is what makes guardGates CHECKABLE rather than merely asserted:
// a test that reads a gate string out of guardGates and looks for it in the
// trailer is tautological — it pins the plumbing and says nothing about whether
// the variable named is the one that check reads, at the polarity that turns it
// off. Adversarial review proved that gap live: RUNECHO_GUARD_QUALIFIED=0
// inverted to =1, and three env-var names misspelled, all passed the suite.
//
// Kept here rather than beside guardGates because it is the TEST's independent
// second opinion. Deriving both from one table would restate the claim instead
// of checking it.
var gateFuncs = map[string]func() bool{
	"file-scope":       fileScopeEnabled,
	"qualified":        qualifiedEnabled,
	"deps-go":          depQualifiedGoEnabled,
	"dangling":         danglingEnabled,
	"dropped-import":   droppedImportEnabled,
	"duplicate-symbol": duplicateEnabled,
	"call-shape":       callShapeEnabled,
	"recv-method":      recvMethodEnabled,
	"var-type":         varTypeEnabled,
	"lint":             lintEnabled,
}

// TestGuardGatesActuallyDisableTheirCheck runs each advertised remedy and watches
// the check's own predicate.
//
// Both halves are needed, and they catch different mutations. Applying the
// advertised setting must turn the check OFF — that catches a polarity inversion
// on a default-on check like qualified, where "=1" is the default-ON posture and
// a user following it would watch the check keep firing. Setting the SAME
// variable to "1" must turn it back ON — that catches a misspelled variable name,
// which for a default-off check would leave the predicate at false and make the
// first half pass vacuously.
func TestGuardGatesActuallyDisableTheirCheck(t *testing.T) {
	for name, gate := range guardGates {
		t.Run(name, func(t *testing.T) {
			enabled, ok := gateFuncs[name]
			if !ok {
				t.Fatalf("no predicate bound for %q — add it to gateFuncs so its gate is checkable", name)
			}
			k, v, found := strings.Cut(gate, "=")
			if !found {
				t.Fatalf("guardGates[%q] = %q is not a KEY=VALUE setting", name, gate)
			}
			t.Setenv(k, v)
			if enabled() {
				t.Errorf("%s: the advertised remedy %q does not disable the check — "+
					"a user who follows it watches the check keep firing", name, gate)
			}
			t.Setenv(k, "1")
			if !enabled() {
				t.Errorf("%s: %q is not the variable this check reads — setting it to 1 "+
					"does not enable the check, so the advertised remedy names the wrong knob", name, k)
			}
		})
	}
}

// TestTrailers_AdditivePlusGated names both remedies and says which is which —
// the combination where an unqualified "silence repeats via .runechoguardignore"
// is half true and therefore most misleading.
func TestTrailers_AdditivePlusGated(t *testing.T) {
	f := firedChecks{Violations: true, CallShape: true}
	for surface, got := range map[string]string{
		"ask":       askTrailer(f),
		"precommit": precommitRemedyLine(f),
	} {
		if !strings.Contains(got, ".runechoguardignore (the unresolved-symbol check only)") {
			t.Errorf("%s: does not scope the ignore file to the check that consumes it: %q", surface, got)
		}
		if !strings.Contains(got, "RUNECHO_GUARD_CALLSHAPE=0 disables that check") {
			t.Errorf("%s: does not name the call-shape gate: %q", surface, got)
		}
	}
}

// TestGateClause_AgreesInNumber keeps the line reading as prose. A generated
// list that says "disables those checks" for one check is the kind of tell that
// makes a user trust the rest of the message less.
func TestGateClause_AgreesInNumber(t *testing.T) {
	if got := gateClause([]string{"A=0"}); got != "A=0 disables that check" {
		t.Errorf("singular: got %q", got)
	}
	if got := gateClause([]string{"A=0", "B=0"}); got != "A=0 / B=0 disable those checks" {
		t.Errorf("plural: got %q", got)
	}
}

// TestFiredGates_FollowsCheckOrder pins the remedy list to the same canonical
// order the log reason uses, so an ask and its decisions.jsonl record read in
// the same sequence.
func TestFiredGates_FollowsCheckOrder(t *testing.T) {
	f := firedChecks{Lint: true, FileScope: true, Dangling: true}
	want := []string{"RUNECHO_GUARD_FILESCOPE=0", "RUNECHO_GUARD_DANGLING=0", "RUNECHO_GUARD_LINT=0"}
	got := firedGates(f)
	if len(got) != len(want) {
		t.Fatalf("firedGates: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("firedGates: got %v, want %v", got, want)
		}
	}
}

// TestFiredNames_StillBuildsAskReason guards the extraction that made one list
// serve both callers: #330 freezes decisionRecord.Reason's byte format, so
// askReason must remain exactly the "+"-join of firedNames, including the
// all-eleven case and the empty-input fallback.
func TestFiredNames_StillBuildsAskReason(t *testing.T) {
	all := firedChecks{
		Violations: true, FileScope: true, Qualified: true, DepsGo: true,
		Dangling: true, Dropped: true, Duplicate: true, CallShape: true,
		RecvMethod: true, VarType: true, Lint: true,
	}
	if got, want := askReason(all), strings.Join(checkOrder, "+"); got != want {
		t.Errorf("askReason(all fired):\n got %q\nwant %q", got, want)
	}
	if got, want := strings.Join(all.firedNames(), "+"), strings.Join(checkOrder, "+"); got != want {
		t.Errorf("firedNames is not checkOrder when everything fires:\n got %q\nwant %q", got, want)
	}
	if got := askReason(firedChecks{}); got != "violations" {
		t.Errorf("askReason(nothing fired) = %q, want the %q fallback", got, "violations")
	}
	if n := len(firedChecks{}.firedNames()); n != 0 {
		t.Errorf("firedNames(nothing fired) returned %d names, want 0", n)
	}
}

// TestDangling_AskDoesNotOfferTheIgnoreFile is #267's end-to-end assertion, and
// the only one here that proves the WIRING rather than the builder. The unit
// tests above would all still pass if renderHookDecision kept writing the old
// fixed string, so this drives the real hook — an enrolled store, a dangling-only
// finding — and reads the trailer off the emitted PreToolUse payload.
//
// Dangling is the check #267's failure scenario is written against: it needs no
// third-party binary (unlike lint), fires from a plain Edit, and is one of the
// five that .runechoguardignore never reached.
func TestDangling_AskDoesNotOfferTheIgnoreFile(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")

	file := filepath.Join(top, "known.go")
	in := payloadOld(t, "Edit", file, "func DoThing() {}", "", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("want ask, got %q", d.Hook.PermissionDec)
	}
	reason := d.Hook.PermissionReason
	if strings.Contains(reason, ".runechoguardignore") {
		t.Errorf("a dangling-only ask still offers .runechoguardignore, which guard.Run alone consumes:\n%s", reason)
	}
	if !strings.Contains(reason, "RUNECHO_GUARD_DANGLING=0 disables that check") {
		t.Errorf("a dangling-only ask does not name the gate that silences it:\n%s", reason)
	}
	if !strings.Contains(reason, "RUNECHO_GUARD_SKIP=1") {
		t.Errorf("a dangling-only ask dropped the unconditional remedy:\n%s", reason)
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what it
// wrote. runPreCommit reports to os.Stderr directly rather than to an injected
// writer, so there is no seam to pass a buffer through.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	os.Stderr = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// TestPreCommit_FileScopeReportDoesNotOfferTheIgnoreFile is the pre-commit twin
// of TestDangling_AskDoesNotOfferTheIgnoreFile, and exists for the same reason:
// every other assertion on this surface calls precommitRemedyLine DIRECTLY, so
// all of them would still pass if main.go kept writing its old fixed string.
// Adversarial review proved that — reverting that one line left the whole
// package green. This drives the real runPreCommit and reads its stderr.
//
// file-scope is the check chosen because it is one of only four that can fire on
// this surface at all (runPreCommit appends violations, qualified, deps-go and
// file-scope), it needs no Go module or third-party binary, and it is one of the
// three of those four that .runechoguardignore never reached.
func TestPreCommit_FileScopeReportDoesNotOfferTheIgnoreFile(t *testing.T) {
	_, _, wt := bareWorktrees(t)
	db := storeAt(t)
	enrollWithSnapshot(t, db, "container", wt, "render")
	t.Setenv("RUNECHO_GUARD_FILESCOPE", "1")

	// `render` IS in the repo index, so the additive check resolves it and stays
	// silent — file-scope is the only check that fires, which is what makes the
	// trailer's claim testable in isolation.
	stage(t, wt, "app.py", "def caller():\n    return render(1)\n")

	t.Chdir(wt)
	var code int
	out := captureStderr(t, func() { code = runPreCommit(false, false) })

	if code != 1 {
		t.Fatalf("runPreCommit = %d, want 1 — file-scope did not fire, so this test "+
			"proves nothing about the trailer.\nstderr:\n%s", code, out)
	}
	if !strings.Contains(out, "render") {
		t.Fatalf("report does not name the out-of-scope symbol; wrong check fired:\n%s", out)
	}
	if strings.Contains(out, ".runechoguardignore") {
		t.Errorf("a file-scope-only pre-commit block still offers .runechoguardignore, "+
			"which fileScopeViolations never consults:\n%s", out)
	}
	if !strings.Contains(out, "RUNECHO_GUARD_FILESCOPE=0") {
		t.Errorf("the report does not name the gate that actually silences file-scope:\n%s", out)
	}
}
