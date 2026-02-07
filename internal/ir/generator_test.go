package ir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerator_Generate_Determinism(t *testing.T) {
	// Create temporary test directory with source files
	tmpDir := t.TempDir()

	testFiles := map[string]string{
		"alpha.ts": `
function foo() {}
export { foo };
`,
		"beta.js": `
class Bar {}
export default Bar;
`,
		"gamma.gs": `
const baz = () => {};
export const baz;
`,
	}

	for name, content := range testFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file %s: %v", name, err)
		}
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	// Generate IR 100 times
	results := make([]*IR, 100)
	for i := 0; i < 100; i++ {
		ir, err := generator.Generate(tmpDir)
		if err != nil {
			t.Fatalf("Generate failed on iteration %d: %v", i, err)
		}
		results[i] = ir
	}

	// Verify all results are identical
	first := results[0]
	for i := 1; i < 100; i++ {
		if !equalIR(first, results[i]) {
			t.Errorf("IR %d differs from first IR", i)
			t.Logf("First: %+v", first)
			t.Logf("Current: %+v", results[i])
		}
	}
}

func TestGenerator_Generate_JSONDeterminism(t *testing.T) {
	// Create test directory
	tmpDir := t.TempDir()

	testFiles := map[string]string{
		"z.ts": "function z() {}",
		"a.ts": "function a() {}",
		"m.ts": "function m() {}",
	}

	for name, content := range testFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file %s: %v", name, err)
		}
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	// Generate IR and marshal to JSON 100 times
	jsonResults := make([]string, 100)
	for i := 0; i < 100; i++ {
		ir, err := generator.Generate(tmpDir)
		if err != nil {
			t.Fatalf("Generate failed on iteration %d: %v", i, err)
		}

		// Marshal to JSON
		data, err := ir.MarshalJSON()
		if err != nil {
			t.Fatalf("Marshal failed on iteration %d: %v", i, err)
		}

		jsonResults[i] = string(data)
	}

	// Verify all JSON outputs are byte-identical
	first := jsonResults[0]
	for i := 1; i < 100; i++ {
		if first != jsonResults[i] {
			t.Errorf("JSON output %d differs from first output", i)
			t.Logf("First:\n%s", first)
			t.Logf("Current:\n%s", jsonResults[i])
			break // Only show first difference
		}
	}
}

func TestGenerator_Generate_IgnoredPaths(t *testing.T) {
	tmpDir := t.TempDir()

	// Create files in ignored directories
	ignoredDirs := []string{"node_modules", "dist", ".git", ".cursor", ".vscode"}
	for _, dir := range ignoredDirs {
		dirPath := filepath.Join(tmpDir, dir)
		if err := os.Mkdir(dirPath, 0755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", dir, err)
		}

		filePath := filepath.Join(dirPath, "ignored.js")
		if err := os.WriteFile(filePath, []byte("function ignored() {}"), 0644); err != nil {
			t.Fatalf("Failed to write ignored file: %v", err)
		}
	}

	// Create file in root (should be included)
	includedPath := filepath.Join(tmpDir, "included.js")
	if err := os.WriteFile(includedPath, []byte("function included() {}"), 0644); err != nil {
		t.Fatalf("Failed to write included file: %v", err)
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	ir, err := generator.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Should only have 1 file (included.js)
	if len(ir.Files) != 1 {
		t.Errorf("Expected 1 file, got %d", len(ir.Files))
		for path := range ir.Files {
			t.Logf("  Found: %s", path)
		}
	}
}

func TestGenerator_Generate_OnlySupportedExtensions(t *testing.T) {
	tmpDir := t.TempDir()

	testFiles := map[string]string{
		"test.js":  "function js() {}",
		"test.ts":  "function ts() {}",
		"test.gs":  "function gs() {}",
		"test.py":  "def python(): pass",
		"test.txt": "not code",
		"test.md":  "# Markdown",
	}

	for name, content := range testFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file %s: %v", name, err)
		}
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	ir, err := generator.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Should only have 3 files (.js, .ts, .gs)
	if len(ir.Files) != 3 {
		t.Errorf("Expected 3 files, got %d", len(ir.Files))
		for path := range ir.Files {
			t.Logf("  Found: %s", path)
		}
	}
}

func TestGenerator_Update_IncrementalUpdate(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial files
	file1 := filepath.Join(tmpDir, "unchanged.js")
	file2 := filepath.Join(tmpDir, "changed.js")

	if err := os.WriteFile(file1, []byte("function unchanged() {}"), 0644); err != nil {
		t.Fatalf("Failed to write file1: %v", err)
	}
	if err := os.WriteFile(file2, []byte("function old() {}"), 0644); err != nil {
		t.Fatalf("Failed to write file2: %v", err)
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	// Generate initial IR
	initialIR, err := generator.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Initial generate failed: %v", err)
	}

	// Modify one file
	if err := os.WriteFile(file2, []byte("function new() {}"), 0644); err != nil {
		t.Fatalf("Failed to modify file2: %v", err)
	}

	updatedIR, err := generator.Update(initialIR, tmpDir)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Verify unchanged file kept same hash
	unchangedPath := "unchanged.js"
	if updatedIR.Files[unchangedPath].Hash != initialIR.Files[unchangedPath].Hash {
		t.Errorf("Unchanged file hash should not change")
	}

	// Verify changed file has different hash
	changedPath := "changed.js"
	if updatedIR.Files[changedPath].Hash == initialIR.Files[changedPath].Hash {
		t.Errorf("Changed file hash should differ")
	}

	// Verify changed file has updated structure
	if len(updatedIR.Files[changedPath].Functions) == 0 {
		t.Errorf("Changed file should have parsed functions")
	}
}

func TestGenerator_Generate_PathNormalization(t *testing.T) {
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "test.js")
	if err := os.WriteFile(testFile, []byte("function test() {}"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	ir, err := generator.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Verify all paths use forward slashes (not backslashes)
	for path := range ir.Files {
		for i := 0; i < len(path); i++ {
			if path[i] == '\\' {
				t.Errorf("Path contains backslash: %s", path)
			}
		}
	}
}

func TestGenerator_Generate_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	ir, err := generator.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Should have empty files map
	if len(ir.Files) != 0 {
		t.Errorf("Expected 0 files, got %d", len(ir.Files))
	}

	// Should still have version
	if ir.Version != 1 {
		t.Errorf("Expected version 1, got %d", ir.Version)
	}
}

func TestGenerator_Generate_UnicodeNormalization(t *testing.T) {
	// Test that NFD (macOS) and NFC (Linux) filenames produce byte-identical IR JSON
	// This ensures cross-platform determinism.
	//
	// Example: "café" can be represented as:
	// - NFC: café (5 bytes: c a f é)
	// - NFD: café (6 bytes: c a f e ́)
	//
	// Unicode character U+00E9 (LATIN SMALL LETTER E WITH ACUTE):
	// - NFC: \u00e9 (single codepoint)
	// - NFD: \u0065\u0301 (e + combining acute accent)

	// Create two temporary directories
	tmpDirNFC := t.TempDir()
	tmpDirNFD := t.TempDir()

	// Test content (identical)
	testContent := "function test() {}\nexport { test };"

	// Create file with NFC filename in first directory
	// "café.ts" using NFC (composed form)
	filenameNFC := "caf\u00e9.ts" // café using single codepoint
	pathNFC := filepath.Join(tmpDirNFC, filenameNFC)
	if err := os.WriteFile(pathNFC, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to write NFC file: %v", err)
	}

	// Create file with NFD filename in second directory
	// "café.ts" using NFD (decomposed form)
	filenameNFD := "cafe\u0301.ts" // café using e + combining acute
	pathNFD := filepath.Join(tmpDirNFD, filenameNFD)
	if err := os.WriteFile(pathNFD, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to write NFD file: %v", err)
	}

	// Verify the filenames are different byte sequences but represent the same string
	if filenameNFC == filenameNFD {
		t.Fatalf("Test setup error: NFC and NFD filenames should be different byte sequences")
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	// Generate IR for NFC directory
	irNFC, err := generator.Generate(tmpDirNFC)
	if err != nil {
		t.Fatalf("Generate failed for NFC: %v", err)
	}

	// Generate IR for NFD directory
	irNFD, err := generator.Generate(tmpDirNFD)
	if err != nil {
		t.Fatalf("Generate failed for NFD: %v", err)
	}

	// Marshal both to JSON
	jsonNFC, err := irNFC.MarshalJSON()
	if err != nil {
		t.Fatalf("Marshal failed for NFC: %v", err)
	}

	jsonNFD, err := irNFD.MarshalJSON()
	if err != nil {
		t.Fatalf("Marshal failed for NFD: %v", err)
	}

	// Verify JSON outputs are byte-identical
	if string(jsonNFC) != string(jsonNFD) {
		t.Errorf("NFC and NFD produced different JSON outputs")
		t.Logf("NFC JSON:\n%s", string(jsonNFC))
		t.Logf("NFD JSON:\n%s", string(jsonNFD))

		// Find first difference
		minLen := len(jsonNFC)
		if len(jsonNFD) < minLen {
			minLen = len(jsonNFD)
		}
		for i := 0; i < minLen; i++ {
			if jsonNFC[i] != jsonNFD[i] {
				t.Logf("First byte difference at position %d:", i)
				t.Logf("  NFC: 0x%02x ('%c')", jsonNFC[i], jsonNFC[i])
				t.Logf("  NFD: 0x%02x ('%c')", jsonNFD[i], jsonNFD[i])
				break
			}
		}
	}

	// Verify RootHash is identical
	if irNFC.RootHash != irNFD.RootHash {
		t.Errorf("NFC and NFD produced different RootHash values")
		t.Logf("NFC RootHash: %s", irNFC.RootHash)
		t.Logf("NFD RootHash: %s", irNFD.RootHash)
	}

	// Verify normalized path keys are identical
	if len(irNFC.Files) != len(irNFD.Files) {
		t.Errorf("Different number of files: NFC=%d, NFD=%d", len(irNFC.Files), len(irNFD.Files))
	}

	// Extract and compare path keys
	var pathsNFC []string
	for path := range irNFC.Files {
		pathsNFC = append(pathsNFC, path)
	}
	var pathsNFD []string
	for path := range irNFD.Files {
		pathsNFD = append(pathsNFD, path)
	}

	if len(pathsNFC) == 1 && len(pathsNFD) == 1 {
		if pathsNFC[0] != pathsNFD[0] {
			t.Errorf("Path keys differ:")
			t.Logf("  NFC: %q (bytes: %x)", pathsNFC[0], []byte(pathsNFC[0]))
			t.Logf("  NFD: %q (bytes: %x)", pathsNFD[0], []byte(pathsNFD[0]))
		}
	}
}

// Helper functions

func equalIR(a, b *IR) bool {
	if a.Version != b.Version {
		return false
	}
	if a.RootHash != b.RootHash {
		return false
	}
	if len(a.Files) != len(b.Files) {
		return false
	}

	for path, fileA := range a.Files {
		fileB, exists := b.Files[path]
		if !exists {
			return false
		}
		if !equalFileIR(fileA, fileB) {
			return false
		}
	}

	return true
}
