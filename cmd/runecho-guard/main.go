// Command runecho-guard is a git pre-commit hook that validates symbol references
// in the staged diff against the RunEcho IR snapshot. It blocks commits that
// reference symbols not present in the indexed IR (hallucinated names).
//
// Usage:
//
//	runecho-guard [--dry-run] [--verbose]
//	runecho-guard --hook-mode  (Claude Code PreToolUse hook — reads JSON from stdin)
//
// Environment:
//
//	RUNECHO_GUARD_SKIP=1        bypass all checks, exit 0 / approve
//	RUNECHO_HOME                override central store directory (default ~/.runecho)
//	RUNECHO_GUARD_MAX_AGE=<dur> staleness warning threshold (default 24h)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/store"
)

const version = "0.1.0"

// gitTimeout caps each git subprocess in the hook path. If git blocks
// (credential helper, network mount, locked index), the hook would otherwise
// hang indefinitely.
const gitTimeout = 3 * time.Second

func main() {
	os.Exit(run())
}

func run() int {
	dryRun := flag.Bool("dry-run", false, "report violations but exit 0")
	verbose := flag.Bool("verbose", false, "print every checked symbol")
	hookMode := flag.Bool("hook-mode", false, "Claude Code PreToolUse hook mode — reads JSON from stdin, writes JSON to stdout")
	flag.Parse()

	// Bypass check after flag parsing. In hook mode this defers (emits nothing),
	// letting Claude Code's normal permission flow run unobstructed.
	if os.Getenv("RUNECHO_GUARD_SKIP") == "1" {
		if *hookMode {
			hookDefer()
		}
		return 0
	}

	if *hookMode {
		return runHookMode()
	}

	// Resolve central store.
	dir, err := runechoDir()
	if err != nil {
		warnf("cannot resolve store dir: %v", err)
		return 0
	}
	dbPath := filepath.Join(dir, "history.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// runecho not installed/configured on this machine — skip silently.
		return 0
	}

	db, err := snapshot.Open(dbPath)
	if err != nil {
		warnf("cannot open store: %v", err)
		return 0
	}
	defer db.Close()

	// Resolve the enrolled repo for the current working tree. resolveRepo keys on
	// the git-common-dir (stable across all worktrees), so bare-repo claudew
	// worktrees resolve in O(1). repoRoot is the enrolled repo's real working
	// tree — where ParseStagedDiff and the ignorefile are read from.
	cwd, err := os.Getwd()
	if err != nil {
		warnf("cannot determine working directory: %v", err)
		return 0
	}
	repo, repoRoot, ok := resolveRepo(db, cwd)
	if !ok {
		infof("skipping: repo not enrolled (run: runecho-ir repo add .)")
		return 0
	}

	// Ensure at least one snapshot exists.
	snaps, err := db.List(repo.ID, 1)
	if err != nil {
		warnf("store error: %v", err)
		return 0
	}
	if len(snaps) == 0 {
		infof("skipping: no snapshot yet (run: runecho-ir repo reindex %s)", repo.Name)
		return 0
	}

	// Warn if IR is stale.
	maxAge, err := guard.ParseMaxAge()
	if err != nil {
		warnf("%v", err)
		return 1
	}
	if age := time.Since(snaps[0].Timestamp); age > maxAge {
		days := int(age.Hours() / 24)
		warnf("IR is %d day(s) old — results may be incomplete", days)
	}

	// Load symbol set.
	symbols, err := db.SymbolsForLatestSnapshot(repo.ID)
	if err != nil {
		warnf("cannot load symbol set: %v", err)
		return 0
	}

	// Parse staged diff.
	diffs, err := guard.ParseStagedDiff(repoRoot)
	if err != nil {
		warnf("cannot parse staged diff: %v", err)
		return 0
	}
	if len(diffs) == 0 {
		return 0
	}

	// Ignorefile at repo root.
	ignorePath := filepath.Join(repoRoot, ".runechoguardignore")

	if *verbose {
		infof("checking %d file(s) against %d known symbols", len(diffs), len(symbols))
	}

	violations := guard.Run(symbols, ignorePath, diffs)

	if len(violations) == 0 {
		if *verbose {
			infof("all references resolved")
		}
		return 0
	}

	// Report violations.
	fmt.Fprintf(os.Stderr, "[runecho-guard] %d unresolved symbol(s):\n", len(violations))
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s:%d: %s%s\n", v.File, v.Line, v.Symbol, suggestionSuffix(v.Suggestion))
	}
	fmt.Fprintf(os.Stderr, "\nNote: only bare calls are checked (method calls x.Foo() are skipped).\n")
	fmt.Fprintf(os.Stderr, "Add false positives to .runechoguardignore, or bypass with RUNECHO_GUARD_SKIP=1.\n")

	if *dryRun {
		return 0
	}
	return 1
}

// runHookMode implements the Claude Code PreToolUse hook contract (2026 form:
// hookSpecificOutput.permissionDecision). Reads JSON from stdin, and either denies
// the tool call (hallucinated symbol) or defers to the normal permission flow.
// It NEVER emits "allow": a guard's job is to block, not to auto-approve every
// edit out from under the user's permission prompts. Exits 0 unconditionally —
// the decision is communicated through the JSON output (or its absence).
func runHookMode() int {
	var payload struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			FilePath  string `json:"file_path"`
			NewString string `json:"new_string"` // Edit tool
			Content   string `json:"content"`    // Write tool
			Edits     []struct {
				NewString string `json:"new_string"`
			} `json:"edits"` // MultiEdit tool
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		hookDefer()
		return 0
	}

	text := hookText(payload.ToolName, payload.ToolInput.NewString, payload.ToolInput.Content, payload.ToolInput.Edits)
	filePath := payload.ToolInput.FilePath
	if text == "" || filePath == "" {
		hookDefer()
		return 0
	}
	// Reject null bytes (invalid on all supported OSes) and extreme lengths.
	if strings.ContainsRune(filePath, 0) || len(filePath) > 4096 {
		hookDefer()
		return 0
	}

	lang := guard.LangFor(filePath)
	if lang == guard.LangUnknown {
		hookDefer()
		return 0
	}

	symbols, ignorePath, latest, ok := lookupSymbolsFor(filepath.Dir(filePath))
	if !ok {
		hookDefer()
		return 0
	}

	diffs := []guard.FileDiff{{
		Path:       filePath,
		AddedLines: guard.TextToAddedLines(text),
	}}

	violations := guard.Run(symbols, ignorePath, diffs)
	if len(violations) == 0 {
		// No hallucinated symbols. Defer to the normal permission flow, but if the
		// IR is stale the check may be incomplete — say so via additionalContext
		// (which informs Claude without forcing an allow/deny).
		hookDeferStale(latest)
		return 0
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "[runecho-guard] %d unresolved symbol(s) in new content:\n", len(violations))
	for _, v := range violations {
		fmt.Fprintf(&sb, "  line %d: %s%s\n", v.Line, v.Symbol, suggestionSuffix(v.Suggestion))
	}
	fmt.Fprintf(&sb, "Add false positives to .runechoguardignore or set RUNECHO_GUARD_SKIP=1 to bypass.")
	hookDeny(sb.String())
	return 0
}

// hookText returns the new content to check for the given tool. For MultiEdit it
// concatenates every edit's replacement text so symbols introduced in any edit
// are validated.
func hookText(toolName, newString, content string, edits []struct {
	NewString string `json:"new_string"`
}) string {
	switch toolName {
	case "Edit":
		return newString
	case "Write":
		return content
	case "MultiEdit":
		var parts []string
		for _, e := range edits {
			if e.NewString != "" {
				parts = append(parts, e.NewString)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// suggestionSuffix renders the model-free "did you mean" hint, or "" if none.
func suggestionSuffix(suggestion string) string {
	if suggestion == "" {
		return ""
	}
	return fmt.Sprintf("  (did you mean %q?)", suggestion)
}

// lookupSymbolsFor loads the symbol set for the repo containing dir, plus the
// timestamp of the latest snapshot (for staleness reporting). Returns ok=false on
// any degradation condition (no DB, not enrolled, no snapshot).
func lookupSymbolsFor(dir string) (symbols map[string]struct{}, ignorePath string, latest time.Time, ok bool) {
	storeDir, err := runechoDir()
	if err != nil {
		return nil, "", time.Time{}, false
	}
	dbPath := filepath.Join(storeDir, "history.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, "", time.Time{}, false
	}
	db, err := snapshot.Open(dbPath)
	if err != nil {
		return nil, "", time.Time{}, false
	}
	defer db.Close()

	repo, repoRoot, ok := resolveRepo(db, dir)
	if !ok {
		return nil, "", time.Time{}, false
	}

	snaps, err := db.List(repo.ID, 1)
	if err != nil || len(snaps) == 0 {
		return nil, "", time.Time{}, false
	}

	syms, err := db.SymbolsForLatestSnapshot(repo.ID)
	if err != nil {
		return nil, "", time.Time{}, false
	}

	return syms, filepath.Join(repoRoot, ".runechoguardignore"), snaps[0].Timestamp, true
}

// hookDefer emits no decision, so Claude Code applies its normal permission flow.
func hookDefer() {}

// hookDeferStale defers, but attaches an advisory note when the IR is older than
// the staleness threshold — the symbol check may have missed recently-added names.
func hookDeferStale(latest time.Time) {
	maxAge, err := guard.ParseMaxAge()
	if err != nil {
		return // bad config — stay silent rather than nag with a broken message
	}
	age := time.Since(latest)
	if age <= maxAge {
		return
	}
	days := int(age.Hours() / 24)
	ctx := fmt.Sprintf("RunEcho IR is %d day(s) stale; symbol checks may be incomplete — run `runecho-ir repo reindex`.", days)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":     "PreToolUse",
			"additionalContext": ctx,
		},
	})
}

// hookDeny blocks the tool call with the given reason (2026 hookSpecificOutput form).
func hookDeny(reason string) {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	})
}

// resolveRepo finds the enrolled repo whose worktree contains dir, and the real
// working-tree path (a git directory) to read the staged diff and ignorefile
// from. It resolves in three tiers:
//
//  1. Fast path — git-common-dir. The common-dir is the stable identity shared
//     by every worktree of a repo (bare or not), so a bare-repo claudew worktree
//     resolves in O(1): one git subprocess plus one indexed query. This is the
//     steady-state path for all repos enrolled under schema V4.
//  2. Enrolled-path lookup — for repos enrolled before V4 populated common_dir
//     (or when git-common-dir is unavailable). On a hit, common_dir is
//     backfilled so subsequent fires take the fast path.
//  3. Worktree-list compat shim — findEnrolledRepoViaWorktrees, for pre-V4
//     bare-repo enrollments whose path is a specific worktree. Also backfills.
//
// repoRoot is always the enrolled repo.Path (a real working tree). The symbol
// set is loaded by repo.ID downstream, independent of this path.
func resolveRepo(db *snapshot.DB, dir string) (*snapshot.Repo, string, bool) {
	commonDir, cdErr := gitutil.CommonDir(dir)
	if cdErr == nil {
		if repo, err := db.GetRepoByCommonDir(commonDir); err == nil && repo != nil {
			return repo, repo.Path, true
		}
	}
	// Compat tiers for pre-V4 repos (or when git-common-dir is unavailable).
	repoRoot, err := gitTopLevelFor(dir)
	if err != nil {
		return nil, "", false
	}
	if repo, err := db.GetRepoByPath(repoRoot); err == nil && repo != nil {
		backfillCommonDir(db, repo.ID, commonDir, cdErr)
		return repo, repoRoot, true
	}
	if repo, enrolledRoot := findEnrolledRepoViaWorktrees(db, repoRoot); repo != nil {
		backfillCommonDir(db, repo.ID, commonDir, cdErr)
		return repo, enrolledRoot, true
	}
	return nil, "", false
}

// backfillCommonDir records common_dir on a repo resolved via a compat tier, so
// the next guard fire takes resolveRepo's O(1) fast path. Best-effort: a write
// error or an unavailable git-common-dir simply leaves the compat tier in place.
func backfillCommonDir(db *snapshot.DB, repoID int64, commonDir string, cdErr error) {
	if cdErr == nil && commonDir != "" {
		_ = db.SetRepoCommonDir(repoID, commonDir)
	}
}

// findEnrolledRepoViaWorktrees is the legacy compatibility shim for repos
// enrolled before schema V4 populated common_dir (resolveRepo's tier 3). It
// handles bare-repo worktrees (claudew agent dirs) where git show-toplevel
// returns a transient worktree path that was never enrolled, trying:
//  1. git-common-dir — the bare root for bare repos; the .git parent for regular
//     linked worktrees. Covers regular non-bare linked-worktree repos cleanly.
//  2. git worktree list — each registered worktree path. Covers bare-repo layouts
//     where enrollment used a specific worktree (e.g. the `main` branch worktree).
//
// Returns both the matched repo and the enrolled root path (a real git
// directory). resolveRepo backfills common_dir on a hit here, so this shim
// self-retires after the first guard fire on each pre-V4 repo.
func findEnrolledRepoViaWorktrees(db *snapshot.DB, worktreePath string) (*snapshot.Repo, string) {
	if commonDir, err := gitCommonDirFor(worktreePath); err == nil {
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(worktreePath, commonDir)
		}
		commonDir = filepath.Clean(commonDir)
		// Non-bare repos: common-dir is the .git dir — strip to get the repo root.
		if filepath.Base(commonDir) == ".git" {
			commonDir = filepath.Dir(commonDir)
		}
		// Skip if common-dir resolved to worktreePath itself (bare repo main
		// worktree: git returns "." which joins to worktreePath). That path was
		// already tried by the caller; re-checking it wastes a DB roundtrip.
		if commonDir != worktreePath {
			if repo, err := db.GetRepoByPath(commonDir); err == nil && repo != nil {
				return repo, commonDir
			}
		}
	}
	for _, wt := range gitWorktreePathsFor(worktreePath) {
		if wt == worktreePath {
			continue
		}
		if repo, err := db.GetRepoByPath(wt); err == nil && repo != nil {
			return repo, wt
		}
	}
	return nil, ""
}

func gitCommonDirFor(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitWorktreePathsFor returns all working-tree paths registered for the git
// repo containing dir, parsed from `git worktree list --porcelain`.
func gitWorktreePathsFor(dir string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if after, ok := strings.CutPrefix(line, "worktree "); ok {
			if p := strings.TrimSpace(after); p != "" {
				paths = append(paths, p)
			}
		}
	}
	return paths
}

func gitTopLevelFor(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runechoDir is the package-local alias to the shared store helper.
func runechoDir() (string, error) { return store.RunechoDir() }

func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[runecho-guard] WARNING: "+format+"\n", args...)
}

func infof(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[runecho-guard] "+format+"\n", args...)
}
