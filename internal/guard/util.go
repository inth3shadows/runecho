package guard

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// maxLineBytes caps a single scanned line. The 16 MiB stdin limit bounds total
// hook input, but one pathological line with no newlines (a minified blob or an
// attack payload) becomes a single huge AddedLine whose per-line regex scan
// allocates proportionally — RE2 keeps it linear (no hang), but on the
// latency-sensitive PreToolUse hook the allocation/GC still stalls the editor. A
// real source line is never this long; truncation at most drops symbols past the
// cap on one pathological line — a narrow false negative in the safe direction.
const maxLineBytes = 64 << 10 // 64 KiB

// capLine truncates an over-long line to maxLineBytes on a byte boundary. The
// scanners are byte-indexed and tolerate a truncated tail (a partial trailing
// token at worst), so no rune-boundary care is needed.
func capLine(s string) string {
	if len(s) > maxLineBytes {
		return s[:maxLineBytes]
	}
	return s
}

// TextToAddedLines converts a multi-line string into AddedLine entries (1-based).
func TextToAddedLines(text string) []AddedLine {
	raw := strings.Split(text, "\n")
	lines := make([]AddedLine, len(raw))
	for i, l := range raw {
		lines[i] = AddedLine{LineNo: i + 1, Text: capLine(l)}
	}
	return lines
}

// AddedLinesWithGap converts independent text blocks into AddedLine entries whose
// per-block line numbers are separated by a one-line gap, so the stateful scanners
// (ExtractRefs, ExtractImports, firstUnqualifiedUseLines) reset multi-line
// string/comment state at each block boundary rather than leaking it from one
// block into the next. Used for MultiEdit, whose edits are unrelated regions: an
// unterminated string/template opened in one edit must not blank real calls in
// the next. A single block behaves identically to TextToAddedLines (1-based).
func AddedLinesWithGap(blocks []string) []AddedLine {
	var lines []AddedLine
	no := 0
	for i, b := range blocks {
		if i > 0 {
			no++ // skip a line number → LineNo discontinuity → scanner state reset
		}
		for _, t := range strings.Split(b, "\n") {
			no++
			lines = append(lines, AddedLine{LineNo: no, Text: capLine(t)})
		}
	}
	return lines
}

// ParseMaxAge reads RUNECHO_GUARD_MAX_AGE and returns the configured staleness
// threshold (default 24h).
func ParseMaxAge() (time.Duration, error) {
	if s := os.Getenv("RUNECHO_GUARD_MAX_AGE"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("bad RUNECHO_GUARD_MAX_AGE %q: %w", s, err)
		}
		return d, nil
	}
	return 24 * time.Hour, nil
}
