package main

// Usage:
//   ai-context [--budget=<tokens>] [--providers=<list>] [--session=<id>] [--prompt=<text>] [root]
//
// Assembles and prints the session turn-1 context block for Claude Code injection.
// Replaces the inline context assembly in ir-injector.sh.
//
//   --budget=4000      approximate token budget (default 4000)
//   --providers=...    comma-separated: ir,handoff,tasks,gitdiff,churn (default: ir,gitdiff,handoff,tasks)
//   --session=<id>     session ID (informational, passed to providers)
//   --prompt=<text>    user prompt for relevance scoring
//   root               project root (default: current working directory)

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ctx "github.com/inth3shadows/runecho/internal/context"
)

func main() {
	budget := flag.Int("budget", 4000, "approximate token budget")
	providers := flag.String("providers", "", "comma-separated provider list")
	sessionID := flag.String("session", "", "session ID")
	prompt := flag.String("prompt", "", "user prompt for relevance scoring")
	flag.Parse()

	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-context: failed to resolve root: %v\n", err)
		os.Exit(1)
	}

	var providerList []string
	if *providers != "" {
		for _, p := range strings.Split(*providers, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				providerList = append(providerList, p)
			}
		}
	}

	req := ctx.Request{
		Workspace: absRoot,
		SessionID: *sessionID,
		Prompt:    *prompt,
		Budget:    *budget,
	}

	compiler := ctx.NewCompiler()
	output, err := compiler.Compile(req, providerList)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-context: compile error: %v\n", err)
		os.Exit(1)
	}

	if output != "" {
		fmt.Println(output)
	}
}
