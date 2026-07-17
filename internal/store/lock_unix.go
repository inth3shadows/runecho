//go:build unix

package store

import (
	"os"
	"syscall"
)

// WithFileLock runs fn while holding an exclusive advisory lock on lockPath, so
// concurrent processes serialize a load-modify-save of a shared file instead of
// clobbering each other (last-writer-wins would silently drop the other's write).
// Fail-open: if the lock file can't be opened or the lock can't be taken, fn still
// runs unlocked — the lock is best-effort and must never block a latency-sensitive
// caller (the guard hook). The lock is process-advisory (flock), released on
// close, and the lock file itself is never read, so a stale one is harmless.
func WithFileLock(lockPath string, fn func()) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		fn()
		return
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		fn()
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	fn()
}
