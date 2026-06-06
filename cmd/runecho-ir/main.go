package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/claims"
	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/store"
)

// Usage: runecho-ir [root-path]
// Generates .ai/ir.json for the project at root-path (default: current directory).
// If .ai/ir.json already exists, performs incremental update (only re-parses changed files).
//
// Subcommands:
//   runecho-ir snapshot [--label=manual] [--session=""] [root]
//   runecho-ir diff [--since=label | id-a id-b] [--compact] [root]
//   runecho-ir log [--n=10] [root]
//   runecho-ir verify [--session=""] [root]
//   runecho-ir churn [--n=20] [--min-changes=2] [--compact] [root]
//   runecho-ir validate-claims --text=<file> [--ir=<path>]
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
		case "repo":
			runRepo(os.Args[2:])
			return
		case "backup":
			runBackup(os.Args[2:])
			return
		case "validate-claims":
			runValidateClaims(os.Args[2:])
			return
		case "--help", "-h", "help":
			printUsage()
			os.Exit(0)
		case "--version", "-v":
			fmt.Println("runecho-ir dev")
			os.Exit(0)
		default:
			if strings.HasPrefix(os.Args[1], "-") {
				fmt.Fprintf(os.Stderr, "runecho-ir: unknown flag %q\n", os.Args[1])
				printUsage()
				os.Exit(1)
			}
		}
	}
	// Default: index behavior (backward compat).
	runIndex(os.Args)
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: runecho-ir [root-path]")
	fmt.Fprintln(os.Stderr, "       runecho-ir snapshot [--label=manual] [--session=<id>] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir diff [--since=<label>] [--compact] [--json] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir log [--n=10] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir verify [--session=<id>] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir churn [--n=20] [--min-changes=2] [--compact] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir repo add <path> [--name=<n>] [--cap=<N>] [--source-root=<path>]")
	fmt.Fprintln(os.Stderr, "       runecho-ir repo list | rm <name> | reindex <name>")
	fmt.Fprintln(os.Stderr, "       runecho-ir backup [dest.db]")
	fmt.Fprintln(os.Stderr, "       runecho-ir validate-claims --text=<file> [--ir=<path>]")
}

// runRepo dispatches the central-store registry subcommands.
func runRepo(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir repo add|list|rm|reindex ...")
		os.Exit(1)
	}
	switch args[0] {
	case "add":
		runRepoAdd(args[1:])
	case "list", "ls":
		runRepoList(args[1:])
	case "rm", "remove":
		runRepoRemove(args[1:])
	case "reindex":
		runRepoReindex(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "runecho-ir repo: unknown subcommand %q\n", args[0])
		os.Exit(1)
	}
}

// runRepoAdd enrolls a repo explicitly. An explicit --name that collides is an
// error (strict); a derived name auto-disambiguates.
func runRepoAdd(args []string) {
	fs := flag.NewFlagSet("repo add", flag.ExitOnError)
	name := fs.String("name", "", "repo name (default: derived from path)")
	cap := fs.Int("cap", 0, "max files to index, 0 = unlimited")
	sourceRoot := fs.String("source-root", "", "directory to walk for IR generation (default: same as path; use for bare-repo worktree layouts)")
	fs.Parse(args)

	root := resolveRoot(fs.Args())
	db := mustOpenDB()
	defer db.Close()

	if existing, err := db.GetRepoByPath(root); err != nil {
		fatal(err)
	} else if existing != nil {
		fmt.Printf("Already enrolled: %s (id=%d) -> %s\n", existing.Name, existing.ID, existing.Path)
		return
	}

	n := *name
	if n == "" {
		var uErr error
		n, uErr = snapshot.UniqueName(db, snapshot.DeriveRepoName(root))
		if uErr != nil {
			fatal(uErr)
		}
	}
	id, err := db.EnrollRepo(n, root, *sourceRoot, *cap)
	if err != nil {
		fatal(err)
	}
	// Record the git-common-dir so the guard resolves this repo in O(1) from any
	// worktree (schema V4). Best-effort: a non-git path just defers to lazy backfill.
	if cd, cdErr := gitutil.CommonDir(root); cdErr == nil {
		_ = db.SetRepoCommonDir(id, cd)
	}
	// Read the stored source root back rather than re-deriving EnrollRepo's
	// empty-defaults-to-path rule here, so the displayed value can't drift from it.
	suffix := ""
	if enrolled, err := db.GetRepoByName(n); err == nil && enrolled != nil && enrolled.SourceRoot != root {
		suffix = fmt.Sprintf(" (source-root: %s)", enrolled.SourceRoot)
	}
	fmt.Printf("Enrolled %s (id=%d cap=%d) -> %s%s\n", n, id, *cap, root, suffix)
}

// runRepoList prints all enrolled repos and their indexing state.
func runRepoList(args []string) {
	db := mustOpenDB()
	defer db.Close()
	repos, err := db.ListRepos()
	if err != nil {
		fatal(err)
	}
	if len(repos) == 0 {
		fmt.Println("No repos enrolled. Add one: runecho-ir repo add <path>")
		return
	}
	fmt.Printf("%-24s  %-4s  %-20s  %-6s  %-5s  %-7s  %s\n", "NAME", "ID", "LAST-INDEXED", "ERRORS", "CAP", "COVER", "PATH")
	fmt.Println(strings.Repeat("-", 108))
	for _, r := range repos {
		last := "never"
		if !r.LastIndexed.IsZero() {
			last = r.LastIndexed.Format(time.RFC3339)
		}
		// Coverage = files in the latest snapshot vs supported files seen by the
		// last walk. "-" until a post-V5 reindex has measured the denominator.
		cover := "-"
		if r.SupportedSeen > 0 {
			if latest, err := db.List(r.ID, 1); err == nil && len(latest) == 1 {
				cover = fmt.Sprintf("%.1f%%", snapshot.CoveragePercent(latest[0].FileCount, r.SupportedSeen))
			}
		}
		fmt.Printf("%-24s  %-4d  %-20s  %-6d  %-5d  %-7s  %s\n",
			r.Name, r.ID, last, r.ParseErrors, r.FileCap, cover, r.Path)
	}
}

// runRepoRemove purges a repo and its entire history by name.
func runRepoRemove(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir repo rm <name>")
		os.Exit(1)
	}
	db := mustOpenDB()
	defer db.Close()
	repo, err := db.GetRepoByName(args[0])
	if err != nil {
		fatal(err)
	}
	if repo == nil {
		fmt.Fprintf(os.Stderr, "No repo named %q\n", args[0])
		os.Exit(1)
	}
	if err := db.PurgeRepo(repo.ID); err != nil {
		fatal(err)
	}
	fmt.Printf("Removed %s (id=%d) and its history.\n", repo.Name, repo.ID)
}

// runRepoReindex rebuilds an enrolled repo's IR and records a snapshot, by name.
// Serial, fresh-per-repo. Cap is advisory: actual file count is reported and a
// warning is logged when it exceeds the cap (honest coverage — no silent claim).
func runRepoReindex(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir repo reindex <name>")
		os.Exit(1)
	}
	db := mustOpenDB()
	defer db.Close()
	repo, err := db.GetRepoByName(args[0])
	if err != nil {
		fatal(err)
	}
	if repo == nil {
		fmt.Fprintf(os.Stderr, "No repo named %q\n", args[0])
		os.Exit(1)
	}

	srcRoot := repo.EffectiveSourceRoot()
	irData, stats := buildIR(srcRoot, repo.FileCap)
	if err := irData.Save(filepath.Join(srcRoot, ".ai", "ir.json")); err != nil {
		fatal(fmt.Errorf("save ir.json: %w", err))
	}
	id, err := db.SaveSnapshot(repo.ID, "", "reindex", srcRoot, irData)
	if err != nil {
		fatal(err)
	}
	if err := db.TouchRepo(repo.ID, time.Now(), stats.ParseErrors, stats.SupportedSeen); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to record index time: %v\n", err)
	}
	short := irData.RootHash
	if len(short) > 12 {
		short = short[:12]
	}
	fmt.Printf("Reindexed %s: snapshot id=%d files=%d root_hash=%s...%s\n",
		repo.Name, id, len(irData.Files), short, coverageSuffix(stats))
}

// runBackup writes an atomic backup of the central store via VACUUM INTO.
func runBackup(args []string) {
	dest := ""
	if len(args) > 0 {
		dest = args[0]
	}
	if dest == "" {
		dir, err := runechoDir()
		if err != nil {
			fatal(err)
		}
		dest = filepath.Join(dir, "backups", "history-backup.db")
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			fatal(err)
		}
	}
	if _, err := os.Stat(dest); err == nil {
		fatal(fmt.Errorf("backup destination already exists: %s (VACUUM INTO requires a new file)", dest))
	}
	db := mustOpenDB()
	defer db.Close()
	if err := db.BackupTo(dest); err != nil {
		fatal(err)
	}
	fmt.Printf("Backup written: %s\n", dest)
}

// buildIR generates a fresh IR for root (full, not incremental).
// fileCap limits the number of files indexed (0 = unlimited).
// Returns the IR and the walk's honest-coverage stats.
func buildIR(root string, fileCap int) (*ir.IR, ir.Stats) {
	abs, err := filepath.Abs(root)
	if err != nil {
		fatal(err)
	}
	result, stats, err := ir.NewGenerator(ir.GeneratorConfig{
		IgnoredPaths: ir.DefaultIgnoredPaths,
		FileCap:      fileCap,
	}).Generate(abs)
	if err != nil {
		fatal(fmt.Errorf("generate IR for %q: %w", abs, err))
	}
	return result, stats
}

// coverageSuffix formats " coverage=N/M (P%)" from walk stats, or "" when the
// walk saw no supported files (nothing meaningful to report).
func coverageSuffix(stats ir.Stats) string {
	if stats.SupportedSeen == 0 {
		return ""
	}
	return fmt.Sprintf(" coverage=%d/%d (%.0f%%)", stats.Indexed, stats.SupportedSeen, stats.Coverage())
}

// runIndex is the original runecho-ir [root] behavior.
func runIndex(args []string) {
	rootPath := "."
	if len(args) > 1 {
		if strings.HasPrefix(args[1], "-") {
			fmt.Fprintf(os.Stderr, "runecho-ir: unknown flag %q\n", args[1])
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

	generator := ir.NewGenerator(ir.GeneratorConfig{IgnoredPaths: ir.DefaultIgnoredPaths})

	var result *ir.IR
	var stats ir.Stats
	var genErr error

	if _, err := os.Stat(irPath); err == nil {
		existing, loadErr := ir.Load(irPath)
		switch {
		case loadErr != nil:
			fmt.Fprintf(os.Stderr, "Warning: failed to load existing IR, regenerating: %v\n", loadErr)
			result, stats, genErr = generator.Generate(absRoot)
		case existing.Version != ir.IRVersion:
			// An old-format IR cannot be incrementally updated: Update reuses
			// unchanged files verbatim, which would leave fields added by newer
			// versions (e.g. v2 refs) empty forever.
			fmt.Fprintf(os.Stderr, "IR format v%d -> v%d: full regenerate\n", existing.Version, ir.IRVersion)
			result, stats, genErr = generator.Generate(absRoot)
		default:
			result, stats, genErr = generator.Update(existing, absRoot)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(irPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to create .ai directory: %v\n", err)
			os.Exit(1)
		}
		result, stats, genErr = generator.Generate(absRoot)
	}

	err = genErr

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
	fmt.Printf("Indexed %d files — root_hash: %s...%s\n", len(result.Files), shortHash, coverageSuffix(stats))
}

// runSnapshot saves a snapshot of the current ir.json.
func runSnapshot(args []string) {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	label := fs.String("label", "manual", "snapshot label (e.g. session-start, session-end, manual)")
	sessionID := fs.String("session", "", "session ID")
	fs.Parse(args)

	root := resolveRoot(fs.Args())
	db := mustOpenDB()
	defer db.Close()

	// Resolve (auto-enrolling) first so the snapshot honors the repo's file cap —
	// otherwise an uncapped snapshot would never match a capped reindex of the same repo.
	repo := resolveRepoForWrite(db, root)
	irData, stats := buildIR(root, repo.FileCap) // always fresh: snapshot/diff/verify reflect current code, never a stale ir.json
	id, err := db.SaveSnapshot(repo.ID, *sessionID, *label, root, irData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// Record the capture point (self-observing: last_indexed staleness).
	if err := db.TouchRepo(repo.ID, time.Now(), stats.ParseErrors, stats.SupportedSeen); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to record index time: %v\n", err)
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
	asJSON := fs.Bool("json", false, "machine-readable JSON (parity with the MCP diff tool)")
	fs.Parse(args)

	db := mustOpenDB()
	defer db.Close()

	var result snapshot.DiffResult

	if *since != "" {
		// --since mode: A = last snapshot by label, B = live ir.json. root is
		// resolved here, not at the top: in two-ID mode the leading positional
		// is a snapshot id, not a path, so resolving a root there is meaningless.
		root := resolveRoot(fs.Args())
		repoID := lookupRepoID(db, root)
		if repoID < 0 {
			fmt.Fprintf(os.Stderr, "Repo %q is not enrolled (no snapshots yet)\n", root)
			os.Exit(0)
		}
		var meta *snapshot.SnapshotMeta
		var err error
		if *sessionID != "" {
			meta, err = db.GetLatestByLabelSession(repoID, *since, *sessionID)
		} else {
			meta, err = db.GetLatestByLabel(repoID, *since)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if meta == nil {
			suffix := ""
			if *sessionID != "" {
				suffix = fmt.Sprintf(" (session %q)", *sessionID)
			}
			fmt.Fprintf(os.Stderr, "No snapshot found with label %q%s for root %q\n", *since, suffix, root)
			os.Exit(0)
		}
		irData, _ := buildIR(root, repoFileCap(db, root)) // always fresh: snapshot/diff/verify reflect current code, never a stale ir.json
		result, err = db.DiffLive(*meta, irData)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Two positional ID mode.
		positional := fs.Args()
		if len(positional) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: runecho-ir diff --since=<label> [root]")
			fmt.Fprintln(os.Stderr, "       runecho-ir diff <id-a> <id-b> [root]")
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
		// A diff must never cross repo boundaries (parity with the MCP oracle's
		// scopedSnapshot). RepoID 0 means an unowned/legacy snapshot — refuse it.
		if metaA.RepoID == 0 || metaA.RepoID != metaB.RepoID {
			fmt.Fprintf(os.Stderr, "Refusing cross-repo diff: snapshots %d and %d are not in the same enrolled repo\n", idA, idB)
			os.Exit(1)
		}
		result, err = db.Diff(*metaA, *metaB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	switch {
	case *asJSON:
		// Same shape as the MCP `diff` oracle tool (snapshot.DiffPayload), so a
		// machine consumer like the harness gate parses one stable contract.
		out, err := json.MarshalIndent(snapshot.DiffPayload(result), "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(out))
	case *compact:
		line := snapshot.FormatCompact(result)
		if line != "" {
			fmt.Println(line)
		}
	default:
		fmt.Print(snapshot.FormatFull(result))
	}
}

// runLog prints a table of recent snapshots.
func runLog(args []string) {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	n := fs.Int("n", 10, "number of snapshots to show")
	fs.Parse(args)

	root := resolveRoot(fs.Args())
	db := mustOpenDB()
	defer db.Close()

	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Println("No snapshots found.")
		return
	}
	metas, err := db.List(repoID, *n)
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
		// Date portion only; guard the slice like session above (a zero or
		// malformed Timestamp must not panic the listing).
		ts := m.Timestamp.Format(time.RFC3339)
		if len(ts) > 10 {
			ts = ts[:10]
		}
		session := m.SessionID
		if len(session) > 25 {
			session = session[:22] + "..."
		}
		fmt.Printf("%-5d  %-15s  %-25s  %-10s  %-8d  %s...\n",
			m.ID, m.Label, session, ts, m.FileCount, shortHash)
	}
}

// runVerify diffs the most recent session-start snapshot against live ir.json.
func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	sessionID := fs.String("session", "", "session ID to verify (optional)")
	fs.Parse(args)

	root := resolveRoot(fs.Args())
	db := mustOpenDB()
	defer db.Close()

	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Println("No session-start snapshot found.")
		fmt.Println("Run: runecho-ir snapshot --label=session-start")
		os.Exit(0)
	}

	var meta *snapshot.SnapshotMeta
	var err error

	if *sessionID != "" {
		// Find session-start for this specific session.
		// GetLatestByLabel doesn't filter by session, so we use List and filter.
		metas, listErr := db.List(repoID, 100)
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
		meta, err = db.GetLatestByLabel(repoID, "session-start")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	if meta == nil {
		fmt.Println("No session-start snapshot found.")
		fmt.Println("Run: runecho-ir snapshot --label=session-start")
		os.Exit(0)
	}

	irData, _ := buildIR(root, repoFileCap(db, root)) // always fresh: snapshot/diff/verify reflect current code, never a stale ir.json
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
			fmt.Fprintf(os.Stderr, "runecho-ir: unexpected flag %q where root path was expected\n", args[0])
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

// runChurn reports file and symbol churn rate across recent snapshots.
func runChurn(args []string) {
	fs := flag.NewFlagSet("churn", flag.ExitOnError)
	n := fs.Int("n", 20, "number of snapshots to analyze")
	minChanges := fs.Int("min-changes", 2, "minimum diffs a file/symbol must appear in to be considered hot")
	compact := fs.Bool("compact", false, "single-line compact output")
	fs.Parse(args)

	root := resolveRoot(fs.Args())
	db := mustOpenDB()
	defer db.Close()

	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Println("No snapshots found.")
		return
	}
	report, err := db.Churn(repoID, *n)
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
	refs := claims.ExtractSymbolRefs(text)

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
	if len(mismatches) > 0 {
		os.Exit(1)
	}
}

// runechoDir is the package-local alias to the shared store helper.
func runechoDir() (string, error) { return store.RunechoDir() }

// mustOpenDB opens the central snapshot store (~/.runecho/history.db) or exits.
// History is centralized so the oracle serves all enrolled repos from one
// durable, integrity-checked store; the working ir.json stays repo-local.
func mustOpenDB() *snapshot.DB {
	dir, err := runechoDir()
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		fatal(fmt.Errorf("create %s: %w", dir, err))
	}
	db, err := snapshot.Open(filepath.Join(dir, "history.db"))
	if err != nil {
		fatal(fmt.Errorf("open snapshot DB: %w", err))
	}
	return db
}

// resolveRepoForWrite returns the enrolled repo for root, auto-enrolling on first
// write (snapshot). Name defaults to the path basename, disambiguated with a
// numeric suffix on collision; use `repo add --name` for a chosen label (Stage 2).
// Returning the full repo lets callers apply its FileCap when generating IR.
func resolveRepoForWrite(db *snapshot.DB, root string) *snapshot.Repo {
	repo, err := db.GetRepoByPath(root)
	if err != nil {
		fatal(err)
	}
	if repo != nil {
		return repo
	}
	uname, uErr := snapshot.UniqueName(db, snapshot.DeriveRepoName(root))
	if uErr != nil {
		fatal(uErr)
	}
	if _, err := db.EnrollRepo(uname, root, root, 0); err != nil {
		fatal(err)
	}
	repo, err = db.GetRepoByPath(root)
	if err != nil {
		fatal(err)
	}
	// Record the git-common-dir for O(1) cross-worktree guard lookup (schema V4).
	// Best-effort: a non-git root just defers to the guard's lazy backfill.
	if repo != nil {
		if cd, cdErr := gitutil.CommonDir(root); cdErr == nil {
			_ = db.SetRepoCommonDir(repo.ID, cd)
		}
	}
	return repo
}

// repoFileCap returns the enrolled repo's file cap for root, or 0 (unlimited) if
// not enrolled. Compare commands (diff/verify) generate live IR under this cap so
// it matches the cap used when the baseline snapshot was stored.
func repoFileCap(db *snapshot.DB, root string) int {
	repo, err := db.GetRepoByPath(root)
	if err != nil {
		fatal(err)
	}
	if repo == nil {
		return 0
	}
	return repo.FileCap
}

// lookupRepoID returns the repo_id for root, or -1 if never enrolled. Read
// commands treat -1 as "no history for this repo".
func lookupRepoID(db *snapshot.DB, root string) int64 {
	repo, err := db.GetRepoByPath(root)
	if err != nil {
		fatal(err)
	}
	if repo == nil {
		return -1
	}
	return repo.ID
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	os.Exit(1)
}
