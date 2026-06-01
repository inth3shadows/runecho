// Package store provides shared access to the central RunEcho store directory.
package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// RunechoDir returns the central store directory: $RUNECHO_HOME if set,
// otherwise ~/.runecho. This is the single definition shared by all entry
// points; duplicate copies in cmd packages caused divergence risk.
func RunechoDir() (string, error) {
	if h := os.Getenv("RUNECHO_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".runecho"), nil
}
