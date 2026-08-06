package main

import (
	"os"

	"github.com/inth3shadows/runecho/internal/guard"
)

// recvMethodEnabled gates the Go receiver-method check (RUNECHO_GUARD_RECVMETHOD=1,
// default OFF).
//
// Off by default because that is how every check newer than the additive one has
// shipped here — qualified, deps-go, file-scope, dangling, duplicate, call-shape
// — and because its measured reach is small: the compiler-oracle differential
// catches 6 of 59 adjudicated `v.Method()` mutations with it on. It measured zero
// false positives on runecho and on 468k lines of golang.org/x/text, so the gate
// is about earning confidence through dogfooding rather than about known risk.
//
// It is wired at all because the alternative is worse. The check existed for a
// full review cycle reachable only from tests — the same shape as #299, where the
// PostToolUse recorder shipped unwired and every downstream feature was silently
// inert for every external user.
func recvMethodEnabled() bool { return os.Getenv("RUNECHO_GUARD_RECVMETHOD") == "1" }

// recvMethodViolations runs the receiver-method check for one file and stamps
// each violation's File field. wholeFileLines is the current on-disk file
// (pre-edit in hook mode); addedLines is the proposed new text. Returns nil when
// the flag is off or the language is not Go.
func recvMethodViolations(lang guard.Lang, wholeFileLines, addedLines []guard.AddedLine, known map[string]struct{}, path string) []guard.Violation {
	if !recvMethodEnabled() || lang != guard.LangGo {
		return nil
	}
	vs := guard.GoReceiverMethodViolations(wholeFileLines, addedLines, known)
	for i := range vs {
		vs[i].File = path
	}
	return vs
}
