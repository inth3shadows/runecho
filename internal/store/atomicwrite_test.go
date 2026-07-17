package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAtomicWriteFile covers the shared write helper's contract: it creates the
// file, overwrites it in place, produces an owner-only (0600) file, never clobbers
// a fixed-name "<path>.tmp" sibling (its temp names are always "<base>.tmp-*"), and
// leaves no temp file behind on success.
func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	if err := AtomicWriteFile(path, []byte("v1")); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "v1" {
		t.Errorf("content = %q, want v1", b)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatalf("stat: %v", err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600 (owner-only)", fi.Mode().Perm())
	}

	// Overwrite in place with different-length content.
	if err := AtomicWriteFile(path, []byte("v2-longer")); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "v2-longer" {
		t.Errorf("content = %q, want v2-longer", b)
	}

	// A pre-existing fixed-name "<path>.tmp" sibling must survive — the helper only
	// ever creates unique "<base>.tmp-*" temps, so a concurrent writer's scratch is
	// never clobbered (the F61 invariant IR.Save relied on).
	sentinel := path + ".tmp"
	if err := os.WriteFile(sentinel, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(path, []byte("v3")); err != nil {
		t.Fatalf("write v3: %v", err)
	}
	if b, _ := os.ReadFile(sentinel); string(b) != "other" {
		t.Errorf("fixed-name sibling clobbered: %q", b)
	}

	// No orphaned temp after a successful write.
	if leftover, _ := filepath.Glob(path + ".tmp-*"); len(leftover) != 0 {
		t.Errorf("temp files left after success: %v", leftover)
	}
}
