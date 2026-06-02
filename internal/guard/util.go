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
