package main

import (
	"os"

	"github.com/inth3shadows/runecho/internal/depindex"
	"github.com/inth3shadows/runecho/internal/guard"
)

// depQualifiedGoEnabled reports whether external-dependency qualified-call
// checking is on (RUNECHO_GUARD_DEPS_GO=1). Default OFF, dogfood-first, like
// every other experimental guard surface.
//
// This one earns the flag more than most: it is the first check whose symbol
// table comes from OUTSIDE the repo, so its correctness depends on an
// environment RunEcho does not control — the module cache, go.mod's pinned
// versions, a vendor directory, a workspace overlay. The tri-state in
// internal/depindex is what keeps an incomplete index from becoming a false
// positive, and the flag is what keeps the blast radius small until that has
// been proven in real use.
func depQualifiedGoEnabled() bool { return os.Getenv("RUNECHO_GUARD_DEPS_GO") == "1" }

// newGoDepIndex builds the per-run Go dependency index rooted at startDir, or
// nil when the check is disabled. nil is a valid argument to
// GoDepQualifiedViolations, so callers need no branch beyond the language check
// they already have.
//
// One index per guard invocation: it memoizes lookups internally, and a guard
// run is short enough that the module cache cannot change underneath it.
func newGoDepIndex(startDir string) depindex.Index {
	if !depQualifiedGoEnabled() {
		return nil
	}
	return depindex.NewGoIndex(startDir)
}

// goDepQualifiedViolations runs the external-dependency check for one Go file
// and stamps each violation's File field. wholeFileLines is the current on-disk
// file (pre-edit in hook mode); addedLines is the proposed new text; modulePath
// is the repo's own module path, used only to EXCLUDE same-repo imports, which
// qualified.go validates against the repo IR instead. Returns nil when the flag
// is off, the index is nil, or the language is not Go.
//
// Accepted limitation, matching the guard's existing oversized-file posture: if
// wholeFileLines is nil because the on-disk file exceeded the read cap, the
// shadow gate sees only addedLines, so a shadowing local elsewhere in the unread
// file is invisible. Degraded exactly as the rest of the guard degrades on
// oversized input.
func goDepQualifiedViolations(lang guard.Lang, wholeFileLines, addedLines []guard.AddedLine, modulePath string, idx depindex.Index, path string) []guard.Violation {
	if idx == nil || lang != guard.LangGo {
		return nil
	}
	vs := guard.GoDepQualifiedViolations(wholeFileLines, addedLines, modulePath, idx)
	for i := range vs {
		vs[i].File = path
	}
	return vs
}
