package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inth3shadows/runecho/internal/document"
)

func main() {
	irDiff := flag.String("ir-diff", "", "IR diff from session (empty = skip if all docs exist)")
	docsFlag := flag.String("docs", "", "override configured docs: README.md,TECHNICAL.md,USAGE.md")
	dryRun := flag.Bool("dry-run", false, "print what would be generated, no writes")
	force := flag.Bool("force", false, "bypass change-gate and regenerate all docs")
	flag.Parse()

	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-document: cannot resolve path %q: %v\n", root, err)
		os.Exit(1)
	}

	apiKey := os.Getenv("RUNECHO_CLASSIFIER_KEY")
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "ai-document: RUNECHO_CLASSIFIER_KEY not set, skipping\n")
		os.Exit(0)
	}

	// Check status of all possible doc files upfront
	statuses := document.CheckDocStatus(absRoot, document.AllDocFilenames())

	ctx, err := document.GatherContext(absRoot, *irDiff)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-document: gather context: %v\n", err)
		os.Exit(1)
	}

	// --docs flag overrides .ai/document.yaml
	if *docsFlag != "" {
		ctx.DocTypes = strings.Split(*docsFlag, ",")
	}

	// Narrow statuses to the configured doc types
	modeStatuses := make(map[string]document.DocStatus, len(ctx.DocTypes))
	for _, fn := range ctx.DocTypes {
		modeStatuses[fn] = statuses[fn]
	}

	// Change-gate: all configured docs exist and no IR diff → skip (unless --force)
	if !*force && *irDiff == "" {
		allExist := true
		for _, fn := range ctx.DocTypes {
			if !modeStatuses[fn].Exists {
				allExist = false
				break
			}
		}
		if allExist {
			fmt.Fprintf(os.Stderr, "ai-document: all docs exist and no IR diff, skipping\n")
			os.Exit(0)
		}
	}

	docs, err := document.Generate(ctx, modeStatuses, apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-document: generate: %v\n", err)
		os.Exit(1)
	}

	if *dryRun {
		for _, fn := range ctx.DocTypes {
			printDryRun(fn, docs[fn])
		}
		os.Exit(0)
	}

	if err := document.Write(absRoot, docs, modeStatuses); err != nil {
		fmt.Fprintf(os.Stderr, "ai-document: write: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "ai-document: done (docs=%s root=%s)\n",
		strings.Join(ctx.DocTypes, ","), absRoot)
}

func printDryRun(filename, content string) {
	if content == "" {
		fmt.Printf("[dry-run] %s — (empty, would skip)\n", filename)
		return
	}
	lines := splitLines(content)
	preview := lines
	if len(lines) > 5 {
		preview = lines[:5]
	}
	fmt.Printf("[dry-run] %s:\n", filename)
	for _, l := range preview {
		fmt.Printf("  %s\n", l)
	}
	if len(lines) > 5 {
		fmt.Printf("  ... (%d more lines)\n", len(lines)-5)
	}
	fmt.Println()
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
