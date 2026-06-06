package snapshot

import (
	"math"
	"time"
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

// SymbolDelta is a single symbol added or removed.
type SymbolDelta struct {
	Name string
	Kind string // "function" | "class" | "export" | "import"
}

// FileDiff is the structural diff for one file between two snapshots.
type FileDiff struct {
	Path       string
	Status     string // "added" | "removed" | "modified" | "unchanged"
	HashBefore string
	HashAfter  string
	Added      []SymbolDelta
	Removed    []SymbolDelta
}

// DiffResult is the full structural diff between two snapshots.
type DiffResult struct {
	SnapshotA    SnapshotMeta
	SnapshotB    SnapshotMeta
	Files        []FileDiff
	TotalAdded   int
	TotalRemoved int
}
