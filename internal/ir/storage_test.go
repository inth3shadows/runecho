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
				Hash:      "abc123",
				Imports:   []string{"react"},
				Functions: []string{"foo", "bar"},
				Classes:   []string{"Zulu"},
				Exports:   []string{"foo"},
			},
			"alpha.ts": {
				Hash:      "def456",
				Imports:   []string{"lodash"},
				Functions: []string{"baz"},
				Classes:   []string{},
				Exports:   []string{"baz"},
			},
			"beta.js": {
				Hash:      "ghi789",
				Imports:   []string{},
				Functions: []string{"test"},
				Classes:   []string{"Test"},
				Exports:   []string{},
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

func TestIR_SaveAndLoad_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	irPath := filepath.Join(tmpDir, "ir.json")

	original := &IR{
		Version: 1,
		Files: map[string]FileIR{
			"test.ts": {
				Hash:      "abcdef123456",
				Imports:   []string{"react", "lodash"},
				Functions: []string{"foo", "bar"},
				Classes:   []string{"Test"},
				Exports:   []string{"foo", "bar"},
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
		equalStringSlices(a.Imports, b.Imports) &&
		equalStringSlices(a.Functions, b.Functions) &&
		equalStringSlices(a.Classes, b.Classes) &&
		equalStringSlices(a.Exports, b.Exports)
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

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
