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

	server := mcp.NewServer("runecho", version)
	mcp.NewOracle(db, dbPath).Register(server)

	if err := server.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "runecho-mcp: serve: %v\n", err)
		os.Exit(1)
	}
}

// runechoDir resolves the central store directory: $RUNECHO_HOME if set, else
// ~/.runecho. Mirrors cmd/ir so both share one store.
func runechoDir() (string, error) {
	if h := os.Getenv("RUNECHO_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".runecho"), nil
}
