package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// CLI-level coverage for `repo prune` (#351). The store-side retention rules
// are pinned in internal/snapshot/prune_test.go; these pin the wiring — most
// importantly that --dry-run really does not delete.

// seedPrunableStore enrolls a repo directly in the store and gives it n
// reindex snapshots with distinct root hashes. Going through the store rather
// than the CLI keeps these tests off the IR generator, which is not what they
// are about.
func seedPrunableStore(t *testing.T, home, name string, n int) int64 {
	t.Helper()
	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	repoID, err := db.EnrollRepo(name, filepath.Join(home, "src-"+name), "", 0)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	for i := 0; i < n; i++ {
		doc := &ir.IR{
			Version:  1,
			RootHash: strings.Repeat("0", 4) + string(rune('a'+i%26)) + strings.Repeat("x", i),
			Files:    map[string]ir.FileIR{"main.go": {Hash: "h", Symbols: []ir.Symbol{{Name: "Fn", Kind: "function"}}}},
		}
		if _, wrote, err := db.SaveReindexSnapshot(repoID, "", "/tmp/src", doc); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		} else if !wrote {
			t.Fatalf("seed %d was deduped; fixture is not producing distinct snapshots", i)
		}
	}
	return repoID
}

func countReindexInStore(t *testing.T, home string, repoID int64) int {
	t.Helper()
	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	metas, err := db.List(repoID, 10000)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	n := 0
	for _, m := range metas {
		if m.Label == snapshot.ReindexLabel {
			n++
		}
	}
	return n
}

// TestRepoPrune_DryRunDeletesNothing is the safety contract. A preview that
// deletes is worse than no preview, because it is trusted.
func TestRepoPrune_DryRunDeletesNothing(t *testing.T) {
	home := t.TempDir()
	repoID := seedPrunableStore(t, home, "dry", 12)

	code, out, stderr := runWith(t, home, []string{"runecho-ir", "repo", "prune", "--keep=3", "--dry-run"})
	if code != ExitOK {
		t.Fatalf("prune --dry-run: code %d: %s", code, stderr)
	}
	if !strings.Contains(out, "Would prune 9") {
		t.Errorf("stdout %q: expected it to report 9 prunable snapshots", strings.TrimSpace(out))
	}
	if !strings.Contains(out, "Nothing deleted") {
		t.Errorf("stdout %q: dry-run must say plainly that it deleted nothing", strings.TrimSpace(out))
	}
	if n := countReindexInStore(t, home, repoID); n != 12 {
		t.Errorf("reindex snapshots = %d after --dry-run, want 12 — the preview deleted rows", n)
	}
}

// TestRepoPrune_DeletesAndReportsTheSameCountTheDryRunPredicted: the preview
// and the action must agree at the CLI boundary too, not just in the store.
func TestRepoPrune_DeletesAndReportsTheSameCountTheDryRunPredicted(t *testing.T) {
	home := t.TempDir()
	repoID := seedPrunableStore(t, home, "real", 12)

	_, dryOut, _ := runWith(t, home, []string{"runecho-ir", "repo", "prune", "--keep=3", "--dry-run"})
	code, out, stderr := runWith(t, home, []string{"runecho-ir", "repo", "prune", "--keep=3"})
	if code != ExitOK {
		t.Fatalf("prune: code %d: %s", code, stderr)
	}
	if !strings.Contains(dryOut, "9") || !strings.Contains(out, "Pruned 9") {
		t.Errorf("dry-run said %q but the delete said %q", strings.TrimSpace(dryOut), strings.TrimSpace(out))
	}
	if n := countReindexInStore(t, home, repoID); n != 3 {
		t.Errorf("reindex snapshots = %d, want 3", n)
	}
}

// TestRepoPrune_ScopedToNamedRepo: --repo must not reach a sibling.
func TestRepoPrune_ScopedToNamedRepo(t *testing.T) {
	home := t.TempDir()
	victim := seedPrunableStore(t, home, "victim", 8)
	bystander := seedPrunableStore(t, home, "bystander", 8)

	code, out, stderr := runWith(t, home, []string{"runecho-ir", "repo", "prune", "--keep=2", "--repo=victim"})
	if code != ExitOK {
		t.Fatalf("prune --repo: code %d: %s", code, stderr)
	}
	if !strings.Contains(out, "victim") {
		t.Errorf("stdout %q should name the scoped repo", strings.TrimSpace(out))
	}
	if n := countReindexInStore(t, home, victim); n != 2 {
		t.Errorf("victim reindex snapshots = %d, want 2", n)
	}
	if n := countReindexInStore(t, home, bystander); n != 8 {
		t.Errorf("bystander reindex snapshots = %d, want 8 untouched", n)
	}
}

// TestRepoPrune_UnknownRepoIsAnError: a typo'd --repo must not silently fall
// back to pruning every repo.
func TestRepoPrune_UnknownRepoIsAnError(t *testing.T) {
	home := t.TempDir()
	repoID := seedPrunableStore(t, home, "present", 8)

	code, _, stderr := runWith(t, home, []string{"runecho-ir", "repo", "prune", "--keep=1", "--repo=nope"})
	if code == ExitOK {
		t.Error("prune --repo=nope exited 0; an unknown repo must be an error, not a store-wide prune")
	}
	if !strings.Contains(stderr, "No repo named") {
		t.Errorf("stderr %q: expected \"No repo named\"", stderr)
	}
	if n := countReindexInStore(t, home, repoID); n != 8 {
		t.Errorf("reindex snapshots = %d after a failed --repo lookup, want 8 untouched", n)
	}
}

// TestRepoPrune_RejectsKeepZero: keep=0 would delete the snapshot the guard
// reads. The CLI must refuse before reaching the store.
func TestRepoPrune_RejectsKeepZero(t *testing.T) {
	home := t.TempDir()
	repoID := seedPrunableStore(t, home, "zero", 5)

	code, _, stderr := runWith(t, home, []string{"runecho-ir", "repo", "prune", "--keep=0"})
	if code == ExitOK {
		t.Error("prune --keep=0 exited 0; want a refusal")
	}
	if !strings.Contains(stderr, "--keep must be >= 1") {
		t.Errorf("stderr %q: expected the keep-range refusal", stderr)
	}
	if n := countReindexInStore(t, home, repoID); n != 5 {
		t.Errorf("reindex snapshots = %d, want 5 untouched", n)
	}
}

// TestRepoPrune_VacuumKeepsTheStoreUsable: --vacuum rewrites the file, so the
// store must still open and answer afterwards.
func TestRepoPrune_VacuumKeepsTheStoreUsable(t *testing.T) {
	home := t.TempDir()
	repoID := seedPrunableStore(t, home, "vac", 9)

	code, out, stderr := runWith(t, home, []string{"runecho-ir", "repo", "prune", "--keep=2", "--vacuum"})
	if code != ExitOK {
		t.Fatalf("prune --vacuum: code %d: %s", code, stderr)
	}
	if !strings.Contains(out, "Vacuum complete") {
		t.Errorf("stdout %q: expected the vacuum to report completion", strings.TrimSpace(out))
	}
	if n := countReindexInStore(t, home, repoID); n != 2 {
		t.Errorf("reindex snapshots = %d after --vacuum, want 2", n)
	}
}

// TestReindexAll_PruneFlagIsOptIn: a bare `reindex --all` must never delete.
// This is what stops the periodic job's retention from leaking into every
// manual reindex a user runs.
func TestReindexAll_PruneFlagIsOptIn(t *testing.T) {
	home := t.TempDir()
	repoID := seedPrunableStore(t, home, "optin", 8)
	// The seeded repo's source dir does not exist, so doReindex will fail for
	// it — that is fine and deliberate: what matters is whether the snapshots
	// survive, and a failing reindex must not be a reason to skip retention
	// (that is asserted in the --prune case below).
	//
	// --keep=2 is passed WITHOUT --prune on purpose. Omitting it would leave
	// keep at its default of 30, which is above this fixture's 8 snapshots, so
	// a bug that pruned unconditionally would still delete nothing and the
	// assertion would pass for the wrong reason. With keep=2 the only way all 8
	// survive is if the prune genuinely did not run.
	_, _, _ = runWith(t, home, []string{"runecho-ir", "repo", "reindex", "--all", "--keep=2"})
	if n := countReindexInStore(t, home, repoID); n != 8 {
		t.Errorf("reindex snapshots = %d after a bare `reindex --all --keep=2`, want 8 — retention must be opt-in, not implied by --keep", n)
	}

	_, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "reindex", "--all", "--prune", "--keep=2"})
	if n := countReindexInStore(t, home, repoID); n != 2 {
		t.Errorf("reindex snapshots = %d after `reindex --all --prune --keep=2`, want 2", n)
	}
	if !strings.Contains(out, "Pruned 6") {
		t.Errorf("stdout %q: expected the prune to report 6 removed", strings.TrimSpace(out))
	}
}

// TestReindexAll_PruneNeverVacuums: the hourly job must not rewrite the whole
// store on every tick. Asserted on the output contract, since the vacuum is
// only reachable through the --vacuum flag on `repo prune`.
func TestReindexAll_PruneNeverVacuums(t *testing.T) {
	home := t.TempDir()
	seedPrunableStore(t, home, "novac", 6)
	_, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "reindex", "--all", "--prune", "--keep=1"})
	if strings.Contains(out, "Vacuum") {
		t.Errorf("stdout %q: `reindex --all --prune` must not vacuum — that belongs to an explicit `repo prune --vacuum`", strings.TrimSpace(out))
	}
}

// TestPeriodicJobPrunes pins the installed schedule. Both installers must pass
// --prune, or the store resumes growing on any machine that installs fresh.
// launchd takes an argv array with no shell, which is why this is a flag rather
// than a chained `&& repo prune`.
func TestPeriodicJobPrunes(t *testing.T) {
	entry := cronEntry("/usr/local/bin/runecho-ir", "/tmp/reindex.log")
	if !strings.Contains(entry, "repo reindex --all --prune") {
		t.Errorf("cron entry %q does not prune; the hourly job will grow the store unbounded", entry)
	}
	if strings.Contains(entry, "--vacuum") {
		t.Errorf("cron entry %q vacuums on every tick; that rewrites the whole store hourly", entry)
	}

	plist := launchdPlist("/usr/local/bin/runecho-ir", "/tmp/out.log", "/tmp/err.log")
	if !strings.Contains(plist, "<string>--prune</string>") {
		t.Errorf("launchd plist does not pass --prune:\n%s", plist)
	}
	if strings.Contains(plist, "--vacuum") {
		t.Error("launchd plist passes --vacuum; that rewrites the whole store hourly")
	}
	// The reindex itself must survive alongside the new flag — dropping it would
	// leave a job that only prunes.
	for _, want := range []string{"<string>repo</string>", "<string>reindex</string>", "<string>--all</string>"} {
		if !strings.Contains(plist, want) {
			t.Errorf("launchd plist lost %s:\n%s", want, plist)
		}
	}
}
