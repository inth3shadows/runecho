package main

import (
	"errors"
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
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir repo add|list|rm|reindex|prune|prune-missing ...")
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
	case "prune":
		return runRepoPrune(args[1:])
	case "prune-missing":
		return runRepoPruneMissing(args[1:])
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
	missing := 0
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
		path := r.Path
		if rootIsMissing(r.EffectiveSourceRoot()) {
			missing++
			path += " [missing]"
		}
		fmt.Printf("%-24s  %-4d  %-20s  %-6d  %-5d  %-7s  %s\n",
			r.Name, r.ID, last, r.ParseErrors, r.FileCap, cover, path)
	}
	// Quiet in the common case (issue #370): the rot is otherwise invisible
	// until it surfaces as an unrelated warning or, since #376, a blocked
	// commit — this is the first point in the workflow where it can be seen
	// before it bites.
	if missing > 0 {
		fmt.Printf("\n%d of %d enrolled repo(s) have a missing source root — runecho-ir repo prune-missing\n",
			missing, len(repos))
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
	removeRefreshLock(repo.ID)
	fmt.Printf("Removed %s (id=%d) and its history.\n", repo.Name, repo.ID)
	return 0
}

// runRepoReindex rebuilds an enrolled repo's IR and records a snapshot.
// Accepts a name, "." (CWD lookup), or --all to reindex every enrolled repo.
func runRepoReindex(args []string) int {
	fs := flag.NewFlagSet("repo reindex", flag.ContinueOnError)
	all := fs.Bool("all", false, "reindex all enrolled repos")
	// --prune is what the installed periodic job passes so history stays bounded
	// without a second scheduled entry. It is a flag rather than shell chaining
	// (`reindex --all && prune`) because the macOS launchd plist runs an argv
	// array with no shell, so a chained command could only be expressed on one
	// of the two platforms. Opt-in either way: a bare `reindex --all` never
	// deletes anything.
	prune := fs.Bool("prune", false, "after reindexing, trim reindex history to --keep per repo")
	keep := fs.Int("keep", defaultPruneKeep, "with --prune: reindex snapshots to keep per repo")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	if *all {
		return runRepoReindexAll(*prune, *keep)
	}
	if *prune {
		fmt.Fprintln(os.Stderr, "runecho-ir repo reindex: --prune applies to --all; use `repo prune --repo=<name>` for one repo")
		return ExitError
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

// runRepoReindexAll reindexes every enrolled repo in sequence. With prune set
// it then trims reindex history to keepN per repo — this is the shape the
// installed hourly job runs, so the store stays bounded without a second
// scheduled entry.
func runRepoReindexAll(prune bool, keepN int) int {
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

	if prune {
		// Prune even when some repo failed to reindex: retention is about rows
		// already in the store, and skipping it on an unrelated failure is how a
		// store quietly resumes growing. Never VACUUM here — that rewrites the
		// whole file and has no business running on an hourly timer.
		n, err := db.PruneReindexSnapshots(0, keepN)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: prune after reindex failed: %v\n", err)
		} else if n > 0 {
			fmt.Printf("Pruned %d reindex snapshot(s) (keep=%d).\n", n, keepN)
		}
	}
	return exitCode
}

// doReindex is the shared reindex implementation: builds IR, saves ir.json,
// stores a snapshot, and updates the repo's last-indexed timestamp.
func doReindex(db *snapshot.DB, repo *snapshot.Repo) int {
	srcRoot := repo.EffectiveSourceRoot()
	// Hold the E6 refresh lock across the whole build→save→snapshot: buildIR reads
	// the existing ir.json for incremental reuse, so a concurrent PostToolUse hook
	// that also load-modify-saves ir.json would otherwise lose (or be lost to) this
	// reindex — exactly what CLAUDE.md tells the user to run on staleness (#137).
	// Mirrors the hook, which holds the same lock across its own generate→save.
	exitCode := 0
	withRepoRefreshLock(repo.ID, func() {
		irData, stats, code := buildIR(srcRoot, repo.FileCap)
		if code != 0 {
			exitCode = code
			return
		}
		if err := irData.Save(filepath.Join(srcRoot, ".ai", "ir.json")); err != nil {
			exitCode = printErr(fmt.Errorf("save ir.json: %w", err))
			return
		}
		// Dedup: an unchanged tree touches the existing snapshot's timestamp
		// instead of writing an identical copy of every file/symbol/ref row
		// (#351). TouchRepo below still runs either way — see its comment.
		id, wrote, err := db.SaveReindexSnapshot(repo.ID, "", srcRoot, irData)
		if err != nil {
			exitCode = printErr(err)
			return
		}
		// Unconditional, in BOTH branches: repos.last_indexed is what the
		// staleness path and the coverage counters read, and a repo whose
		// content simply did not change this tick is fully current — recording
		// it as anything else would be a lie the guard acts on.
		if err := db.TouchRepo(repo.ID, time.Now(), stats.ParseErrors, stats.SupportedSeen); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to record index time: %v\n", err)
		}
		short := irData.RootHash
		if len(short) > 12 {
			short = short[:12]
		}
		if wrote {
			fmt.Printf("Reindexed %s: snapshot id=%d files=%d root_hash=%s...%s\n",
				repo.Name, id, len(irData.Files), short, coverageSuffix(stats))
		} else {
			fmt.Printf("Unchanged %s: snapshot id=%d files=%d root_hash=%s (touched)%s\n",
				repo.Name, id, len(irData.Files), short, coverageSuffix(stats))
		}
	})
	return exitCode
}

// defaultPruneKeep is how many "reindex" snapshots per repo `repo prune` keeps.
//
// Chosen against the only consumer that reads snapshot history by depth:
// `runecho-ir churn` defaults to --n=20 (see runChurn) and walks the last N
// snapshots by count via db.List. 30 clears that with headroom. The other
// history readers — diff --since=<label>, truth-trail, map --since= — resolve
// only the single newest snapshot for a label, so any keep >= 1 is safe for
// them.
//
// Count-based rather than age-based on purpose: churn counts snapshots, so an
// age rule would silently shrink a quiet repo's usable window for a reason
// unrelated to what anything actually reads.
const defaultPruneKeep = 30

// runRepoPrune trims "reindex" snapshot history to the newest --keep per repo.
// Other labels are never touched — see pruneReindexWhere for why.
func runRepoPrune(args []string) int {
	fs := flag.NewFlagSet("repo prune", flag.ContinueOnError)
	keep := fs.Int("keep", defaultPruneKeep, "reindex snapshots to keep per repo")
	repoName := fs.String("repo", "", "prune only this repo (default: every enrolled repo)")
	dryRun := fs.Bool("dry-run", false, "report what would be deleted, delete nothing")
	vacuum := fs.Bool("vacuum", false, "VACUUM the store afterwards to return freed space to the filesystem")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}
	if *keep < 1 {
		fmt.Fprintf(os.Stderr, "runecho-ir repo prune: --keep must be >= 1, got %d\n", *keep)
		return ExitError
	}

	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()

	// repoID 0 means "every repo" to the store layer.
	var repoID int64
	scope := "all repos"
	if *repoName != "" {
		repo, err := db.GetRepoByName(*repoName)
		if err != nil {
			return printErr(err)
		}
		if repo == nil {
			fmt.Fprintf(os.Stderr, "No repo named %q\n", *repoName)
			return ExitError
		}
		repoID = repo.ID
		scope = repo.Name
	}

	if *dryRun {
		n, err := db.CountPruneReindexCandidates(repoID, *keep)
		if err != nil {
			return printErr(err)
		}
		fmt.Printf("Would prune %d reindex snapshot(s) from %s (keep=%d). Nothing deleted.\n", n, scope, *keep)
		return ExitOK
	}

	n, err := db.PruneReindexSnapshots(repoID, *keep)
	if err != nil {
		return printErr(err)
	}
	fmt.Printf("Pruned %d reindex snapshot(s) from %s (keep=%d).\n", n, scope, *keep)

	if *vacuum {
		// Deleting rows frees pages inside the file; only VACUUM hands them back
		// to the filesystem. On a multi-gigabyte store this rewrites every live
		// page and takes a while — which is exactly why it is opt-in.
		fmt.Println("Vacuuming (rewrites the whole store; this can take a while)...")
		if err := db.Vacuum(); err != nil {
			return printErr(err)
		}
		fmt.Println("Vacuum complete.")
	}
	return ExitOK
}

// runRepoPruneMissing lists — or with --yes purges — enrolled repos whose
// source root no longer exists on disk.
//
// List-only by default, and deliberately with no heuristic to make it
// automatic. A stat() miss means "not there right now", which is also what an
// unmounted external drive, a detached network share, or a temporarily moved
// checkout looks like. Purging a repo's entire history on one failed stat is
// unrecoverable, so the decision stays with a human who can read the printed
// paths and recognise them. There is deliberately no age or streak threshold:
// a drive that has been unplugged for a month is still not gone.
//
// This is never wired into `reindex --all`, cron, or launchd for the same
// reason.
//
// It is not, despite appearances, a fix for snapshot growth. buildIR already
// refuses a vanished root via requireExistingDir before any snapshot is
// written, so these repos are dead weight and hourly log noise, not a source
// of junk rows.
// rootIsMissing reports whether root is definitively gone: only a real
// os.ErrNotExist counts. A permission error or an I/O error on a flaky mount
// is NOT evidence the repo is gone, so those report false — treating them as
// missing is how --yes would delete something real. Shared by
// runRepoPruneMissing and runRepoList so "missing" cannot mean two different
// things between the two commands (issue #370: the rot must be visible in
// `repo list` in exactly the same terms prune-missing acts on).
func rootIsMissing(root string) bool {
	_, statErr := os.Stat(root)
	return errors.Is(statErr, os.ErrNotExist)
}

func runRepoPruneMissing(args []string) int {
	fs := flag.NewFlagSet("repo prune-missing", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "actually purge the listed repos and their history")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}

	db, code := mustOpenDB()
	if code != 0 {
		return code
	}
	defer db.Close()

	repos, err := db.ListRepos()
	if err != nil {
		return printErr(err)
	}

	var missing []snapshot.Repo
	for i := range repos {
		if rootIsMissing(repos[i].EffectiveSourceRoot()) {
			missing = append(missing, repos[i])
		}
	}

	if len(missing) == 0 {
		fmt.Println("No enrolled repos have a missing source root.")
		return ExitOK
	}

	for _, r := range missing {
		fmt.Printf("%s (id=%d) -> %s [missing]\n", r.Name, r.ID, r.EffectiveSourceRoot())
	}

	if !*yes {
		fmt.Printf("\n%d of %d enrolled repo(s) have a missing source root. Nothing deleted.\n", len(missing), len(repos))
		fmt.Println("Re-run with --yes to purge them and their history — only once you have")
		fmt.Println("read the paths above and confirmed they are gone for good, not on an")
		fmt.Println("unmounted drive or a detached share.")
		return ExitOK
	}

	purged := 0
	for _, r := range missing {
		// PurgeRepo is the existing, orphan-tested whole-repo funnel that
		// `repo rm` uses; no new delete path is introduced here.
		if err := db.PurgeRepo(r.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not purge %s (id=%d): %v\n", r.Name, r.ID, err)
			continue
		}
		removeRefreshLock(r.ID)
		purged++
	}
	fmt.Printf("Purged %d of %d missing repo(s) and their history.\n", purged, len(missing))
	if purged != len(missing) {
		return ExitError
	}
	return ExitOK
}
