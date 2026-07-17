package store

import (
	"fmt"
	"path/filepath"
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
