// Command runecho-mcp is the RunEcho truth-oracle MCP server. It speaks
// newline-delimited JSON-RPC 2.0 over stdio and exposes read-only structure /
// drift / hash / status / health tools over the central snapshot store. It is
// model-free: deterministic queries only, no LLM, vendor-neutral.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/inth3shadows/runecho/internal/mcp"
	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/store"
	"github.com/inth3shadows/runecho/internal/version"
)

func main() { os.Exit(run(os.Args, os.Stdin, os.Stdout, os.Stderr)) }

// run is the testable seam, matching runecho-ir's shape. main() is otherwise
// unreachable from a test, which is how this binary — one of the three RunEcho
// ships — went out with zero of them while 29,000 lines of tests sat next to it.
// The wiring here is exactly what a packaging regression breaks: a store that
// fails to open, an oracle that is never registered, or a diagnostic written to
// stdout, which corrupts the stdio JSON-RPC framing for every client.
//
// args is threaded in rather than read from the process-global os.Args so the
// seam stays honest: a test can drive the flag path without mutating global
// state that `go test` also owns.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Answer flags before touching the store. This binary had no flag parsing at
	// all, so ANY argument fell through to Serve and blocked forever on a
	// JSON-RPC `initialize` frame that a command-line caller never sends —
	// indistinguishable from a hang, and the slower Open got (PRAGMA quick_check
	// reads the whole file) the more it looked like one (#351).
	//
	// The unknown-flag rejection matters as much as the two named flags: a typo
	// (`--verison`) or a wrapper passing something unrecognised reproduces the
	// original hang one argument over. Exiting with usage is the failure mode a
	// human can act on. Mirrors cmd/runecho-ir/main.go, which handles the same
	// three cases ahead of any DB work.
	//
	// A bare invocation with no arguments is the normal case — that is how an
	// MCP host launches this — and falls straight through to serving.
	if len(args) > 1 {
		switch args[1] {
		case "--version", "-v":
			fmt.Fprintln(stdout, "runecho-mcp "+version.Version)
			return 0
		case "--help", "-h", "help":
			printUsage(stdout)
			return 0
		default:
			fmt.Fprintf(stderr, "runecho-mcp: unexpected argument %q\n", args[1])
			printUsage(stderr)
			return 2
		}
	}

	dir, err := runechoDir()
	if err != nil {
		fmt.Fprintf(stderr, "runecho-mcp: %v\n", err)
		return 1
	}
	// 0700: keep other local users out of the central store on a shared host (the
	// dir mode gates traversal to history.db and its sidecars). Matches runecho-ir.
	if err := os.MkdirAll(dir, 0700); err != nil {
		fmt.Fprintf(stderr, "runecho-mcp: create %s: %v\n", dir, err)
		return 1
	}
	dbPath := filepath.Join(dir, "history.db")

	db, err := snapshot.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "runecho-mcp: open store: %v\n", err)
		return 1
	}
	defer db.Close()

	// Diagnostics to stderr; stdout is reserved for JSON-RPC frames (stdio
	// transport — a stray stdout write corrupts the protocol).
	server := mcp.NewServer("runecho", version.Version).WithLogWriter(stderr)
	mcp.NewOracle(db, dbPath).Register(server)

	if err := server.Serve(stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "runecho-mcp: serve: %v\n", err)
		return 1
	}
	return 0
}

// printUsage states the one thing a human running this by hand needs to know:
// it is a stdio server, not an interactive command, so an empty terminal after
// launching it is expected rather than a hang.
func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: runecho-mcp [--version|-v] [--help|-h]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  RunEcho's read-only truth-oracle MCP server. Takes no arguments in normal")
	fmt.Fprintln(w, "  use: an MCP host launches it and speaks newline-delimited JSON-RPC 2.0 over")
	fmt.Fprintln(w, "  stdin/stdout. Run from a terminal it will simply wait for a request.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "  Store: $RUNECHO_HOME/history.db (default ~/.runecho/history.db)")
	fmt.Fprintln(w, "  Inspect an install with: runecho-ir doctor")
}

// runechoDir delegates to the shared store helper so all entry points use a
// single definition and stay in sync when the resolution logic changes.
func runechoDir() (string, error) { return store.RunechoDir() }
