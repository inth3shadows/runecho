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
	if !callShapeEnabled() || lang != guard.LangPython {
		return nil
	}
	var removed []guard.AddedLine
	if removedText != "" {
		removed = guard.TextToAddedLines(removedText)
	}
	return guard.PyCallShapeMismatches(lang, wholeFileLines, fd, removed, toolName == "Write")
}

// callShapeSection appends the call-shape section of an ask to sb and returns
// the symbol names to record on the decision. Extracted so the full ask and the
// store-free ask below render byte-identical text: two copies of this format
// string would drift, and the store-free path is the one nobody looks at.
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

// askWithoutIndex emits the ask for a path where the symbol pipeline never ran —
// an unenrolled tree, an unreadable store, an enrolled repo with no snapshot —
// and reports whether it emitted. Two checks survive those states: a contract
// binding, which resolves off the repo row alone, and call-shape, which needs no
// store row at all. Everything else there is genuinely unanswerable.
//
// Callers must invoke this INSTEAD OF their defer, not before it: the hook emits
// exactly one decision.
//
// With no mismatches it delegates verbatim to askContractOnly rather than
// re-rendering, so the long-shipped contract-only text and its "contract" log
// reason are untouched by this path existing.
func askWithoutIndex(out io.Writer, cw *contractWarning, ms []guard.CallShapeMismatch, filePath string, lang guard.Lang, repoName string) bool {
	if len(ms) == 0 {
		return askContractOnly(out, cw, filePath, lang)
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
	sb.WriteString("Approve if these are legitimate (new/local/dynamic, or an intended removal). Silence repeats via .runechoguardignore, or RUNECHO_GUARD_SKIP=1 to disable.")
	hookAsk(out, sb.String())

	// Not contractReason(cw != nil, askReason(...)) with all-false flags:
	// askReason falls back to "violations" when nothing is set, which would log a
	// hallucination bucket for an ask that found no unresolved symbol. Only the
	// checks that actually ran may name themselves here — fpreport buckets on the
	// exact string, and a phantom "violations" term inflates the very rate the
	// un-gating decision rests on.
	rec := decisionRecord{
		Mode:     "hook",
		Repo:     repoName,
		File:     filePath,
		Lang:     string(lang),
		Decision: "ask",
		Reason:   contractReason(cw != nil, askReason(false, false, false, false, true)),
		Symbols:  syms,
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
