package main

import (
	"os"
	"path"
	"sort"
	"strings"

	"github.com/inth3shadows/runecho/internal/guard"
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
//
// definitive distinguishes WHY the text came back empty, because the two
// causes need opposite treatment downstream. A missing file (new file being
// created) means "nothing was defined before" — "" is the correct, complete
// answer, safe for addedDefs to treat as ground truth (definitive=true). An
// existing file that's unreadable or exceeds the cap means "we don't know
// what was defined before" — treating that "" as ground truth would make
// addedDefs report every def in the edit as newly added, including ones the
// file already had (false positives on exactly the large/generated files a
// dogfood-first guard should stay quietest on), so the caller must skip the
// check entirely instead (definitive=false).
func wholeFileText(filePath string) (text string, definitive bool) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", os.IsNotExist(err)
	}
	if len(data) > maxInFileBytes {
		return "", false
	}
	return string(data), true
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
// index. Shares openLatestSnapshot with checkDanglingRefs — only the query
// (DefsOfName vs RefsToName) and the warning shape differ. queryErrs mirrors
// checkDanglingRefs: per-symbol store queries that failed and were skipped, so
// zero warnings + queryErrs > 0 must not read as a definitive clean pass.
func checkDuplicateDefs(dir, filePath string, added []string) (warns []duplicateWarning, queryErrs int) {
	if len(added) == 0 {
		return nil, 0
	}
	// Test files legitimately reuse symbol names across files (test functions,
	// fixtures, table-driven helpers named after what they cover), so a symbol
	// "introduced" in a test file that also exists in another test file is almost
	// always a false positive, not a reimplementation. Skip the check for them —
	// this was ~95% of the dogfood asks.
	if isTestFile(filePath) {
		return nil, 0
	}
	db, snapID, self, ok, degraded := openLatestSnapshot(dir, filePath)
	if !ok {
		if degraded {
			// Store-level failure (unreadable store, failed List, schema-newer):
			// the check could not run at all — count it so zero warnings does not
			// read as a clean pass (#138), mirroring the per-symbol queryErrs below.
			return nil, 1
		}
		return nil, 0
	}
	defer db.Close()

	for _, a := range added {
		paths, err := db.DefsOfName(snapID, a)
		if err != nil {
			queryErrs++
			continue // fail-open for this symbol, keep checking the rest
		}
		others := duplicateCandidates(excludeSelf(paths, self), self)
		if len(others) > 0 {
			if len(others) > maxDuplicateLocations {
				others = others[:maxDuplicateLocations]
			}
			warns = append(warns, duplicateWarning{Symbol: a, Locations: others})
		}
	}
	return warns, queryErrs
}

// duplicateCandidates keeps only the other-file definitions that are a genuine
// duplicate of a symbol added to the edited file: in the SAME directory (a proxy
// for the same package/module) and not themselves a test file. A name shared
// across directories is not a duplicate — it is an unrelated same-named symbol in
// another package (every Go binary defines `main`; many packages define `Load` /
// `New` / `String`), which for Go the compiler already keeps distinct and which
// dominated the dogfood false positives. Within a single directory two files
// sharing a top-level name is a real duplicate (a Go compile error the check
// surfaces early; a genuine JS/Py reimplementation).
func duplicateCandidates(others []string, self string) []string {
	selfDir := path.Dir(self)
	var out []string
	for _, o := range others {
		if path.Dir(o) == selfDir && !isTestFile(o) {
			out = append(out, o)
		}
	}
	return out
}

// isTestFile reports whether a path is a test file for its language — the class
// of file whose symbol names (Test*/spec/fixture helpers) recur across files by
// convention rather than by reimplementation. Accepts absolute or repo-relative
// paths (both use '/' on the supported platforms).
func isTestFile(p string) bool {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"): // Go
		return true
	case strings.HasSuffix(base, "_test.py") || strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"): // Python
		return true
	}
	// JS/TS: *.test.* / *.spec.* (before the extension).
	for _, infix := range []string{".test.", ".spec."} {
		if strings.Contains(base, infix) {
			return true
		}
	}
	// A conventional tests/ directory anywhere in the path.
	return strings.HasPrefix(p, "tests/") || strings.Contains(p, "/tests/")
}
