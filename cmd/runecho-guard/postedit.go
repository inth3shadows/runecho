package main

import "github.com/inth3shadows/runecho/internal/guard"

// postEditFile returns the whole file as it will be after the tool call: a
// Write's content, or the pre-edit file with the Edit/MultiEdit replayed on it
// (simulateMultiEdit). ok is false when that cannot be known: the pre-edit file
// was not read, or an edit does not apply the way the tool applies it (a plain
// Edit's replace_all is not in the payload, so a repeated old_string is
// unknown too).
//
// A check that asks "is X true of this file" (a Go dot import, a Python star
// import) reads this, not the hunk and the pre-edit file separately: an edit
// that removes X in one place and adds code relying on it in another is only
// visible in the result (#562 G10, #564 P9).
func postEditFile(edit hookEdit, fileLines []guard.AddedLine) ([]guard.AddedLine, bool) {
	var edits []editOp
	switch edit.ToolName {
	case "Write":
		return guard.TextToAddedLines(edit.Content), true
	case "Edit":
		edits = []editOp{{OldString: edit.OldString, NewString: edit.NewString}}
	case "MultiEdit":
		edits = edit.Edits
	default:
		return nil, false
	}
	post, _, _, ok := simulateMultiEdit(fileLines, edits)
	return post, ok
}
