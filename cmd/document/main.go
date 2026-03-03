package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/inth3shadows/runecho/internal/document"
)

func main() {
	irDiff := flag.String("ir-diff", "", "IR diff from session (empty = skip if all docs exist)")
	modeFlag := flag.String("mode", "", "override path detection: work|personal")
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

	// Determine which doc files we manage based on mode hint (pre-detection).
	// We check statuses for all possible files; mode will filter later.
	allFiles := []string{"README.md", "TECHNICAL.md", "USAGE.md"}
	statuses := document.CheckDocStatus(absRoot, allFiles)

	readmeStatus := statuses["README.md"]
	techStatus := statuses["TECHNICAL.md"]
	usageStatus := statuses["USAGE.md"]
	allExist := readmeStatus.Exists && techStatus.Exists && usageStatus.Exists

	// Change-gate: all docs exist and no IR diff → nothing to do (unless --force)
	if !*force && allExist && *irDiff == "" {
		fmt.Fprintf(os.Stderr, "ai-document: all docs exist and no IR diff, skipping\n")
		os.Exit(0)
	}

	ctx, err := document.GatherContext(absRoot, *irDiff)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-document: gather context: %v\n", err)
		os.Exit(1)
	}

	// Apply mode override
	if *modeFlag != "" {
		switch *modeFlag {
		case "work":
			ctx.Mode = document.ModeWork
		case "personal":
			ctx.Mode = document.ModePersonal
		default:
			fmt.Fprintf(os.Stderr, "ai-document: unknown --mode %q (use work|personal)\n", *modeFlag)
			os.Exit(1)
		}
	}

	// Narrow statuses to files we'll actually manage
	filenames := document.AllDocFilenames(ctx.Mode)
	modeStatuses := make(map[string]document.DocStatus, len(filenames))
	for _, fn := range filenames {
		modeStatuses[fn] = statuses[fn]
	}

	// Personal/unknown mode change-gate: if README exists and no IR diff, skip (unless --force)
	if !*force && ctx.Mode != document.ModeWork {
		rs := modeStatuses["README.md"]
		if rs.Exists && *irDiff == "" {
			fmt.Fprintf(os.Stderr, "ai-document: README exists and no IR diff, skipping\n")
			os.Exit(0)
		}
	}

	docs, err := document.Generate(ctx, modeStatuses, apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-document: generate: %v\n", err)
		os.Exit(1)
	}

	if *dryRun {
		printDryRun("README.md", docs.Readme)
		if ctx.Mode == document.ModeWork {
			printDryRun("TECHNICAL.md", docs.Technical)
			printDryRun("USAGE.md", docs.Usage)
		}
		os.Exit(0)
	}

	if err := document.Write(absRoot, docs, modeStatuses, ctx.Mode); err != nil {
		fmt.Fprintf(os.Stderr, "ai-document: write: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "ai-document: done (mode=%s root=%s)\n",
		document.ModeString(ctx.Mode), absRoot)
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
