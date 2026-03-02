package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FindJSONL locates the Claude Code session log for the given session ID.
// Tries the computed slug path first; falls back to walking ~/.claude/projects/.
func FindJSONL(sessionID, projectRoot string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home dir: %w", err)
	}

	projectsDir := filepath.Join(homeDir, ".claude", "projects")
	filename := sessionID + ".jsonl"

	// Try direct path via slug
	slug := pathToSlug(projectRoot)
	direct := filepath.Join(projectsDir, slug, filename)
	if _, err := os.Stat(direct); err == nil {
		return direct, nil
	}

	// Fall back: walk all project dirs
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", projectsDir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(projectsDir, entry.Name(), filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("session log not found for session %s (searched %s)", sessionID, projectsDir)
}

// pathToSlug converts a filesystem path to Claude Code's project directory slug.
// Rule: replace each non-alphanumeric, non-hyphen character with a hyphen.
// Example: C:\Users\ericm\personal_projects\.ai → C--Users-ericm-personal-projects--ai
func pathToSlug(path string) string {
	// Normalize separators so the rule applies uniformly
	path = strings.ReplaceAll(path, "\\", "/")
	var b strings.Builder
	for _, c := range path {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}
