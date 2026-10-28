package main

import (
	"os"
	"path/filepath"
)

// existingDir returns the nearest existing directory at or above filePath's
// parent. Repo resolution runs git inside a directory, and a Write that
// creates a new package targets a directory that does not exist yet: every
// `git -C` there fails, so the guard deferred "no-repo" on exactly the edits
// most likely to carry whole-file hallucinations (#472). The ancestor is in
// the same repository whenever the new path is, and outside any repository
// when it is not, so resolution answers the same question it always did.
func existingDir(filePath string) string {
	dir := filepath.Dir(filePath)
	for {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}
