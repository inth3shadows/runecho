//go:build !unix

package main

// withFileLock has no cross-process locking on non-Unix platforms; it simply
// runs fn. RunEcho's guard targets Unix (WSL/macOS); this keeps a Windows
// cross-compile green without pulling in a platform-specific lock API. The
// atomic temp+rename in saveLearnedAllow still prevents a torn file there.
func withFileLock(lockPath string, fn func()) { fn() }
