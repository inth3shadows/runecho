package guard

import (
	"bufio"
	"context"
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
// added lines. Returns an empty slice (no error) when nothing is staged. partial
// is true when an oversized diff line forced the parse to stop early — see
// parseDiffOutput; the caller should treat the result as incomplete coverage.
//
// ctx is forwarded to the git subprocess so the caller's deadline (gitTimeout)
// also bounds this last unbounded git call — the same cap (gitutil.Timeout) used
// for every other git subprocess in the hook path.
func ParseStagedDiff(ctx context.Context, repoRoot string) (diffs []FileDiff, partial bool, err error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "diff", "--cached", "--unified=0")
	out, err := cmd.Output()
	if err != nil {
		return nil, false, fmt.Errorf("git diff --cached: %w", err)
	}
	return parseDiffOutput(string(out))
}

// parseDiffOutput parses the raw unified-diff text into FileDiff entries.
// Exported for testing without subprocess. partial is true when scanning hit
// bufio.ErrTooLong: an oversized diff line truncated the stream, so any file
// after it was never seen — the returned diffs cover only what preceded the blob.
func parseDiffOutput(raw string) (diffs []FileDiff, partial bool, err error) {
	var result []FileDiff
	var cur *FileDiff
	newLineNo := 0 // running new-file line counter within the current hunk

	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 128*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

		// New file boundary: "+++ b/<path>" (or git's C-quoted form when
		// core.quotePath escapes spaces / non-ASCII bytes).
		if strings.HasPrefix(line, "+++ ") {
			if path, ok := parseDiffNewPath(line); ok {
				result = append(result, FileDiff{Path: path})
				cur = &result[len(result)-1]
				newLineNo = 0
			} else {
				// "+++ /dev/null" (deletion target) or an unparseable header:
				// no file to attach added lines to.
				cur = nil
			}
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
			// can still validate all files that preceded it — but flag partial so
			// the caller can warn that coverage was incomplete rather than silently
			// skipping every file past the blob.
			return result, true, nil
		}
		return nil, false, fmt.Errorf("scan diff: %w", err)
	}
	return result, false, nil
}

// parseDiffNewPath extracts the new-file path from a unified-diff "+++ " header.
// It handles the normal "+++ b/<path>" form and git's C-quoted form
// ("+++ \"b/<path>\"") emitted when core.quotePath escapes spaces, control bytes,
// or non-ASCII (UTF-8) characters — without it, those files are silently skipped,
// a false-negative vector for the guard. Returns ok=false for "+++ /dev/null"
// (a deletion target) and any header that doesn't carry a b/ path.
func parseDiffNewPath(line string) (string, bool) {
	rest := strings.TrimPrefix(line, "+++ ")
	if strings.HasPrefix(rest, "\"") {
		// git-quoted path: C-style escapes (octal \nnn, \", \\) — the same
		// grammar strconv.Unquote understands. On failure, fall through with the
		// raw string so the b/ check below rejects it rather than mis-detecting.
		if unq, err := strconv.Unquote(rest); err == nil {
			rest = unq
		}
	}
	if rest == "/dev/null" {
		return "", false
	}
	if !strings.HasPrefix(rest, "b/") {
		return "", false
	}
	return filepath.ToSlash(strings.TrimPrefix(rest, "b/")), true
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
