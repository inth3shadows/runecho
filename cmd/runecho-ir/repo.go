package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

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
	fs := flag.NewFlagSet("repo add", flag.ContinueOnError)
	name := fs.String("name", "", "repo name (default: derived from path)")
	cap := fs.Int("cap", 0, "max files to index, 0 = unlimited")
	sourceRoot := fs.String("source-root", "", "directory to walk for IR generation (default: same as path; use for bare-repo worktree layouts)")
	noHooks := fs.Bool("no-hooks", false, "skip automatic git hook installation")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	root, code := resolveRoot(fs.Args())
	if code != 0 {
		return code
	}
	// A relative --source-root would be stored verbatim and re-resolved against
	// whatever CWD a later reindex runs from — silently walking the wrong tree.
	// Pin it to an absolute path at enroll time (F41/F98).
	if *sourceRoot != "" {
		abs, absErr := filepath.Abs(*sourceRoot)
		if absErr != nil {
			return printErr(fmt.Errorf("resolve --source-root: %w", absErr))
		}
		*sourceRoot = abs
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

	// Auto-reindex immediately so the IR is ready without a separate step. A
	// failed initial index must surface in the exit code — "Enrolled" + exit 0
	// with no snapshot behind it is exactly the silent state scripts need to
	// catch (F42). The error itself is already printed by doReindex.
	reindexCode := 0
	enrolled, err2 := db.GetRepoByName(n)
	if err2 == nil && enrolled != nil {
		fmt.Printf("Indexing %s...\n", n)
		reindexCode = doReindex(db, enrolled)
	}

	// Auto-install all git hooks unless suppressed.
	if !*noHooks {
		if installed, err := installHooks(root, false); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not install hooks: %v\n", err)
			fmt.Fprintf(os.Stderr, "  Run manually: runecho-ir install\n")
		} else if installed == 0 {
			fmt.Fprintf(os.Stderr, "Warning: no hooks installed (existing non-runecho hooks). Overwrite with: runecho-ir install --force\n")
		}
	}
	return reindexCode
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
	if code, ok := parseSub(fs, args); !ok {
		return code
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
