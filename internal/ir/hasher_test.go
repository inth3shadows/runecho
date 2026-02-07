package ir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashFile_Determinism(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := []byte("deterministic content for hashing")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Hash 100 times
	hashes := make([]string, 100)
	for i := 0; i < 100; i++ {
		hash, err := HashFile(testFile)
		if err != nil {
			t.Fatalf("Hash failed on iteration %d: %v", i, err)
		}
		hashes[i] = hash
	}

	// Verify all hashes are identical
	first := hashes[0]
	for i := 1; i < 100; i++ {
		if hashes[i] != first {
			t.Errorf("Hash %d differs from first hash", i)
			t.Logf("First: %s", first)
			t.Logf("Current: %s", hashes[i])
		}
	}
}

func TestHashFile_Format(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	hash, err := HashFile(testFile)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	// SHA256 hex should be 64 characters
	if len(hash) != 64 {
		t.Errorf("Hash length is %d, expected 64", len(hash))
	}

	// Should be lowercase hex
	for i, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("Character at position %d is not lowercase hex: %c", i, c)
		}
	}
}

func TestHashFile_DifferentContent(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")

	if err := os.WriteFile(file1, []byte("content A"), 0644); err != nil {
		t.Fatalf("Failed to write file1: %v", err)
	}
	if err := os.WriteFile(file2, []byte("content B"), 0644); err != nil {
		t.Fatalf("Failed to write file2: %v", err)
	}

	hash1, err := HashFile(file1)
	if err != nil {
		t.Fatalf("Hash file1 failed: %v", err)
	}

	hash2, err := HashFile(file2)
	if err != nil {
		t.Fatalf("Hash file2 failed: %v", err)
	}

	if hash1 == hash2 {
		t.Errorf("Different files should have different hashes")
	}
}

func TestHashFile_SameContent(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")

	content := []byte("identical content")
	if err := os.WriteFile(file1, content, 0644); err != nil {
		t.Fatalf("Failed to write file1: %v", err)
	}
	if err := os.WriteFile(file2, content, 0644); err != nil {
		t.Fatalf("Failed to write file2: %v", err)
	}

	hash1, err := HashFile(file1)
	if err != nil {
		t.Fatalf("Hash file1 failed: %v", err)
	}

	hash2, err := HashFile(file2)
	if err != nil {
		t.Fatalf("Hash file2 failed: %v", err)
	}

	if hash1 != hash2 {
		t.Errorf("Identical content should have same hash")
		t.Logf("Hash1: %s", hash1)
		t.Logf("Hash2: %s", hash2)
	}
}

func TestHashFile_NonExistentFile(t *testing.T) {
	_, err := HashFile("/nonexistent/path/to/file.txt")
	if err == nil {
		t.Errorf("Expected error for non-existent file")
	}
}

func TestHashBytes_Determinism(t *testing.T) {
	content := []byte("test content for byte hashing")

	// Hash 100 times
	hashes := make([]string, 100)
	for i := 0; i < 100; i++ {
		hashes[i] = HashBytes(content)
	}

	// Verify all hashes are identical
	first := hashes[0]
	for i := 1; i < 100; i++ {
		if hashes[i] != first {
			t.Errorf("Hash %d differs from first hash", i)
		}
	}
}

func TestHashBytes_Format(t *testing.T) {
	hash := HashBytes([]byte("test"))

	// SHA256 hex should be 64 characters
	if len(hash) != 64 {
		t.Errorf("Hash length is %d, expected 64", len(hash))
	}

	// Should be lowercase hex
	for i, c := range hash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("Character at position %d is not lowercase hex: %c", i, c)
		}
	}
}
