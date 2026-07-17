//go:build !unix

package store

// WithFileLock has no cross-process locking on non-Unix platforms; it simply runs
// fn. RunEcho targets Unix (WSL/macOS); this keeps a Windows cross-compile green
// without pulling in a platform-specific lock API. Callers still rely on the atomic
// temp+rename in their save path for torn-write safety there.
func WithFileLock(lockPath string, fn func()) { fn() }
