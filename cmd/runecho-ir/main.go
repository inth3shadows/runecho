package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/claims"
	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/store"
)

// Exit codes returned by every runecho-ir subcommand.
const (
	ExitOK     = 0 // clean run — success or no notable findings
	ExitNoData = 1 // soft condition: not enrolled, no matching snapshot, stale claims found
	ExitError  = 2 // hard error: bad args, I/O failure, database error
)

// Usage: runecho-ir [root-path]
// Generates .ai/ir.json for the project at root-path (default: current directory).
// If .ai/ir.json already exists, performs incremental update (only re-parses changed files).
//
// Subcommands:
//
//	runecho-ir snapshot [--label=manual] [--session=""] [root]
//	runecho-ir diff [--since=label | id-a id-b] [--compact] [root]
//	runecho-ir log [--n=10] [root]
//	runecho-ir verify [--session=""] [root]
//	runecho-ir churn [--n=20] [--min-changes=2] [--compact] [root]
//	runecho-ir truth-trail [--since=session-start] [--session=<id>] [--text=<file>] [root]
//	runecho-ir validate-claims --text=<file> [--ir=<path>]
func main() {
	os.Exit(run())
}

// run is the testable entry point. All subcommand handlers return an int exit
// code; main() is the only caller of os.Exit. This mirrors the runecho-guard
// seam (run() int / main() { os.Exit(run()) }) so both commands are testable
// without subprocess overhead.
func run() int {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "snapshot":
			return runSnapshot(os.Args[2:])
		case "diff":
			return runDiff(os.Args[2:])
		case "map":
			return runMap(os.Args[2:])
		case "log":
			return runLog(os.Args[2:])
		case "verify":
			return runVerify(os.Args[2:])
		case "churn":
			return runChurn(os.Args[2:])
		case "repo":
			return runRepo(os.Args[2:])
		case "backup":
			return runBackup(os.Args[2:])
		case "install":
			return runInstall(os.Args[2:])
		case "truth-trail":
			return runTruthTrail(os.Args[2:])
		case "validate-claims":
			return runValidateClaims(os.Args[2:])
		case "--help", "-h", "help":
			printUsage()
			return 0
		case "--version", "-v":
			fmt.Println("runecho-ir dev")
			return 0
		default:
			if strings.HasPrefix(os.Args[1], "-") {
				fmt.Fprintf(os.Stderr, "runecho-ir: unknown flag %q\n", os.Args[1])
				printUsage()
				return ExitError
			}
		}
	}
	// Default: index behavior (backward compat).
	return runIndex(os.Args)
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: runecho-ir [root-path]")
	fmt.Fprintln(os.Stderr, "       runecho-ir snapshot [--label=manual] [--session=<id>] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir diff [--since=<label>] [--compact] [--json] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir map [--by-file] [--kind=func|class|export|import] [--dir=<p>] [--since=<label>] [--compact] [--json] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir log [--n=10] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir verify [--session=<id>] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir churn [--n=20] [--min-changes=2] [--compact] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir repo add <path> [--name=<n>] [--cap=<N>] [--source-root=<path>] [--no-hooks]")
	fmt.Fprintln(os.Stderr, "       runecho-ir repo list | rm <name> | reindex <name|.> [--all]")
	fmt.Fprintln(os.Stderr, "       runecho-ir install [--periodic] [--force] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir backup [dest.db]")
	fmt.Fprintln(os.Stderr, "       runecho-ir truth-trail [--since=session-start] [--session=<id>] [--text=<file>] [root]")
	fmt.Fprintln(os.Stderr, "       runecho-ir validate-claims --text=<file> [--ir=<path>]")
}

// runRepo dispatches the central-store registry subcommands.
func runRepo(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir repo add|list|rm|reindex ...")
		return ExitError
	}
	switch args[0] {
	case "add":
		return runRepoAdd(args[1:])
	case "list", "ls":
		return runRepoList(args[1:])
	case "rm", "remove":
		return runRepoRemove(args[1:])
	case "reindex":
		return runRepoReindex(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "runecho-ir repo: unknown subcommand %q\n", args[0])
		return ExitError
	}
}

// runRepoAdd enrolls a repo explicitly. An explicit --name that collides is an
// error (strict); a derived name auto-disambiguates.
func runRepoAdd(args []string) int {
	fs := flag.NewFlagSet("repo add", flag.ExitOnError)
	name := fs.String("name", "", "repo name (default: derived from path)")
	cap := fs.Int("cap", 0, "max files to index, 0 = unlimited")
	sourceRoot := fs.String("source-root", "", "directory to walk for IR generation (default: same as path; use for bare-repo worktree layouts)")
	noHooks := fs.Bool("no-hooks", false, "skip automatic git hook installation")
	fs.Parse(args)

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()

	if existing, err := db.GetRepoByPath(root); err != nil {
		return printErr(err)
	} else if existing != nil {
		fmt.Printf("Already enrolled: %s (id=%d) -> %s\n", existing.Name, existing.ID, existing.Path)
		return 0
	}

	n := *name
	if n == "" {
		var uErr error
		n, uErr = snapshot.UniqueName(db, snapshot.DeriveRepoName(root))
		if uErr != nil {
			return printErr(uErr)
		}
	}
	id, err := db.EnrollRepo(n, root, *sourceRoot, *cap)
	if err != nil {
		return printErr(err)
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

	// Auto-reindex immediately so the IR is ready without a separate step.
	enrolled, err2 := db.GetRepoByName(n)
	if err2 == nil && enrolled != nil {
		fmt.Printf("Indexing %s...\n", n)
		doReindex(db, enrolled)
	}

	// Auto-install all git hooks unless suppressed.
	if !*noHooks {
		if err := installHooks(root, false); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not install hooks: %v\n", err)
			fmt.Fprintf(os.Stderr, "  Run manually: runecho-ir install\n")
		}
	}
	return 0
}

// runRepoList prints all enrolled repos and their indexing state.
func runRepoList(args []string) int {
	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()
	repos, err := db.ListRepos()
	if err != nil {
		return printErr(err)
	}
	if len(repos) == 0 {
		fmt.Println("No repos enrolled. Add one: runecho-ir repo add <path>")
		return 0
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
	return 0
}

// runRepoRemove purges a repo and its entire history by name.
func runRepoRemove(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir repo rm <name>")
		return ExitError
	}
	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()
	repo, err := db.GetRepoByName(args[0])
	if err != nil {
		return printErr(err)
	}
	if repo == nil {
		fmt.Fprintf(os.Stderr, "No repo named %q\n", args[0])
		return ExitError
	}
	if err := db.PurgeRepo(repo.ID); err != nil {
		return printErr(err)
	}
	fmt.Printf("Removed %s (id=%d) and its history.\n", repo.Name, repo.ID)
	return 0
}

// runRepoReindex rebuilds an enrolled repo's IR and records a snapshot.
// Accepts a name, "." (CWD lookup), or --all to reindex every enrolled repo.
func runRepoReindex(args []string) int {
	fs := flag.NewFlagSet("repo reindex", flag.ContinueOnError)
	all := fs.Bool("all", false, "reindex all enrolled repos")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}

	if *all {
		return runRepoReindexAll()
	}

	if len(fs.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir repo reindex <name|.> [--all]")
		return ExitError
	}

	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()

	arg := fs.Args()[0]
	var repo *snapshot.Repo
	var err error

	if arg == "." || filepath.IsAbs(arg) {
		root, rcode := resolveRoot(fs.Args())
		if rcode != 0 {
			return rcode
		}
		r, _, ok := db.ResolveRepo(root)
		if !ok {
			fmt.Fprintf(os.Stderr, "No repo enrolled at %q — run: runecho-ir repo add .\n", root)
			return ExitError
		}
		repo = r
	} else {
		repo, err = db.GetRepoByName(arg)
		if err != nil {
			return printErr(err)
		}
		if repo == nil {
			fmt.Fprintf(os.Stderr, "No repo named %q\n", arg)
			return ExitError
		}
	}

	return doReindex(db, repo)
}

// runRepoReindexAll reindexes every enrolled repo in sequence.
func runRepoReindexAll() int {
	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()

	repos, err := db.ListRepos()
	if err != nil {
		return printErr(err)
	}
	if len(repos) == 0 {
		fmt.Println("No repos enrolled.")
		return ExitOK
	}

	exitCode := ExitOK
	for i := range repos {
		if c := doReindex(db, &repos[i]); c != 0 {
			exitCode = c
		}
	}
	return exitCode
}

// doReindex is the shared reindex implementation: builds IR, saves ir.json,
// stores a snapshot, and updates the repo's last-indexed timestamp.
func doReindex(db *snapshot.DB, repo *snapshot.Repo) int {
	srcRoot := repo.EffectiveSourceRoot()
	irData, stats, code := buildIR(srcRoot, repo.FileCap)
	if code != 0 {
		return code
	}
	if err := irData.Save(filepath.Join(srcRoot, ".ai", "ir.json")); err != nil {
		return printErr(fmt.Errorf("save ir.json: %w", err))
	}
	id, err := db.SaveSnapshot(repo.ID, "", "reindex", srcRoot, irData)
	if err != nil {
		return printErr(err)
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
	return 0
}

// runInstall installs git hooks in the current (or given) repo and optionally
// a periodic reindex job (launchd on macOS, cron on Linux).
// --periodic alone (no root) installs only the periodic job without touching hooks.
func runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	periodic := fs.Bool("periodic", false, "also install an hourly reindex job (launchd on macOS, cron on Linux)")
	force := fs.Bool("force", false, "overwrite existing hooks not created by runecho")
	fs.Parse(args)

	// If a root path was given (or we're inside a git repo), install hooks.
	if len(fs.Args()) > 0 || !*periodic {
		root, code := resolveRoot(fs.Args())
		if code != 0 {
			return code
		}
		if err := installHooks(root, *force); err != nil {
			if !*periodic {
				return printErr(err)
			}
			fmt.Fprintf(os.Stderr, "Warning: could not install hooks: %v\n", err)
		}
	}

	if *periodic {
		if err := installPeriodic(); err != nil {
			return printErr(err)
		}
	}
	return 0
}

// installHooks installs pre-commit (guard) and post-commit/post-merge/post-checkout
// (background reindex) hooks into the git repo containing root.
func installHooks(root string, force bool) error {
	gitDir, err := gitutil.AbsGitDir(root)
	if err != nil {
		return fmt.Errorf("find git dir: %w", err)
	}
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}

	irBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve binary path: %w", err)
	}
	guardBin := filepath.Join(filepath.Dir(irBin), "runecho-guard")

	preCommit := fmt.Sprintf("#!/usr/bin/env bash\nexec %q \"$@\"\n", guardBin)
	reindex := fmt.Sprintf("#!/usr/bin/env bash\n%q repo reindex . >/dev/null 2>&1 &\n", irBin)
	// post-checkout: only reindex on branch switches ($3 == 1), not file checkouts.
	postCheckout := fmt.Sprintf("#!/usr/bin/env bash\n[ \"$3\" = \"1\" ] && %q repo reindex . >/dev/null 2>&1 &\n", irBin)

	hooks := map[string]string{
		"pre-commit":    preCommit,
		"post-commit":   reindex,
		"post-merge":    reindex,
		"post-checkout": postCheckout,
	}
	for name, content := range hooks {
		if err := installHookFile(hooksDir, name, content, force); err != nil {
			return err
		}
	}
	fmt.Printf("Hooks installed in %s\n", hooksDir)
	return nil
}

// installHookFile writes a single hook script. Skips if an existing hook is not
// a runecho hook (unless force). Overwrites existing runecho hooks always.
func installHookFile(hooksDir, name, content string, force bool) error {
	path := filepath.Join(hooksDir, name)
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), "runecho") && !force {
			fmt.Fprintf(os.Stderr, "  Skipping %s: existing hook (use --force to overwrite)\n", name)
			return nil
		}
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		return fmt.Errorf("write %s hook: %w", name, err)
	}
	fmt.Printf("  Installed %s\n", name)
	return nil
}

// installPeriodic installs an hourly reindex job via launchd (macOS) or cron (Linux).
func installPeriodic() error {
	irBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve binary path: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(irBin)
	default:
		return installCron(irBin)
	}
}

// installLaunchd writes a launchd plist and loads it (macOS).
func installLaunchd(irBin string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	plistPath := filepath.Join(agentsDir, "com.runecho.reindex.plist")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.runecho.reindex</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>repo</string>
		<string>reindex</string>
		<string>--all</string>
	</array>
	<key>StartInterval</key>
	<integer>3600</integer>
	<key>StandardOutPath</key>
	<string>/tmp/runecho-reindex.log</string>
	<key>StandardErrorPath</key>
	<string>/tmp/runecho-reindex.log</string>
</dict>
</plist>
`, html.EscapeString(irBin))
	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	// Unload first (idempotent — ignore error if not loaded), then load.
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}
	fmt.Printf("Periodic reindex installed (hourly): %s\n", plistPath)
	return nil
}

// installCron adds an hourly crontab entry on Linux/other.
func installCron(irBin string) error {
	entry := fmt.Sprintf("0 * * * * %q repo reindex --all >>/tmp/runecho-reindex.log 2>&1 # runecho", irBin)
	// Read existing crontab, strip any prior runecho entry, append new one.
	existing, _ := exec.Command("crontab", "-l").Output()
	lines := strings.Split(strings.TrimRight(string(existing), "\n"), "\n")
	filtered := lines[:0]
	for _, l := range lines {
		if !strings.Contains(l, "# runecho") {
			filtered = append(filtered, l)
		}
	}
	filtered = append(filtered, entry)
	input := strings.Join(filtered, "\n") + "\n"
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(input)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install crontab: %w", err)
	}
	fmt.Println("Periodic reindex installed (hourly via cron)")
	return nil
}

// runBackup writes an atomic backup of the central store via VACUUM INTO.
func runBackup(args []string) int {
	dest := ""
	if len(args) > 0 {
		dest = args[0]
	}
	if dest == "" {
		dir, err := runechoDir()
		if err != nil {
			return printErr(err)
		}
		dest = filepath.Join(dir, "backups", "history-backup.db")
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return printErr(err)
		}
	}
	if _, err := os.Stat(dest); err == nil {
		return printErr(fmt.Errorf("backup destination already exists: %s (VACUUM INTO requires a new file)", dest))
	}
	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()
	if err := db.BackupTo(dest); err != nil {
		return printErr(err)
	}
	fmt.Printf("Backup written: %s\n", dest)
	return 0
}

// buildIR generates a fresh IR for root (full, not incremental).
// fileCap limits the number of files indexed (0 = unlimited).
// Returns the IR, the walk's honest-coverage stats, and an exit code (0 = ok).
func buildIR(root string, fileCap int) (*ir.IR, ir.Stats, int) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, ir.Stats{}, printErr(err)
	}
	result, stats, err := ir.NewGenerator(ir.GeneratorConfig{
		IgnoredPaths: ir.DefaultIgnoredPaths,
		FileCap:      fileCap,
	}).Generate(abs)
	if err != nil {
		return nil, ir.Stats{}, printErr(fmt.Errorf("generate IR for %q: %w", abs, err))
	}
	return result, stats, 0
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
func runIndex(args []string) int {
	rootPath := "."
	if len(args) > 1 {
		if strings.HasPrefix(args[1], "-") {
			fmt.Fprintf(os.Stderr, "runecho-ir: unknown flag %q\n", args[1])
			printUsage()
			return ExitError
		}
		rootPath = args[1]
	}

	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to resolve path %q: %v\n", rootPath, err)
		return ExitError
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
			return ExitError
		}
		result, stats, genErr = generator.Generate(absRoot)
	}

	err = genErr

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}

	if err := result.Save(irPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to save IR: %v\n", err)
		return ExitError
	}

	shortHash := result.RootHash
	if len(shortHash) > 12 {
		shortHash = shortHash[:12]
	}
	fmt.Printf("Indexed %d files — root_hash: %s...%s\n", len(result.Files), shortHash, coverageSuffix(stats))
	return 0
}

// runSnapshot saves a snapshot of the current ir.json.
func runSnapshot(args []string) int {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	label := fs.String("label", "manual", "snapshot label (e.g. session-start, session-end, manual)")
	sessionID := fs.String("session", "", "session ID")
	fs.Parse(args)

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()

	// Resolve (auto-enrolling) first so the snapshot honors the repo's file cap —
	// otherwise an uncapped snapshot would never match a capped reindex of the same repo.
	repo, code := resolveRepoForWrite(db, root)
	if code != 0 {
		return code
	}
	irData, stats, code := buildIR(root, repo.FileCap) // always fresh: snapshot/diff/verify reflect current code, never a stale ir.json
	if code != 0 {
		return code
	}
	id, err := db.SaveSnapshot(repo.ID, *sessionID, *label, root, irData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
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
	return 0
}

// runDiff shows structural diff between two snapshots (or a snapshot vs live).
func runDiff(args []string) int {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	since := fs.String("since", "", "diff since latest snapshot with this label vs live ir.json")
	sessionID := fs.String("session", "", "filter by session ID (used with --since)")
	compact := fs.Bool("compact", false, "single-line compact output")
	asJSON := fs.Bool("json", false, "machine-readable JSON (parity with the MCP diff tool)")
	fs.Parse(args)

	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()

	var result snapshot.DiffResult

	if *since != "" {
		// --since mode: A = last snapshot by label, B = live ir.json. root is
		// resolved here, not at the top: in two-ID mode the leading positional
		// is a snapshot id, not a path, so resolving a root there is meaningless.
		root, code := resolveRoot(fs.Args())
		if code != 0 {
			return code
		}
		repoID := lookupRepoID(db, root)
		if repoID < 0 {
			fmt.Fprintf(os.Stderr, "Repo %q is not enrolled — run: runecho-ir repo add .\n", root)
			return ExitNoData
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
			return ExitError
		}
		if meta == nil {
			suffix := ""
			if *sessionID != "" {
				suffix = fmt.Sprintf(" (session %q)", *sessionID)
			}
			fmt.Fprintf(os.Stderr, "No snapshot found with label %q%s for root %q\n", *since, suffix, root)
			return ExitNoData
		}
		irData, _, irCode := buildIR(root, repoFileCap(db, root)) // always fresh: snapshot/diff/verify reflect current code, never a stale ir.json
		if irCode != 0 {
			return irCode
		}
		var diffErr error
		result, diffErr = db.DiffLive(*meta, irData)
		if diffErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", diffErr)
			return ExitError
		}
	} else {
		// Two positional ID mode.
		positional := fs.Args()
		if len(positional) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: runecho-ir diff --since=<label> [root]")
			fmt.Fprintln(os.Stderr, "       runecho-ir diff <id-a> <id-b> [root]")
			return ExitError
		}
		idA, err := strconv.ParseInt(positional[0], 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid snapshot ID %q\n", positional[0])
			return ExitError
		}
		idB, err := strconv.ParseInt(positional[1], 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid snapshot ID %q\n", positional[1])
			return ExitError
		}
		metaA, err := db.GetByID(idA)
		if err != nil || metaA == nil {
			fmt.Fprintf(os.Stderr, "Snapshot %d not found\n", idA)
			return ExitError
		}
		metaB, err := db.GetByID(idB)
		if err != nil || metaB == nil {
			fmt.Fprintf(os.Stderr, "Snapshot %d not found\n", idB)
			return ExitError
		}
		// A diff must never cross repo boundaries (parity with the MCP oracle's
		// scopedSnapshot). RepoID 0 means an unowned/legacy snapshot — refuse it.
		if metaA.RepoID == 0 || metaA.RepoID != metaB.RepoID {
			fmt.Fprintf(os.Stderr, "Refusing cross-repo diff: snapshots %d and %d are not in the same enrolled repo\n", idA, idB)
			return ExitError
		}
		result, err = db.Diff(*metaA, *metaB)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return ExitError
		}
	}

	switch {
	case *asJSON:
		// Same shape as the MCP `diff` oracle tool (snapshot.DiffPayload), so a
		// machine consumer like the harness gate parses one stable contract.
		out, err := json.MarshalIndent(snapshot.DiffPayload(result), "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return ExitError
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
	return 0
}

// runLog prints a table of recent snapshots.
func runLog(args []string) int {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	n := fs.Int("n", 10, "number of snapshots to show")
	fs.Parse(args)

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	db, dbCode := mustOpenDB()
	if dbCode != 0 {
		return dbCode
	}
	defer db.Close()

	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Fprintf(os.Stderr, "Repo %q is not enrolled — run: runecho-ir repo add .\n", root)
		return ExitNoData
	}
	metas, err := db.List(repoID, *n)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}
	if len(metas) == 0 {
		fmt.Println("No snapshots found.")
		return ExitNoData
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
	return 0
}

// runVerify diffs the most recent session-start snapshot against live ir.json.
func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	sessionID := fs.String("session", "", "session ID to verify (optional)")
	fs.Parse(args)

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	db, dbCode := mustOpenDB()
	if dbCode != 0 {
		return dbCode
	}
	defer db.Close()

	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Fprintf(os.Stderr, "Repo %q is not enrolled — run: runecho-ir repo add .\n", root)
		return ExitNoData
	}

	var meta *snapshot.SnapshotMeta
	var err error

	if *sessionID != "" {
		// Direct SQL lookup — a List(100) scan silently missed the snapshot when
		// more than 100 newer snapshots existed.
		meta, err = db.GetLatestByLabelSession(repoID, "session-start", *sessionID)
	} else {
		meta, err = db.GetLatestByLabel(repoID, "session-start")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}

	if meta == nil {
		fmt.Println("No session-start snapshot found.")
		fmt.Println("Run: runecho-ir snapshot --label=session-start")
		return ExitNoData
	}

	irData, _, irCode := buildIR(root, repoFileCap(db, root)) // always fresh: snapshot/diff/verify reflect current code, never a stale ir.json
	if irCode != 0 {
		return irCode
	}
	result, err := db.DiffLive(*meta, irData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}

	fmt.Printf("Verifying against snapshot id=%d label=%s session=%s ts=%s\n\n",
		meta.ID, meta.Label, meta.SessionID, meta.Timestamp.Format(time.RFC3339))
	fmt.Print(snapshot.FormatFull(result))
	return 0
}

// resolveRoot returns the absolute project root from optional positional args,
// and an exit code (0 = ok, 1 = error already printed).
func resolveRoot(args []string) (string, int) {
	rootPath := "."
	if len(args) > 0 {
		if strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(os.Stderr, "runecho-ir: unexpected flag %q where root path was expected\n", args[0])
			return "", ExitError
		}
		rootPath = args[0]
	}
	abs, err := filepath.Abs(rootPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to resolve root %q: %v\n", rootPath, err)
		return "", ExitError
	}
	return abs, 0
}

// runChurn reports file and symbol churn rate across recent snapshots.
func runChurn(args []string) int {
	fs := flag.NewFlagSet("churn", flag.ExitOnError)
	n := fs.Int("n", 20, "number of snapshots to analyze")
	minChanges := fs.Int("min-changes", 2, "minimum diffs a file/symbol must appear in to be considered hot")
	compact := fs.Bool("compact", false, "single-line compact output")
	fs.Parse(args)

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	db, dbCode := mustOpenDB()
	if dbCode != 0 {
		return dbCode
	}
	defer db.Close()

	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Fprintf(os.Stderr, "Repo %q is not enrolled — run: runecho-ir repo add .\n", root)
		return ExitNoData
	}
	report, err := db.Churn(repoID, *n)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}

	if *compact {
		fmt.Println(snapshot.FormatChurnCompact(report))
	} else {
		fmt.Print(snapshot.FormatChurn(report, *minChanges))
	}
	return 0
}

// runValidateClaims extracts code symbol references from a text file and
// cross-checks them against the IR. Reports identifiers referenced but not
// found in the IR (potential hallucinations).
func runValidateClaims(args []string) int {
	fs := flag.NewFlagSet("validate-claims", flag.ExitOnError)
	textFile := fs.String("text", "", "path to text file containing assistant message")
	irPath := fs.String("ir", ".ai/ir.json", "path to ir.json")
	fs.Parse(args)

	if *textFile == "" {
		fmt.Fprintln(os.Stderr, "Error: --text=<file> required")
		return ExitError
	}

	// Load text.
	textData, err := os.ReadFile(*textFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot read text file %q: %v\n", *textFile, err)
		return ExitError
	}
	text := string(textData)

	// Load IR symbols.
	irData, err := ir.Load(*irPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot load IR %q: %v\n", *irPath, err)
		return ExitError
	}
	knownSymbols := make(map[string]bool)
	for _, fileEntry := range irData.Files {
		for _, s := range fileEntry.Symbols {
			if s.Kind == "function" || s.Kind == "class" {
				knownSymbols[s.Name] = true
			}
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
	// Stable output: refs is a map, so without an explicit sort the order is
	// non-deterministic — unacceptable in a tool whose contract is determinism.
	sort.Slice(mismatches, func(i, j int) bool { return mismatches[i].Ref < mismatches[j].Ref })

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
		return ExitNoData
	}
	return ExitOK
}

// runechoDir is the package-local alias to the shared store helper.
func runechoDir() (string, error) { return store.RunechoDir() }

// mustOpenDB opens the central snapshot store (~/.runecho/history.db) or returns 1.
// History is centralized so the oracle serves all enrolled repos from one
// durable, integrity-checked store; the working ir.json stays repo-local.
func mustOpenDB() (*snapshot.DB, int) {
	dir, err := runechoDir()
	if err != nil {
		return nil, printErr(err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, printErr(fmt.Errorf("create %s: %w", dir, err))
	}
	db, err := snapshot.Open(filepath.Join(dir, "history.db"))
	if err != nil {
		return nil, printErr(fmt.Errorf("open snapshot DB: %w", err))
	}
	return db, 0
}

// resolveRepoForWrite returns the enrolled repo for root, auto-enrolling on first
// write (snapshot). 3-tier resolution (common-dir → top-level → worktree shim)
// finds an already-enrolled repo from any worktree, preventing duplicate
// enrollments. When truly new, enroll at the git top-level path (canonical).
// Returning the full repo lets callers apply its FileCap when generating IR.
func resolveRepoForWrite(db *snapshot.DB, root string) (*snapshot.Repo, int) {
	if repo, _, ok := db.ResolveRepo(root); ok {
		return repo, 0
	}
	// Not enrolled — auto-enroll. Use git top-level as the canonical path so
	// worktrees of the same repo always enroll at the same location.
	enrollPath := root
	if topLevel, err := gitutil.TopLevel(root); err == nil {
		enrollPath = topLevel
	}
	uname, uErr := snapshot.UniqueName(db, snapshot.DeriveRepoName(enrollPath))
	if uErr != nil {
		return nil, printErr(uErr)
	}
	if _, err := db.EnrollRepo(uname, enrollPath, enrollPath, 0); err != nil {
		return nil, printErr(err)
	}
	repo, err := db.GetRepoByPath(enrollPath)
	if err != nil {
		return nil, printErr(err)
	}
	// Record the git-common-dir for O(1) cross-worktree lookup (schema V4).
	if repo != nil {
		if cd, cdErr := gitutil.CommonDir(enrollPath); cdErr == nil {
			_ = db.SetRepoCommonDir(repo.ID, cd)
		}
	}
	return repo, 0
}

// repoFileCap returns the enrolled repo's file cap for root, or 0 (unlimited) if
// not enrolled. 3-tier resolution finds the repo from any worktree/cwd so the
// cap matches the cap used when the baseline snapshot was stored.
func repoFileCap(db *snapshot.DB, root string) int {
	repo, _, ok := db.ResolveRepo(root)
	if !ok {
		return 0
	}
	return repo.FileCap
}

// lookupRepoID returns the repo_id for the enrolled repo containing root, or -1
// if none. Uses 3-tier resolution so linked worktrees of the same repo resolve
// to the same repo_id. Read commands treat -1 as "no history for this repo".
func lookupRepoID(db *snapshot.DB, root string) int64 {
	repo, _, ok := db.ResolveRepo(root)
	if !ok {
		return -1
	}
	return repo.ID
}

// runTruthTrail fuses diff, callers, churn, and stale-claims into one change receipt.
// Exits ExitNoData (1) when --text finds stale claims, not enrolled, or no baseline snapshot.
// Exits ExitError (2) on I/O or database failure.
func runTruthTrail(args []string) int {
	fs := flag.NewFlagSet("truth-trail", flag.ExitOnError)
	since := fs.String("since", "session-start", "baseline label (diff since latest snapshot with this label vs live code)")
	sessionID := fs.String("session", "", "filter by session ID (used with --since)")
	textFile := fs.String("text", "", "path to prose file to check for stale symbol refs")
	fs.Parse(args)

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()

	repoID := lookupRepoID(db, root)
	if repoID < 0 {
		fmt.Fprintf(os.Stderr, "Repo %q is not enrolled — run: runecho-ir repo add .\n", root)
		return ExitNoData
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
		return ExitError
	}
	if meta == nil {
		suffix := ""
		if *sessionID != "" {
			suffix = fmt.Sprintf(" (session %q)", *sessionID)
		}
		fmt.Fprintf(os.Stderr, "No snapshot found with label %q%s\n", *since, suffix)
		fmt.Fprintf(os.Stderr, "Run: runecho-ir snapshot --label=%s\n", *since)
		return ExitNoData
	}

	liveIR, _, irCode := buildIR(root, repoFileCap(db, root))
	if irCode != 0 {
		return irCode
	}

	text := ""
	if *textFile != "" {
		data, err := os.ReadFile(*textFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read text file %q: %v\n", *textFile, err)
			return ExitError
		}
		text = string(data)
	}

	trail, err := snapshot.TruthTrail(db, repoID, *meta, liveIR, 0, text)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return ExitError
	}

	fmt.Print(snapshot.FormatTrail(trail))

	if len(trail.StaleClaims) > 0 {
		return ExitNoData
	}
	return ExitOK
}

// printErr writes "Error: <err>" to stderr and returns ExitError.
// It replaces the old fatal() helper: instead of calling os.Exit directly,
// callers return the code so main() (and tests) control process exit.
func printErr(err error) int {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	return ExitError
}
