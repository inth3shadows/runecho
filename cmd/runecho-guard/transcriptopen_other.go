//go:build !unix

package main

import "os"

// openTranscript is a plain open off Unix, where a path cannot name a FIFO.
// RunEcho targets Unix (WSL/macOS); this keeps a Windows cross-compile green.
func openTranscript(path string) (*os.File, error) {
	return os.Open(path)
}
