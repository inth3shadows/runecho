package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// corruptStoreUnusedPage overwrites the last page of a filler table in the
// store, so quick_check fails while the tables the guard reads stay intact.
func corruptStoreUnusedPage(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	conn.Exec("CREATE TABLE filler (x TEXT)")
	row := strings.Repeat("a", 256)
	for i := 0; i < 300; i++ {
		conn.Exec("INSERT INTO filler VALUES (?)", row)
	}
	conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	var pages, size int64
	conn.QueryRow("PRAGMA page_count").Scan(&pages)
	conn.QueryRow("PRAGMA page_size").Scan(&size)
	conn.Close()
	f, err := os.OpenFile(dbPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt(bytes.Repeat([]byte{0xBD}, int(size)), (pages-1)*size)
	f.Close()
	conn, _ = sql.Open("sqlite", dbPath)
	defer conn.Close()
	var res string
	conn.QueryRow("PRAGMA quick_check").Scan(&res)
	if res == "ok" {
		t.Fatal("corruption did not take: quick_check still ok")
	}
}

// #441: the pre-commit guard opens without the whole-file scan (~3 s per commit
// on a ~0.9 GiB store), so latent corruption in a page it never reads must not
// switch it off: a staged hallucination is still caught. Before, Open's
// quick_check failed and the guard degraded to a pass.
func TestRunPreCommit_LatentCorruptionStillChecks(t *testing.T) {
	_, wtA, wtB := bareWorktrees(t)
	db := storeAt(t)
	enrollWithSnapshot(t, db, "container-wtA", wtA, "real_helper")
	db.Close()
	corruptStoreUnusedPage(t, filepath.Join(os.Getenv("RUNECHO_HOME"), "history.db"))

	stage(t, wtB, "app.py", "def caller():\n    return missing_helper_xyz()\n")
	t.Chdir(wtB)
	if got := runPreCommit(false, false); got != 1 {
		t.Fatalf("runPreCommit = %d, want 1 — the guard stopped checking on a store whose damage it never reads", got)
	}
}

// With a failure recorded by runecho-ir (#441), the guard must keep checking —
// undamaged pages still answer — and say the store failed its last check.
func TestRunPreCommit_RecordedCorruptionWarnsAndStillChecks(t *testing.T) {
	_, wtA, wtB := bareWorktrees(t)
	db := storeAt(t)
	enrollWithSnapshot(t, db, "container-wtA", wtA, "real_helper")
	db.Close()
	dbPath := filepath.Join(os.Getenv("RUNECHO_HOME"), "history.db")
	if err := os.WriteFile(dbPath+".corrupt", []byte("2026-10-03T00:00:00Z integrity check failed: page 9\n"), 0600); err != nil {
		t.Fatal(err)
	}

	stage(t, wtB, "app.py", "def caller():\n    return missing_helper_xyz()\n")
	t.Chdir(wtB)
	var got int
	stderr := captureStderr(t, func() { got = runPreCommit(false, false) })
	if got != 1 {
		t.Fatalf("runPreCommit = %d, want 1 — a recorded failure must not switch the guard off", got)
	}
	if !strings.Contains(stderr, "failed its last integrity check") {
		t.Errorf("no warning about the recorded failure; stderr: %q", stderr)
	}
}
