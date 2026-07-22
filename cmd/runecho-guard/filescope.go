package main

import (
	"os"

	"github.com/inth3shadows/runecho/internal/guard"
)

// fileScopeEnabled reports whether the file-scope resolution check is on
// (RUNECHO_GUARD_FILESCOPE=1). Default OFF, dogfood-first, like every other
// experimental guard surface. This one is the most false-positive-delicate of
// them all — it is adjacent to what a linter (pyflakes F821) does — so it stays
// gated until a dogfood window shows a zero false-positive rate. See
// internal/guard/filescope.go for the firewall and abstain stack.
func fileScopeEnabled() bool { return os.Getenv("RUNECHO_GUARD_FILESCOPE") == "1" }

// fileScopeViolations runs the file-scope resolution check for one file and
// stamps each violation's File field. wholeFileLines is the current on-disk file
// (pre-edit in hook mode) — the check returns nothing without it. fd is passed
// whole so the string-state seed (AbsPath / SeedByLine) travels with it; without
// that, an edit landing inside a docstring would be scanned as code. repoSymbols
// MUST be the repo's own indexed symbol set, NOT a set already widened with
// learned-allow entries: the firewall's meaning is "this name is a real symbol in
// the repo", and a learned-allow name is one the user taught the guard to accept,
// which this check must never re-raise.
//
// No deduplication against guard.Run's output is required, and that is a property
// of the firewall rather than an oversight: the additive check flags only names
// ABSENT from the known set, while this one fires only on names PRESENT in it. The
// two are disjoint by construction, so a symbol can never be reported twice.
func fileScopeViolations(lang guard.Lang, wholeFileLines []guard.AddedLine, fd guard.FileDiff, repoSymbols map[string]struct{}, path string) []guard.Violation {
	if !fileScopeEnabled() || lang != guard.LangPython {
		return nil
	}
	vs := guard.FileScopeViolations(lang, wholeFileLines, fd, repoSymbols)
	for i := range vs {
		vs[i].File = path
	}
	return vs
}

// snapshotSymbols copies a symbol set so later in-place folds (in-file defs,
// learned-allow) cannot widen what the file-scope firewall considers "known to
// the repo". Only called when the check is actually enabled, so the default-off
// path pays nothing on the hook's ~12 ms budget.
func snapshotSymbols(symbols map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(symbols))
	for s := range symbols {
		out[s] = struct{}{}
	}
	return out
}
