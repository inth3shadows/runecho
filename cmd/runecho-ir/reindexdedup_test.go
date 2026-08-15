package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inth3shadows/runecho/internal/snapshot"
)

// Caller-side coverage for the #351 dedup. The store-side behaviour is pinned
// in internal/snapshot/reindexdedup_test.go; what these tests pin is that
// doReindex wires it up correctly — specifically that TouchRepo still runs when
// the snapshot write is skipped.

// openStore opens the central store under home for direct assertions the CLI
// does not surface.
func openStore(t *testing.T, home string) *snapshot.DB {
	t.Helper()
	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func reindexSnapshotCount(t *testing.T, db *snapshot.DB, repoID int64) int {
	t.Helper()
	metas, err := db.List(repoID, 1000)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	n := 0
	for _, m := range metas {
		if m.Label == snapshot.ReindexLabel {
			n++
		}
	}
	return n
}

func repoByName(t *testing.T, db *snapshot.DB, name string) *snapshot.Repo {
	t.Helper()
	repos, err := db.ListRepos()
	if err != nil {
		t.Fatalf("list repos: %v", err)
	}
	for i := range repos {
		if repos[i].Name == name {
			return &repos[i]
		}
	}
	t.Fatalf("repo %q not enrolled; have %d", name, len(repos))
	return nil
}

// enrollForReindex creates a git repo with one Go file, enrolls it (which
// auto-reindexes, producing the baseline snapshot), and returns its name.
func enrollForReindex(t *testing.T, home, dir string) string {
	t.Helper()
	irGitInit(t, dir)
	name := "dedup-fixture"
	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "repo", "add", "--name=" + name, "--no-hooks", dir}); code != 0 {
		t.Fatalf("repo add: code %d: %s", code, stderr)
	}
	return name
}

// TestReindex_UnchangedTreeDoesNotGrowTheStore is the end-to-end shape of #351:
// re-running reindex over an untouched tree must not add a snapshot.
func TestReindex_UnchangedTreeDoesNotGrowTheStore(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	name := enrollForReindex(t, home, dir)

	db := openStore(t, home)
	repo := repoByName(t, db, name)
	before := reindexSnapshotCount(t, db, repo.ID)
	if before == 0 {
		t.Fatal("repo add produced no reindex snapshot; the fixture proves nothing")
	}
	db.Close()

	code, stdout, stderr := runWith(t, home, []string{"runecho-ir", "repo", "reindex", name})
	if code != 0 {
		t.Fatalf("reindex: code %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Unchanged") {
		t.Errorf("reindex of an untouched tree printed %q, want it to report Unchanged", strings.TrimSpace(stdout))
	}

	db2 := openStore(t, home)
	if after := reindexSnapshotCount(t, db2, repo.ID); after != before {
		t.Errorf("reindex snapshots %d -> %d over an unchanged tree; the duplicate write was not skipped", before, after)
	}
}

// TestReindex_ChangedTreeStillWrites is the guard against an over-eager dedup:
// real content changes must still land.
func TestReindex_ChangedTreeStillWrites(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	name := enrollForReindex(t, home, dir)

	db := openStore(t, home)
	repo := repoByName(t, db, name)
	before := reindexSnapshotCount(t, db, repo.ID)
	db.Close()

	// A new exported symbol changes the root hash.
	if err := os.WriteFile(filepath.Join(dir, "added.go"), []byte("package stub\n\nfunc Added() {}\n"), 0o644); err != nil {
		t.Fatalf("write added.go: %v", err)
	}

	code, stdout, stderr := runWith(t, home, []string{"runecho-ir", "repo", "reindex", name})
	if code != 0 {
		t.Fatalf("reindex: code %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Reindexed") {
		t.Errorf("reindex after a real change printed %q, want it to report Reindexed", strings.TrimSpace(stdout))
	}

	db2 := openStore(t, home)
	if after := reindexSnapshotCount(t, db2, repo.ID); after != before+1 {
		t.Errorf("reindex snapshots %d -> %d after a real change, want %d", before, after, before+1)
	}
}

// TestReindex_UnchangedTreeStillTouchesRepo pins the caller-side half of the
// "touch, don't skip" argument: repos.last_indexed drives the coverage and
// freshness reporting, and a repo whose content simply did not change this tick
// is fully current. Moving TouchRepo inside the `if wrote` branch fails only
// this test.
func TestReindex_UnchangedTreeStillTouchesRepo(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	name := enrollForReindex(t, home, dir)

	db := openStore(t, home)
	repo := repoByName(t, db, name)
	// Back-date rather than sleep: last_indexed is RFC3339 (second
	// granularity), so a same-second re-index would be indistinguishable from
	// no update at all.
	stale := time.Now().UTC().Add(-72 * time.Hour)
	if err := db.TouchRepo(repo.ID, stale, 0, 0); err != nil {
		t.Fatalf("back-date last_indexed: %v", err)
	}
	staleValue := repoByName(t, db, name).LastIndexed
	db.Close()

	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "repo", "reindex", name}); code != 0 {
		t.Fatalf("reindex: code %d: %s", code, stderr)
	}

	db2 := openStore(t, home)
	got := repoByName(t, db2, name).LastIndexed
	if got == staleValue {
		t.Errorf("last_indexed still %v after an unchanged reindex — TouchRepo was skipped along with the snapshot write, so this repo will read as stale despite being current", staleValue)
	}
}
