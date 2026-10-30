package main

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

// isCursorPayload reports whether a hook payload's cursor_version field is
// present and non-empty. Raw, so a non-string value still counts as Cursor
// rather than failing the decode and skipping the guard.
func isCursorPayload(raw json.RawMessage) bool {
	v := bytes.TrimSpace(raw)
	return len(v) > 0 && !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte(`""`))
}

// cursorWriteAsEdit turns Cursor's whole-file Write of an existing file into
// the single Edit it amounts to (#543). Cursor sends every agent edit as a
// Write carrying the full new file, and a Write is checked whole, so a name in
// the file the guard cannot resolve would be flagged again on every later
// edit to that file — under Cursor's default deny, a block nobody can dismiss.
// As an Edit, only the changed lines are checked, and every check sees the
// same shapes it sees for a Claude Code Edit, removed text included.
//
// old_string is the changed span of whole lines, widened by a line of context
// until it is non-empty and occurs once in the file, which is what an Edit
// requires. ok is false, and the Write is checked whole as before, when the
// file is absent, unreadable, not a regular file, over maxInFileBytes,
// unchanged, or when replacing the span does not reproduce content exactly.
func cursorWriteAsEdit(filePath, content string) (oldString, newString string, ok bool) {
	if filePath == "" || strings.ContainsRune(filePath, 0) || len(filePath) > 4096 {
		return "", "", false
	}
	fi, err := os.Stat(filePath)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxInFileBytes {
		return "", "", false
	}
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) > maxInFileBytes {
		return "", "", false
	}
	old := string(data)
	if old == content {
		return "", "", false
	}
	ol, nl := strings.SplitAfter(old, "\n"), strings.SplitAfter(content, "\n")
	p := 0
	for p < len(ol) && p < len(nl) && ol[p] == nl[p] {
		p++
	}
	s := 0
	for s < len(ol)-p && s < len(nl)-p && ol[len(ol)-1-s] == nl[len(nl)-1-s] {
		s++
	}
	for {
		oldString = strings.Join(ol[p:len(ol)-s], "")
		newString = strings.Join(nl[p:len(nl)-s], "")
		if oldString != "" && strings.Count(old, oldString) == 1 {
			break
		}
		switch {
		case p > 0:
			p--
		case s > 0:
			s--
		default:
			return "", "", false
		}
	}
	// Unreachable by construction (a unique whole-line span between equal
	// prefix and suffix rebuilds content), kept so a future change to the span
	// logic falls back to the whole-file check instead of checking the wrong text.
	if strings.Replace(old, oldString, newString, 1) != content {
		return "", "", false
	}
	return oldString, newString, true
}

// identRE matches identifier-shaped words; verifyEdit treats every one in a
// Cursor Edit's old_string as known.
var identRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
