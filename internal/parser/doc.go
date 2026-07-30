package parser

import "strings"

// maxDocLen bounds a recorded doc line, in runes. The field is emitted for every
// symbol in a `structure` response, so an unbounded first line — a wrapped
// one-sentence summary written as a single long line, or a docstring whose
// author put a paragraph before the first newline — would multiply the payload
// the same way per-symbol hashes did (see withoutSymbolHashes). 200 runes fits
// any conventional summary line; longer text is truncated rather than dropped,
// because a truncated summary still orients and a missing one does not.
const maxDocLen = 200

// firstDocLine reduces an already-unwrapped doc comment to its first meaningful
// line: leading blank lines skipped, surrounding whitespace trimmed, everything
// from the second line on discarded.
//
// It never synthesizes text. The result is a verbatim prefix of the comment the
// author wrote (modulo whitespace and the length cap) — that is the whole point
// of the `doc` field, and why it is named `doc` rather than `purpose`. Callers
// pass comment text with syntax markers already removed: Go via
// ast.CommentGroup.Text(), Python via pyDocstringText.
//
// Returns "" when there is no such line, which callers record as "no doc" — the
// field is then omitted entirely rather than emitted empty.
func firstDocLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > maxDocLen {
			return strings.TrimSpace(string(r[:maxDocLen]))
		}
		return line
	}
	return ""
}
