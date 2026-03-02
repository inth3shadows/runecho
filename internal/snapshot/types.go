package snapshot

import "time"

// SnapshotMeta describes a stored IR snapshot (no file/symbol data).
type SnapshotMeta struct {
	ID        int64
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
