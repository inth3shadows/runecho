package snapshot

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestOpenRefusesNewerSchema pins the documented permanent open failure: a store
// stamped with a user_version above what this binary supports must be refused via
// ErrSchemaNewer, never silently opened. Opening it would let a stale binary
// operate on a newer schema — exactly the corruption ErrSchemaNewer exists to
// prevent. Both Open and OpenFast share the migrate() gate and must refuse alike.
func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.db")

	// Create a valid current-schema store, stamp it one version ahead, checkpoint
	// so the header change lands in the main file, then close.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.conn.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion+1)); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	if _, err := db.conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	db.Close()

	_, err = Open(path)
	if err == nil {
		t.Fatal("Open accepted a newer-than-supported schema; want refusal")
	}
	if !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("Open error = %v, want wrapped ErrSchemaNewer", err)
	}

	if _, err := OpenFast(path); !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("OpenFast error = %v, want wrapped ErrSchemaNewer", err)
	}
}

// TestOpenRefusesCorruptDB pins the durability guarantee "never serve a corrupt
// DB": Open runs PRAGMA quick_check and must fail rather than hand back a handle
// to a damaged store. We grow the file across several b-tree pages, checkpoint WAL
// into the main file, then overwrite a span of page data with garbage.
func TestOpenRefusesCorruptDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Fill enough rows to span multiple pages so the corruption lands on real
	// b-tree content, not free space.
	if _, err := db.conn.Exec("CREATE TABLE filler (x TEXT)"); err != nil {
		t.Fatalf("create filler: %v", err)
	}
	row := strings.Repeat("a", 256)
	for i := 0; i < 300; i++ {
		if _, err := db.conn.Exec("INSERT INTO filler VALUES (?)", row); err != nil {
			t.Fatalf("insert filler: %v", err)
		}
	}
	if _, err := db.conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	db.Close()

	// Overwrite a 16 KiB span starting after the first page with garbage.
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open db file: %v", err)
	}
	garbage := make([]byte, 16*1024)
	for i := range garbage {
		garbage[i] = 0xBD
	}
	if _, err := f.WriteAt(garbage, 4096); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	f.Close()

	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a corrupt DB; want integrity refusal")
	}
}

// A checked Open records its outcome for opens that skip the scan (#441): a
// failure leaves a marker that CorruptFinding reports and NeedsCheck acts on,
// without making OpenFast refuse (that would lock out doctor, runecho-mcp and a
// restore); a pass — here after the store is replaced, as a restore would —
// clears the marker and stamps the pass, and a stale stamp asks for a re-check.
func TestCheckedOpenRecordsItsOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if NeedsCheck(path, time.Hour) {
		t.Fatal("NeedsCheck right after a passing Open")
	}
	if _, err := db.conn.Exec("CREATE TABLE filler (x TEXT)"); err != nil {
		t.Fatal(err)
	}
	row := strings.Repeat("a", 256)
	for i := 0; i < 300; i++ {
		if _, err := db.conn.Exec("INSERT INTO filler VALUES (?)", row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_WRONLY, 0)
	f.WriteAt(bytes.Repeat([]byte{0xBD}, 16*1024), 4096)
	f.Close()

	if _, err := Open(path); !errors.Is(err, ErrIntegrityFailed) {
		t.Fatalf("Open on a corrupt DB: err = %v, want ErrIntegrityFailed", err)
	}
	if detail, bad := CorruptFinding(path); !bad || !strings.Contains(detail, "integrity check failed") {
		t.Fatalf("CorruptFinding = %q, %v; want the recorded failure", detail, bad)
	}
	if !NeedsCheck(path, time.Hour) {
		t.Fatal("NeedsCheck false while a failure is recorded")
	}
	if db, err := OpenFast(path); err != nil {
		t.Fatalf("OpenFast must not refuse on a recorded failure: %v", err)
	} else {
		db.Close()
	}

	// Restore a good copy; the next checked Open passes, clears and stamps.
	os.Remove(path + "-wal")
	os.Remove(path + "-shm")
	if err := os.WriteFile(path, good, 0600); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatalf("Open on the restored store: %v", err)
	}
	db.Close()
	if _, bad := CorruptFinding(path); bad {
		t.Fatal("passing check left the marker")
	}
	if NeedsCheck(path, time.Hour) {
		t.Fatal("NeedsCheck right after the restore passed")
	}

	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(checkedStampPath(path), old, old)
	if !NeedsCheck(path, time.Hour) {
		t.Fatal("NeedsCheck false with a stale stamp")
	}

	// A marker that exists but cannot be read still counts.
	if err := os.Mkdir(corruptMarkerPath(path), 0700); err != nil {
		t.Fatal(err)
	}
	if _, bad := CorruptFinding(path); !bad {
		t.Fatal("an unreadable marker read as all-clear")
	}
}
