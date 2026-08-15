package snapshot

import (
	"fmt"
	"testing"
)

// Retention tests for #351. The invariant that matters most is not "N rows
// survive" — it is that the survivors are the NEWEST N and that no other label
// is ever touched, because session-start/probe/manual snapshots are the
// reference points `diff --since=<label>` and truth-trail pin.

// seedReindex writes n reindex snapshots for repoID with distinct root hashes,
// oldest first, and returns their IDs in that order. Distinct hashes matter:
// SaveReindexSnapshot would otherwise dedup them into a single row and the
// prune tests would have nothing to prune.
func seedReindex(t *testing.T, db *DB, repoID int64, n int) []int64 {
	t.Helper()
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		id, wrote, err := db.SaveReindexSnapshot(repoID, "", "/tmp/seed", makeIR(fmt.Sprintf("hash-%03d", i), "Fn"))
		if err != nil {
			t.Fatalf("seed reindex %d: %v", i, err)
		}
		if !wrote {
			t.Fatalf("seed reindex %d was deduped; the fixture is not producing distinct snapshots", i)
		}
		ids = append(ids, id)
	}
	return ids
}

func snapshotIDsWithLabel(t *testing.T, db *DB, repoID int64, label string) map[int64]bool {
	t.Helper()
	metas, err := db.List(repoID, 10000)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := map[int64]bool{}
	for _, m := range metas {
		if m.Label == label {
			out[m.ID] = true
		}
	}
	return out
}

func enrollForPrune(t *testing.T, db *DB, name string) int64 {
	t.Helper()
	id, err := db.EnrollRepo(name, "/tmp/"+name, "", 0)
	if err != nil {
		t.Fatalf("EnrollRepo(%s): %v", name, err)
	}
	return id
}

// TestPruneReindexSnapshots_KeepsExactlyTheNewestN asserts both halves: the
// count, and the identity of the survivors. Counting alone would pass a prune
// that kept the OLDEST N — the exact inversion that silently destroys the
// snapshot the guard actually reads.
func TestPruneReindexSnapshots_KeepsExactlyTheNewestN(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForPrune(t, db, "keepn")
	ids := seedReindex(t, db, repo, 35)

	deleted, err := db.PruneReindexSnapshots(repo, 30)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 5 {
		t.Errorf("deleted = %d, want 5 (35 seeded, keep 30)", deleted)
	}

	survivors := snapshotIDsWithLabel(t, db, repo, ReindexLabel)
	if len(survivors) != 30 {
		t.Fatalf("survivors = %d, want 30", len(survivors))
	}
	// ids is oldest-first, so the last 30 are the ones that must remain.
	for _, id := range ids[:5] {
		if survivors[id] {
			t.Errorf("snapshot %d is among the 5 oldest but survived a keep=30 prune", id)
		}
	}
	for _, id := range ids[5:] {
		if !survivors[id] {
			t.Errorf("snapshot %d is among the newest 30 but was pruned", id)
		}
	}
}

// TestPruneReindexSnapshots_LeavesNoOrphans is the fence that replaces the
// ON DELETE CASCADE the schema deliberately does not carry (issue #13). Any
// prune path that does not go through deleteSnapshotsTx fails here.
func TestPruneReindexSnapshots_LeavesNoOrphans(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForPrune(t, db, "orphans")
	seedReindex(t, db, repo, 12)

	if _, err := db.PruneReindexSnapshots(repo, 3); err != nil {
		t.Fatalf("prune: %v", err)
	}
	assertNoOrphans(t, db, "PruneReindexSnapshots")
}

// TestPruneReindexSnapshots_ExemptLabelsUntouched is the highest-value test
// here. session-start / probe / manual labels are what `diff --since=<label>`
// and truth-trail resolve against; dropping the label filter would reclaim
// almost nothing (they are a rounding error by volume) and silently destroy a
// feature. keep=1 makes the reindex pruning maximally aggressive so any label
// leakage is unmissable.
func TestPruneReindexSnapshots_ExemptLabelsUntouched(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForPrune(t, db, "labels")
	seedReindex(t, db, repo, 8)

	exempt := []string{"session-start", "probe", "release-baseline"}
	for i, label := range exempt {
		if _, err := db.SaveSnapshot(repo, "", label, "/tmp/labels", makeIR(fmt.Sprintf("exempt-%d", i), "Fn")); err != nil {
			t.Fatalf("save %s: %v", label, err)
		}
	}
	// A second row under one exempt label: keep-N must not apply to it either.
	if _, err := db.SaveSnapshot(repo, "", "session-start", "/tmp/labels", makeIR("exempt-extra", "Fn")); err != nil {
		t.Fatalf("save second session-start: %v", err)
	}

	if _, err := db.PruneReindexSnapshots(repo, 1); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if n := countLabel(t, db, repo, ReindexLabel); n != 1 {
		t.Errorf("reindex snapshots = %d after keep=1, want 1", n)
	}
	if n := countLabel(t, db, repo, "session-start"); n != 2 {
		t.Errorf("session-start snapshots = %d, want 2 — prune is not respecting the label filter", n)
	}
	for _, label := range []string{"probe", "release-baseline"} {
		if n := countLabel(t, db, repo, label); n != 1 {
			t.Errorf("%s snapshots = %d, want 1 — prune reached an exempt label", label, n)
		}
	}
}

// TestPruneReindexSnapshots_ScopesToOneRepo mirrors PurgeRepo's bystander
// pattern: pruning one repo must not touch a sibling.
func TestPruneReindexSnapshots_ScopesToOneRepo(t *testing.T) {
	db, _ := openTemp(t)
	victim := enrollForPrune(t, db, "victim")
	bystander := enrollForPrune(t, db, "bystander")
	seedReindex(t, db, victim, 10)
	seedReindex(t, db, bystander, 10)

	if _, err := db.PruneReindexSnapshots(victim, 2); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n := countLabel(t, db, victim, ReindexLabel); n != 2 {
		t.Errorf("victim reindex snapshots = %d, want 2", n)
	}
	if n := countLabel(t, db, bystander, ReindexLabel); n != 10 {
		t.Errorf("bystander reindex snapshots = %d, want 10 untouched — prune is missing its repo_id scope", n)
	}
	assertNoOrphans(t, db, "scoped PruneReindexSnapshots")
}

// TestPruneReindexSnapshots_AllReposWhenUnscoped: repoID 0 means every repo.
func TestPruneReindexSnapshots_AllReposWhenUnscoped(t *testing.T) {
	db, _ := openTemp(t)
	a := enrollForPrune(t, db, "all-a")
	b := enrollForPrune(t, db, "all-b")
	seedReindex(t, db, a, 6)
	seedReindex(t, db, b, 6)

	deleted, err := db.PruneReindexSnapshots(0, 2)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 8 {
		t.Errorf("deleted = %d, want 8 (4 from each of two repos)", deleted)
	}
	for _, repo := range []int64{a, b} {
		if n := countLabel(t, db, repo, ReindexLabel); n != 2 {
			t.Errorf("repo %d has %d reindex snapshots, want 2", repo, n)
		}
	}
	assertNoOrphans(t, db, "unscoped PruneReindexSnapshots")
}

// TestCountPruneReindexCandidates_MatchesActualDelete pins the dry-run contract:
// the preview must equal what the delete does. They share pruneReindexWhere
// today; this fails if a future change duplicates the SQL instead of reusing it.
func TestCountPruneReindexCandidates_MatchesActualDelete(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForPrune(t, db, "dryrun")
	seedReindex(t, db, repo, 17)

	predicted, err := db.CountPruneReindexCandidates(repo, 4)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if predicted != 13 {
		t.Errorf("predicted = %d, want 13", predicted)
	}

	// The count must not have deleted anything.
	if n := countLabel(t, db, repo, ReindexLabel); n != 17 {
		t.Fatalf("counting deleted rows: %d reindex snapshots remain, want 17", n)
	}

	actual, err := db.PruneReindexSnapshots(repo, 4)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if actual != predicted {
		t.Errorf("dry-run predicted %d, delete removed %d — the preview lies", predicted, actual)
	}
}

// TestPruneReindexSnapshots_NoOpWhenUnderKeep: fewer snapshots than keep is a
// clean zero, not an error and not an over-delete.
func TestPruneReindexSnapshots_NoOpWhenUnderKeep(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForPrune(t, db, "under")
	seedReindex(t, db, repo, 3)

	deleted, err := db.PruneReindexSnapshots(repo, 30)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
	if n := countLabel(t, db, repo, ReindexLabel); n != 3 {
		t.Errorf("reindex snapshots = %d, want 3", n)
	}
}

// TestPruneReindexSnapshots_RejectsKeepZero: keep=0 would delete every reindex
// snapshot including the one the guard reads. Refuse rather than obey.
func TestPruneReindexSnapshots_RejectsKeepZero(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForPrune(t, db, "keepzero")
	seedReindex(t, db, repo, 4)

	for _, keep := range []int{0, -1} {
		if _, err := db.PruneReindexSnapshots(repo, keep); err == nil {
			t.Errorf("PruneReindexSnapshots(keep=%d) returned nil error; want a refusal", keep)
		}
		if _, err := db.CountPruneReindexCandidates(repo, keep); err == nil {
			t.Errorf("CountPruneReindexCandidates(keep=%d) returned nil error; want a refusal", keep)
		}
	}
	if n := countLabel(t, db, repo, ReindexLabel); n != 4 {
		t.Errorf("reindex snapshots = %d after refused prunes, want 4 untouched", n)
	}
}

// TestVacuum_PreservesData: VACUUM rewrites the whole file, so it must be shown
// not to lose rows. Size is not asserted — page reuse makes that flaky.
func TestVacuum_PreservesData(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForPrune(t, db, "vacuum")
	seedReindex(t, db, repo, 6)
	if _, err := db.PruneReindexSnapshots(repo, 2); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if err := db.Vacuum(); err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	if n := countLabel(t, db, repo, ReindexLabel); n != 2 {
		t.Errorf("reindex snapshots = %d after VACUUM, want 2", n)
	}
	assertNoOrphans(t, db, "Vacuum")
	if err := db.integrityCheck(); err != nil {
		t.Errorf("integrity check after VACUUM: %v", err)
	}
}

// TestPruneReindexSnapshots_CrossesChunkBoundary exercises the multi-transaction
// path. Every other prune test here seeds fewer than pruneChunkSize prunable
// snapshots, so they all complete in ONE chunk and would stay green if chunking
// were broken outright — the batching would be present but never executed.
//
// Seeds enough to require at least three chunks, including a final partial one
// (the off-by-one most likely to drop or double-delete rows at a boundary).
func TestPruneReindexSnapshots_CrossesChunkBoundary(t *testing.T) {
	db, _ := openTemp(t)
	repo := enrollForPrune(t, db, "chunked")

	// The fixture size is derived from pruneChunkSize, so it grows with any
	// change to it. Bail out rather than seeding a pathological number of rows:
	// TestPruneReindexSnapshots_ChunkSizeIsBounded is the test that fails on an
	// oversized constant, and it does so instantly. Without this guard, raising
	// the constant to 100000 made this test try to seed 250,000 snapshots and
	// hang the package instead of reporting anything useful.
	if pruneChunkSize > 100 {
		t.Skipf("pruneChunkSize = %d is out of the sane range; ChunkSizeIsBounded reports that", pruneChunkSize)
	}

	keep := 5
	// 2.5 chunks' worth of deletions, so the last chunk is deliberately partial.
	prunable := pruneChunkSize*2 + pruneChunkSize/2
	total := prunable + keep
	ids := seedReindex(t, db, repo, total)

	deleted, err := db.PruneReindexSnapshots(repo, keep)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != int64(prunable) {
		t.Errorf("deleted = %d, want %d — a chunk boundary dropped or double-counted rows", deleted, prunable)
	}

	survivors := snapshotIDsWithLabel(t, db, repo, ReindexLabel)
	if len(survivors) != keep {
		t.Fatalf("survivors = %d, want %d", len(survivors), keep)
	}
	// ids is oldest-first: exactly the newest `keep` must remain, and every
	// deletion must have landed — not just the first chunk's worth.
	for _, id := range ids[:prunable] {
		if survivors[id] {
			t.Errorf("snapshot %d should have been pruned but survived; a later chunk did not run", id)
		}
	}
	for _, id := range ids[prunable:] {
		if !survivors[id] {
			t.Errorf("snapshot %d is among the newest %d but was pruned", id, keep)
		}
	}
	assertNoOrphans(t, db, "chunked PruneReindexSnapshots")
}

// TestPruneReindexSnapshots_ChunkSizeIsBounded guards the constant itself. The
// whole reason chunking exists is to keep a single write transaction well under
// the 5s busy_timeout; someone raising this to "reduce commit overhead" would
// silently reintroduce the lock starvation it prevents.
func TestPruneReindexSnapshots_ChunkSizeIsBounded(t *testing.T) {
	if pruneChunkSize < 1 {
		t.Fatalf("pruneChunkSize = %d; must be at least 1 or prune makes no progress", pruneChunkSize)
	}
	// ~30ms per snapshot on the heaviest repo measured; 200 would put a chunk
	// near the 5s busy_timeout with no margin.
	if pruneChunkSize > 100 {
		t.Errorf("pruneChunkSize = %d; at roughly 30ms per snapshot on a heavy repo that approaches the 5s busy_timeout, which is the lock starvation chunking exists to prevent", pruneChunkSize)
	}
}
