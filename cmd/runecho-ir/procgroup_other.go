//go:build !unix

package main

import (
	"os/exec"
	"time"
)

// killGroupOnCancel: no process groups here; bound stragglers with WaitDelay
// only. freshen does not run on Windows anyway (see freshenLocked).
func killGroupOnCancel(cmd *exec.Cmd) { cmd.WaitDelay = 5 * time.Second }

// tryFreshenLock: no flock; run unlocked (fail-open, like store.WithFileLock).
func tryFreshenLock(string) (func(), bool) { return func() {}, true }
