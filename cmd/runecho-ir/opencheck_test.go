package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// corruptUnusedPage adds a filler table to the store at dbPath and overwrites
// its last page, so PRAGMA quick_check fails while the tables commands read
// (repos, snapshots) stay intact. It asserts the corruption took, so a test
// built on it cannot pass vacuously.
func corruptUnusedPage(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("CREATE TABLE filler (x TEXT)"); err != nil {
		t.Fatal(err)
	}
	row := strings.Repeat("a", 256)
	for i := 0; i < 300; i++ {
		if _, err := conn.Exec("INSERT INTO filler VALUES (?)", row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	var pages, size int64
	conn.QueryRow("PRAGMA page_count").Scan(&pages)
	conn.QueryRow("PRAGMA page_size").Scan(&size)
	conn.Close()

	f, err := os.OpenFile(dbPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xBD}, int(size)), (pages-1)*size); err != nil {
		t.Fatal(err)
	}
	f.Close()

	conn, _ = sql.Open("sqlite", dbPath)
	defer conn.Close()
	var res string
	conn.QueryRow("PRAGMA quick_check").Scan(&res)
	if res == "ok" {
		t.Fatal("corruption did not take: quick_check still ok")
	}
}

// checkpointedBytes flushes the WAL and returns the store file's bytes.
func checkpointedBytes(t *testing.T, dbPath string) []byte {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	b, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// putStore replaces the store with b and drops its sidecars and any recorded
// check outcome other than the ones the caller sets up.
func putStore(t *testing.T, dbPath string, b []byte) {
	t.Helper()
	for _, sfx := range []string{"-wal", "-shm", ".corrupt"} {
		os.Remove(dbPath + sfx)
	}
	if err := os.WriteFile(dbPath, b, 0600); err != nil {
		t.Fatal(err)
	}
}

// #441: runecho-ir skips the whole-file quick_check while the last pass is
// fresh and clean, so latent damage in pages a command never reads does not
// stop it; commands that copy, bulk-delete, or sweep the store always check
// and refuse. A recorded failure, or a stale pass, makes the next command
// re-check — failing while the store is corrupt, clearing once it is restored.
func TestOpenCheck_FastVsVerifiedCommands(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	irGitInit(t, dir)
	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "repo", "add", "--name", "oc", "--no-hooks", dir}); code != 0 {
		t.Fatalf("repo add: code %d: %s", code, stderr)
	}
	dbPath := filepath.Join(home, "history.db")
	if _, err := os.Stat(dbPath + ".checked"); err != nil {
		t.Fatalf("the first open did not run and stamp a check: %v", err)
	}
	good := checkpointedBytes(t, dbPath)
	corruptUnusedPage(t, dbPath)
	corrupt := checkpointedBytes(t, dbPath)

	run := func(args ...string) (int, string) {
		code, _, stderr := runWith(t, home, append([]string{"runecho-ir"}, args...))
		return code, stderr
	}

	for _, args := range [][]string{{"repo", "list"}, {"log", dir}, {"repo", "reindex", "oc"}} {
		if code, stderr := run(args...); code != 0 || strings.Contains(stderr, "integrity") {
			t.Errorf("%v: code %d, stderr %q — a fresh, clean pass must let it skip the scan", args, code, stderr)
		}
	}

	// Each checked command on its own fresh corrupt copy with no recorded
	// failure: only its own scan can make it refuse and write the marker.
	for _, args := range [][]string{
		{"backup", filepath.Join(t.TempDir(), "b.db")},
		{"repo", "prune", "--dry-run"},
		{"repo", "prune-missing"},
		{"repo", "rm", "oc"},
		{"repo", "reindex", "--all"},
	} {
		putStore(t, dbPath, corrupt)
		code, stderr := run(args...)
		if code == 0 || !strings.Contains(stderr, "integrity check failed") {
			t.Errorf("%v: code %d, stderr %q — want its own integrity refusal", args, code, stderr)
		}
		if _, err := os.Stat(dbPath + ".corrupt"); err != nil {
			t.Errorf("%v: no failure recorded — did it skip the scan?", args)
		}
	}

	// With the failure recorded, a fast command re-checks and fails too...
	if code, stderr := run("repo", "list"); code == 0 || !strings.Contains(stderr, "integrity check failed") {
		t.Errorf("repo list with a recorded failure: code %d, stderr %q — want a re-check that fails", code, stderr)
	}
	// ...and after a restore the same command passes and clears it.
	putStore(t, dbPath, good)
	os.WriteFile(dbPath+".corrupt", []byte("old finding\n"), 0600)
	if code, stderr := run("repo", "list"); code != 0 {
		t.Errorf("repo list after a restore: code %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(dbPath + ".corrupt"); !os.IsNotExist(err) {
		t.Error("a passing re-check left the recorded failure")
	}

	// A stale pass on a store with latent damage: the next command re-checks.
	putStore(t, dbPath, corrupt)
	old := time.Now().Add(-storeCheckMaxAge - time.Hour)
	os.Chtimes(dbPath+".checked", old, old)
	if code, stderr := run("repo", "list"); code == 0 || !strings.Contains(stderr, "integrity check failed") {
		t.Errorf("repo list with a stale pass: code %d, stderr %q — want a re-check that fails", code, stderr)
	}
}
