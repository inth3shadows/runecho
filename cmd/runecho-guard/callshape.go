package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/inth3shadows/runecho/internal/guard"
)

// #243 slice 1 — call-shape agreement, keyword-argument half. The guard's other
// checks all ask "does this symbol exist"; this one asks "is the symbol being
// USED as it is declared". A misspelled keyword argument resolves, passes every
// existence check, and fails at runtime — in Python, possibly deep inside a test
// run, which is where the debugging time actually goes.
//
// See internal/guard/callshapecheck.go for the scope (Python, same-file
// declarations, keyword names only) and the abstain ladder.

// callShapeEnabled reports whether the call-shape check is on
// (RUNECHO_GUARD_CALLSHAPE=1). Default OFF, dogfood-first, exactly like
// RUNECHO_GUARD_DANGLING / _FILESCOPE / _DUPLICATE before it: no default-on until
// an fpreport window shows the false-positive rate on real edits, not just on
// corpora. The differential test measures 0 false positives over 611
// oracle-checkable sites in four Python corpora, which is necessary evidence and
// not sufficient — a corpus shares the implementation's assumptions.
func callShapeEnabled() bool { return os.Getenv("RUNECHO_GUARD_CALLSHAPE") == "1" }

// callShapeMismatches runs the check for one edit. wholeFileLines is the pre-edit
// on-disk file; removedText is the text this edit deletes (Edit/MultiEdit
// old_string — "" for Write, which is correct: a Write's added text IS the whole
// post-edit file, so nothing about its declarations is stale).
//
// addedIsWholeFile must be true only for Write. Getting it wrong in the true
// direction would compare calls against a partial hunk's declarations; in the
// false direction it merely costs reach on Write edits.
func callShapeMismatches(lang guard.Lang, wholeFileLines []guard.AddedLine, fd guard.FileDiff, toolName, removedText string) []guard.CallShapeMismatch {
	ms, _ := callShapeMismatchesWithReason(lang, wholeFileLines, fd, toolName, removedText)
	return ms
}

// callShapeMismatchesWithReason is callShapeMismatches plus the reason a
// candidate kwarg-bearing call was declined — see
// guard.PyCallShapeMismatchesWithReason, which this wraps. Empty for the
// flag-off / wrong-language cases, which are Skipped, never Unknown.
func callShapeMismatchesWithReason(lang guard.Lang, wholeFileLines []guard.AddedLine, fd guard.FileDiff, toolName, removedText string) ([]guard.CallShapeMismatch, string) {
	if !callShapeEnabled() || lang != guard.LangPython {
		return nil, ""
	}
	var removed []guard.AddedLine
	if removedText != "" {
		removed = guard.TextToAddedLines(removedText)
	}
	return guard.PyCallShapeMismatchesWithReason(lang, wholeFileLines, fd, removed, toolName == "Write")
}

// callShapeSection appends the call-shape section of an ask to sb and returns
// the symbol names to record on the decision. Extracted so the full ask and the
// store-free ask below render this SECTION identically: two copies of the format
// string would drift, and the store-free path is the one nobody looks at. The
// surrounding trailer is deliberately not shared — see askWithoutIndex.
//
// Returns the CALLEE, not the keyword: guardstats and fpreport aggregate by
// symbol, and a keyword name is not one. These names are deliberately not
// learn-eligible — see LearnSymbols on decisionRecord.
func callShapeSection(sb *strings.Builder, ms []guard.CallShapeMismatch) []string {
	if len(ms) == 0 {
		return nil
	}
	fmt.Fprintf(sb, "[runecho-guard] %d keyword argument(s) the declaration does not accept — the symbol resolves but the call does not match it:\n", len(ms))
	syms := make([]string, 0, len(ms))
	for _, m := range ms {
		fmt.Fprintf(sb, "  snippet line %d: %s(%s=…)%s — %s is declared at %s %d and accepts: %s\n",
			m.LineNo, m.Callee, m.Keyword, suggestionSuffix(m.Suggestions), m.Callee,
			declLineLabel(m.DeclLineIsSnippet), m.DeclLine, acceptedList(m.Accepted))
		syms = append(syms, m.Callee)
	}
	return syms
}

// askWithoutIndexTrailer picks the closing line for a store-free ask. The
// call-shape-only string is reproduced BYTE-IDENTICALLY to what shipped before
// lint could answer here: it is the text an approving user has been reading,
// and a silent reword would make before/after dogfood transcripts
// incomparable for the check whose rate is actually being measured.
//
// Each variant names only the flags that can actually silence the findings it
// accompanies. Naming RUNECHO_GUARD_CALLSHAPE=0 on a lint-only ask sends the
// user to a switch that changes nothing, which is the same "remedy that
// cannot work" failure the call-shape trailer's own comment warns about.
func askWithoutIndexTrailer(hasShapes, hasLints bool) string {
	switch {
	case hasShapes && hasLints:
		return "Approve if these are legitimate (a dynamic or re-bound callee, a signature this edit does not show, or a name defined at runtime). RUNECHO_GUARD_CALLSHAPE=0 / RUNECHO_GUARD_LINT=0 disable the individual checks; RUNECHO_GUARD_SKIP=1 disables the guard."
	case hasLints:
		return "Approve if these are legitimate (a name defined at runtime, or an intended redefinition). RUNECHO_GUARD_LINT=0 disables this check; RUNECHO_GUARD_SKIP=1 disables the guard."
	default:
		return "Approve if the call is legitimate (a dynamic or re-bound callee, or a signature this edit does not show). RUNECHO_GUARD_CALLSHAPE=0 disables this check; RUNECHO_GUARD_SKIP=1 disables the guard."
	}
}

// askWithoutIndex emits the ask for a path where the symbol pipeline never ran —
// an unenrolled tree, an unreadable store, an enrolled repo with no snapshot —
// and reports whether it emitted. Three checks survive those states: a contract
// binding, which resolves off the repo row alone, and call-shape and lint, which
// need no store row at all. Everything else there is genuinely unanswerable.
//
// Callers must invoke this INSTEAD OF their defer, not before it: the hook emits
// exactly one decision.
//
// With no findings from either store-free check it delegates verbatim to
// askContractOnly rather than re-rendering, so the long-shipped contract-only
// text and its "contract" log reason are untouched by this path existing.
//
// editHash is threaded straight through to the ask record — see askContractOnly.
func askWithoutIndex(out io.Writer, cw *contractWarning, ms []guard.CallShapeMismatch, lints []lintFinding, filePath string, lang guard.Lang, repoName, advisory, editHash string) bool {
	if len(ms) == 0 && len(lints) == 0 {
		// nil checks: this whole function is the degraded-store path, which has
		// no per-check results slice to project (see answerDegradedStore's doc).
		return askContractOnly(out, cw, filePath, lang, editHash, nil, nil)
	}
	var sb strings.Builder
	// Contract first, matching the full ask's ordering: "should you be editing
	// this file at all" precedes "is this call shaped the way it is declared",
	// and reading it the other way round invites fixing the call and
	// re-submitting the same out-of-scope edit.
	if cw != nil {
		sb.WriteString(cw.section())
	}
	syms := callShapeSection(&sb, ms)
	syms = append(syms, lintSection(&sb, lints)...)
	// Not the full ask's trailer. That one offers .runechoguardignore, which
	// guard.Run consumes and neither store-free check consults — and on an
	// unenrolled tree there is no resolved repo root to hold one anyway. Naming
	// a remedy that cannot work is worse than naming fewer: the user tries it,
	// nothing changes, and the next ask reads as the guard being broken.
	sb.WriteString(askWithoutIndexTrailer(len(ms) > 0, len(lints) > 0))
	hookAskContext(out, sb.String(), advisory)

	// Not contractReason(cw != nil, askReason(...)) with all-false flags:
	// askReason falls back to "violations" when nothing is set, which would log a
	// hallucination bucket for an ask that found no unresolved symbol. Only the
	// checks that actually ran may name themselves here — fpreport buckets on the
	// exact string, and a phantom "violations" term inflates the very rate the
	// un-gating decision rests on.
	// checks: only the two verdicts this degraded path can actually assert.
	// There is no per-check results slice here (see answerDegradedStore's
	// doc), so an empty ms/lints does not mean "ran clean" — it may just mean
	// the check was never applicable (gate off, wrong language, no store
	// dependency but still not reached). Claiming "ok" for that would be a
	// fabricated verdict, exactly what checkStatusMap's contract forbids;
	// leaving it absent is the honest "not reported" the rest of #333 uses.
	checks := map[string]string{}
	if len(ms) > 0 {
		checks["call-shape"] = "violation"
	}
	if len(lints) > 0 {
		checks["lint"] = "violation"
	}
	rec := decisionRecord{
		Mode:     "hook",
		Repo:     repoName,
		File:     filePath,
		Lang:     string(lang),
		Decision: "ask",
		Reason:   contractReason(cw != nil, askReason(firedChecks{CallShape: len(ms) > 0, Lint: len(lints) > 0})),
		Symbols:  syms,
		Edit:     editHash,
		Checks:   checks,
	}
	if cw != nil {
		rec.Contract, rec.ContractHash = cw.Name, shortHash(cw.ActivatedHash)
	}
	logDecision(rec)
	return true
}

// acceptedList renders a declaration's accepted keyword names for the ask
// message. A declaration accepting none reads as "(none)" rather than as an empty
// gap, which would look like a formatting bug rather than a fact — and "accepts
// nothing by keyword" is exactly the case where the reader most needs to be told
// so plainly.
// declLineLabel names which coordinate system a declaration's line number is in.
// A signature read from the edit's own added text is numbered within the hunk, not
// within the file, and printing that as a file line sends the reader somewhere
// else entirely.
func declLineLabel(isSnippet bool) string {
	if isSnippet {
		return "snippet line"
	}
	return "line"
}

func acceptedList(accepted []string) string {
	if len(accepted) == 0 {
		return "(none)"
	}
	return strings.Join(accepted, ", ")
}
