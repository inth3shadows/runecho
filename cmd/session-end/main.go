package main

import (
	"io"
	"os"
	"time"

	"github.com/inth3shadows/runecho/internal/sessionend"
)

func main() {
	// Read stdin with a 5s timeout. Claude Code writes the event immediately,
	// so a timeout means we were invoked interactively with no piped input.
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(os.Stdin)
		ch <- result{data, err}
	}()

	var input []byte
	select {
	case r := <-ch:
		if r.err != nil {
			os.Exit(0)
		}
		input = r.data
	case <-time.After(5 * time.Second):
		os.Stderr.WriteString("ai-session-end: no stdin event after 5s — run via Claude Code SessionEnd hook or pipe JSON\n")
		os.Exit(0)
	}

	_ = sessionend.Run(input)
	// Always exit 0 — session-end hooks must not block Claude Code.
}
