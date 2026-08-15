package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// `repo prune-missing` is the only command in this file group that can delete a
// repo's entire history, so what these tests pin hardest is the things it must
// NOT do: purge without --yes, and treat anything other than a definitive
// "not there" as gone.

// enrollAt registers a repo pointing at root, whether or not root exists, and
// gives it one snapshot so a purge has history to remove.
func enrollAt(t *testing.T, home, name, root string) int64 {
	t.Helper()
	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	id, err := db.EnrollRepo(name, root, "", 0)
	if err != nil {
		t.Fatalf("enroll %s: %v", name, err)
	}
	if _, _, err := db.SaveReindexSnapshot(id, "", root, smallIR("hash-"+name)); err != nil {
		t.Fatalf("seed snapshot for %s: %v", name, err)
	}
	return id
}

func enrolledNames(t *testing.T, home string) map[string]bool {
	t.Helper()
	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	repos, err := db.ListRepos()
	if err != nil {
		t.Fatalf("list repos: %v", err)
	}
	out := map[string]bool{}
	for _, r := range repos {
		out[r.Name] = true
	}
	return out
}

// TestRepoPruneMissing_ListOnlyByDefault is the safety contract. Without --yes
// nothing may be deleted, no matter how many repos are missing.
func TestRepoPruneMissing_ListOnlyByDefault(t *testing.T) {
	home := t.TempDir()
	present := t.TempDir()
	enrollAt(t, home, "present", present)
	enrollAt(t, home, "vanished", filepath.Join(t.TempDir(), "never-created"))

	code, out, stderr := runWith(t, home, []string{"runecho-ir", "repo", "prune-missing"})
	if code != ExitOK {
		t.Fatalf("prune-missing: code %d: %s", code, stderr)
	}
	if !strings.Contains(out, "vanished") || !strings.Contains(out, "[missing]") {
		t.Errorf("stdout %q should list the vanished repo", strings.TrimSpace(out))
	}
	if !strings.Contains(out, "Nothing deleted") {
		t.Errorf("stdout %q must say plainly that it deleted nothing", strings.TrimSpace(out))
	}

	names := enrolledNames(t, home)
	if !names["vanished"] {
		t.Error("the vanished repo was purged without --yes")
	}
	if !names["present"] {
		t.Error("the present repo was purged")
	}
}

// TestRepoPruneMissing_YesPurgesOnlyTheMissing: --yes must remove the missing
// repos and leave every live one alone.
func TestRepoPruneMissing_YesPurgesOnlyTheMissing(t *testing.T) {
	home := t.TempDir()
	present := t.TempDir()
	enrollAt(t, home, "present", present)
	enrollAt(t, home, "gone-a", filepath.Join(t.TempDir(), "nope-a"))
	enrollAt(t, home, "gone-b", filepath.Join(t.TempDir(), "nope-b"))

	code, out, stderr := runWith(t, home, []string{"runecho-ir", "repo", "prune-missing", "--yes"})
	if code != ExitOK {
		t.Fatalf("prune-missing --yes: code %d: %s", code, stderr)
	}
	if !strings.Contains(out, "Purged 2") {
		t.Errorf("stdout %q: expected it to report 2 purged", strings.TrimSpace(out))
	}

	names := enrolledNames(t, home)
	if names["gone-a"] || names["gone-b"] {
		t.Error("--yes left a missing repo enrolled")
	}
	if !names["present"] {
		t.Error("--yes purged a repo whose source root exists")
	}
}

// TestRepoPruneMissing_SourceRootWinsOverPath: a bare-repo worktree enrolment
// stores a distinct source_root, and that is the directory the reindexer
// actually walks. Checking Path instead would call a live repo missing —
// exactly the false positive that makes --yes dangerous.
func TestRepoPruneMissing_SourceRootWinsOverPath(t *testing.T) {
	home := t.TempDir()
	liveSourceRoot := t.TempDir()

	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// Path points somewhere that does not exist; source_root is live. The repo
	// is healthy — EffectiveSourceRoot is what gets walked.
	if _, err := db.EnrollRepo("worktree-style", filepath.Join(t.TempDir(), "bare-container"), liveSourceRoot, 0); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	db.Close()

	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "prune-missing"})
	if code != ExitOK {
		t.Fatalf("prune-missing: code %d", code)
	}
	if strings.Contains(out, "worktree-style") {
		t.Errorf("stdout %q flags a repo whose source_root exists — the check is reading Path instead of EffectiveSourceRoot", strings.TrimSpace(out))
	}
	if !strings.Contains(out, "No enrolled repos have a missing source root") {
		t.Errorf("stdout %q: expected the all-clear", strings.TrimSpace(out))
	}
}

// TestRepoPruneMissing_UnreadableIsNotMissing separates "definitely not there"
// from "cannot tell". Only ENOENT is evidence a repo is gone; a permission
// error or an I/O error on a flaky mount is not, and treating any stat failure
// as gone is exactly how --yes deletes something real.
//
// The fixture uses a path whose parent is a regular file, which yields ENOTDIR
// — an error that is NOT errors.Is(err, os.ErrNotExist). That is portable and
// works when tests run as root, unlike a chmod-based permission fixture.
func TestRepoPruneMissing_UnreadableIsNotMissing(t *testing.T) {
	home := t.TempDir()
	base := t.TempDir()
	regular := filepath.Join(base, "a-regular-file")
	if err := os.WriteFile(regular, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	// Sanity-check the fixture rather than assuming it: if this ever starts
	// reporting ErrNotExist, the test below would pass for the wrong reason.
	if _, statErr := os.Stat(filepath.Join(regular, "sub")); errors.Is(statErr, os.ErrNotExist) {
		t.Skipf("fixture does not produce a non-ENOENT stat error on this platform: %v", statErr)
	}

	enrollAt(t, home, "unreadable", filepath.Join(regular, "sub"))

	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "prune-missing"})
	if code != ExitOK {
		t.Fatalf("prune-missing: code %d", code)
	}
	if strings.Contains(out, "unreadable") {
		t.Errorf("stdout %q flags a repo whose root returned a non-ENOENT stat error — \"cannot tell\" is being read as \"gone\", which makes --yes destructive on an unmounted drive", strings.TrimSpace(out))
	}

	// And --yes must not purge it either.
	if code, _, _ := runWith(t, home, []string{"runecho-ir", "repo", "prune-missing", "--yes"}); code != ExitOK {
		t.Fatalf("prune-missing --yes: code %d", code)
	}
	if !enrolledNames(t, home)["unreadable"] {
		t.Error("--yes purged a repo whose root only failed to stat; that is unrecoverable on a false signal")
	}
}

// TestRepoPruneMissing_NoneMissingIsCleanExit: the common case must be quiet
// and exit 0.
func TestRepoPruneMissing_NoneMissingIsCleanExit(t *testing.T) {
	home := t.TempDir()
	enrollAt(t, home, "alive", t.TempDir())

	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "prune-missing"})
	if code != ExitOK {
		t.Fatalf("prune-missing: code %d, want 0", code)
	}
	if !strings.Contains(out, "No enrolled repos have a missing source root") {
		t.Errorf("stdout %q: expected the all-clear", strings.TrimSpace(out))
	}
	if !enrolledNames(t, home)["alive"] {
		t.Error("a live repo was purged")
	}
}

// TestRepoPruneMissing_NotWiredIntoReindexAll: retention of dead enrolments must
// never happen as a side effect of the scheduled job. `reindex --all --prune`
// is what cron runs; it must leave a missing repo's registration alone.
func TestRepoPruneMissing_NotWiredIntoReindexAll(t *testing.T) {
	home := t.TempDir()
	enrollAt(t, home, "ghost", filepath.Join(t.TempDir(), "absent"))

	// Exit code is ignored: reindexing a vanished root fails by design.
	_, _, _ = runWith(t, home, []string{"runecho-ir", "repo", "reindex", "--all", "--prune", "--keep=1"})

	if !enrolledNames(t, home)["ghost"] {
		t.Error("the scheduled `reindex --all --prune` deregistered a repo — deleting a repo's history must never be a side effect of the hourly job")
	}
}

// smallIR is a minimal valid IR — enough for a snapshot to have child rows.
func smallIR(rootHash string) *ir.IR {
	return &ir.IR{
		Version:  1,
		RootHash: rootHash,
		Files:    map[string]ir.FileIR{"main.go": {Hash: rootHash, Symbols: []ir.Symbol{{Name: "Fn", Kind: "function"}}}},
	}
}
