package main

import "strings"

// remedy.go — naming only the escape hatches that can actually work (#267).
//
// The full ask and the pre-commit report each closed with a fixed trailer
// offering `.runechoguardignore`. That file is consumed by exactly one place —
// guard.Run (internal/guard/validate.go), the additive unresolved-symbol check —
// and reaches none of the other ten checks. Every other finding (file-scope,
// qualified, deps-go, dangling, dropped-import, duplicate-symbol, call-shape,
// recv-method, var-type, lint) passed straight through it, so a user whose ask
// was raised by one of those was told to edit a file that would not change the
// answer.
//
// A remedy that cannot work is worse than one fewer remedy: the user spends the
// trust once, nothing changes, and the next ask reads as the guard being broken
// rather than as a finding. The store-free ask already made this argument for
// itself in #266 (askWithoutIndexTrailer, callshape.go); this is the same fix on
// the two surfaces that PR deliberately left alone.
//
// Both builders below name the SAME remedies for a given firedChecks, and both
// reproduce their long-shipped string BYTE-IDENTICALLY in the additive-only
// case — by far the modal one. That is not politeness: it is the text approving
// users have been reading, and a silent reword would make before/after dogfood
// transcripts incomparable for the check whose rate is actually being measured.

// guardGates maps a check name — one of checkOrder's eleven — to the exact
// environment setting that silences that check and nothing else.
//
// "violations" (the additive hallucination check) is deliberately ABSENT: it is
// the guard's unconditional core and has no gate of its own. Its remedy is
// .runechoguardignore, which both builders below name separately. That absence
// is load-bearing, not an oversight, and TestEveryCheckHasARemedy pins it —
// see that test for why a twelfth check must land in one bucket or the other.
var guardGates = map[string]string{
	"file-scope":       "RUNECHO_GUARD_FILESCOPE=0",
	"qualified":        "RUNECHO_GUARD_QUALIFIED=0",
	"deps-go":          "RUNECHO_GUARD_DEPS_GO=0",
	"dangling":         "RUNECHO_GUARD_DANGLING=0",
	"dropped-import":   "RUNECHO_GUARD_DROPPED_IMPORT=0",
	"duplicate-symbol": "RUNECHO_GUARD_DUPLICATE=0",
	"call-shape":       "RUNECHO_GUARD_CALLSHAPE=0",
	"recv-method":      "RUNECHO_GUARD_RECVMETHOD=0",
	"var-type":         "RUNECHO_GUARD_VARTYPE=0",
	"lint":             "RUNECHO_GUARD_LINT=0",
}

// firedGates returns the settings that silence the checks that actually fired,
// in checkOrder's canonical order, skipping any check with no gate of its own.
//
// Reads firedChecks.firedNames rather than its own list of eleven bools: a
// second hand-kept copy of that list is exactly how a check added later ends up
// logged by askReason but missing from the user's remedy line — the drift
// hookBlockIndices was extracted to remove for the two seed builders.
func firedGates(f firedChecks) []string {
	var gates []string
	for _, name := range f.firedNames() {
		if g, ok := guardGates[name]; ok {
			gates = append(gates, g)
		}
	}
	return gates
}

// gateClause renders the fired gates as one sentence fragment, agreeing in
// number so the line reads as prose rather than as a generated list. Callers
// must not call it with an empty slice — every arm that reaches it has already
// tested len(gates) > 0.
func gateClause(gates []string) string {
	if len(gates) == 1 {
		return gates[0] + " disables that check"
	}
	return strings.Join(gates, " / ") + " disable those checks"
}

// askTrailer picks the closing line for the full hook ask.
//
// The lead sentence is invariant: varying the "approve if…" rationale per
// combination would need 2^11 strings, and rewording shipped user-facing
// guidance is its own change with its own review (#267's own framing). Only the
// remedy half varies, which is the half that was wrong.
func askTrailer(f firedChecks) string {
	const lead = "Approve if these are legitimate (new/local/dynamic, or an intended removal). "
	gates := firedGates(f)
	switch {
	case f.Violations && len(gates) == 0:
		// Byte-identical to the trailer that shipped from #243 onward.
		return lead + "Silence repeats via .runechoguardignore, or RUNECHO_GUARD_SKIP=1 to disable."
	case f.Violations:
		return lead + "Silence repeats via .runechoguardignore (the unresolved-symbol check only); " +
			gateClause(gates) + "; RUNECHO_GUARD_SKIP=1 disables the guard."
	case len(gates) > 0:
		return lead + gateClause(gates) + "; RUNECHO_GUARD_SKIP=1 disables the guard."
	default:
		// Unreachable from renderHookDecision, which reaches the trailer only
		// when len(violations) > 0 or fired.anyNonViolation() — and every path
		// into either sets f.Violations or a gated flag. Kept as the safe
		// fallback rather than a panic: RUNECHO_GUARD_SKIP=1 is the one remedy
		// that is true unconditionally, so an unforeseen combination degrades to
		// fewer remedies rather than to a false one.
		return lead + "RUNECHO_GUARD_SKIP=1 disables the guard."
	}
}

// precommitRemedyLine is askTrailer's counterpart for the pre-commit report.
//
// A separate builder, not a shared string: the two surfaces have always phrased
// this differently ("add false positives" / "bypass with" against the ask's
// "silence repeats" / "to disable"), the pre-commit line is terminal output
// rather than a hook payload, and only four of the eleven checks can fire here
// at all (runPreCommit appends violations, qualified, deps-go and file-scope —
// checkStatusMap's doc says the same thing, but names runArgs, which is now the
// flag dispatcher rather than the body). The part that could drift — WHICH gates
// — is shared via firedGates.
func precommitRemedyLine(f firedChecks) string {
	gates := firedGates(f)
	switch {
	case f.Violations && len(gates) == 0:
		// Byte-identical to the long-shipped line.
		return "Add false positives to .runechoguardignore, or bypass with RUNECHO_GUARD_SKIP=1."
	case f.Violations:
		return "Add false positives to .runechoguardignore (the unresolved-symbol check only); " +
			gateClause(gates) + "; bypass everything with RUNECHO_GUARD_SKIP=1."
	case len(gates) > 0:
		return gateClause(gates) + "; bypass with RUNECHO_GUARD_SKIP=1."
	default:
		// Unreachable for the same reason as askTrailer's default arm, and kept
		// for the same reason.
		return "Bypass with RUNECHO_GUARD_SKIP=1."
	}
}
