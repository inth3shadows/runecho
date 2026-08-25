package snapshot

import (
	"math"
	"time"

	"github.com/inth3shadows/runecho/internal/ir"
)

// CoveragePercent returns indexed/supported as a percentage rounded to one
// decimal. Integer division truncated here before (199/200 read as 99, and
// anything under 1% read as 0 — "fully uncovered") — one decimal keeps small
// nonzero coverage visible. Returns 0 when supported is 0; callers gate on
// supported > 0 for the "not yet measured" case.
func CoveragePercent(indexed, supported int) float64 {
	if supported <= 0 {
		return 0
	}
	return math.Round(float64(indexed)*1000/float64(supported)) / 10
}

// FileChurn tracks how many diffs a file was modified/added/removed in.
type FileChurn struct {
	Path      string
	Changes   int // diffs where this file was modified/added/removed
	DiffCount int // total diffs analyzed
}

// SymbolChurn tracks how many diffs a symbol appeared in as added or removed.
type SymbolChurn struct {
	Name      string
	Kind      string
	FilePath  string
	Changes   int
	DiffCount int
}

// ChurnReport is the result of a churn analysis across N snapshots.
type ChurnReport struct {
	Root          string
	SnapshotCount int
	DiffCount     int
	Since         time.Time
	Until         time.Time
	Files         []FileChurn   // sorted Changes DESC
	Symbols       []SymbolChurn // sorted Changes DESC
}

// SnapshotMeta describes a stored IR snapshot (no file/symbol data).
type SnapshotMeta struct {
	ID        int64
	RepoID    int64
	SessionID string
	Label     string
	Timestamp time.Time
	Root      string
	RootHash  string
	FileCount int
}

// SymbolDelta is a single symbol added, removed, or modified.
type SymbolDelta struct {
	Name string
	Kind string // "function" | "class" | "export" | "import"
	// Hash is the symbol's body hash, when the parser produced one (AST-extracted
	// functions). Empty means "no body hash available" — such a symbol can only be
	// added/removed, never reported "modified". Not serialized in diff output.
	Hash string `json:"-"`
}

// FileDiff is the structural diff for one file between two snapshots.
type FileDiff struct {
	Path       string
	Status     string // "added" | "removed" | "modified" | "unchanged"
	HashBefore string
	HashAfter  string
	Added      []SymbolDelta
	Removed    []SymbolDelta
	// Modified lists symbols present in both snapshots whose body hash changed
	// (e.g. a function whose signature or body was edited in place). Only symbols
	// that carry a body hash on both sides can appear here.
	Modified []SymbolDelta
}

// DiffResult is the full structural diff between two snapshots.
type DiffResult struct {
	SnapshotA     SnapshotMeta
	SnapshotB     SnapshotMeta
	Files         []FileDiff
	TotalAdded    int
	TotalRemoved  int
	TotalModified int

	// Skipped names the paths the LIVE walk declined and why -- the files a
	// symbol could have been deleted from without this diff noticing. It is
	// meaningful only in DiffLive mode; see SkippedKnown.
	Skipped []ir.SkippedFile
	// SkippedKnown separates "the walk reported no skips" from "there was no
	// walk". A snapshot-to-snapshot Diff compares two stored indexes and never
	// touches the filesystem, so it cannot answer the question at all -- and an
	// empty Skipped there would assert "nothing was skipped", which is precisely
	// the fail-open this field exists to prevent. False means "unknown";
	// DiffPayload omits the key entirely rather than emitting an empty list.
	SkippedKnown bool
	// SkippedTruncated forwards ir.Stats.SkippedTruncated: the skip list hit its
	// cap, so absence from Skipped no longer implies "indexed".
	SkippedTruncated bool
}
