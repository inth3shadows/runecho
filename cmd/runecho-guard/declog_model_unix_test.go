//go:build unix

package main

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// F2 (#531 review): a FIFO with no writer blocks open(2) itself, so a
// regular-file check after the open never runs. The read must return "", fast.
func TestTranscriptModel_FIFODoesNotBlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fifo.jsonl")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	got := make(chan string, 1)
	go func() { got <- transcriptModel(p) }()
	select {
	case m := <-got:
		if m != "" {
			t.Errorf("transcriptModel(fifo) = %q, want \"\"", m)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transcriptModel blocked on a FIFO")
	}
}
