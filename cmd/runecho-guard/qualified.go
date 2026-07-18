package main

import (
	"os"

	"github.com/inth3shadows/runecho/internal/guard"
)

// qualifiedEnabled reports whether same-repo internal-package qualified-call
// checking is on (RUNECHO_GUARD_QUALIFIED=1). Default OFF, dogfood-first — like
// the other experimental guard surfaces (E1 dangling, E5 duplicate, learned-
// allow) — because qualified-reference validation is the most false-positive-
// delicate check the guard has (see internal/guard/qualified.go for the gate
// stack that keeps it zero-FP). It only covers Go same-repo internal packages;
// external deps, stdlib, and object-instance method calls are never flagged.
func qualifiedEnabled() bool { return os.Getenv("RUNECHO_GUARD_QUALIFIED") == "1" }

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
	if !qualifiedEnabled() || lang != guard.LangGo || modulePath == "" {
		return nil
	}
	vs := guard.GoQualifiedViolations(wholeFileLines, addedLines, symbols, modulePath)
	for i := range vs {
		vs[i].File = path
	}
	return vs
}
