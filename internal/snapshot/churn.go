package snapshot

import (
	"fmt"
	"sort"
	"strings"
)

// Churn computes file and symbol churn across the last n snapshots for repoID.
// Returns an empty ChurnReport (no error) when fewer than 2 snapshots exist.
func (db *DB) Churn(repoID int64, n int) (ChurnReport, error) {
	metas, err := db.List(repoID, n)
	if err != nil {
		return ChurnReport{}, fmt.Errorf("list snapshots: %w", err)
	}

	if len(metas) < 2 {
		report := ChurnReport{SnapshotCount: len(metas)}
		if len(metas) == 1 {
			report.Root = metas[0].Root
		}
		return report, nil
	}

	// List returns newest-first; reverse to chronological order for sequential diffs.
	for i, j := 0, len(metas)-1; i < j; i, j = i+1, j-1 {
		metas[i], metas[j] = metas[j], metas[i]
	}

	diffCount := len(metas) - 1
	fileChanges := make(map[string]int)
	symbolChanges := make(map[string]int) // key: "path\x00kind\x00name"

	for i := 0; i < diffCount; i++ {
		diff, err := db.Diff(metas[i], metas[i+1])
		if err != nil {
			return ChurnReport{}, fmt.Errorf("diff snapshots %d→%d: %w", metas[i].ID, metas[i+1].ID, err)
		}
		for _, fd := range diff.Files {
			if fd.Status != "unchanged" {
				fileChanges[fd.Path]++
			}
			for _, sym := range fd.Added {
				key := fd.Path + "\x00" + sym.Kind + "\x00" + sym.Name
				symbolChanges[key]++
			}
			for _, sym := range fd.Removed {
				key := fd.Path + "\x00" + sym.Kind + "\x00" + sym.Name
				symbolChanges[key]++
			}
			for _, sym := range fd.Modified {
				key := fd.Path + "\x00" + sym.Kind + "\x00" + sym.Name
				symbolChanges[key]++
			}
		}
	}

	// Build FileChurn slice.
	files := make([]FileChurn, 0, len(fileChanges))
	for path, changes := range fileChanges {
		files = append(files, FileChurn{Path: path, Changes: changes, DiffCount: diffCount})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Changes != files[j].Changes {
			return files[i].Changes > files[j].Changes
		}
		return files[i].Path < files[j].Path
	})

	// Build SymbolChurn slice.
	symbols := make([]SymbolChurn, 0, len(symbolChanges))
	for key, changes := range symbolChanges {
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) != 3 {
			continue
		}
		symbols = append(symbols, SymbolChurn{
			FilePath:  parts[0],
			Kind:      parts[1],
			Name:      parts[2],
			Changes:   changes,
			DiffCount: diffCount,
		})
	}
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].Changes != symbols[j].Changes {
			return symbols[i].Changes > symbols[j].Changes
		}
		return symbols[i].Name < symbols[j].Name
	})

	return ChurnReport{
		Root:          metas[len(metas)-1].Root, // newest snapshot's root path (informational)
		SnapshotCount: len(metas),
		DiffCount:     diffCount,
		Since:         metas[0].Timestamp,
		Until:         metas[len(metas)-1].Timestamp,
		Files:         files,
		Symbols:       symbols,
	}, nil
}

// ChurnPayload converts a ChurnReport into the canonical JSON-friendly map for
// `runecho-ir churn --json`, mirroring DiffPayload's shape convention: snake_case
// top-level keys, entries filtered to minChanges (matching FormatChurn's "hot"
// threshold) so the JSON and text outputs agree on what counts as hot.
func ChurnPayload(r ChurnReport, minChanges int) map[string]interface{} {
	hotFiles := make([]FileChurn, 0, len(r.Files))
	for _, f := range r.Files {
		if f.Changes >= minChanges {
			hotFiles = append(hotFiles, f)
		}
	}
	hotSymbols := make([]SymbolChurn, 0, len(r.Symbols))
	for _, s := range r.Symbols {
		if s.Changes >= minChanges {
			hotSymbols = append(hotSymbols, s)
		}
	}
	return map[string]interface{}{
		"summary":        FormatChurnCompact(r, minChanges),
		"snapshot_count": r.SnapshotCount,
		"diff_count":     r.DiffCount,
		"since":          r.Since,
		"until":          r.Until,
		"min_changes":    minChanges,
		"hot_files":      hotFiles,
		"hot_symbols":    hotSymbols,
	}
}

// FormatChurn formats a full churn report, omitting entries below minChanges.
func FormatChurn(r ChurnReport, minChanges int) string {
	if r.SnapshotCount < 2 {
		return fmt.Sprintf("CHURN: insufficient snapshots (need ≥ 2, have %d)", r.SnapshotCount)
	}

	since := r.Since.Format("2006-01-02")
	until := r.Until.Format("2006-01-02")
	header := fmt.Sprintf("CHURN REPORT [%d snapshots → %d diffs, %s → %s]\n",
		r.SnapshotCount, r.DiffCount, since, until)

	var sb strings.Builder
	sb.WriteString(header)

	// Hot files.
	hotFiles := make([]FileChurn, 0, len(r.Files))
	for _, f := range r.Files {
		if f.Changes >= minChanges {
			hotFiles = append(hotFiles, f)
		}
	}
	if len(hotFiles) > 0 {
		sb.WriteString(fmt.Sprintf("\nHot files (changed in %d+ diffs):\n", minChanges))
		for _, f := range hotFiles {
			sb.WriteString(fmt.Sprintf("  %-40s %d/%d\n", f.Path, f.Changes, f.DiffCount))
		}
	}

	// Hot symbols.
	hotSymbols := make([]SymbolChurn, 0, len(r.Symbols))
	for _, s := range r.Symbols {
		if s.Changes >= minChanges {
			hotSymbols = append(hotSymbols, s)
		}
	}
	if len(hotSymbols) > 0 {
		sb.WriteString(fmt.Sprintf("\nHot symbols (changed in %d+ diffs):\n", minChanges))
		for _, s := range hotSymbols {
			sb.WriteString(fmt.Sprintf("  %-30s (%s)  %d/%d  %s\n",
				s.Name, s.Kind, s.Changes, s.DiffCount, s.FilePath))
		}
	}

	// r.Files only holds files that changed at least once, so the non-hot remainder
	// are files that changed but stayed below the hot threshold — NOT files that
	// never changed (those are never tracked). Label it honestly.
	coolCount := len(r.Files) - len(hotFiles)
	if coolCount > 0 {
		sb.WriteString(fmt.Sprintf("\nBelow threshold: %d %s changed in fewer than %d of %d diffs.\n",
			coolCount, pluralWord(coolCount, "file"), minChanges, r.DiffCount))
	}

	return sb.String()
}

// FormatChurnCompact returns a single-line churn summary. minChanges is the
// same hotness threshold the detailed formats use — previously hardcoded to 2
// here, which made the JSON "summary" line contradict its own hot_files list
// at any non-default --min-changes.
func FormatChurnCompact(r ChurnReport, minChanges int) string {
	if r.SnapshotCount < 2 {
		return fmt.Sprintf("CHURN: insufficient snapshots (need ≥ 2, have %d)", r.SnapshotCount)
	}

	since := r.Since.Format("2006-01-02")
	until := r.Until.Format("2006-01-02")

	hotFiles, hotSymbols := 0, 0
	for _, f := range r.Files {
		if f.Changes >= minChanges {
			hotFiles++
		}
	}
	for _, s := range r.Symbols {
		if s.Changes >= minChanges {
			hotSymbols++
		}
	}

	return fmt.Sprintf("CHURN: %d hot %s, %d hot %s across %d diffs (%s → %s)",
		hotFiles, pluralWord(hotFiles, "file"),
		hotSymbols, pluralWord(hotSymbols, "symbol"),
		r.DiffCount, since, until)
}
