package main

import (
	"os"

	"github.com/inth3shadows/runecho/internal/guard"
)

// varTypeEnabled gates the Go local-variable-type method check
// (RUNECHO_GUARD_VARTYPE=1, default OFF).
//
// A separate flag from RUNECHO_GUARD_RECVMETHOD, deliberately: #315 is an
// open, in-flight dogfood gate measuring RECVMETHOD's current (receiver-only)
// behavior. Folding this check under the same flag would change what that
// flag measures mid-window and invalidate #315's data. Off by default for the
// same reason every check past the additive one has shipped that way —
// earning confidence through dogfooding before any default-on decision.
func varTypeEnabled() bool { return os.Getenv("RUNECHO_GUARD_VARTYPE") == "1" }

// varTypeViolations runs the local-variable-type method check for one file
// and stamps each violation's File field. wholeFileLines is the current
// on-disk file (pre-edit in hook mode); addedLines is the proposed new text.
// Returns nil when the flag is off or the language is not Go.
func varTypeViolations(lang guard.Lang, wholeFileLines, addedLines []guard.AddedLine, known map[string]struct{}, path string) []guard.Violation {
	if !varTypeEnabled() || lang != guard.LangGo {
		return nil
	}
	vs := guard.GoVarTypeMethodViolations(wholeFileLines, addedLines, known)
	for i := range vs {
		vs[i].File = path
	}
	return vs
}
