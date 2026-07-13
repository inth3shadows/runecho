package ir

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIR_MarshalJSON_Determinism(t *testing.T) {
	// Create IR with files in random insertion order
	ir := &IR{
		Version: 1,
		Files: map[string]FileIR{
			"zebra.ts": {
				Hash: "abc123",
				Symbols: []Symbol{
					{Name: "Zulu", Kind: "class"},
					{Name: "foo", Kind: "export"},
					{Name: "bar", Kind: "function"},
					{Name: "foo", Kind: "function"},
					{Name: "react", Kind: "import"},
				},
			},
			"alpha.ts": {
				Hash: "def456",
				Symbols: []Symbol{
					{Name: "baz", Kind: "export"},
					{Name: "baz", Kind: "function"},
					{Name: "lodash", Kind: "import"},
				},
			},
			"beta.js": {
				Hash: "ghi789",
				Symbols: []Symbol{
					{Name: "Test", Kind: "class"},
					{Name: "test", Kind: "function"},
				},
			},
		},
	}

	// Marshal 100 times
	results := make([][]byte, 100)
	for i := 0; i < 100; i++ {
		data, err := json.Marshal(ir)
		if err != nil {
			t.Fatalf("Marshal failed on iteration %d: %v", i, err)
		}
		results[i] = data
	}

	// Verify all results are byte-identical
	first := string(results[0])
	for i := 1; i < 100; i++ {
		current := string(results[i])
		if first != current {
			t.Errorf("Marshal result %d differs from first result", i)
			t.Logf("First:\n%s", first)
			t.Logf("Current:\n%s", current)
		}
	}
}

func TestIR_MarshalJSON_FilesAreSorted(t *testing.T) {
	ir := &IR{
		Version: 1,
		Files: map[string]FileIR{
			"z.ts": {Hash: "hash1"},
			"a.ts": {Hash: "hash2"},
			"m.ts": {Hash: "hash3"},
		},
	}

	data, err := json.Marshal(ir)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Parse JSON to check order
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	filesMap := parsed["files"].(map[string]interface{})

	// In the JSON string, "a.ts" should appear before "m.ts" and "z.ts"
	jsonStr := string(data)
	posA := indexOf(jsonStr, `"a.ts"`)
	posM := indexOf(jsonStr, `"m.ts"`)
	posZ := indexOf(jsonStr, `"z.ts"`)

	if posA == -1 || posM == -1 || posZ == -1 {
		t.Fatalf("Not all file keys found in JSON")
	}

	if !(posA < posM && posM < posZ) {
		t.Errorf("Files not in sorted order in JSON: a.ts at %d, m.ts at %d, z.ts at %d", posA, posM, posZ)
	}

	// Verify all files are present
	if len(filesMap) != 3 {
		t.Errorf("Expected 3 files, got %d", len(filesMap))
	}
}

// TestLoadCapped_RejectsOversized pins F1: Load caps the file size so a crafted
// or corrupt .ai/ir.json (which the PostToolUse guard auto-reads on every edit)
// can't OOM the process. Tested via loadCapped with a small explicit limit so it
// doesn't need a 100 MiB fixture.
func TestLoadCapped_RejectsOversized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ir.json")
	// 4 KiB of content, cap at 1 KiB → must be rejected without unmarshalling.
	if err := os.WriteFile(path, []byte("["+string(make([]byte, 4096))+"]"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCapped(path, 1024); err == nil {
		t.Fatal("expected loadCapped to reject a file over the size cap")
	}
	// A small valid IR under the cap still loads.
	small := &IR{Version: IRVersion, Files: map[string]FileIR{}}
	if err := small.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCapped(path, 1<<20); err != nil {
		t.Fatalf("loadCapped should accept a file under the cap: %v", err)
	}
}

// TestIR_Save_OwnerOnlyPerms pins B4: the IR file is 0600 and its dir 0700 (it
// holds symbol/import names), not world-readable.
func TestIR_Save_OwnerOnlyPerms(t *testing.T) {
	dir := t.TempDir()
	irPath := filepath.Join(dir, ".ai", "ir.json")
	if err := (&IR{Version: IRVersion, Files: map[string]FileIR{}}).Save(irPath); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(irPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("ir.json perm = %o, want 0600", perm)
	}
	di, err := os.Stat(filepath.Dir(irPath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0700 {
		t.Errorf(".ai dir perm = %o, want 0700", perm)
	}
}

func TestIR_SaveAndLoad_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	irPath := filepath.Join(tmpDir, "ir.json")

	original := &IR{
		Version: IRVersion,
		Files: map[string]FileIR{
			"test.ts": {
				Hash: "abcdef123456",
				// Canonical order: sortSymbols orders by kind (alphabetical:
				// class, export, function, import) then name.
				Symbols: []Symbol{
					{Name: "Test", Kind: "class", Line: 1},
					{Name: "bar", Kind: "export"},
					{Name: "foo", Kind: "export"},
					{Name: "bar", Kind: "function", Line: 9, Hash: "h2"},
					{Name: "foo", Kind: "function", Line: 3, Hash: "h1"},
					{Name: "lodash", Kind: "import"},
					{Name: "react", Kind: "import"},
				},
				Refs: []string{"baz", "qux"},
			},
		},
	}

	// Save
	if err := original.Save(irPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load
	loaded, err := Load(irPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify
	if loaded.Version != original.Version {
		t.Errorf("Version mismatch: got %d, want %d", loaded.Version, original.Version)
	}

	if len(loaded.Files) != len(original.Files) {
		t.Errorf("Files count mismatch: got %d, want %d", len(loaded.Files), len(original.Files))
	}

	for path, origFile := range original.Files {
		loadedFile, exists := loaded.Files[path]
		if !exists {
			t.Errorf("File %s missing after load", path)
			continue
		}

		if !equalFileIR(origFile, loadedFile) {
			t.Errorf("File %s differs after load", path)
			t.Logf("Original: %+v", origFile)
			t.Logf("Loaded: %+v", loadedFile)
		}
	}
}

func TestIR_Save_Determinism(t *testing.T) {
	tmpDir := t.TempDir()

	ir := &IR{
		Version: 1,
		Files: map[string]FileIR{
			"test.ts": {Hash: "abc123"},
		},
	}

	// Save 10 times to different files
	paths := make([]string, 10)
	for i := 0; i < 10; i++ {
		path := filepath.Join(tmpDir, "ir_"+string(rune(i+'0'))+".json")
		paths[i] = path
		if err := ir.Save(path); err != nil {
			t.Fatalf("Save %d failed: %v", i, err)
		}
	}

	// Read all files and verify they're byte-identical
	first, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("Failed to read first file: %v", err)
	}

	for i := 1; i < 10; i++ {
		current, err := os.ReadFile(paths[i])
		if err != nil {
			t.Fatalf("Failed to read file %d: %v", i, err)
		}

		if string(first) != string(current) {
			t.Errorf("File %d differs from first file", i)
			t.Logf("First:\n%s", string(first))
			t.Logf("Current:\n%s", string(current))
		}
	}
}

func TestIR_Save_DefaultPath(t *testing.T) {
	// Create temporary directory and change to it
	tmpDir := t.TempDir()
	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(tmpDir)

	// Create .ai directory
	os.Mkdir(".ai", 0755)

	ir := &IR{
		Version: 1,
		Files: map[string]FileIR{
			"test.ts": {Hash: "abc123"},
		},
	}

	// Save with empty string - should use DefaultIRPath
	if err := ir.Save(""); err != nil {
		t.Fatalf("Save with empty path failed: %v", err)
	}

	// Verify file exists at DefaultIRPath
	if _, err := os.Stat(DefaultIRPath); os.IsNotExist(err) {
		t.Errorf("File not created at DefaultIRPath: %s", DefaultIRPath)
	}

	// Verify we can load it back
	loaded, err := Load(DefaultIRPath)
	if err != nil {
		t.Fatalf("Load from DefaultIRPath failed: %v", err)
	}

	if loaded.Version != ir.Version {
		t.Errorf("Version mismatch after load")
	}
}

// Helper functions

func equalFileIR(a, b FileIR) bool {
	return a.Hash == b.Hash &&
		equalSymbols(a.Symbols, b.Symbols) &&
		equalStringSlices(a.Refs, b.Refs)
}

func equalSymbols(a, b []Symbol) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestFileIR_LegacyJSONUnmarshal is the IR v5 compat-shim guarantee: a pre-v5
// ir.json (parallel functions/classes/exports/imports arrays + symbol_hashes/
// symbol_lines maps, with NO `symbols` array) still loads, and its data is
// reconstructed into the canonical Symbols slice with the right kinds, lines,
// and hashes.
func TestFileIR_LegacyJSONUnmarshal(t *testing.T) {
	legacy := `{
		"hash": "h1",
		"imports": ["fmt"],
		"functions": ["DoThing"],
		"classes": ["Widget"],
		"exports": ["MAX"],
		"refs": ["println"],
		"symbol_hashes": {"function:DoThing": "abc123"},
		"symbol_lines": {"function:DoThing": 5, "class:Widget": 9, "export:MAX": 3}
	}`
	var f FileIR
	if err := json.Unmarshal([]byte(legacy), &f); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if f.Hash != "h1" || !equalStringSlices(f.Refs, []string{"println"}) {
		t.Errorf("hash/refs lost: hash=%q refs=%v", f.Hash, f.Refs)
	}
	// Sorted by (kind, name): class, export, function, import.
	want := []Symbol{
		{Name: "Widget", Kind: "class", Line: 9},
		{Name: "MAX", Kind: "export", Line: 3},
		{Name: "DoThing", Kind: "function", Line: 5, Hash: "abc123"},
		{Name: "fmt", Kind: "import"},
	}
	if !equalSymbols(f.Symbols, want) {
		t.Errorf("reconstructed Symbols = %+v, want %+v", f.Symbols, want)
	}
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// TestIR_Save_UniqueTempName pins the F61 fix: Save must not use a fixed
// "<path>.tmp" scratch name. With a fixed name, two concurrent Saves interleave
// writes into the same temp file and the loser renames a torn mix into place
// (the E6 auto-refresh hook hits exactly this). A unique per-call temp name
// makes each rename atomic with its own complete content. Pinned observably:
// a pre-existing sibling file named exactly "<path>.tmp" must survive Save
// untouched.
func TestIR_Save_UniqueTempName(t *testing.T) {
	dir := t.TempDir()
	irPath := filepath.Join(dir, "ir.json")
	sentinel := irPath + ".tmp"
	if err := os.WriteFile(sentinel, []byte("other writer's data"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := (&IR{Version: IRVersion, Files: map[string]FileIR{}}).Save(irPath); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel %s gone after Save: %v", sentinel, err)
	}
	if string(got) != "other writer's data" {
		t.Errorf("Save clobbered the fixed-name temp file: %q", got)
	}
	if _, err := Load(irPath); err != nil {
		t.Errorf("saved IR does not load back: %v", err)
	}
}
