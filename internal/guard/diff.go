package guard

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/inth3shadows/runecho/internal/gitutil"
)

// maxDiffBytes caps the total staged-diff output read into memory. A huge staged
// blob would otherwise fully buffer via cmd.Output(); past the cap the diff is
// truncated and reported partial (the same incomplete-scan contract the per-line
// scanner cap uses), bounding memory on the pre-commit path.
const maxDiffBytes = 64 << 20 // 64 MiB

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
	// Own cancel so we can stop git early once the byte cap is hit.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := gitutil.Command(ctx, repoRoot, "diff", "--cached", "--unified=0")
	// Capture stderr so a git failure carries its diagnostic (parity with the old
	// cmd.Output() path and with gitutil.runGit), not a bare "exit status 128".
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, fmt.Errorf("git diff --cached: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, false, fmt.Errorf("git diff --cached: %w", err)
	}

	// Read one byte past the cap to detect overflow without buffering the rest.
	out, readErr := io.ReadAll(io.LimitReader(stdout, maxDiffBytes+1))
	truncated := int64(len(out)) > maxDiffBytes
	if truncated {
		out = out[:maxDiffBytes]
		cancel() // we have enough; stop git
	}
	// Drain any remainder so Wait doesn't block on a full pipe, then reap.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	// When not truncated, a read or non-zero-exit error is a real failure. When
	// truncated we intentionally killed git, so its Wait error is expected.
	if !truncated {
		if readErr != nil {
			return nil, false, fmt.Errorf("git diff --cached: %w", readErr)
		}
		if waitErr != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, false, fmt.Errorf("git diff --cached: %w: %s", waitErr, msg)
			}
			return nil, false, fmt.Errorf("git diff --cached: %w", waitErr)
		}
	}

	diffs, scanPartial, perr := parseDiffOutput(string(out))
	return diffs, truncated || scanPartial, perr
}

// parseDiffOutput parses the raw unified-diff text into FileDiff entries.
// Exported for testing without subprocess. partial is true when scanning hit
// bufio.ErrTooLong: an oversized diff line truncated the stream, so any file
// after it was never seen — the returned diffs cover only what preceded the blob.
func parseDiffOutput(raw string) (diffs []FileDiff, partial bool, err error) {
	var result []FileDiff
	var cur *FileDiff
	newLineNo := 0 // running new-file line counter within the current hunk
	// inHunk distinguishes real header lines from hunk content that merely looks
	// like one: an added line whose CONTENT starts "++ " renders as "+++ ...",
	// and treating it as a file boundary clears cur — silently dropping every
	// added line for the rest of that file. A real "+++ " header only appears
	// between a "diff --git" line and that file's first "@@" hunk, never inside
	// one. (Hunk lines start with '+', '-', ' ', '\' or '@@', so a bare "@@ "
	// inside a hunk is always a genuine next-hunk header.)
	inHunk := false

	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 128*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

		// Next file section: header lines may follow again.
		if strings.HasPrefix(line, "diff --git ") {
			inHunk = false
			continue
		}
		// New file boundary: "+++ b/<path>" (or git's C-quoted form when
		// core.quotePath escapes spaces / non-ASCII bytes).
		if !inHunk && strings.HasPrefix(line, "+++ ") {
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
		// Skip "--- a/..." and other header lines. Inside a hunk, "--- x" can
		// only be a removed line with "-- x" content — skipping it there is
		// equivalent to the removed-line fall-through (neither advances the
		// new-file counter), so this branch stays unconditional.
		if strings.HasPrefix(line, "--- ") ||
			strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "new file") ||
			strings.HasPrefix(line, "deleted file") || strings.HasPrefix(line, "similarity") ||
			strings.HasPrefix(line, "rename") || strings.HasPrefix(line, "Binary") {
			continue
		}

		// Hunk header: @@ -old +new[,count] @@ — checked BEFORE the cur==nil
		// skip so inHunk is set even for a file whose +++ header didn't parse:
		// otherwise a crafted "+++ "-looking added line inside that skipped
		// file's hunk would still read as a file boundary and attach a phantom
		// FileDiff.
		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
			if cur != nil {
				newLineNo = parseHunkStart(line)
			}
			continue
		}

		if cur == nil {
			continue
		}

		// Added line. Inside a hunk a "+++"-prefixed line is content (the header
		// branch above only consumes it between files), so don't exclude it here.
		if strings.HasPrefix(line, "+") && (inHunk || !strings.HasPrefix(line, "+++")) {
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
