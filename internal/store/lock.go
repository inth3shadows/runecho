package store

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// RefreshLockPath returns the path of the E6 refresh advisory lock for repoID
// inside the store dir. The lock serializes ir.json load-modify-save across the
// PostToolUse guard hook and the CLI (repo reindex / index) so concurrent writers
// can't lose each other's refresh (a last-writer-wins clobber). It is keyed by
// repo ID and lives in the store dir — never beside ir.json, which would litter
// git status on every refresh. Both sides MUST derive the path here so the two
// always agree on the name; a divergent name would silently defeat the lock.
func RefreshLockPath(dir string, repoID int64) string {
	return filepath.Join(dir, fmt.Sprintf("e6-refresh-%d.lock", repoID))
}

// refreshLockPrefix and refreshLockSuffix bracket the repo ID in a refresh
// lock's filename. They exist so RefreshLockPath and RefreshLockID cannot
// disagree about the shape: a sweeper that parsed a name the writer never
// produces would delete nothing, and one that parsed a name too loosely would
// delete somebody else's file.
const (
	refreshLockPrefix = "e6-refresh-"
	refreshLockSuffix = ".lock"
)

// RefreshLockGlob matches every refresh lock in a store dir. Callers pass it to
// filepath.Glob and hand each basename to RefreshLockID.
func RefreshLockGlob(dir string) string {
	return filepath.Join(dir, refreshLockPrefix+"*"+refreshLockSuffix)
}

// RefreshLockID is the inverse of RefreshLockPath: it recovers the repo ID from
// a lock's BASE name, reporting false for anything that is not a name
// RefreshLockPath would have produced.
//
// Strict on purpose. This is the predicate a sweeper deletes on, so "did not
// parse" must mean "leave it alone", never "assume it is ours". A negative or
// zero id is refused for the same reason — RefreshLockPath is only ever called
// with a real repo row's id.
func RefreshLockID(base string) (int64, bool) {
	digits, ok := strings.CutPrefix(base, refreshLockPrefix)
	if !ok {
		return 0, false
	}
	digits, ok = strings.CutSuffix(digits, refreshLockSuffix)
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
