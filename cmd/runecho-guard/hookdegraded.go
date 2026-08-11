package main

import (
	"io"
	"os/exec"

	"github.com/inth3shadows/runecho/internal/guard"
)

// hookEdit is the tool call a PreToolUse hook is asked about: the tool's name
// plus the text it adds and removes. runHookMode fills it once from the payload
// so an extracted phase takes one parameter instead of five, and so the phase's
// signature says "this reads the edit" rather than enumerating fields.
type hookEdit struct {
	ToolName  string
	NewString string // Edit
	OldString string // Edit
	Content   string // Write
	Edits     []editOp
}

// answerDegradedStore handles every case where the symbol index could not be
// read, and reports whether it asked (true) or deferred (false). The caller
// returns either way — this branch is terminal — so the bool exists purely to
// make the classification assertable; hookdegraded_test.go is what asserts it,
// and without that test the return value would be dead weight claiming to be a
// seam.
//
// Extracted from runHookMode verbatim. It was ~78 lines inside a 489-line
// function, and it is genuinely separable: it is reached only when res.OK is
// false, it returns unconditionally, and it shares no state with the checks
// below it. The comments are unchanged because they are the reason this branch
// looks the way it does.
//
// A contract binding resolves off the repo row alone, so it survives the one
// degraded state that still resolves a repo: enrolled but with no usable
// snapshot. Answer it rather than defer — the user declared a scope this session
// and the answer does not depend on the index.
//
// It does NOT survive the other two, but call-shape does, and that is why the
// contract binding is no longer the only thing answered here (#261). res.NoRepo
// means an unenrolled tree, which cannot hold a binding, and res.Warn
// (schema-newer) returns before ResolveRepo ever runs because the binary cannot
// read the store at all — cw is nil in both by construction. Call-shape has no
// store dependency at all: it resolves a call against declarations in the file
// in front of it, so those are precisely the states where it still answers
// correctly and was silent.
func answerDegradedStore(out io.Writer, res lookupResult, edit hookEdit, filePath string, lang guard.Lang, removedText string) bool {
	// Gated on the flag AND on Python so the default path pays nothing. An
	// unenrolled tree is the common case for a globally installed hook, and
	// charging every edit there a file read for a check nobody switched on is
	// the trade this gate exists to refuse — the alternative considered was
	// hoisting readFileLines above the store gate unconditionally.
	//
	// res.Warn is excluded deliberately. Schema-newer means this binary cannot
	// read the store at all, and that advisory is surfaced ALWAYS, strict or
	// not, because the fix is "reinstall" and nothing else the guard says
	// matters until it happens. An ask returns before the switch below, so
	// answering call-shape there would trade a loud "your binary is stale" for
	// a quiet keyword finding, and log reason "call-shape" in place of
	// "schema-newer" — deleting the exact signal #207's gv stamp exists to
	// preserve. The other two degraded arms lose nothing: NoRepo is silent by
	// design, and the strict store-degraded advisory rides along on the ask.
	var degradedShapes []guard.CallShapeMismatch
	if res.Warn == "" && callShapeEnabled() && lang == guard.LangPython {
		// Same construction as the enrolled path. Duplicated rather than hoisted
		// because the two are mutually exclusive — this branch returns — so
		// hoisting would charge every ENROLLED edit for a read it already does
		// further down, to save a read this branch only makes when the flag is on.
		preLines := readFileLines(filePath)
		fd := guard.FileDiff{
			Path:       filePath,
			AddedLines: hookAddedLines(edit.ToolName, edit.NewString, edit.Content, edit.Edits),
			SeedByLine: hookSeedByLine(edit.ToolName, edit.OldString, edit.Edits, preLines, lang),
		}
		degradedShapes = callShapeMismatches(lang, preLines, fd, edit.ToolName, removedText)
	}
	// Lint answers here for exactly the same reason call-shape does (#261): it
	// has no store dependency at all — ruff reads the Write payload's own
	// content and nothing else — so an unenrolled tree, the common case for a
	// globally installed hook, is precisely where the flag would otherwise be
	// advertised ("needs no index", TECHNICAL.md) and silently do nothing.
	// res.Warn is excluded on the same grounds as above: the schema-newer
	// advisory must not be traded for a quiet finding.
	//
	// No suppressAlreadyReported call here, deliberately: guard.Run never ran
	// on this path (that IS the degraded state), so there are no additive
	// findings for a lint finding to duplicate.
	var degradedLint []lintFinding
	if res.Warn == "" && lintEnabled() && edit.ToolName == "Write" && lang == guard.LangPython {
		if _, err := exec.LookPath("ruff"); err == nil {
			// Abstain reason is discarded rather than surfaced: this branch is
			// ALREADY degraded and says so through its own advisory, and there
			// is no per-check results slice here to carry a Verdict.
			degradedLint, _ = lintFindingsWithReason(filePath, hookText(edit.ToolName, edit.NewString, edit.Content, edit.Edits))
		}
	}
	// Under strict, a store-degraded edit gets an advisory saying symbol
	// validation is off. An ask returns before that switch, so the advisory
	// rides along on the ask rather than being dropped: the finding and the
	// fact that coverage was incomplete are both true, and the user needs
	// both. NoRepo is silent by design and contributes nothing here.
	var advisory string
	if !res.NoRepo && strictMode() {
		advisory = strictStoreDegradedAdvisory
	}
	if askWithoutIndex(out, res.Contract, degradedShapes, degradedLint, filePath, lang, res.RepoName, advisory, editFingerprint(edit)) {
		return true
	}
	switch {
	case res.Warn != "":
		// Schema-newer: already loud regardless of strict — surfaced always.
		hookDeferContext(out, res.Warn)
		logDecision(decisionRecord{Mode: "hook", Repo: res.RepoName, File: filePath, Lang: string(lang), Decision: "defer", Reason: "schema-newer"})
	case res.NoRepo:
		// Not enrolled — silent skip; strict does not change this.
		hookDefer()
		logDecision(decisionRecord{Mode: "hook", File: filePath, Lang: string(lang), Decision: "defer", Reason: "no-repo"})
	default:
		// Store accessible but degraded (no snapshot, no symbols, etc.).
		// Under strict, surface an advisory so the user knows validation is off.
		if strictMode() {
			hookDeferContext(out, strictStoreDegradedAdvisory)
		} else {
			hookDefer()
		}
		logDecision(decisionRecord{Mode: "hook", Repo: res.RepoName, File: filePath, Lang: string(lang), Decision: "defer", Reason: "store-degraded"})
	}
	return false
}
