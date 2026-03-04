package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// Usage: ai-ir [root-path]
// Generates .ai/ir.json for the project at root-path (default: current directory).
// If .ai/ir.json already exists, performs incremental update (only re-parses changed files).
//
// Subcommands:
//   ai-ir snapshot [--label=manual] [--session=""] [root]
//   ai-ir diff [--since=label | id-a id-b] [--compact] [root]
//   ai-ir log [--n=10] [root]
//   ai-ir verify [--session=""] [root]
//   ai-ir churn [--n=20] [--min-changes=2] [--compact] [root]
//   ai-ir validate-claims --text=<file> [--ir=<path>]
func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "snapshot":
			runSnapshot(os.Args[2:])
			return
		case "diff":
			runDiff(os.Args[2:])
			return
		case "log":
			runLog(os.Args[2:])
			return
		case "verify":
			runVerify(os.Args[2:])
			return
		case "churn":
			runChurn(os.Args[2:])
			return
		case "validate-claims":
			runValidateClaims(os.Args[2:])
			return
		case "--help", "-h", "help":
			printUsage()
			os.Exit(0)
		case "--version", "-v":
			fmt.Println("ai-ir dev")
			os.Exit(0)
		default:
			if strings.HasPrefix(os.Args[1], "-") {
				fmt.Fprintf(os.Stderr, "ai-ir: unknown flag %q\n", os.Args[1])
				printUsage()
				os.Exit(1)
			}
		}
	}
	// Default: index behavior (backward compat).
	runIndex(os.Args)
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: ai-ir [root-path]")
	fmt.Fprintln(os.Stderr, "       ai-ir snapshot [--label=manual] [--session=<id>] [root]")
	fmt.Fprintln(os.Stderr, "       ai-ir diff [--since=<label>] [--compact] [root]")
	fmt.Fprintln(os.Stderr, "       ai-ir log [--n=10] [root]")
	fmt.Fprintln(os.Stderr, "       ai-ir verify [--session=<id>] [root]")
	fmt.Fprintln(os.Stderr, "       ai-ir churn [--n=20] [--min-changes=2] [--compact] [root]")
	fmt.Fprintln(os.Stderr, "       ai-ir validate-claims --text=<file> [--ir=<path>]")
}

// runIndex is the original ai-ir [root] behavior.
func runIndex(args []string) {
	rootPath := "."
	if len(args) > 1 {
		if strings.HasPrefix(args[1], "-") {
			fmt.Fprintf(os.Stderr, "ai-ir: unknown flag %q\n", args[1])
			printUsage()
			os.Exit(1)
		}
		rootPath = args[1]
	}

	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to resolve path %q: %v\n", rootPath, err)
		os.Exit(1)
	}

	irPath := filepath.Join(absRoot, ".ai", "ir.json")

	config := ir.GeneratorConfig{
		IgnoredPaths: []string{"node_modules", "dist", ".git", ".cursor", ".vscode", "testdata"},
	}
	generator := ir.NewGenerator(config)

	var result *ir.IR

	if _, err := os.Stat(irPath); err == nil {
		existing, loadErr := ir.Load(irPath)
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load existing IR, regenerating: %v\n", loadErr)
			result, err = generator.Generate(absRoot)
		} else {
			result, err = generator.Update(existing, absRoot)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(irPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create .ai directory: %v\n", err)
			os.Exit(1)
		}
		result, err = generator.Generate(absRoot)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := result.Save(irPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to save IR: %v\n", err)
		os.Exit(1)
	}

	shortHash := result.RootHash
	if len(shortHash) > 12 {
		shortHash = shortHash[:12]
	}
	fmt.Printf("Indexed %d files — root_hash: %s...\n", len(result.Files), shortHash)
}

// runSnapshot saves a snapshot of the current ir.json.
func runSnapshot(args []string) {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	label := fs.String("label", "manual", "snapshot label (e.g. session-start, session-end, manual)")
	sessionID := fs.String("session", "", "session ID")
	fs.Parse(args)

	root := resolveRoot(fs.Args())
	irData := mustLoadIR(root)
	db := mustOpenDB(root)
	defer db.Close()

	id, err := db.SaveSnapshot(*sessionID, *label, root, irData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	shortHash := irData.RootHash
	if len(shortHash) > 12 {
		shortHash = shortHash[:12]
	}
	fmt.Printf("Snapshot saved: id=%d label=%s root_hash=%s... files=%d\n",
		id, *label, shortHash, len(irData.Files))
}

// runDiff shows structural diff between two snapshots (or a snapshot vs live).
func runDiff(args []string) {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	since := fs.String("since", "", "diff since latest snapshot with this label vs live ir.json")
	sessionID := fs.String("session", "", "filter by session ID (used with --since)")
	compact := fs.Bool("compact", false, "single-line compact output")
	fs.Parse(args)

	root := resolveRoot(positionalAfterFlags(fs.Args()))
	db := mustOpenDB(root)
	defer db.Close()

	var result snapshot.DiffResult

	if *since != "" {
		// --since mode: A = last snapshot by label, B = live ir.json.
		_ = sessionID // future: filter by session if needed
		meta, err := db.GetLatestByLabel(root, *since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if meta == nil {
			fmt.Fprintf(os.Stderr, "No snapshot found with label %q for root %q\n", *since, root)
			os.Exit(0)
		}
		irData := mustLoadIR(root)
		result, err = db.DiffLive(*meta, irData)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Two positional ID mode.
		positional := fs.Args()
		if len(positional) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: ai-ir diff --since=<label> [root]")
			fmt.Fprintln(os.Stderr, "       ai-ir diff <id-a> <id-b> [root]")
			os.Exit(1)
		}
		idA, err := strconv.ParseInt(positional[0], 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid snapshot ID %q\n", positional[0])
			os.Exit(1)
		}
		idB, err := strconv.ParseInt(positional[1], 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid snapshot ID %q\n", positional[1])
			os.Exit(1)
		}
		metaA, err := db.GetByID(idA)
		if err != nil || metaA == nil {
			fmt.Fprintf(os.Stderr, "Snapshot %d not found\n", idA)
			os.Exit(1)
		}
		metaB, err := db.GetByID(idB)
		if err != nil || metaB == nil {
			fmt.Fprintf(os.Stderr, "Snapshot %d not found\n", idB)
			os.Exit(1)
		}
		result, err = db.Diff(*metaA, *metaB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	if *compact {
		line := snapshot.FormatCompact(result)
		if line != "" {
			fmt.Println(line)
		}
	} else {
		fmt.Print(snapshot.FormatFull(result))
	}
}

// runLog prints a table of recent snapshots.
func runLog(args []string) {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	n := fs.Int("n", 10, "number of snapshots to show")
	fs.Parse(args)

	root := resolveRoot(fs.Args())
	db := mustOpenDB(root)
	defer db.Close()

	metas, err := db.List(root, *n)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(metas) == 0 {
		fmt.Println("No snapshots found.")
		return
	}

	fmt.Printf("%-5s  %-15s  %-25s  %-10s  %-8s  %s\n",
		"ID", "LABEL", "SESSION", "TIMESTAMP", "FILES", "HASH")
	fmt.Println(strings.Repeat("-", 90))
	for _, m := range metas {
		shortHash := m.RootHash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		ts := m.Timestamp.Format(time.RFC3339)
		session := m.SessionID
		if len(session) > 25 {
			session = session[:22] + "..."
		}
		fmt.Printf("%-5d  %-15s  %-25s  %-10s  %-8d  %s...\n",
			m.ID, m.Label, session, ts[:10], m.FileCount, shortHash)
	}
}

// runVerify diffs the most recent session-start snapshot against live ir.json.
func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	sessionID := fs.String("session", "", "session ID to verify (optional)")
	fs.Parse(args)

	root := resolveRoot(fs.Args())
	db := mustOpenDB(root)
	defer db.Close()

	var meta *snapshot.SnapshotMeta
	var err error

	if *sessionID != "" {
		// Find session-start for this specific session.
		// GetLatestByLabel doesn't filter by session, so we use List and filter.
		metas, listErr := db.List(root, 100)
		if listErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", listErr)
			os.Exit(1)
		}
		for i := range metas {
			if metas[i].Label == "session-start" && metas[i].SessionID == *sessionID {
				meta = &metas[i]
				break
			}
		}
	} else {
		meta, err = db.GetLatestByLabel(root, "session-start")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	if meta == nil {
		fmt.Println("No session-start snapshot found.")
		fmt.Println("Run: ai-ir snapshot --label=session-start")
		os.Exit(0)
	}

	irData := mustLoadIR(root)
	result, err := db.DiffLive(*meta, irData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Verifying against snapshot id=%d label=%s session=%s ts=%s\n\n",
		meta.ID, meta.Label, meta.SessionID, meta.Timestamp.Format(time.RFC3339))
	fmt.Print(snapshot.FormatFull(result))
}

// resolveRoot returns the absolute project root from optional positional args.
func resolveRoot(args []string) string {
	rootPath := "."
	if len(args) > 0 {
		if strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(os.Stderr, "ai-ir: unexpected flag %q where root path was expected\n", args[0])
			os.Exit(1)
		}
		rootPath = args[0]
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to resolve root %q: %v\n", rootPath, err)
		os.Exit(1)
	}
	return abs
}

// positionalAfterFlags returns the non-flag args (already parsed by fs.Args()).
func positionalAfterFlags(args []string) []string {
	return args
}

// mustLoadIR loads .ai/ir.json from root or exits.
func mustLoadIR(root string) *ir.IR {
	irPath := filepath.Join(root, ".ai", "ir.json")
	irData, err := ir.Load(irPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load ir.json at %q: %v\n", irPath, err)
		fmt.Fprintln(os.Stderr, "Run 'ai-ir [root]' first to generate the index.")
		os.Exit(1)
	}
	return irData
}

// runChurn reports file and symbol churn rate across recent snapshots.
func runChurn(args []string) {
	fs := flag.NewFlagSet("churn", flag.ExitOnError)
	n := fs.Int("n", 20, "number of snapshots to analyze")
	minChanges := fs.Int("min-changes", 2, "minimum diffs a file/symbol must appear in to be considered hot")
	compact := fs.Bool("compact", false, "single-line compact output")
	fs.Parse(args)

	root := resolveRoot(fs.Args())
	db := mustOpenDB(root)
	defer db.Close()

	report, err := db.Churn(root, *n)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if *compact {
		fmt.Println(snapshot.FormatChurnCompact(report))
	} else {
		fmt.Print(snapshot.FormatChurn(report, *minChanges))
	}
}

// runValidateClaims extracts code symbol references from a text file and
// cross-checks them against the IR. Reports identifiers referenced but not
// found in the IR (potential hallucinations).
func runValidateClaims(args []string) {
	fs := flag.NewFlagSet("validate-claims", flag.ExitOnError)
	textFile := fs.String("text", "", "path to text file containing assistant message")
	irPath := fs.String("ir", ".ai/ir.json", "path to ir.json")
	fs.Parse(args)

	if *textFile == "" {
		fmt.Fprintln(os.Stderr, "Error: --text=<file> required")
		os.Exit(1)
	}

	// Load text.
	textData, err := os.ReadFile(*textFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot read text file %q: %v\n", *textFile, err)
		os.Exit(1)
	}
	text := string(textData)

	// Load IR symbols.
	irData, err := ir.Load(*irPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot load IR %q: %v\n", *irPath, err)
		os.Exit(1)
	}
	knownSymbols := make(map[string]bool)
	for _, fileEntry := range irData.Files {
		for _, fn := range fileEntry.Functions {
			knownSymbols[fn] = true
		}
		for _, cl := range fileEntry.Classes {
			knownSymbols[cl] = true
		}
	}

	// Extract symbol references from text.
	refs := extractSymbolRefs(text)

	type Mismatch struct {
		Ref     string `json:"ref"`
		Context string `json:"context"`
	}
	var mismatches []Mismatch
	for ref, ctx := range refs {
		if !knownSymbols[ref] {
			mismatches = append(mismatches, Mismatch{Ref: ref, Context: ctx})
		}
	}

	out := map[string]interface{}{
		"checked":    len(refs),
		"mismatches": mismatches,
	}
	if mismatches == nil {
		out["mismatches"] = []Mismatch{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

// extractSymbolRefs returns a map of symbol → context snippet from text.
// Targets: backtick-quoted identifiers, and "func X", "type X", "var X" patterns.
// Conservative: only flags names with uppercase (CamelCase) or containing underscore+uppercase.
func extractSymbolRefs(text string) map[string]string {
	refs := make(map[string]string)
	lines := bufio.NewScanner(strings.NewReader(text))

	// Patterns
	backtickRe := regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*)`")
	declRe := regexp.MustCompile(`\b(?:func|type|var|const)\s+([A-Z][A-Za-z0-9_]*)`)

	for lines.Scan() {
		line := lines.Text()

		for _, m := range backtickRe.FindAllStringSubmatch(line, -1) {
			sym := m[1]
			if isCodeSymbol(sym) {
				if _, seen := refs[sym]; !seen {
					refs[sym] = truncate(line, 80)
				}
			}
		}

		for _, m := range declRe.FindAllStringSubmatch(line, -1) {
			sym := m[1]
			if isCodeSymbol(sym) {
				if _, seen := refs[sym]; !seen {
					refs[sym] = truncate(line, 80)
				}
			}
		}
	}
	return refs
}

// isCodeSymbol returns true if the name looks like a CamelCase code identifier.
// Requires mixed case (both upper and lower letters) to avoid false positives on:
//   - ALL_CAPS shell/env constants (IR_DRIFT, OPUS_BLOCKED)
//   - snake_case shell functions (emit_fault, validate_claims)
//   - Python dunders (__all__, __init__)
//
// Only CamelCase/PascalCase names like IRProvider, ValidateClaims, FileIR pass.
func isCodeSymbol(name string) bool {
	if len(name) <= 2 {
		return false
	}
	hasUpper := false
	hasLower := false
	for _, r := range name {
		if r >= 'A' && r <= 'Z' {
			hasUpper = true
		}
		if r >= 'a' && r <= 'z' {
			hasLower = true
		}
	}
	// Must have both upper and lower to be CamelCase
	return hasUpper && hasLower
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// mustOpenDB opens .ai/history.db from root or exits.
func mustOpenDB(root string) *snapshot.DB {
	aiDir := filepath.Join(root, ".ai")
	if err := os.MkdirAll(aiDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to create .ai dir: %v\n", err)
		os.Exit(1)
	}
	dbPath := filepath.Join(aiDir, "history.db")
	db, err := snapshot.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open snapshot DB: %v\n", err)
		os.Exit(1)
	}
	return db
}
