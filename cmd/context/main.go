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
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ctx "github.com/inth3shadows/runecho/internal/context"
	"github.com/inth3shadows/runecho/internal/ir"
)

func main() {
	budget := flag.Int("budget", 4000, "approximate token budget")
	providers := flag.String("providers", "", "comma-separated provider list")
	sessionID := flag.String("session", "", "session ID")
	prompt := flag.String("prompt", "", "user prompt for relevance scoring")
	noCache := flag.Bool("no-cache", false, "bypass result cache")
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

	// Attempt cache lookup unless --no-cache is set.
	providersKey := *providers
	if providersKey == "" {
		providersKey = strings.Join(ctx.DefaultProviders, ",")
	}

	if !*noCache {
		stateHash := computeStateHash(absRoot)
		promptHash := hashString(*prompt)
		dbPath := filepath.Join(absRoot, ".ai", "context-cache.db")
		if cache, err := ctx.OpenResultCache(dbPath); err == nil {
			defer cache.Close()
			if cached, ok := cache.Get(stateHash, promptHash, providersKey, *budget); ok {
				if cached != "" {
					fmt.Println(cached)
				}
				return
			}
			// Cache miss: compile and store.
			compiler := ctx.NewCompiler()
			output, err := compiler.Compile(req, providerList)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ai-context: compile error: %v\n", err)
				os.Exit(1)
			}
			cache.Put(stateHash, promptHash, providersKey, output, *budget)
			if output != "" {
				fmt.Println(output)
			}
			return
		}
		// Cache open failed — fall through to uncached compile.
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

// computeStateHash produces a short hex fingerprint of the workspace's
// context-relevant files: ir.json root_hash plus secondary input files.
func computeStateHash(workspace string) string {
	h := sha256.New()

	// Primary: ir_hash from ir.json.
	irPath := filepath.Join(workspace, ".ai", "ir.json")
	if irData, err := ir.Load(irPath); err == nil {
		h.Write([]byte(irData.RootHash))
	}

	// Secondary: files read by providers (content-hash for correctness).
	for _, rel := range []string{
		".ai/handoff.md",
		".ai/tasks.json",
		".ai/contract.md",
		".ai/verify.jsonl",
	} {
		if data, err := os.ReadFile(filepath.Join(workspace, rel)); err == nil {
			h.Write(data)
		}
	}

	sum := fmt.Sprintf("%x", h.Sum(nil))
	return sum[:16]
}

// hashString returns the first 16 hex chars of SHA256(s).
func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)[:16]
}
