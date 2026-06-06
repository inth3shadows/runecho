// Command runecho-mcp is the RunEcho truth-oracle MCP server. It speaks
// newline-delimited JSON-RPC 2.0 over stdio and exposes read-only structure /
// drift / hash / status / health tools over the central snapshot store. It is
// model-free: deterministic queries only, no LLM, vendor-neutral.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/inth3shadows/runecho/internal/mcp"
	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/store"
)

const version = "0.1.0"

func main() {
	dir, err := runechoDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "runecho-mcp: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "runecho-mcp: create %s: %v\n", dir, err)
		os.Exit(1)
	}
	dbPath := filepath.Join(dir, "history.db")

	db, err := snapshot.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "runecho-mcp: open store: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// Diagnostics to stderr; stdout is reserved for JSON-RPC frames (stdio
	// transport — a stray stdout write corrupts the protocol).
	server := mcp.NewServer("runecho", version).WithLogWriter(os.Stderr)
	mcp.NewOracle(db, dbPath).Register(server)

	if err := server.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "runecho-mcp: serve: %v\n", err)
		os.Exit(1)
	}
}

// runechoDir delegates to the shared store helper so all entry points use a
// single definition and stay in sync when the resolution logic changes.
func runechoDir() (string, error) { return store.RunechoDir() }
