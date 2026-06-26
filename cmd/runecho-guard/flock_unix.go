//go:build unix

package main

import (
	"os"
	"syscall"
)

// withFileLock runs fn while holding an exclusive advisory lock on lockPath, so
// concurrent PostToolUse hooks serialize their load-modify-save of the
// learned-allow store instead of clobbering each other (last-writer-wins would
// silently drop increments). Fail-open: if the lock file can't be opened or the
// lock can't be taken, fn still runs unlocked — persistence is best-effort and
// must never block the hook. The lock is process-advisory (flock), released on
// close, and the lock file itself is never read, so a stale one is harmless.
func withFileLock(lockPath string, fn func()) {
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
