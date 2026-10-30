package main

import (
	"bytes"
	"encoding/json"

	"github.com/inth3shadows/runecho/internal/guard"
)

// isCursorPayload reports whether a hook payload's cursor_version field is
// present and non-empty. Raw, so a non-string value still counts as Cursor
// rather than failing the decode and skipping the guard.
func isCursorPayload(raw json.RawMessage) bool {
	v := bytes.TrimSpace(raw)
	return len(v) > 0 && !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte(`""`))
}

// cursorUnchangedLines reports, for a Cursor Write over an existing file, which
// lines of the new file the Write left exactly as they were: those in the
// common prefix and suffix of the pre-edit and proposed files (#543). Cursor
// sends every agent edit as a whole-file Write, and a Write is checked whole,
// so a name in the file the guard cannot resolve would be flagged again on
// every later edit to it: under Cursor's default deny, a block nobody can
// dismiss. Dropping findings on those lines gives the edit the same reach as
// a Claude Code Edit of the changed span, while every check still scans the
// whole file, so string and bracket state, lint, and the edit fingerprint are
// those of the Write.
//
// nil (keep every finding) unless the payload is Cursor's and a Write, and the
// pre-edit file was read: a new, empty, unreadable or oversized file is checked
// whole, as before. Hook-mode Write line numbers are the file's own (the
// AddedLines are 1..N over content), which is what makes this sound.
func cursorUnchangedLines(cursor bool, toolName string, fileLines, newLines []guard.AddedLine) func(line int) bool {
	if !cursor || toolName != "Write" || len(fileLines) == 0 {
		return nil
	}
	p := 0
	for p < len(fileLines) && p < len(newLines) && fileLines[p].Text == newLines[p].Text {
		p++
	}
	s := 0
	for s < len(fileLines)-p && s < len(newLines)-p &&
		fileLines[len(fileLines)-1-s].Text == newLines[len(newLines)-1-s].Text {
		s++
	}
	last := len(newLines) - s
	return func(line int) bool { return line <= p || line > last }
}

// dropUnchanged removes the findings unchanged reports as on an unchanged
// line; a nil unchanged returns xs as is.
func dropUnchanged[T any](xs []T, lineOf func(T) int, unchanged func(int) bool) []T {
	if unchanged == nil || len(xs) == 0 {
		return xs
	}
	var out []T
	for _, x := range xs {
		if !unchanged(lineOf(x)) {
			out = append(out, x)
		}
	}
	return out
}

func violationLine(v guard.Violation) int         { return v.Line }
func callShapeLine(m guard.CallShapeMismatch) int { return m.LineNo }
func lintLine(f lintFinding) int                  { return f.Line }
