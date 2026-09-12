package main

import (
	"io"
	"os/exec"
	"time"

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
// means an unenrolled tree, which cannot hold a binding (it carries its own
// one-time notice instead — see enrollNotice), and res.Warn
// (schema-newer) returns before ResolveRepo ever runs because the binary cannot
// read the store at all — cw is nil in both by construction. Call-shape has no
// store dependency at all: it resolves a call against declarations in the file
// in front of it, so those are precisely the states where it still answers
// correctly and was silent.
func answerDegradedStore(out io.Writer, res lookupResult, edit hookEdit, filePath string, lang guard.Lang, removedText string) bool {
	// The two store-free checks are computed by storeFreeChecks, which #394 split
	// out so the protocol renderer can report a VERDICT for them on this arm
	// rather than the nothing the hook needs. The hook wants only the findings;
	// it discards the results slice, because a degraded-store ask has never
	// carried a per-check map (see askWithoutIndex) and #394 does not change
	// what the hook logs.
	degradedShapes, degradedLint, _ := storeFreeChecks(res, edit, filePath, lang, removedText)
	// Both degraded arms that can speak produce their advisory here, before the
	// ask, because an ask returns before the defer switch: an advisory computed
	// later would simply be dropped whenever a store-free check fired. Under
	// strict, a store-degraded edit says symbol validation is off; an unenrolled
	// tree says the repo is not enrolled, once and only once. The finding and
	// the fact that coverage was incomplete are both true and the user needs
	// both. The two are mutually exclusive by construction — NoRepo means no
	// store row was resolved at all — so a switch, not two ifs.
	var advisory string
	switch {
	case res.NoRepo:
		// #392: the unenrolled arm is silent except for ONE notice per repo,
		// naming the repo that is going unguarded. Computed HERE, before the
		// ask, so it rides along on an ask exactly as the strict advisory does
		// — and so the marker is written either way. Were it computed only in
		// the defer arm, an edit that happened to trip call-shape or lint would
		// consume the "first edit" without saying anything, and the notice
		// would arrive on the NEXT edit instead. Returns "" for every repo
		// already noticed, which is all of them after the first edit.
		advisory = enrollNotice(res, time.Now())
	case strictMode():
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
		// Not enrolled — silent skip; strict does not change this. The one
		// exception is the first edit in a given repo, which carries the
		// enrollment notice computed above (#392); advisory is "" on every
		// later edit, and the log record is identical either way. The reason
		// stays "no-repo" deliberately: guardstats, the census and TECHNICAL.md
		// all bucket on that exact string, and the marker file is the notice's
		// own audit trail.
		if advisory != "" {
			hookDeferContext(out, advisory)
		} else {
			hookDefer()
		}
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

// storeFreeChecks runs the two checks that need no store row at all — call-shape
// (it resolves a call against declarations in the file in front of it) and lint
// (ruff reads the Write payload's own content) — for an edit whose store lookup
// failed. #261 wired them into the hook's degraded arm; #394 split them out here
// so the protocol renderer can report a verdict for them on the same arm.
//
// It returns the findings the hook renders AND the CheckResults only the
// protocol consumes. The hook discards the latter deliberately: a degraded-store
// ask has never carried a per-check map, and changing that would shift
// fpreport's CheckRuns tallies for a surface this issue is not about.
//
// The nine store-DEPENDENT checks are not here. They cannot answer without an
// index, and the protocol synthesises their Unknown from the arm's own defer
// reason rather than pretending this function skipped them.
func storeFreeChecks(res lookupResult, edit hookEdit, filePath string, lang guard.Lang, removedText string) ([]guard.CallShapeMismatch, []lintFinding, []CheckResult) {
	var degradedShapes []guard.CallShapeMismatch
	var degradedLint []lintFinding
	var shapeReason, lintReason string
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
	// preserve. The other two degraded arms lose nothing: NoRepo's own advisory
	// and the strict store-degraded one both ride along on the ask.
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
		degradedShapes, shapeReason = callShapeMismatchesWithReason(lang, preLines, fd, edit.ToolName, removedText)
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
	if res.Warn == "" && lintEnabled() && edit.ToolName == "Write" && lang == guard.LangPython {
		if _, err := exec.LookPath("ruff"); err == nil {
			// The abstain reason is CAPTURED now (#394), not discarded. The hook
			// still has nowhere to put it — a degraded-store ask carries no
			// per-check map — but the protocol document must say "unknown, and
			// here is why" rather than collapsing an abstention into silence.
			degradedLint, lintReason = lintFindingsWithReason(filePath, hookText(edit.ToolName, edit.NewString, edit.Content, edit.Edits))
		}
	}
	callShapeResult := CheckResult{Check: "call-shape", Verdict: VerdictSkipped}
	if res.Warn == "" && callShapeEnabled() && lang == guard.LangPython {
		callShapeResult = classifyResult("call-shape", len(degradedShapes) > 0, shapeReason)
	}
	lintResult := CheckResult{Check: "lint", Verdict: VerdictSkipped}
	if res.Warn == "" && lintEnabled() && edit.ToolName == "Write" && lang == guard.LangPython {
		lintResult = classifyResult("lint", len(degradedLint) > 0, lintReason)
	}
	return degradedShapes, degradedLint, []CheckResult{callShapeResult, lintResult}
}
