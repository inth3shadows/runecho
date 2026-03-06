package main

import (
	"io"
	"os"

	"github.com/inth3shadows/runecho/internal/sessionend"
)

func main() {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(0) // never block the session
	}

	_ = sessionend.Run(input)
	// Always exit 0 — session-end hooks must not block Claude Code.
}
