package depindex

import (
	"os"
	"path/filepath"
)

// maxWalkUp bounds every upward directory walk (go.mod, go.work). 64 parents is
// far past any real tree, and a bounded walk cannot hang on a pathological mount.
const maxWalkUp = 64

// maxResolvesPerRun bounds how many distinct packages one index will read from
// disk. With go/parser's measured p50 of 0.29ms per package this is a runaway
// backstop rather than a latency control — the per-package byte cap does that
// work — so it is set high enough that no realistic commit reaches it.
//
// The cap is a COUNT, not a wall clock, so a verdict stays a pure function of the
// input: the same edit yields the same answer on a fast and a slow machine. Same
// reasoning as the parser's maxParseNestDepth.
const maxResolvesPerRun = 128

// absDir returns dir as an absolute path, falling back to the input when the
// working directory cannot be determined.
//
// This matters more than it looks: filepath.Dir(".") is ".", so a relative start
// would end an upward walk on its first step and report "no go.mod" from inside
// a module.
func absDir(dir string) string {
	if filepath.IsAbs(dir) {
		return dir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
