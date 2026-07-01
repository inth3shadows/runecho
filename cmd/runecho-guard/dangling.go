package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// E1 dangling-refs: warn when an edit removes a symbol definition that other
// files still reference. The mirror of the additive hallucination check — that
// one asks "this symbol doesn't exist", this one asks "this symbol still has
// callers". Read-only against the latest snapshot's V6 refs index, ask-posture,
// and gated OFF by default so the behavior is dogfooded before becoming default
// (same discipline as C3 learned-allow and the E6 auto-fresh gate).

// maxDanglingReferrers caps how many referrer paths are listed per symbol in the
// ask message — enough to be actionable without flooding the prompt.
const maxDanglingReferrers = 5

// danglingEnabled reports whether E1 checking is on (RUNECHO_GUARD_DANGLING=1).
// When unset, the whole E1 path is skipped — no extra store read, no behavior
// change — so it is inert until explicitly enabled for dogfooding.
func danglingEnabled() bool { return os.Getenv("RUNECHO_GUARD_DANGLING") == "1" }

// droppedImportEnabled reports whether the dropped-import check is on
// (RUNECHO_GUARD_DROPPED_IMPORT=1). Same dogfood-first discipline as E1: inert
// until explicitly enabled, so it can be exercised before becoming default.
func droppedImportEnabled() bool { return os.Getenv("RUNECHO_GUARD_DROPPED_IMPORT") == "1" }

// danglingWarning is one removed definition that is still referenced elsewhere.
type danglingWarning struct {
	Symbol    string
	Referrers []string // repo-relative paths other than the edited file (capped)
}

// hookOldText returns the text being REMOVED by the given tool — the inverse of
// hookText. For Edit it is old_string; for MultiEdit, every edit's old_string
// concatenated. Write is handled by the caller (it has no old_string; the
// pre-edit on-disk file is the authority) and yields "" here.
func hookOldText(toolName, oldString string, edits []editOp) string {
	switch toolName {
	case "Edit":
		return oldString
	case "MultiEdit":
		var parts []string
		for _, e := range edits {
			if e.OldString != "" {
				parts = append(parts, e.OldString)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// deletedDefs returns the top-level definitions present in oldText but absent
// from newText — the symbols this edit removes. A name in both is an in-place
// redefinition (signature edit, move within the hunk), not a deletion, and is
// excluded. Returns nil (and does no work) when oldText defines nothing, so the
// common no-deletion edit short-circuits before any caller touches the store.
func deletedDefs(lang guard.Lang, oldText, newText string) []string {
	old := defSet(lang, oldText)
	if len(old) == 0 {
		return nil
	}
	kept := defSet(lang, newText)
	var out []string
	for d := range old {
		if _, ok := kept[d]; !ok {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// defSet extracts the top-level definition names from text into a set, using the
// same extractor (and therefore the same symbol granularity) as the additive
// check's addInFileDefs.
func defSet(lang guard.Lang, text string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, d := range guard.ExtractDefs(lang, guard.TextToAddedLines(text)) {
		m[d] = struct{}{}
	}
	return m
}

// checkDanglingRefs returns one warning per deleted def that is still referenced
// by a file OTHER than the one being edited, per the latest snapshot's refs
// index. The self-exclusion is the key false-positive killer: deleting a def
// together with its only (same-file) uses is legitimate and stays silent; only a
// cross-file referrer is the real "you'll break callers" signal.
//
// Fail-open everywhere: a missing store, unenrolled repo, no snapshot, or any
// query error yields nil (no warning), never a false block. An empty deleted
// set short-circuits before opening the store.
func checkDanglingRefs(dir, filePath string, deleted []string) []danglingWarning {
	if len(deleted) == 0 {
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
	// OpenFast skips the on-open integrity scan — this fires only on deletion
	// edits, but still on a latency-sensitive path; integrity is the writer's job.
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

	var warns []danglingWarning
	for _, d := range deleted {
		paths, err := db.RefsToName(snapID, d)
		if err != nil {
			continue // skip this symbol, keep checking the rest
		}
		others := excludeSelf(paths, self)
		if len(others) > 0 {
			if len(others) > maxDanglingReferrers {
				others = others[:maxDanglingReferrers]
			}
			warns = append(warns, danglingWarning{Symbol: d, Referrers: others})
		}
	}
	return warns
}

// repoRelPath renders filePath in the same form the refs index stores file paths
// (repo-relative, forward-slash, NFC-normalized — see ir.normalizePath). It uses
// the edited file's own worktree top as the base: a file's path relative to its
// worktree top matches its path relative to the enrolled source root (worktrees
// share the tree layout), so self-exclusion works even across linked worktrees —
// the cross-worktree footgun that bit E6. Returns "" if the base can't be
// resolved, which simply disables self-exclusion for this call.
func repoRelPath(filePath string) string {
	top, err := gitutil.TopLevel(filepath.Dir(filePath))
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(top, filePath)
	if err != nil {
		return ""
	}
	return norm.NFC.String(strings.TrimPrefix(filepath.ToSlash(rel), "./"))
}

// excludeSelf returns paths with self removed. When self is "" (base unresolved)
// nothing is excluded — the safe-but-noisier direction (a possible self-only
// false warn) over silently dropping a real cross-file referrer.
func excludeSelf(paths []string, self string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if self != "" && p == self {
			continue
		}
		out = append(out, p)
	}
	return out
}

// askReason names the decision-log reason for an ask so the dogfood stream is
// greppable by which check(s) fired. Joins the active checks with '+' so any
// combination is represented (e.g. "violations+dropped-import").
func askReason(hasViolations, hasDangling, hasDropped, hasDuplicate bool) string {
	var parts []string
	if hasViolations {
		parts = append(parts, "violations")
	}
	if hasDangling {
		parts = append(parts, "dangling")
	}
	if hasDropped {
		parts = append(parts, "dropped-import")
	}
	if hasDuplicate {
		parts = append(parts, "duplicate-symbol")
	}
	if len(parts) == 0 {
		return "violations"
	}
	return strings.Join(parts, "+")
}
