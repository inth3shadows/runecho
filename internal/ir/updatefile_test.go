package ir

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUpdateFile covers the targeted single-file refresh: add, modify, delete,
// no-op, and out-of-repo — each leaving every other entry untouched and matching
// what a full Generate would produce.
func TestUpdateFile(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("a.go", "package x\n\nfunc Alpha() {}\n")
	gen := NewGenerator(GeneratorConfig{})
	base, _, err := gen.Generate(root)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Add a new file → changed, new entry present, old entry intact.
	write("b.go", "package x\n\nfunc Beta() {}\n")
	added, changed, err := gen.UpdateFile(base, root, filepath.Join(root, "b.go"))
	if err != nil || !changed {
		t.Fatalf("add: changed=%v err=%v", changed, err)
	}
	if _, ok := added.Files["b.go"]; !ok {
		t.Error("b.go not added")
	}
	if _, ok := added.Files["a.go"]; !ok {
		t.Error("a.go entry lost on add")
	}
	full, _, _ := gen.Generate(root)
	if added.RootHash != full.RootHash {
		t.Errorf("after add, RootHash %s != full generate %s", added.RootHash, full.RootHash)
	}

	// No-op: re-running UpdateFile on an unchanged file reports changed=false.
	if _, changed, _ := gen.UpdateFile(added, root, filepath.Join(root, "b.go")); changed {
		t.Error("unchanged file reported as changed")
	}

	// Modify → changed.
	write("b.go", "package x\n\nfunc Beta() {}\nfunc Gamma() {}\n")
	modified, changed, _ := gen.UpdateFile(added, root, filepath.Join(root, "b.go"))
	if !changed {
		t.Error("modified file not detected")
	}

	// Delete → entry removed, changed.
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	deleted, changed, _ := gen.UpdateFile(modified, root, filepath.Join(root, "b.go"))
	if !changed {
		t.Error("deleted file not detected")
	}
	if _, ok := deleted.Files["b.go"]; ok {
		t.Error("b.go entry not removed on delete")
	}

	// Out-of-repo edit → no-op, unchanged.
	if _, changed, _ := gen.UpdateFile(deleted, root, filepath.Join(t.TempDir(), "elsewhere.go")); changed {
		t.Error("out-of-repo file reported as changed")
	}
}
