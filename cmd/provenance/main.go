// ai-provenance — session provenance export for a task.
//
// Usage:
//
//	ai-provenance export <task-id> [--json] [project-dir]
//	ai-provenance list [project-dir]
//
// export: print a provenance trace (sessions, faults, verify) for a task.
// list:   print tasks that have at least one recorded session.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/inth3shadows/runecho/internal/provenance"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "export":
		runExport(os.Args[2:])
	case "list":
		runList(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "ai-provenance: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func runExport(args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "ai-provenance export: task-id required")
		os.Exit(1)
	}
	taskID := rest[0]

	root := "."
	if len(rest) > 1 {
		root = rest[1]
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fatalf("cannot resolve root %q: %v", root, err)
	}

	prov, err := provenance.Export(absRoot, taskID)
	if err != nil {
		fatalf("export: %v", err)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(prov); err != nil {
			fatalf("encode: %v", err)
		}
		return
	}

	fmt.Println(provenance.FormatText(prov))
}

func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	root := "."
	if rest := fs.Args(); len(rest) > 0 {
		root = rest[0]
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fatalf("cannot resolve root %q: %v", root, err)
	}

	summaries, err := provenance.List(absRoot)
	if err != nil {
		fatalf("list: %v", err)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(summaries); err != nil {
			fatalf("encode: %v", err)
		}
		return
	}

	if len(summaries) == 0 {
		fmt.Println("No tasks with recorded sessions.")
		return
	}

	fmt.Printf("%-6s %-8s %8s %10s  %s\n", "TASK", "STATUS", "SESSIONS", "COST", "TITLE")
	for _, s := range summaries {
		fmt.Printf("%-6s %-8s %8d %10s  %s\n",
			"#"+s.Task.ID, s.Task.Status,
			s.SessionCount, fmt.Sprintf("$%.4f", s.TotalCost),
			s.Task.Title)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  ai-provenance export <task-id> [--json] [project-dir]")
	fmt.Fprintln(os.Stderr, "  ai-provenance list [--json] [project-dir]")
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ai-provenance: "+format+"\n", args...)
	os.Exit(1)
}
