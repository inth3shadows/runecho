package main

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// E5 duplicate-symbol: warn when an edit introduces a NEW symbol definition
// (a name not previously defined anywhere in the edited file) whose name is
// already defined in a DIFFERENT file per the latest snapshot's symbol index.
// Ask-posture ("this may be a duplicate/reimplementation"), never a block —
// same discipline as E1 dangling-refs and dropped-import, and gated OFF by
// default so it is dogfooded before becoming default.

// maxDuplicateLocations caps how many other-file locations are listed per
// symbol in the ask message — mirrors maxDanglingReferrers's rationale.
const maxDuplicateLocations = 5

// duplicateEnabled reports whether E5 checking is on (RUNECHO_GUARD_DUPLICATE=1).
// When unset, the whole E5 path is skipped — no extra store read, no behavior
// change — so it is inert until explicitly enabled for dogfooding.
func duplicateEnabled() bool { return os.Getenv("RUNECHO_GUARD_DUPLICATE") == "1" }

// duplicateWarning is one newly-introduced definition that already exists
// elsewhere in the repo's indexed snapshot.
type duplicateWarning struct {
	Symbol    string
	Locations []string // repo-relative paths other than the edited file (capped)
}

// wholeFileText reads filePath's current on-disk (pre-edit) content, capped at
// maxInFileBytes. addedDefs needs the WHOLE file's prior definitions, not just
// the Edit/MultiEdit hunk being replaced (unlike deletedDefs/DroppedImportRefs,
// whose old/new sides both come from the same hunk and so are internally
// consistent) — otherwise a symbol already defined elsewhere in the same,
// untouched part of the file would be misreported as new. Deliberately
// independent of the shared removedText/oldText used by E1/dropped-import:
// broadening THOSE to whole-file would break their hunk-symmetric comparisons.
// Best-effort: a missing or oversized file yields "" (fail-open).
func wholeFileText(filePath string) string {
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) > maxInFileBytes {
		return ""
	}
	return string(data)
}

// addedDefs returns the definitions present in newText that are NOT present in
// oldText — the mirror of deletedDefs. Here oldText is the whole pre-edit file
// (see wholeFileText), so "absent from oldText" means "not previously defined
// anywhere in this file," matching the check's stated scope. Returns nil (no
// work) when newText defines nothing, so the common non-definitional edit
// short-circuits before touching the store.
func addedDefs(lang guard.Lang, oldText, newText string) []string {
	nu := defSet(lang, newText)
	if len(nu) == 0 {
		return nil
	}
	old := defSet(lang, oldText)
	var out []string
	for d := range nu {
		if _, ok := old[d]; !ok {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// checkDuplicateDefs returns one warning per added def that is already defined
// by a file OTHER than the one being edited, per the latest snapshot's symbol
// index. Structurally identical to checkDanglingRefs (same store-open,
// resolve-repo, latest-snapshot, self-exclusion, cap, fail-open pattern) —
// only the query (DefsOfName vs RefsToName) differs.
func checkDuplicateDefs(dir, filePath string, added []string) []duplicateWarning {
	if len(added) == 0 {
		return nil
	}
	storeDir, err := runechoDir()
	if err != nil {
		return nil
	}
	dbPath := filepath.Join(storeDir, "history.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	db, err := snapshot.OpenFast(dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	repo, _, resolved := db.ResolveRepo(dir)
	if !resolved {
		return nil
	}
	snaps, err := db.List(repo.ID, 1)
	if err != nil || len(snaps) == 0 {
		return nil
	}
	snapID := snaps[0].ID

	self := repoRelPath(filePath)

	var warns []duplicateWarning
	for _, a := range added {
		paths, err := db.DefsOfName(snapID, a)
		if err != nil {
			continue // skip this symbol, keep checking the rest
		}
		others := excludeSelf(paths, self)
		if len(others) > 0 {
			if len(others) > maxDuplicateLocations {
				others = others[:maxDuplicateLocations]
			}
			warns = append(warns, duplicateWarning{Symbol: a, Locations: others})
		}
	}
	return warns
}
