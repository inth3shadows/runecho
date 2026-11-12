package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"

	"github.com/inth3shadows/runecho/internal/gitutil"
)

// stagedBlobReader returns a guard.PyStarEnv reader that serves a file from the
// git index (`git cat-file blob :./<path>`, the #524 rule: pre-commit judges
// what is being committed, not the working tree). A path outside root, absent
// from the index, or larger than 1 MiB reports ok=false, which the star-import
// check treats as an unknown source (abstain).
func stagedBlobReader(root string) func(abs string) ([]byte, bool) {
	const maxBytes = 1 << 20
	return func(abs string) ([]byte, bool) {
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") || strings.ContainsAny(rel, "\n\r") {
			return nil, false
		}
		ctx, cancel := context.WithTimeout(context.Background(), gitutil.Timeout)
		defer cancel()
		cmd := gitutil.Command(ctx, root, "cat-file", "blob", ":./"+filepath.ToSlash(rel))
		out, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			return nil, false
		}
		data, rerr := io.ReadAll(io.LimitReader(out, maxBytes+1))
		_, _ = io.Copy(io.Discard, out)
		if werr := cmd.Wait(); werr != nil || rerr != nil {
			return nil, false
		}
		return data, true
	}
}
