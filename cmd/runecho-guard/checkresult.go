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

// storeQueryReason names the Unknown reason for a check whose store lookup
// failed (qErrs, the existing dangling/duplicate query-error count), or "" if
// it didn't.
func storeQueryReason(queryErrs int) string {
	if queryErrs > 0 {
		return "store-query-failed"
	}
	return ""
}

// countUnknown replaces the hand-rolled `degraded int` runHookMode used to
// accumulate from exactly three sources (an unreadable/oversized pre-edit
// file, a dangling-check store-query error, a duplicate-check store-query
// error). This counts a VerdictUnknown from ANY of the eleven checks — see the
// #330 PR description for why that widening is the point of the change, not
// a side effect of it.
func countUnknown(results []CheckResult) int {
	n := 0
	for _, r := range results {
		if r.Verdict == VerdictUnknown {
			n++
		}
	}
	return n
}
