package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/store"
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

// TestRepoList_MarksMissingRootAndReportsCount pins issue #370's visibility
// half: before this, a dead enrollment was invisible in `repo list` — the
// first symptom was a warning in unrelated git output, or since #376 a
// blocked commit. `repo list` must now mark the row and print a footer using
// the SAME predicate `prune-missing` acts on, so the two commands can never
// disagree about what "missing" means.
func TestRepoList_MarksMissingRootAndReportsCount(t *testing.T) {
	home := t.TempDir()
	liveRoot := t.TempDir()
	enrollAt(t, home, "alive", liveRoot)
	enrollAt(t, home, "ghost", filepath.Join(t.TempDir(), "absent-370"))

	code, out, errOut := runWith(t, home, []string{"runecho-ir", "repo", "list"})
	if code != 0 {
		t.Fatalf("repo list: code %d", code)
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "ghost"):
			if !strings.Contains(line, "missing") {
				t.Errorf("the dead enrollment is not marked: %q", line)
			}
		case strings.Contains(line, "alive"):
			if !strings.Contains(line, "ok") || strings.Contains(line, "missing") {
				t.Errorf("the live enrollment is mismarked: %q", line)
			}
		}
	}
	// The footer belongs on stderr. On stdout it adds two lines that a
	// `tail -n +3` reader parses as table rows.
	if !strings.Contains(errOut, "1 of 2 enrolled repo(s) have a missing source root") {
		t.Errorf("the missing count was not reported on stderr: %s", errOut)
	}
	if strings.Contains(out, "missing source root") {
		t.Errorf("the footer leaked onto stdout, which is the data stream: %s", out)
	}
}

// TestRepoList_PathStaysTheLastField pins the column contract, which the
// marker broke and no test caught.
//
// Issue #370's own documented measurement is
// `repo list | tail -n +3 | awk '{print $NF}'`. An earlier form of this marker
// appended " [missing]" to the path, so $NF became the literal "[missing]" —
// every path that command reported was destroyed, and the dead count went
// 3 -> 5 on the real store because the stdout footer added two more lines.
// Asserting only that "[missing]" appears somewhere passes just as happily
// with the marker in the last column, which is how that shipped.
func TestRepoList_PathStaysTheLastField(t *testing.T) {
	home := t.TempDir()
	liveRoot := t.TempDir()
	deadRoot := filepath.Join(t.TempDir(), "absent-370")
	enrollAt(t, home, "alive", liveRoot)
	enrollAt(t, home, "ghost", deadRoot)

	_, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "list"})

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected a header, a rule and two rows; got %d line(s): %s", len(lines), out)
	}
	// Exactly what issue #370's repro does: tail -n +3 | awk '{print $NF}'
	var lastFields []string
	for _, line := range lines[2:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		lastFields = append(lastFields, fields[len(fields)-1])
	}

	want := map[string]bool{liveRoot: true, deadRoot: true}
	for _, got := range lastFields {
		if !want[got] {
			t.Errorf("$NF is %q, not an enrolled path — a marker or footer is in the last column, "+
				"which breaks issue #370's own repro command", got)
		}
	}
	if len(lastFields) != 2 {
		t.Errorf("expected 2 data rows, got %d: %v", len(lastFields), lastFields)
	}
}

// TestRepoList_NoFooterWhenNoneMissing pins the quiet-by-default half: the
// common case (nothing missing) must print no extra line, matching
// prune-missing's own TestRepoPruneMissing_NoneMissingIsCleanExit.
func TestRepoList_NoFooterWhenNoneMissing(t *testing.T) {
	home := t.TempDir()
	enrollAt(t, home, "alive", t.TempDir())

	code, out, _ := runWith(t, home, []string{"runecho-ir", "repo", "list"})
	if code != 0 {
		t.Fatalf("repo list: code %d", code)
	}
	if strings.Contains(out, "missing source root") {
		t.Errorf("repo list printed missing-root output with nothing missing: %s", out)
	}
}

// TestRepoRm_RemovesStaleRefreshLock pins issue #370's lock-hygiene tail item:
// a purged repo's E6 refresh lock (internal/store.RefreshLockPath) must not
// survive the purge. Before this, nothing ever removed these — 652 measured on
// one box, one per repo id ever enrolled, with no sweeper.
func TestRepoRm_RemovesStaleRefreshLock(t *testing.T) {
	home := t.TempDir()
	id := enrollAt(t, home, "locked", t.TempDir())

	lockPath := store.RefreshLockPath(home, id)
	if err := os.WriteFile(lockPath, []byte{}, 0o644); err != nil {
		t.Fatalf("seed lock file: %v", err)
	}

	code, _, _ := runWith(t, home, []string{"runecho-ir", "repo", "rm", "locked"})
	if code != 0 {
		t.Fatalf("repo rm: code %d", code)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("refresh lock for purged repo %d still exists at %s", id, lockPath)
	}
}

// TestRepoPruneMissing_RemovesStaleRefreshLock: same guarantee via the --yes
// bulk-purge path, since it is a separate PurgeRepo call site.
func TestRepoPruneMissing_RemovesStaleRefreshLock(t *testing.T) {
	home := t.TempDir()
	id := enrollAt(t, home, "ghost", filepath.Join(t.TempDir(), "absent-lock-370"))

	lockPath := store.RefreshLockPath(home, id)
	if err := os.WriteFile(lockPath, []byte{}, 0o644); err != nil {
		t.Fatalf("seed lock file: %v", err)
	}

	code, _, _ := runWith(t, home, []string{"runecho-ir", "repo", "prune-missing", "--yes"})
	if code != 0 {
		t.Fatalf("prune-missing --yes: code %d", code)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("refresh lock for pruned repo %d still exists at %s", id, lockPath)
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
