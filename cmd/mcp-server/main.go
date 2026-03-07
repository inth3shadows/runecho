// ai-mcp-server — RunEcho MCP stdio server.
//
// Usage:
//
//	ai-mcp-server [--workspace=<path>]
//
// Workspace resolution order:
//  1. --workspace flag
//  2. RUNECHO_WORKSPACE env var
//  3. Current working directory
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/inth3shadows/runecho/internal/mcp"
)

func main() {
	workspace := flag.String("workspace", "", "project root (overrides RUNECHO_WORKSPACE and $PWD)")
	flag.Parse()

	ws := resolveWorkspace(*workspace)
	srv := mcp.NewServer(ws, "runecho", "0.1.0")
	if err := srv.Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "ai-mcp-server:", err)
		os.Exit(1)
	}
}

func resolveWorkspace(flag string) string {
	if flag != "" {
		abs, err := filepath.Abs(flag)
		if err == nil {
			return abs
		}
		return flag
	}
	if env := os.Getenv("RUNECHO_WORKSPACE"); env != "" {
		abs, err := filepath.Abs(env)
		if err == nil {
			return abs
		}
		return env
	}
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ai-mcp-server: cannot determine working directory:", err)
		os.Exit(1)
	}
	return wd
}
