//go:build unix

package store

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWithFileLock_Serializes verifies the advisory lock actually serializes a
// load-modify-save on the same path. N goroutines each read a counter FILE, sleep
// to widen the window, then write counter+1 while holding the lock. flock excludes
// separate open-file-descriptions even within one process, so with the lock every
// increment lands (final == N); without it the read-sleep-write interleaves and
// updates are lost (final < N). The shared state is a file, not Go memory, so the
// race detector has nothing to flag — the test stays clean under `go test -race`.
func TestWithFileLock_Serializes(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")
	dataPath := filepath.Join(dir, "counter")
	if err := os.WriteFile(dataPath, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}

	const n = 40
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			WithFileLock(lockPath, func() {
				raw, _ := os.ReadFile(dataPath)
				v, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
				time.Sleep(50 * time.Microsecond) // widen the lost-update window
				_ = os.WriteFile(dataPath, []byte(strconv.Itoa(v+1)), 0o600)
			})
		}()
	}
	wg.Wait()

	raw, _ := os.ReadFile(dataPath)
	if got := strings.TrimSpace(string(raw)); got != strconv.Itoa(n) {
		t.Errorf("counter = %s, want %d — the lock did not serialize the file read-modify-write", got, n)
	}
}
