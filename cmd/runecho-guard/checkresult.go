package main

// Verdict is one check's in-process epistemic status for one edit: not just
// "did it fire" (firedChecks, dangling.go) but "could it even answer".
//
// A separate type from guardstats.AuditVerdict (internal/guardstats/fpaudit.go)
// on purpose, despite the naming overlap being deliberate (#330): that one
// classifies a flagged symbol POST-HOC against git history — did the guard's
// past finding turn out to be a false positive, a premature-but-correct one, or
// does it still stand. This one classifies a check's outcome IN-PROCESS, at the
// moment the hook decides. Different questions, different packages —
// guardstats already reads decisions.jsonl, which this package writes, so
// importing it here for a four-constant enum would risk backwards layering.
type Verdict uint8

const (
	VerdictOK        Verdict = iota // check ran, found nothing
	VerdictViolation                // check ran, found something
	VerdictUnknown                  // check could not answer
	VerdictSkipped                  // gate off / wrong language / not applicable
)

// CheckResult is one check's verdict for one edit. Check is one of
// checkOrder's eleven names — the same vocabulary askReason already speaks.
//
// Deliberately does NOT carry the check's own findings (contrast the sketch in
// #330's issue body, which shows a `Violations []guard.Violation` field). Four
// of the eleven checks — dangling, dropped-import, duplicate, call-shape — already
// carry richer, non-guard.Violation-shaped finding types (danglingWarning,
// guard.DroppedImport, duplicateWarning, guard.CallShapeMismatch) into the
// existing ask-rendering code in runHookMode/runArgs, and forcing them into a
// shared field would either lose data or widen guard.Violation itself.
// CheckResult answers "could this check answer", not "what did it find" —
// findings keep flowing through their existing typed variables exactly as
// before this change.
type CheckResult struct {
	Check   string
	Verdict Verdict
	// Reason names why the verdict is Unknown or Skipped — e.g.
	// "oversized-pre-edit-file", "no-module-path", "store-query-failed", or one
	// of GoDepQualifiedViolationsWithReason's / FileScopeViolationsWithReason's
	// captured abstain reasons. Empty for OK and Violation.
	Reason string
}

// checkOrder is askReason's existing iteration order (dangling.go's askReason),
// unchanged by this file — see firedChecksFrom's doc for why that matters.
var checkOrder = []string{
	"violations", "file-scope", "qualified", "deps-go", "dangling",
	"dropped-import", "duplicate-symbol", "call-shape", "recv-method", "var-type",
	"lint",
}

// firedChecksFrom projects a fully-populated (one entry per checkOrder name)
// results slice onto the existing firedChecks type, so callers can keep using
// the untouched askReason(firedChecks)/anyNonViolation() exactly as before.
//
// askReason and firedChecks.anyNonViolation are NOT reimplemented against
// []CheckResult: the #330 issue freezes decisionRecord.Reason's byte format,
// and reimplementing the string-building logic against a new input type is
// exactly the kind of change that could drift a rarely-exercised combination
// without anyone noticing. Routing through the untouched function instead
// makes byte-identical output close to axiomatic — the only new logic that
// needs verifying is this mapping (Verdict == VerdictViolation → the matching
// bool field), a much smaller surface than askReason's own string-joining.
//
// A result whose Check name is not one of checkOrder's eleven is ignored rather
// than erroring: this stays a pure projection, and an unrecognized name is a
// caller bug that unit tests (not a panic on every hook invocation) should
// catch.
func firedChecksFrom(results []CheckResult) firedChecks {
	var f firedChecks
	for _, r := range results {
		if r.Verdict != VerdictViolation {
			continue
		}
		switch r.Check {
		case "violations":
			f.Violations = true
		case "file-scope":
			f.FileScope = true
		case "qualified":
			f.Qualified = true
		case "deps-go":
			f.DepsGo = true
		case "dangling":
			f.Dangling = true
		case "dropped-import":
			f.Dropped = true
		case "duplicate-symbol":
			f.Duplicate = true
		case "call-shape":
			f.CallShape = true
		case "recv-method":
			f.RecvMethod = true
		case "var-type":
			f.VarType = true
		case "lint":
			f.Lint = true
		}
	}
	return f
}

// classifyResult builds a CheckResult from whether the check found something
// and, if not, why it couldn't be sure (empty reason = it ran to completion).
//
// Violation takes precedence over Unknown: if a check finds a real violation
// AND separately abstained on a different candidate in the same edit, the
// violation wins — it's an actionable finding, and once something's already
// asking, "did we check everything" isn't the operative question anymore
// (matches the issue's own framing: "found nothing is not the same as
// checked everything" — the ambiguity that framing describes is specifically
// about the nothing-found case).
func classifyResult(check string, found bool, reason string) CheckResult {
	switch {
	case found:
		return CheckResult{Check: check, Verdict: VerdictViolation}
	case reason != "":
		return CheckResult{Check: check, Verdict: VerdictUnknown, Reason: reason}
	default:
		return CheckResult{Check: check, Verdict: VerdictOK}
	}
}

// String renders a Verdict as the lowercase token decisionRecord.Checks and
// fpreport (#333) key on — "ok"/"violation"/"unknown"/"skipped". A value
// outside the four known constants (impossible from classifyResult, but not
// from a hand-built CheckResult in a test) renders as "unknown" rather than
// panicking or silently keying an empty-string bucket.
func (v Verdict) String() string {
	switch v {
	case VerdictOK:
		return "ok"
	case VerdictViolation:
		return "violation"
	case VerdictSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// checkStatusMap projects results onto check name -> Verdict.String(), for
// decisionRecord.Checks (#333). Persisting this is what lets fpreport tell
// "the check ran and found nothing" apart from "the check never ran" — before
// this, only a Violation verdict left any trace in decisions.jsonl; OK,
// Unknown and Skipped were computed in-process (firedChecksFrom, and the
// degraded counter)
// and then discarded once the hook returned. A results slice missing a
// checkOrder name simply leaves that check absent from the map rather than
// backfilling a fake Skipped — this is NOT a hypothetical: the pre-commit path
// (runArgs) only ever appends 4 of the 11 checkOrder names (violations,
// qualified, deps-go, file-scope), so every pre-commit-mode Checks map is
// missing the other 7 by construction, not by bug. A caller must treat a
// missing key as "not reported by this surface," never as Skipped.
func checkStatusMap(results []CheckResult) map[string]string {
	if len(results) == 0 {
		return nil
	}
	m := make(map[string]string, len(results))
	for _, r := range results {
		m[r.Check] = r.Verdict.String()
	}
	return m
}

// storeQueryReason names the Unknown reason for a check whose store lookup
// failed (qErrs, the existing dangling/duplicate query-error count), or "" if
// it didn't.
func storeQueryReason(queryErrs int) string {
	if queryErrs > 0 {
		return "store-query-failed"
	}
	return ""
}

// gateAbstainReasons are the #359 reasons that mean "a candidate existed and
// this check declined to judge it", as opposed to "the input or environment was
// missing". Both classes record VerdictUnknown in decisions.jsonl — the
// measurement #359 exists for needs every abstention visible — but only the
// degraded class feeds the strict-mode "coverage was incomplete" advisory
// (countDegradedUnknown below).
//
// Why the split: the gate reasons here are expected to be the MODAL outcome for
// var-type and recv-method on real Go edits (gate 5 fires on any method name the
// repo uses anywhere else), and an advisory that appears on nearly every edit is
// the noise class this guard's whole design avoids. The degraded reasons are
// rare and each one really does mean coverage was lost for this edit.
//
// Membership is opt-in: an unregistered reason counts as degraded. Every reason
// that predates #359 (oversized-pre-edit-file, store-query-failed, star-import,
// dynamic-binding, and depindex's go.work/module-cache reasons) is degraded, so
// the default preserves the pre-#359 advisory exactly, and a new gate reason
// that forgets to register itself fails LOUDLY (an advisory that fires too
// often) rather than silently dropping coverage from the count.
var gateAbstainReasons = map[string]struct{}{
	// qualified
	"shadowed-qualifier":  {},
	"unexported-selector": {},
	// recv-method
	"ambiguous-receiver": {},
	// var-type
	"ambiguous-local": {},
	// recv-method + var-type
	"name-known-elsewhere": {},
	// recv-method + var-type gate 4. Registered as a gate after review, against
	// an earlier reading that called it degraded because the index is what is
	// missing. The deciding question is not "what is absent" but "does it fire
	// on an ordinary edit": an edit that adds a type AND a method and calls a
	// sibling method has no indexed member set for that type — the known set is
	// built from the PRE-edit file — so gate 4 fires every time a new type is
	// written, which is exactly the advisory noise this class exists to keep
	// out. recvmethod.go and vartype.go both call it one of the check's own
	// gates, so this also removes a code/doc contradiction.
	"no-indexed-members": {},
	// call-shape
	"unreliable-call-shape":       {},
	"shadowed-callee":             {},
	"nested-def-shadow":           {},
	"ambiguous-decl-shapes":       {},
	"unusable-decl-shape":         {},
	"unparseable-added-signature": {},
	"decl-edited-in-hunk":         {},
}

// countDegradedUnknown counts the DEGRADED-class Unknowns in results — every
// Unknown whose reason is not registered in gateAbstainReasons above. This is
// what the strict-mode "coverage was incomplete" advisory counts, so a check
// that declined a candidate on its own precision gate stays out of the
// user-visible advisory while still being recorded in decisions.jsonl (#359).
//
// It replaced a plain count of every Unknown (#330's countUnknown, itself the
// replacement for a hand-rolled `degraded int`), which in turn had replaced a
// counter fed by exactly three sources: an unreadable/oversized pre-edit file
// and the two store-query errors. Each step widened WHAT is recorded while
// narrowing what the user is told — recording is checkStatusMap's job now.
//
// An Unknown with an EMPTY reason counts as degraded: classifyResult cannot
// produce one (a non-empty reason is what makes a verdict Unknown), so it can
// only come from a hand-built CheckResult, and the cases that build one by hand
// — the oversized-pre-edit-file arms in runHookMode — are degraded by nature.
func countDegradedUnknown(results []CheckResult) int {
	n := 0
	for _, r := range results {
		if r.Verdict != VerdictUnknown {
			continue
		}
		if _, gate := gateAbstainReasons[r.Reason]; gate {
			continue
		}
		n++
	}
	return n
}

// foldAbstainReason combines a check's OWN abstain reason with the shared
// degraded pre-edit-context reason runHookMode computes once per edit (#359).
//
// The degraded reason wins when both apply, and the direction matters: a gate
// reason names one candidate this check declined, while a degraded one says the
// surrounding context was never available — the larger fact, and the only class
// the strict advisory counts (countDegradedUnknown). Letting a gate reason mask
// it would silence the advisory on exactly the edits where coverage really was
// lost.
func foldAbstainReason(own, degraded string) string {
	if degraded != "" {
		return degraded
	}
	return own
}

// checkReasonMap projects results onto check name -> abstain reason, for
// decisionRecord.CheckReasons (#359). Only checks that actually carry a reason
// appear: an OK/Violation verdict never has one, and a Skipped or Unknown
// verdict without a recorded reason contributes nothing to read back. A nil
// return (no reasons at all) keeps the field out of the JSON entirely, which is
// the common case.
//
// Kept as a SEPARATE map from checkStatusMap rather than encoding
// "unknown:reason" into the verdict token: fpreport's CheckRuns keys on that
// token, and widening it would silently split every existing tally bucket.
func checkReasonMap(results []CheckResult) map[string]string {
	var m map[string]string
	for _, r := range results {
		if r.Reason == "" {
			continue
		}
		if m == nil {
			m = make(map[string]string, len(results))
		}
		m[r.Check] = r.Reason
	}
	return m
}
