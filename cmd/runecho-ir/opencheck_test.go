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

// #441: read and incremental-write commands open the store without the
// whole-file scan, so latent corruption in pages they never read does not stop
// them; commands that copy, bulk-delete, or sweep the store keep the scan and
// refuse. Once one of them has failed, the marker it leaves makes the fast
// commands refuse too.
func TestOpenCheck_FastVsVerifiedCommands(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	irGitInit(t, dir)
	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "repo", "add", "--name", "oc", "--no-hooks", dir}); code != 0 {
		t.Fatalf("repo add: code %d: %s", code, stderr)
	}
	corruptUnusedPage(t, filepath.Join(home, "history.db"))

	for _, args := range [][]string{
		{"repo", "list"},
		{"log", dir},
		{"repo", "reindex", "oc"},
	} {
		if code, _, stderr := runWith(t, home, append([]string{"runecho-ir"}, args...)); code != 0 || strings.Contains(stderr, "integrity") {
			t.Errorf("%v: code %d, stderr %q — a fast command must not run the whole-file check", args, code, stderr)
		}
	}

	for _, args := range [][]string{
		{"backup", filepath.Join(t.TempDir(), "b.db")},
		{"repo", "prune", "--dry-run"},
		{"repo", "prune-missing"},
		{"repo", "rm", "oc"},
		{"repo", "reindex", "--all"},
	} {
		code, _, stderr := runWith(t, home, append([]string{"runecho-ir"}, args...))
		if code == 0 || !strings.Contains(stderr, "integrity check failed") {
			t.Errorf("%v: code %d, stderr %q — want an integrity refusal", args, code, stderr)
		}
	}

	code, _, stderr := runWith(t, home, []string{"runecho-ir", "repo", "list"})
	if code == 0 || !strings.Contains(stderr, "failed its last integrity check") {
		t.Errorf("repo list after a failed check: code %d, stderr %q — want the marker refusal", code, stderr)
	}
}
