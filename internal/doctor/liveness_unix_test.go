//go:build unix

package doctor

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// F2 (#555 review): a log path that is a FIFO with no writer must not hang
// doctor; it is not a log, so read nothing and say why.
func TestReadLogTail_FIFODoesNotBlock(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "reindex.log")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := readLogTail(fifo, 1<<20)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("err = %v, want a 'not a regular file' error", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("readLogTail blocked on a FIFO; doctor would hang")
	}
}
