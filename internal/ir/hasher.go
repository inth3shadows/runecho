package ir

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// HashFile computes SHA256 hash of a file and returns it as lowercase hex string.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("failed to hash file: %w", err)
	}

	// Return lowercase hex encoding
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// HashBytes computes SHA256 hash of byte slice and returns lowercase hex string.
func HashBytes(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:])
}

// ComputeRootHash computes deterministic root hash from IR files.
// For each file (sorted by normalized path):
//   normalized_path + ":" + file_hash
// Join with newlines, SHA256 hash, return lowercase hex.
func ComputeRootHash(files map[string]FileIR) string {
	if len(files) == 0 {
		return HashBytes([]byte{})
	}

	// Sort paths for determinism
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	// Build concatenated string
	var builder strings.Builder
	for i, path := range paths {
		if i > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(path)
		builder.WriteByte(':')
		builder.WriteString(files[path].Hash)
	}

	return HashBytes([]byte(builder.String()))
}
