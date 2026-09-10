// Package dirstate answers "is this directory there?" ONCE, with enough
// resolution that each caller can pick its own posture instead of copying a
// predicate.
//
// Three functions used to answer this question three ways (#386):
//
//	input                  rootIsMissing   snapshot.dirExists   guard.dirExists
//	ENOENT                 missing         absent               absent
//	EACCES / EIO           not missing     PRESENT              absent
//	exists, is a file      not missing     absent               absent
//
// Every one of them was defensible alone — prune-missing --yes deletes history,
// so an unmounted drive must not read as gone; the guard would rather block
// than validate against a root it cannot read. The defect was that they
// interact: #382 originally had the resolver choose a candidate with one
// predicate and the guard immediately refuse it with another, so a live sibling
// was skipped in favour of one that was then rejected.
//
// The fix is NOT to make them agree. It is to make the disagreement explicit:
// Stat distinguishes the four cases those predicates were collapsing, and each
// caller states which of them it treats as "there". A new caller then has to
// choose a posture rather than inherit one by accident.
package dirstate

import (
	"errors"
	"io/fs"
	"os"
)

// State is what a stat of a path established.
type State uint8

const (
	// Present: the path exists and is a directory.
	Present State = iota
	// Absent: the path definitively does not exist (os.ErrNotExist). This is
	// the ONLY value that is evidence of deletion, and the only one a
	// destructive path may act on.
	Absent
	// NotADirectory: something is there, but it is not a directory.
	NotADirectory
	// Unreadable: the stat failed for some other reason — a permission error,
	// an I/O error, a flaky or unmounted mount. Nothing is known about the
	// path. Never evidence of deletion.
	Unreadable
)

func (s State) String() string {
	switch s {
	case Present:
		return "present"
	case Absent:
		return "absent"
	case NotADirectory:
		return "not-a-directory"
	default:
		return "unreadable"
	}
}

// Stat classifies p. It is the single definition of these four cases; callers
// express a posture over the result rather than re-deriving it from os.Stat.
func Stat(p string) State {
	fi, err := os.Stat(p)
	if err == nil {
		if fi.IsDir() {
			return Present
		}
		return NotADirectory
	}
	if errors.Is(err, fs.ErrNotExist) {
		return Absent
	}
	return Unreadable
}

// IsGone reports whether p is definitively deleted. This is the predicate for
// anything DESTRUCTIVE: only ENOENT counts, so an unmounted drive, a detached
// share or a permission error never reads as gone.
func IsGone(p string) bool { return Stat(p) == Absent }

// IsUsable reports whether p can be used as a directory right now. This is the
// predicate for anything that would otherwise VALIDATE against a root it cannot
// read: only a confirmed directory passes, so unreadable and not-a-directory
// both fail closed.
func IsUsable(p string) bool { return Stat(p) == Present }

// MayExist reports whether p might still be a live directory — true unless it
// is definitively absent or definitively not a directory. This is the tolerant
// middle posture: it keeps a candidate in play when the answer is unknown, and
// its caller must still confirm with IsUsable before acting on the path.
func MayExist(p string) bool {
	switch Stat(p) {
	case Absent, NotADirectory:
		return false
	default:
		return true
	}
}
