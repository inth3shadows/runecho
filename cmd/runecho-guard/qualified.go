package main

import (
	"os"

	"github.com/inth3shadows/runecho/internal/guard"
)

// qualifiedEnabled reports whether same-repo internal-package qualified-call
// checking is on. Default ON as of #314 — RUNECHO_GUARD_QUALIFIED=0 disables it.
//
// It shipped default-off, dogfood-first, alongside the guard's other
// experimental surfaces (E1 dangling, E5 duplicate, learned-allow), because
// qualified-reference validation is the most false-positive-delicate check the
// guard has (see internal/guard/qualified.go for the gate stack that keeps it
// zero-FP). #314 promoted it: the compiler-oracle differential
// (TestGoResolveNoFalsePositivesAgainstCompiler) now measures it directly —
// 0 proven false positives over this repo (26k lines) and golang.org/x/text
// (473k lines) — and it resolves against the SAME flat repo symbol set the
// always-on hallucination check already trusts, so it introduces no new trust
// dependency. See TECHNICAL.md's "Opt-in checks" section for the full
// evidence and the reasoning that kept GoDepQualified (external deps) off.
func qualifiedEnabled() bool { return os.Getenv("RUNECHO_GUARD_QUALIFIED") != "0" }

// qualifiedViolations runs the Go same-repo qualified-call check for one file and
// stamps each violation's File field. wholeFileLines is the current on-disk file
// (pre-edit in hook mode); addedLines is the new/added text; modulePath is the
// already-resolved go.mod module path (callers resolve it once — see GoModulePath).
// Returns nil — fail-open — when the flag is off, the language is not Go, or
// modulePath is empty.
//
// Accepted limitation (matches the guard's existing oversized-file posture, e.g.
// addInFileDefs): if wholeFileLines is nil because the on-disk file exceeded the
// read cap, the shadow gate sees only addedLines, so a shadow elsewhere in the
// unread file is invisible. This can only bite when the SAME edit both adds a
// same-repo import and calls a nonexistent selector on an alias that a pre-existing
// (unread) local shadows — a rare corner on an 8 MiB+ file, degraded exactly as the
// rest of the guard degrades on oversized input.
func qualifiedViolations(lang guard.Lang, wholeFileLines, addedLines []guard.AddedLine, symbols map[string]struct{}, modulePath, path string) []guard.Violation {
	vs, _ := qualifiedViolationsWithReason(lang, wholeFileLines, addedLines, symbols, modulePath, path)
	return vs
}

// qualifiedViolationsWithReason is qualifiedViolations plus the reason a
// candidate `q.Sym(` call was declined — see
// guard.GoQualifiedViolationsWithReason, which this wraps. Empty for the
// flag-off / wrong-language / no-module-path cases, which are Skipped
// (not applicable), never Unknown.
func qualifiedViolationsWithReason(lang guard.Lang, wholeFileLines, addedLines []guard.AddedLine, symbols map[string]struct{}, modulePath, path string) ([]guard.Violation, string) {
	if !qualifiedEnabled() || lang != guard.LangGo || modulePath == "" {
		return nil, ""
	}
	vs, reason := guard.GoQualifiedViolationsWithReason(wholeFileLines, addedLines, symbols, modulePath)
	for i := range vs {
		vs[i].File = path
	}
	return vs, reason
}
