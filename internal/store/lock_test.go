package store

import (
	"path/filepath"
	"testing"
)

// TestRefreshLockPath pins the exact lock-file name. The guard hook and the CLI
// (reindex/index) both derive their lock path here; a drift in this name would
// silently give them different lock files and defeat the mutual exclusion (#137).
func TestRefreshLockPath(t *testing.T) {
	got := RefreshLockPath("/store", 42)
	want := filepath.Join("/store", "e6-refresh-42.lock")
	if got != want {
		t.Errorf("RefreshLockPath = %q, want %q", got, want)
	}
}
