//go:build !unix

package main

import "os"

// openTranscript is a plain open off Unix. A Windows named pipe (\\.\pipe\x)
// is still caught after the open by transcriptModel's regular-file check.
// RunEcho targets Unix (WSL/macOS); this keeps a Windows cross-compile green.
func openTranscript(path string) (*os.File, error) {
	return os.Open(path)
}
