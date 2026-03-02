package snapshot

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/ir"
)

// Diff computes the structural diff between two stored snapshots.
func (db *DB) Diff(a, b SnapshotMeta) (DiffResult, error) {
	aFiles, err := db.loadFilesBySnapshot(a.ID)
	if err != nil {
		return DiffResult{}, fmt.Errorf("load files for snapshot %d: %w", a.ID, err)
	}
	aSymbols, err := db.loadSymbolsBySnapshot(a.ID)
	if err != nil {
		return DiffResult{}, fmt.Errorf("load symbols for snapshot %d: %w", a.ID, err)
	}
	bFiles, err := db.loadFilesBySnapshot(b.ID)
	if err != nil {
		return DiffResult{}, fmt.Errorf("load files for snapshot %d: %w", b.ID, err)
	}
	bSymbols, err := db.loadSymbolsBySnapshot(b.ID)
	if err != nil {
		return DiffResult{}, fmt.Errorf("load symbols for snapshot %d: %w", b.ID, err)
	}
	return computeDiff(a, b, aFiles, bFiles, aSymbols, bSymbols), nil
}

// DiffLive diffs a stored snapshot against the current live IR (not yet saved).
// b is synthesized as a sentinel SnapshotMeta with ID=-1.
func (db *DB) DiffLive(a SnapshotMeta, liveIR *ir.IR) (DiffResult, error) {
	aFiles, err := db.loadFilesBySnapshot(a.ID)
	if err != nil {
		return DiffResult{}, fmt.Errorf("load files for snapshot %d: %w", a.ID, err)
	}
	aSymbols, err := db.loadSymbolsBySnapshot(a.ID)
	if err != nil {
		return DiffResult{}, fmt.Errorf("load symbols for snapshot %d: %w", a.ID, err)
	}

	bFiles, bSymbols := irToMaps(liveIR)
	b := SnapshotMeta{
		ID:        -1,
		SessionID: "(live)",
		Label:     "(live)",
		Timestamp: time.Now().UTC(),
		Root:      a.Root,
		RootHash:  liveIR.RootHash,
		FileCount: len(liveIR.Files),
	}
	return computeDiff(a, b, aFiles, bFiles, aSymbols, bSymbols), nil
}

// irToMaps converts an IR into the file and symbol maps used by computeDiff.
func irToMaps(irData *ir.IR) (map[string]string, map[string][]SymbolDelta) {
	files := make(map[string]string, len(irData.Files))
	symbols := make(map[string][]SymbolDelta, len(irData.Files))

	for path, file := range irData.Files {
		files[path] = file.Hash
		var deltas []SymbolDelta
		for _, name := range file.Functions {
			deltas = append(deltas, SymbolDelta{Name: name, Kind: "function"})
		}
		for _, name := range file.Classes {
			deltas = append(deltas, SymbolDelta{Name: name, Kind: "class"})
		}
		for _, name := range file.Exports {
			deltas = append(deltas, SymbolDelta{Name: name, Kind: "export"})
		}
		for _, name := range file.Imports {
			deltas = append(deltas, SymbolDelta{Name: name, Kind: "import"})
		}
		symbols[path] = deltas
	}
	return files, symbols
}

// computeDiff is the core diff engine shared by Diff and DiffLive.
func computeDiff(
	a, b SnapshotMeta,
	aFiles, bFiles map[string]string,
	aSymbols, bSymbols map[string][]SymbolDelta,
) DiffResult {
	// Union of all paths.
	allPaths := make(map[string]struct{})
	for p := range aFiles {
		allPaths[p] = struct{}{}
	}
	for p := range bFiles {
		allPaths[p] = struct{}{}
	}

	paths := make([]string, 0, len(allPaths))
	for p := range allPaths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var fileDiffs []FileDiff
	totalAdded, totalRemoved := 0, 0

	for _, path := range paths {
		aHash, inA := aFiles[path]
		bHash, inB := bFiles[path]

		var status string
		switch {
		case inA && !inB:
			status = "removed"
		case !inA && inB:
			status = "added"
		case aHash == bHash:
			status = "unchanged"
		default:
			status = "modified"
		}

		if status == "unchanged" {
			continue // skip unchanged files from diff output
		}

		fd := FileDiff{
			Path:       path,
			Status:     status,
			HashBefore: aHash,
			HashAfter:  bHash,
		}

		// Symbol set-diff for added/removed/modified files.
		aSymSet := symbolSet(aSymbols[path])
		bSymSet := symbolSet(bSymbols[path])

		for key, sym := range bSymSet {
			if _, exists := aSymSet[key]; !exists {
				fd.Added = append(fd.Added, sym)
				totalAdded++
			}
		}
		for key, sym := range aSymSet {
			if _, exists := bSymSet[key]; !exists {
				fd.Removed = append(fd.Removed, sym)
				totalRemoved++
			}
		}
		sort.Slice(fd.Added, func(i, j int) bool { return fd.Added[i].Name < fd.Added[j].Name })
		sort.Slice(fd.Removed, func(i, j int) bool { return fd.Removed[i].Name < fd.Removed[j].Name })

		fileDiffs = append(fileDiffs, fd)
	}

	return DiffResult{
		SnapshotA:    a,
		SnapshotB:    b,
		Files:        fileDiffs,
		TotalAdded:   totalAdded,
		TotalRemoved: totalRemoved,
	}
}

// symbolSet converts a slice of SymbolDelta to a map keyed by "kind:name".
func symbolSet(syms []SymbolDelta) map[string]SymbolDelta {
	m := make(map[string]SymbolDelta, len(syms))
	for _, s := range syms {
		m[s.Kind+":"+s.Name] = s
	}
	return m
}

// FormatCompact returns a single-line summary, or "" if there are no changes.
func FormatCompact(d DiffResult) string {
	if len(d.Files) == 0 {
		return ""
	}
	aShort := shortHash(d.SnapshotA.RootHash)
	bShort := shortHash(d.SnapshotB.RootHash)

	modifiedCount := 0
	for _, f := range d.Files {
		if f.Status == "modified" || f.Status == "added" || f.Status == "removed" {
			modifiedCount++
		}
	}

	addStr := plural(d.TotalAdded, "symbol")
	remStr := plural(d.TotalRemoved, "symbol")
	fileStr := plural(modifiedCount, "file")

	return fmt.Sprintf("IR DIFF [%s→%s]: +%s, -%s, %s modified",
		aShort, bShort, addStr, remStr, fileStr)
}

// FormatFull returns a human-readable per-file breakdown.
func FormatFull(d DiffResult) string {
	if len(d.Files) == 0 {
		return fmt.Sprintf("IR DIFF  %s... → %s...\n\nNo structural changes.",
			shortHash(d.SnapshotA.RootHash),
			shortHash(d.SnapshotB.RootHash),
		)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "IR DIFF  %s... → %s...\n",
		shortHash(d.SnapshotA.RootHash),
		shortHash(d.SnapshotB.RootHash),
	)

	// Group by status.
	groups := map[string][]FileDiff{
		"modified": {},
		"added":    {},
		"removed":  {},
	}
	for _, f := range d.Files {
		groups[f.Status] = append(groups[f.Status], f)
	}

	writeGroup := func(label string, files []FileDiff) {
		if len(files) == 0 {
			return
		}
		fmt.Fprintf(&sb, "\n%s (%d %s):\n", strings.ToUpper(label), len(files), pluralWord(len(files), "file"))
		for _, f := range files {
			suffix := ""
			if f.Status == "added" {
				suffix = "  [NEW FILE]"
			} else if f.Status == "removed" {
				suffix = "  [DELETED]"
			}
			fmt.Fprintf(&sb, "  %s%s\n", f.Path, suffix)
			for _, sym := range f.Added {
				fmt.Fprintf(&sb, "    + %s\n", sym.Name)
			}
			for _, sym := range f.Removed {
				fmt.Fprintf(&sb, "    - %s\n", sym.Name)
			}
		}
	}

	writeGroup("modified", groups["modified"])
	writeGroup("added", groups["added"])
	writeGroup("removed", groups["removed"])

	fmt.Fprintf(&sb, "\nSummary: +%s, -%s across %s\n",
		plural(d.TotalAdded, "symbol"),
		plural(d.TotalRemoved, "symbol"),
		plural(len(d.Files), "file"),
	)
	return sb.String()
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func pluralWord(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
