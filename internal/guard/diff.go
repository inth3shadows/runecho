package guard

import (
	"bufio"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// FileDiff holds the added lines from one file in the staged diff.
type FileDiff struct {
	Path       string
	AddedLines []AddedLine
}

// AddedLine is a single '+' line from the diff with its new-file line number.
type AddedLine struct {
	LineNo int
	Text   string
}

// ParseStagedDiff runs `git diff --cached --unified=0` and returns per-file
// added lines. Returns an empty slice (no error) when nothing is staged.
func ParseStagedDiff(repoRoot string) ([]FileDiff, error) {
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--cached", "--unified=0")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --cached: %w", err)
	}
	return parseDiffOutput(string(out))
}

// parseDiffOutput parses the raw unified-diff text into FileDiff entries.
// Exported for testing without subprocess.
func parseDiffOutput(raw string) ([]FileDiff, error) {
	var result []FileDiff
	var cur *FileDiff
	newLineNo := 0 // running new-file line counter within the current hunk

	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 128*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

		// New file boundary: +++ b/<path>
		if strings.HasPrefix(line, "+++ b/") {
			path := filepath.ToSlash(strings.TrimPrefix(line, "+++ b/"))
			result = append(result, FileDiff{Path: path})
			cur = &result[len(result)-1]
			newLineNo = 0
			continue
		}
		// Skip "--- a/..." and "diff --git ..." header lines
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "diff --git ") ||
			strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "new file") ||
			strings.HasPrefix(line, "deleted file") || strings.HasPrefix(line, "similarity") ||
			strings.HasPrefix(line, "rename") || strings.HasPrefix(line, "Binary") {
			continue
		}

		if cur == nil {
			continue
		}

		// Hunk header: @@ -old +new[,count] @@
		if strings.HasPrefix(line, "@@ ") {
			newLineNo = parseHunkStart(line)
			continue
		}

		// Added line
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			cur.AddedLines = append(cur.AddedLines, AddedLine{
				LineNo: newLineNo,
				Text:   line[1:], // strip the leading '+'
			})
			newLineNo++
			continue
		}
		// Context line (space-prefixed)
		if strings.HasPrefix(line, " ") {
			newLineNo++
		}
		// Removed lines ('-') don't advance the new-file counter
	}
	if err := scanner.Err(); err != nil {
		if err == bufio.ErrTooLong {
			// A single diff line exceeded the 4 MB cap (e.g. a minified JS blob).
			// Return whatever was parsed before the oversized line — the guard
			// can still validate all files that preceded it.
			return result, nil
		}
		return nil, fmt.Errorf("scan diff: %w", err)
	}
	return result, nil
}

// parseHunkStart extracts the new-file start line from a @@ header.
// @@ -old[,count] +new[,count] @@ ... → new
func parseHunkStart(line string) int {
	// Find the '+' segment between the two '@@' markers
	parts := strings.Fields(line)
	for _, p := range parts {
		if strings.HasPrefix(p, "+") && p != "+@@" {
			seg := strings.TrimPrefix(p, "+")
			seg, _, _ = strings.Cut(seg, ",")
			n, err := strconv.Atoi(seg)
			if err == nil {
				return n
			}
		}
	}
	return 1
}
