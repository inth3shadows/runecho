//go:build unix

package main

import (
	"os"
	"syscall"
)

// openTranscript opens path without blocking: a FIFO with no writer blocks in
// open(2) itself, before any check on the opened file could run (#531 review).
// O_NONBLOCK has no effect on reads of a regular file, the only kind
// transcriptModel goes on to read.
func openTranscript(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
