//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// killGroupOnCancel makes a context timeout on cmd a HARD limit. exec's default
// cancel kills only the direct child — bash — while the `go build` processes
// install.sh started keep running and keep the output pipe open, so Wait would
// block until they finished on their own (#375 review: a 1s context on
// `bash -c "sleep 6"` returned after 6s). Running the child in its own process
// group and killing the whole group ends the builds too; WaitDelay bounds any
// straggler still holding a pipe.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
}

// tryFreshenLock takes an exclusive, NON-blocking lock on dir/freshenLockFile.
// ok=false means another freshen holds it right now: the caller skips rather
// than queueing, so one hung build can never stack every later tick (and an
// interactive --reinstall) up behind it. An error opening the file is not
// contention; the caller runs unlocked, as every other lock here fails open.
func tryFreshenLock(dir string) (release func(), ok bool) {
	f, err := os.OpenFile(filepath.Join(dir, freshenLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, false
		}
		return func() {}, true
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, true
}
