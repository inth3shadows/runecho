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
//
// The result carries no skip information (SkippedKnown stays false). Callers
// that HAVE the walk's ir.Stats -- every caller that just built the live IR --
// should use DiffLiveWithStats instead, so the payload can say which files the
// indexer declined. This signature is kept for callers that genuinely do not
// have the stats, and because silently reporting "nothing skipped" for them
// would be a fail-open.
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

// DiffLiveWithStats is DiffLive plus the skip report from the walk that produced
// liveIR.
//
// The two arguments must come from the SAME walk. A diff says what changed among
// the files the indexer read; the stats say which files it never read. Pairing a
// diff with a different walk's stats would answer "was my file examined?" about
// the wrong tree -- which is worse than not answering, because the answer looks
// authoritative.
func (db *DB) DiffLiveWithStats(a SnapshotMeta, liveIR *ir.IR, stats ir.Stats) (DiffResult, error) {
	res, err := db.DiffLive(a, liveIR)
	if err != nil {
		return res, err
	}
	res.Skipped = stats.Skipped
	res.SkippedKnown = true
	res.SkippedTruncated = stats.SkippedTruncated
	return res, nil
}

// irToMaps converts an IR into the file and symbol maps used by computeDiff.
func irToMaps(irData *ir.IR) (map[string]string, map[string][]SymbolDelta) {
	files := make(map[string]string, len(irData.Files))
	symbols := make(map[string][]SymbolDelta, len(irData.Files))

	for path, file := range irData.Files {
		files[path] = file.Hash
		// Internal kinds are excluded so a diff reports what a reader would call a
		// change. Before these kinds existed an unexported helper or a struct field
		// could not appear here at all; including them now would make every diff
		// noisier without reporting anything the tool previously promised.
		deltas := make([]SymbolDelta, 0, len(file.Symbols))
		for _, s := range file.Symbols {
			if ir.InternalKinds[s.Kind] {
				continue
			}
			deltas = append(deltas, SymbolDelta{Name: s.Name, Kind: s.Kind, Hash: s.Hash})
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
	totalAdded, totalRemoved, totalModified := 0, 0, 0

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
			aSym, exists := aSymSet[key]
			switch {
			case !exists:
				fd.Added = append(fd.Added, sym)
				totalAdded++
			case aSym.Hash != "" && sym.Hash != "" && aSym.Hash != sym.Hash:
				// Present in both, body hash changed in place. Only flagged when
				// both sides carry a hash, so a cross-version diff (one side has
				// no hash) never produces a false "modified".
				fd.Modified = append(fd.Modified, sym)
				totalModified++
			}
		}
		for key, sym := range aSymSet {
			if _, exists := bSymSet[key]; !exists {
				fd.Removed = append(fd.Removed, sym)
				totalRemoved++
			}
		}
		sort.Slice(fd.Added, func(i, j int) bool { return lessSymbolDelta(fd.Added[i], fd.Added[j]) })
		sort.Slice(fd.Removed, func(i, j int) bool { return lessSymbolDelta(fd.Removed[i], fd.Removed[j]) })
		sort.Slice(fd.Modified, func(i, j int) bool { return lessSymbolDelta(fd.Modified[i], fd.Modified[j]) })

		fileDiffs = append(fileDiffs, fd)
	}

	return DiffResult{
		SnapshotA:     a,
		SnapshotB:     b,
		Files:         fileDiffs,
		TotalAdded:    totalAdded,
		TotalRemoved:  totalRemoved,
		TotalModified: totalModified,
	}
}

// lessSymbolDelta is a total ordering over SymbolDelta for stable, deterministic
// diff output. Name alone is NOT a total order: a single exported symbol can be
// stored under two kinds (e.g. "export:foo" and "function:foo"), so two deltas
// can share a Name. Sorting on Name only leaves those tied, and sort.Slice is not
// stable, so their order — and the resulting `diff --json` bytes — varied per run
// on identical input, breaking runecho's determinism guarantee. Tie-break on Kind.
func lessSymbolDelta(a, b SymbolDelta) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.Kind < b.Kind
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

	// Count every changed file — added, removed, and modified. The label is
	// "changed", not "modified": an added or removed file is a change but not a
	// modification, so labeling this total "modified" over-counted modifications
	// (#141). The per-modification symbol total stays "~%d".
	changedCount := 0
	for _, f := range d.Files {
		if f.Status == "modified" || f.Status == "added" || f.Status == "removed" {
			changedCount++
		}
	}

	addStr := plural(d.TotalAdded, "symbol")
	remStr := plural(d.TotalRemoved, "symbol")
	fileStr := plural(changedCount, "file")

	if d.TotalModified > 0 {
		return fmt.Sprintf("IR DIFF [%s→%s]: +%s, -%s, ~%d, %s changed",
			aShort, bShort, addStr, remStr, d.TotalModified, fileStr)
	}
	return fmt.Sprintf("IR DIFF [%s→%s]: +%s, -%s, %s changed",
		aShort, bShort, addStr, remStr, fileStr)
}

// DiffPayload converts a DiffResult into the canonical JSON-friendly map shared
// by the `runecho-ir diff --json` CLI flag and the MCP `diff` oracle tool. Both
// surfaces marshal this single shape so a machine consumer (e.g. the harness
// gate) sees identical output regardless of entry point — they cannot drift.
// (The MCP adds a "repo" key on top of this base before marshalling.)
func DiffPayload(d DiffResult) map[string]interface{} {
	// Normalize nil → empty slice so a zero-drift diff marshals "files": []
	// rather than "files": null. The contract is consumed by machines (the
	// harness gate), and an array consumer must never have to null-guard.
	files := d.Files
	if files == nil {
		files = []FileDiff{}
	}
	payload := map[string]interface{}{
		"summary":        FormatCompact(d),
		"total_added":    d.TotalAdded,
		"total_removed":  d.TotalRemoved,
		"total_modified": d.TotalModified,
		"files":          files,
	}
	// `skipped` answers a question `files` cannot: which paths the indexer never
	// read, and therefore where a deleted symbol would be invisible at exit 0
	// with empty stderr. It is present ONLY when a live walk actually measured
	// it. A snapshot-to-snapshot diff never walks the filesystem, so emitting
	// `"skipped": []` there would tell a consumer "nothing was skipped" on the
	// strength of never having looked -- the key's absence is the honest answer,
	// and consumers must treat absent as "unknown", not as "none".
	if d.SkippedKnown {
		// TWO arrays, not one, and the split is the contract.
		//
		// `skipped` is exact paths of FILES the indexer could not read: the blind
		// spots, the thing to fail closed on. `ignored_paths` is DIRECTORIES it
		// was told (or chose, for safety) not to enter: prefix-matched, and
		// informational because the operator configured the ignore list on
		// purpose.
		//
		// The first draft merged them, and the merge was the defect: `.git` is in
		// DefaultIgnoredPaths, so every repo produced a non-empty list, and a
		// gate prefix-matching it blocked an edit to `testdata/README.md`. That is
		// the documentation-only false block the language table exists to
		// prevent, arriving through the other reason code.
		capability, policy := SplitSkips(d.Skipped)
		payload["skipped"] = capability
		payload["ignored_paths"] = policy
		payload["skipped_truncated"] = d.SkippedTruncated
	}
	return payload
}

// SplitSkips partitions a skip list into capability skips (files the indexer
// could not read) and policy skips (directories it did not enter). Both are
// non-nil so a machine consumer never has to null-guard.
func SplitSkips(all []ir.SkippedFile) (capability, policy []ir.SkippedFile) {
	capability, policy = []ir.SkippedFile{}, []ir.SkippedFile{}
	for _, sk := range all {
		if ir.IsPolicySkip(sk.Reason) {
			policy = append(policy, sk)
		} else {
			capability = append(capability, sk)
		}
	}
	return capability, policy
}

// TruncateSkips returns d with its skip list clipped to at most n entries,
// flagging the truncation. For surfaces with a tighter budget than the payload
// cap — the MCP oracle, whose whole purpose is context economy, would otherwise
// inject up to MaxRecordedSkips objects into an agent's context on every call
// against a C or Java repo.
func TruncateSkips(d DiffResult, n int) DiffResult {
	if !d.SkippedKnown || len(d.Skipped) <= n {
		return d
	}
	// Copy: d.Skipped aliases the recorder's backing array, and a re-slice that
	// the caller then treats as complete is exactly the "absence means indexed"
	// fail-open this whole feature exists to close.
	clipped := make([]ir.SkippedFile, n)
	copy(clipped, d.Skipped[:n])
	d.Skipped = clipped
	d.SkippedTruncated = true
	return d
}

// maxShownSkips bounds each list in human output. The machine payload carries
// the whole list (up to its own cap); a terminal does not need it.
const maxShownSkips = 20

// writeSkipBlock renders one labelled group of skips, or nothing when empty.
func writeSkipBlock(sb *strings.Builder, header string, entries []ir.SkippedFile, truncated bool) {
	if len(entries) == 0 {
		return
	}
	count := plural(len(entries), "path")
	if truncated {
		// "1000 paths" on a capped list reads as a total and contradicts the
		// warning two lines down. The list is a floor, so say so.
		count = fmt.Sprintf("%d+ paths", len(entries))
	}
	fmt.Fprintf(sb, "\n%s (%s):\n", header, count)
	for i, sk := range entries {
		if i >= maxShownSkips {
			fmt.Fprintf(sb, "  ... and %d more (use --json for the full list)\n", len(entries)-maxShownSkips)
			break
		}
		fmt.Fprintf(sb, "  %s  [%s]\n", sk.Path, sk.Reason)
	}
	if truncated {
		fmt.Fprintf(sb, "  WARNING: the skip list hit its cap (%d); other paths may also have gone unexamined.\n",
			ir.MaxRecordedSkips)
	}
}

// formatSkipped renders the paths the indexer declined, or "" when there are
// none or when this diff mode could not measure them.
//
// Deliberately loud, and deliberately attached to BOTH branches of FormatFull:
// the zero-drift branch prints "No structural changes", which for a repo in an
// unindexed language is true only in the sense that nothing was ever looked at.
// That sentence unqualified is the entire failure this reporting exists to end.
//
// The two groups are rendered separately for the same reason the payload
// carries two arrays: a pruned `vendor/` is not the same claim as an unreadable
// `Widget.java`, and running them together is what made the merged list
// unusable.
func formatSkipped(d DiffResult) string {
	if !d.SkippedKnown {
		return ""
	}
	capability, policy := SplitSkips(d.Skipped)
	// The human surface drops plain `ignored_dir` entries. They are the
	// operator's OWN configuration echoed back -- `.git` is in
	// DefaultIgnoredPaths, so every repo would print a NOT ENTERED block on every
	// diff, and a note that fires every time stops being read. What survives is
	// what the operator did NOT configure: a directory that turned out to be a
	// symlink, or one we could not read. The full list, ignored_dir included,
	// stays in --json, where a consumer applies its own policy.
	surprises := make([]ir.SkippedFile, 0, len(policy))
	for _, sk := range policy {
		if sk.Reason != ir.SkipIgnoredDir {
			surprises = append(surprises, sk)
		}
	}
	if len(capability) == 0 && len(surprises) == 0 && !d.SkippedTruncated {
		return ""
	}
	var sb strings.Builder
	writeSkipBlock(&sb, "NOT EXAMINED — a symbol removed here would be invisible to this diff",
		capability, d.SkippedTruncated)
	writeSkipBlock(&sb, "NOT ENTERED — a directory the walk could not descend", surprises, false)
	return sb.String()
}

// FormatFull returns a human-readable per-file breakdown.
func FormatFull(d DiffResult) string {
	if len(d.Files) == 0 {
		// Terminated with exactly one newline, with or without a skip block.
		//
		// Both readings of this have been wrong once. The original
		// "...changes.\n%s" appended a blank line on the common clean-repo path;
		// the over-correction dropped the terminator entirely, so `diff` and
		// `verify` (both fmt.Print) ran the shell prompt onto the output's last
		// line and handed line-oriented consumers a partial final line. The skip
		// block already begins with its own "\n" and ends with one.
		base := fmt.Sprintf("IR DIFF  %s... → %s...\n\nNo structural changes.\n",
			shortHash(d.SnapshotA.RootHash),
			shortHash(d.SnapshotB.RootHash),
		)
		return base + formatSkipped(d)
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
			for _, sym := range f.Modified {
				fmt.Fprintf(&sb, "    ~ %s\n", sym.Name)
			}
		}
	}

	writeGroup("modified", groups["modified"])
	writeGroup("added", groups["added"])
	writeGroup("removed", groups["removed"])

	fmt.Fprintf(&sb, "\nSummary: +%s, -%s, ~%s across %s\n",
		plural(d.TotalAdded, "symbol"),
		plural(d.TotalRemoved, "symbol"),
		plural(d.TotalModified, "symbol"),
		plural(len(d.Files), "file"),
	)
	sb.WriteString(formatSkipped(d))
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
