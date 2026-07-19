package main

import (
	"os"

	"github.com/inth3shadows/runecho/internal/depindex"
	"github.com/inth3shadows/runecho/internal/guard"
)

// depQualifiedEnabled reports whether external-dependency qualified-call checking
// is on (RUNECHO_GUARD_DEPS_PY=1). Default OFF, dogfood-first, like every other
// experimental guard surface.
//
// This one earns the flag more than most: it is the first check whose symbol
// table comes from OUTSIDE the repo, so its correctness depends on an environment
// RunEcho does not control. The tri-state in internal/depindex is what keeps an
// incomplete index from becoming a false positive, and the flag is what keeps the
// blast radius small until that has been proven in real use.
func depQualifiedEnabled() bool { return os.Getenv("RUNECHO_GUARD_DEPS_PY") == "1" }

// depQualifiedGoEnabled reports whether the Go half is on
// (RUNECHO_GUARD_DEPS_GO=1). Separate from the Python flag so each language can
// be dogfooded on its own: they share the tri-state resolver but not their
// failure modes — Python's risk is runtime dynamism, Go's is resolving the wrong
// copy of a package (version, replace, vendor, workspace).
func depQualifiedGoEnabled() bool { return os.Getenv("RUNECHO_GUARD_DEPS_GO") == "1" }

// newGoDepIndex builds the per-run Go dependency index rooted at startDir, or nil
// when the check is disabled. nil is a valid argument to
// GoDepQualifiedViolations, so callers need no extra branch.
func newGoDepIndex(startDir string) depindex.Index {
	if !depQualifiedGoEnabled() {
		return nil
	}
	return depindex.NewGoIndex(startDir)
}

// goDepQualifiedViolations runs the external-dependency check for one Go file and
// stamps each violation's File field. modulePath is the repo's own module path,
// used only to exclude same-repo imports (qualified.go owns those). Returns nil
// when the flag is off, the index is nil, or the language is not Go.
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

// newPythonDepIndex builds the per-run dependency index rooted at startDir, or
// returns nil when the check is disabled. nil is a valid argument to
// PyDepQualifiedViolations (it yields no violations), so callers need no branch
// beyond the one they already have for the language.
//
// One index per guard invocation: it memoizes lookups internally, and a guard run
// is short enough that the environment cannot change underneath it.
func newPythonDepIndex(startDir string) depindex.Index {
	if !depQualifiedEnabled() {
		return nil
	}
	return depindex.NewPythonIndex(startDir)
}

// depQualifiedViolations runs the external-dependency check for one Python file
// and stamps each violation's File field. wholeFileLines is the current on-disk
// file (pre-edit in hook mode); addedLines is the new text. Returns nil when the
// flag is off, the index is nil, or the language is not Python.
//
// Same accepted limitation as the same-repo path: if wholeFileLines is nil because
// the file exceeded the read cap, the shadow and monkey-patch gates see only the
// added lines, so a shadow elsewhere in the unread file is invisible. Degraded
// exactly as the rest of the guard degrades on oversized input.
func depQualifiedViolations(lang guard.Lang, wholeFileLines, addedLines []guard.AddedLine, idx depindex.Index, path string) []guard.Violation {
	if idx == nil || lang != guard.LangPython {
		return nil
	}
	vs := guard.PyDepQualifiedViolations(wholeFileLines, addedLines, idx)
	for i := range vs {
		vs[i].File = path
	}
	return vs
}
