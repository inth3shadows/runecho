package guard

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// TextToAddedLines converts a multi-line string into AddedLine entries (1-based).
func TextToAddedLines(text string) []AddedLine {
	raw := strings.Split(text, "\n")
	lines := make([]AddedLine, len(raw))
	for i, l := range raw {
		lines[i] = AddedLine{LineNo: i + 1, Text: l}
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
			lines = append(lines, AddedLine{LineNo: no, Text: t})
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
