package main

import (
	"path/filepath"

	"github.com/inth3shadows/runecho/internal/dirstate"
	"github.com/inth3shadows/runecho/internal/gitutil"
)

// lookupDir returns the directory repo resolution should run in for filePath.
// Resolution runs git inside a directory, and a Write that creates a new
// package targets a directory that does not exist yet: every `git -C` there
// failed, so the guard deferred "no-repo" on exactly the edits most likely to
// carry whole-file hallucinations (#472).
//
// So it walks up from filePath's parent, but only through components that
// definitively do not exist (dirstate.Absent). A component that is a file, or a
// directory that exists but cannot be read, stops the walk and the original
// directory is returned — resolution then misses exactly as it did before,
// rather than climbing into an enclosing repository past a boundary it could
// not see (#538 review).
//
// A walked-up ancestor is accepted only when it lies inside a work tree. In a
// claudew-style bare container (<C>/.bare plus a .git pointer file, worktrees
// beneath) a new path that is in NO worktree would otherwise climb to <C>,
// where git finds the common dir but no work tree, and be checked against
// whichever enrolled worktree shares that common dir (#538 review). The work
// tree check costs one git call, paid only when the walk moved.
func lookupDir(filePath string) string {
	start := filepath.Dir(filePath)
	dir := start
	for {
		switch dirstate.Stat(dir) {
		case dirstate.Present:
			if dir == start {
				return dir
			}
			if _, err := gitutil.TopLevel(dir); err != nil {
				return start
			}
			return dir
		case dirstate.Absent:
			parent := filepath.Dir(dir)
			if parent == dir {
				return start
			}
			dir = parent
		default: // NotADirectory, Unreadable
			return start
		}
	}
}
