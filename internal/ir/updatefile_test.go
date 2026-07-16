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

// TestUpdateFile_SkipsSymlink pins #143: UpdateFile must skip a symlink the same way
// walkSourceFiles does. Otherwise an in-repo symlink pointing OUTSIDE the repo pulls
// the external target's content/symbols into the IR under an in-repo key via the
// per-edit refresh, while a full Generate skipped it. Covers a symlinked file target
// and a file addressed through a symlinked directory; both must match a full walk.
func TestUpdateFile_SkipsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	// The out-of-repo "secret" source the symlinks point at.
	secret := filepath.Join(outside, "secret.go")
	if err := os.WriteFile(secret, []byte("package secret\n\nfunc Exfiltrated() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package x\n\nfunc Alpha() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gen := NewGenerator(GeneratorConfig{})
	base, _, err := gen.Generate(root)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// (1) A symlinked source file inside the repo pointing at the outside secret.
	link := filepath.Join(root, "evil.go")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	updated, changed, err := gen.UpdateFile(base, root, link)
	if err != nil {
		t.Fatalf("UpdateFile(symlink): %v", err)
	}
	if changed {
		t.Error("symlinked file must not change the IR (a full walk skips it)")
	}
	if _, ok := updated.Files["evil.go"]; ok {
		t.Error("symlinked file was indexed under an in-repo key (#143 exfiltration)")
	}
	for path, f := range updated.Files {
		for _, s := range f.Symbols {
			if s.Name == "Exfiltrated" {
				t.Errorf("out-of-repo symbol pulled into IR via symlink under key %q", path)
			}
		}
	}
	// The per-edit result must match a full walk — both skip the symlink.
	if full, _, _ := gen.Generate(root); updated.RootHash != full.RootHash {
		t.Errorf("symlink: RootHash %s != full generate %s", updated.RootHash, full.RootHash)
	}

	// (2) A supported file addressed THROUGH a symlinked directory inside the repo.
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Skipf("directory symlinks unavailable on this platform: %v", err)
	}
	viaDir := filepath.Join(root, "linkdir", "secret.go")
	if _, changed, err := gen.UpdateFile(base, root, viaDir); err != nil || changed {
		t.Errorf("file under a symlinked dir must be skipped: changed=%v err=%v", changed, err)
	}
}
