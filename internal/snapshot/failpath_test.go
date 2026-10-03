package snapshot

import (
	"bytes"
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

// A failed checked Open must reach every fast open (#441): OpenFast skips the
// whole-file scan, so the marker Open leaves is the only way it learns. A later
// checked Open that passes — here after the store is replaced with a good one,
// as a restore would — clears it.
func TestCorruptMarkerBlocksOpenFastUntilACheckedOpenPasses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
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

	if db, err := OpenFast(path); err != nil {
		t.Fatalf("OpenFast before any checked open: %v (no marker yet, want success)", err)
	} else {
		db.Close()
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a corrupt DB")
	}
	if _, err := os.Stat(corruptMarkerPath(path)); err != nil {
		t.Fatalf("failed check left no marker: %v", err)
	}
	if _, err := OpenFast(path); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("OpenFast with a marker: err = %v, want ErrStoreCorrupt", err)
	}

	// Restore a good copy; the next checked Open passes and clears the marker.
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
	if _, err := os.Stat(corruptMarkerPath(path)); !os.IsNotExist(err) {
		t.Fatalf("passing check left the marker: %v", err)
	}
	if db, err := OpenFast(path); err != nil {
		t.Fatalf("OpenFast after a passing check: %v", err)
	} else {
		db.Close()
	}
}
