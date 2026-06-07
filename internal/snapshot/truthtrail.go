package snapshot

import (
	"fmt"
	"sort"
	"strings"

	"github.com/inth3shadows/runecho/internal/claims"
	"github.com/inth3shadows/runecho/internal/ir"
)

// StaleClaim is a symbol referenced in prose that no longer exists in the live IR.
type StaleClaim struct {
	Symbol  string
	Context string // source line excerpt (≤80 bytes)
}

// TrailResult is the fused change receipt produced by TruthTrail.
type TrailResult struct {
	SnapshotRef SnapshotMeta
	Diff        DiffResult
	// Callers maps each removed-symbol name to the files that referenced it
	// in the baseline snapshot (V6+ only; empty for older snapshots).
	Callers map[string][]string
	// FileHot maps file path → change count for files that changed in ≥2 diffs.
	// Files absent from the map were stable across the churn window.
	FileHot    map[string]int
	ChurnDiffs int // total diff count analyzed; 0 = insufficient history
	// StaleClaims is non-nil only when a text was provided to TruthTrail.
	StaleClaims []StaleClaim
}

// TruthTrail builds a fused change receipt for repoID using baseline meta and
// live IR. churnN controls the lookback window (0 → 20). text is prose to
// check for stale symbol refs; empty string skips that section.
func TruthTrail(db *DB, repoID int64, meta SnapshotMeta, liveIR *ir.IR, churnN int, text string) (TrailResult, error) {
	result := TrailResult{SnapshotRef: meta}

	// 1. Structural diff.
	diff, err := db.DiffLive(meta, liveIR)
	if err != nil {
		return result, fmt.Errorf("diff: %w", err)
	}
	result.Diff = diff

	// 2. Callers for removed symbols from the baseline snapshot.
	result.Callers = make(map[string][]string)
	for _, fd := range diff.Files {
		for _, sym := range fd.Removed {
			if _, seen := result.Callers[sym.Name]; seen {
				continue
			}
			callers, err := db.RefsToName(meta.ID, sym.Name)
			if err != nil {
				return result, fmt.Errorf("refs for %q: %w", sym.Name, err)
			}
			result.Callers[sym.Name] = callers
		}
	}

	// 3. Churn context.
	n := churnN
	if n <= 0 {
		n = 20
	}
	churn, err := db.Churn(repoID, n)
	if err != nil {
		return result, fmt.Errorf("churn: %w", err)
	}
	result.FileHot = make(map[string]int)
	result.ChurnDiffs = churn.DiffCount
	for _, fc := range churn.Files {
		if fc.Changes >= 2 {
			result.FileHot[fc.Path] = fc.Changes
		}
	}

	// 4. Stale claims (optional — only when text was supplied).
	if text != "" {
		known := liveSymbolSet(liveIR)
		refs := claims.ExtractSymbolRefs(text)
		for sym, ctx := range refs {
			if !known[sym] {
				result.StaleClaims = append(result.StaleClaims, StaleClaim{Symbol: sym, Context: ctx})
			}
		}
		sort.Slice(result.StaleClaims, func(i, j int) bool {
			return result.StaleClaims[i].Symbol < result.StaleClaims[j].Symbol
		})
	}

	return result, nil
}

// liveSymbolSet returns the set of all declared symbol names in liveIR.
func liveSymbolSet(liveIR *ir.IR) map[string]bool {
	set := make(map[string]bool)
	for _, file := range liveIR.Files {
		for _, name := range file.Functions {
			set[name] = true
		}
		for _, name := range file.Classes {
			set[name] = true
		}
		for _, name := range file.Exports {
			set[name] = true
		}
	}
	return set
}

// FormatTrail formats a TrailResult as a human-readable change receipt.
func FormatTrail(r TrailResult) string {
	label := r.SnapshotRef.Label
	if label == "" {
		label = fmt.Sprintf("snapshot-%d", r.SnapshotRef.ID)
	}
	ts := r.SnapshotRef.Timestamp.Format("2006-01-02")

	var sb strings.Builder
	fmt.Fprintf(&sb, "TRUTH-TRAIL  %s → live  %s\n", label, ts)

	if len(r.Diff.Files) == 0 && len(r.StaleClaims) == 0 {
		fmt.Fprintf(&sb, "\nNo structural changes since %s.\n", label)
		return sb.String()
	}

	if len(r.Diff.Files) > 0 {
		fmt.Fprintf(&sb, "\nCHANGED  %s  +%s  -%s\n",
			plural(len(r.Diff.Files), "file"),
			plural(r.Diff.TotalAdded, "symbol"),
			plural(r.Diff.TotalRemoved, "symbol"),
		)
		for _, fd := range r.Diff.Files {
			hotTag := hotLabel(r.FileHot[fd.Path], r.ChurnDiffs)
			fmt.Fprintf(&sb, "  %s%s\n", fd.Path, hotTag)
			for _, sym := range fd.Added {
				fmt.Fprintf(&sb, "    + %s\n", sym.Name)
			}
			for _, sym := range fd.Removed {
				callers := r.Callers[sym.Name]
				if len(callers) == 0 {
					fmt.Fprintf(&sb, "    - %s\n", sym.Name)
				} else {
					shown := callers
					suffix := ""
					if len(shown) > 3 {
						shown = callers[:3]
						suffix = fmt.Sprintf(" (+%d more)", len(callers)-3)
					}
					fmt.Fprintf(&sb, "    - %s  <- callers: %s%s\n",
						sym.Name, strings.Join(shown, ", "), suffix)
				}
			}
		}
	}

	if len(r.StaleClaims) > 0 {
		sb.WriteString("\nSTALE CLAIMS:\n")
		for _, sc := range r.StaleClaims {
			fmt.Fprintf(&sb, "  %-30s  %q\n", sc.Symbol, sc.Context)
		}
	}

	return sb.String()
}

// hotLabel returns the churn annotation for a file: "[HOT n/total]", "[stable]",
// or "" when there is no churn history.
func hotLabel(changes, totalDiffs int) string {
	if totalDiffs == 0 {
		return ""
	}
	if changes >= 2 {
		return fmt.Sprintf("  [HOT %d/%d]", changes, totalDiffs)
	}
	return "  [stable]"
}
