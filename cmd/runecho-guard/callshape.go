package main

import (
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
