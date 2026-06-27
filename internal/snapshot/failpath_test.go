package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
