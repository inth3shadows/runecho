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
//	RUNECHO_GUARD_STRICT=1      fail-closed on degraded states: in pre-commit mode,
//	                            degraded conditions that normally warn-and-pass instead
//	                            return exit 1; in hook mode, degraded conditions emit
//	                            an advisory via additionalContext but still exit 0.
//	                            Repo-not-enrolled is always a silent skip (not degraded).
//	RUNECHO_GUARD_LEARN=1      enable C3 learned-allow: auto-suppress asks for
//	                            symbols approved >= N times per repo (default OFF).
//	RUNECHO_GUARD_LEARN_N=<n>  approval count before a symbol is trusted (default 2)
//	RUNECHO_GUARD_LEARN_TTL_DAYS=<d>
//	                            days a learned-allow entry survives without being
//	                            re-approved before it decays away (default 14)
//	RUNECHO_GUARD_DANGLING=1   enable E1 dangling-refs: ask when an edit removes a
//	                            symbol definition that other files still reference
//	                            (per the latest snapshot's refs index). Default OFF
//	                            (dogfood gate); ask-posture, fail-open.
//	RUNECHO_GUARD_DEPS_GO=1    enable external-dependency validation for Go: flag a
//	                            call to a symbol absent from an imported external or
//	                            stdlib package (http.Gett where net/http has Get).
//	                            Abstains under go.work, behind a replace directive,
//	                            or when a package is not in the module cache.
//	                            Default OFF (dogfood gate).
//	RUNECHO_GUARD_DUPLICATE=1  enable E5 duplicate-symbol guard: ask when an edit
//	                            introduces a symbol definition whose name is
//	                            already defined in a different file (per the
//	                            latest snapshot's symbol index). Default OFF
//	                            (dogfood gate); ask-posture, fail-open.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/store"
	"github.com/inth3shadows/runecho/internal/version"
)

func main() {
	os.Exit(run())
}

func run() int {
	return runArgs(os.Args[1:])
}

// runArgs contains the actual implementation so tests can call it without
// re-registering flags on the global flag.CommandLine (which panics on
// duplicate registration across test cases in the same process).
func runArgs(args []string) int {
	fs := flag.NewFlagSet("runecho-guard", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "report violations but exit 0")
	verbose := fs.Bool("verbose", false, "print every checked symbol")
	hookMode := fs.Bool("hook-mode", false, "Claude Code PreToolUse hook mode — reads JSON from stdin, writes JSON to stdout")
	outcomeMode := fs.Bool("outcome-mode", false, "Claude Code PostToolUse outcome recorder — reads JSON from stdin, logs approved if a recent ask exists for the edited file")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Println(version.Version)
		return 0
	}

	// Bypass check after flag parsing. In hook mode this defers (emits nothing),
	// letting Claude Code's normal permission flow run unobstructed.
	if os.Getenv("RUNECHO_GUARD_SKIP") == "1" {
		// hookDefer is a no-op, so there is nothing to write here either way.
		return 0
	}

	if *outcomeMode {
		return runOutcomeMode(io.LimitReader(os.Stdin, 16<<20))
	}

	if *hookMode {
		// Cap stdin: an unbounded decode would buffer an arbitrarily large payload
		// before the per-field size checks in runHookMode ever run — a latency
		// footgun for a hook with a ~12ms budget. 16 MiB comfortably exceeds any
		// real tool input. The cap lives here (not in runHookMode) so tests can
		// feed a bare reader without re-wrapping it.
		return runHookMode(io.LimitReader(os.Stdin, 16<<20), os.Stdout)
	}

	strict := strictMode()

	// Resolve central store.
	dir, err := runechoDir()
	if err != nil {
		warnf("cannot resolve store dir: %v", err)
		return degradedExit(strict)
	}
	dbPath := filepath.Join(dir, "history.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		// runecho not installed/configured on this machine — skip silently.
		// Not a degraded state: the store has never been created here.
		return 0
	}

	db, err := snapshot.Open(dbPath)
	if err != nil {
		if errors.Is(err, snapshot.ErrSchemaNewer) {
			warnf("this runecho-guard binary is older than the store — symbol validation is DISABLED until it is rebuilt (bash install.sh): %v", err)
		} else {
			warnf("cannot open store: %v", err)
		}
		return degradedExit(strict)
	}
	defer db.Close()

	// Resolve the enrolled repo for the current working tree. ResolveRepo keys
	// on git-common-dir (stable across all worktrees), so bare-repo claudew
	// worktrees resolve in O(1). repoRoot is the enrolled repo's real working
	// tree — where ParseStagedDiff and the ignorefile are read from.
	cwd, err := os.Getwd()
	if err != nil {
		warnf("cannot determine working directory: %v", err)
		return degradedExit(strict)
	}
	repo, repoRoot, ok := db.ResolveRepo(cwd)
	if !ok {
		// Repo not enrolled is always a silent skip — not a degraded state, just
		// an unenrolled repo. RUNECHO_GUARD_STRICT=1 does not change this.
		infof("skipping: repo not enrolled (run: runecho-ir repo add .)")
		return 0
	}

	// Ensure at least one snapshot exists.
	snaps, err := db.List(repo.ID, 1)
	if err != nil {
		warnf("store error: %v", err)
		return degradedExit(strict)
	}
	if len(snaps) == 0 {
		infof("skipping: no snapshot yet (run: runecho-ir repo reindex %s)", repo.Name)
		return degradedExit(strict)
	}

	// Warn if IR is stale. A bad RUNECHO_GUARD_MAX_AGE must not block commits any
	// harder than any other degraded state — fail open (exit 0) unless strict.
	maxAge, err := guard.ParseMaxAge()
	if err != nil {
		warnf("%v", err)
		return degradedExit(strict)
	}
	if age := time.Since(snaps[0].Timestamp); age > maxAge {
		days := int(age.Hours() / 24)
		warnf("IR is %d day(s) old — results may be incomplete", days)
	}

	// Load symbol set.
	symbols, err := db.SymbolsForLatestSnapshot(repo.ID)
	if err != nil {
		warnf("cannot load symbol set: %v", err)
		return degradedExit(strict)
	}

	// Parse staged diff.
	diffCtx, diffCancel := context.WithTimeout(context.Background(), gitutil.Timeout)
	defer diffCancel()
	diffs, partial, err := guard.ParseStagedDiff(diffCtx, repoRoot)
	if err != nil {
		// Context deadline kills the git subprocess when it stalls (credential
		// helper, locked index). Fail-open by default; fail-closed under strict.
		warnf("cannot parse staged diff: %v", err)
		return degradedExit(strict)
	}
	if partial {
		// An oversized diff line (e.g. a minified blob) truncated the parse: every
		// file staged after it went unchecked. Surface this — a silent skip could
		// let a hallucinated symbol through behind a large generated file.
		warnf("staged diff truncated by an oversized line — files after it were NOT checked")
		if strict {
			return 1
		}
	}
	if len(diffs) == 0 {
		return 0
	}

	// Seed each diff with its on-disk path so the hallucination check can mask a
	// hunk that begins inside a pre-existing docstring — the opening delimiter sits
	// in unchanged context above the hunk, invisible to the added lines alone
	// (issue #145). Diff paths are repoRoot-relative; a file that can't be read
	// disables seeding for that entry (fail-open, handled in openSeedFor).
	for i := range diffs {
		diffs[i].AbsPath = filepath.Join(repoRoot, filepath.FromSlash(diffs[i].Path))
	}

	// Ignorefile at the committing worktree root (NOT repoRoot — see ignorePathFor).
	ignorePath := ignorePathFor(cwd, repoRoot)

	if *verbose {
		infof("checking %d file(s) against %d known symbols", len(diffs), len(symbols))
	}

	violations := guard.Run(symbols, ignorePath, diffs)

	// Same-repo internal-package qualified-call check (RUNECHO_GUARD_QUALIFIED=1,
	// default off). Reads each staged Go file's whole current text for import
	// parsing and the shadow gate; repoRoot anchors the go.mod lookup, resolved
	// once for the whole commit.
	if qualifiedEnabled() {
		if modulePath := guard.GoModulePath(repoRoot); modulePath != "" {
			for _, fd := range diffs {
				if guard.LangFor(fd.Path) != guard.LangGo {
					continue
				}
				whole := readFileLines(fd.AbsPath)
				violations = append(violations, qualifiedViolations(guard.LangGo, whole, fd.AddedLines, symbols, modulePath, fd.Path)...)
			}
		}
	}

	// External-dependency qualified-call check for Go (RUNECHO_GUARD_DEPS_GO=1,
	// default off). One index per commit, rooted at repoRoot so go.mod, the
	// module cache, and any vendor/ or go.work are resolved once.
	if goDepIdx := newGoDepIndex(repoRoot); goDepIdx != nil {
		modulePath := guard.GoModulePath(repoRoot)
		for _, fd := range diffs {
			if guard.LangFor(fd.Path) != guard.LangGo {
				continue
			}
			whole := readFileLines(fd.AbsPath)
			violations = append(violations, goDepQualifiedViolations(guard.LangGo, whole, fd.AddedLines, modulePath, goDepIdx, fd.Path)...)
		}
	}

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

	// Log after the stderr report — fail-open: log errors are silently discarded.
	syms := make([]string, len(violations))
	for i, v := range violations {
		syms[i] = v.Symbol
	}
	logDecision(decisionRecord{
		Mode:     "precommit",
		Repo:     repo.Name,
		Decision: "ask",
		Reason:   "violations",
		Symbols:  syms,
	})

	if *dryRun {
		return 0
	}
	return 1
}

// runOutcomeMode handles --outcome-mode. It reads a PostToolUse JSON payload
// from in, extracts the edited file path, and appends an approved-outcome
// record to decisions.jsonl if a recent ask exists for that file. Always
// exits 0 — outcome logging is observability-only and must never block a tool.
// in is explicit (not os.Stdin) so tests can call it without a subprocess.
func runOutcomeMode(in io.Reader) int {
	var payload struct {
		ToolInput struct {
			FilePath string `json:"file_path"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(in).Decode(&payload); err != nil {
		return 0
	}
	if payload.ToolInput.FilePath == "" {
		return 0
	}
	// Reject null-byte-tainted or absurdly long paths before touching the
	// filesystem (filepath.Dir/os.Stat/git), mirroring the PreToolUse guard.
	if strings.ContainsRune(payload.ToolInput.FilePath, 0) || len(payload.ToolInput.FilePath) > 4096 {
		return 0
	}
	logOutcomeForFile(payload.ToolInput.FilePath)
	// E6 auto-fresh IR: reindex the edited file so the NEXT PreToolUse check sees
	// symbols this edit added — closes the stale-IR false-positive class. Fail-open.
	refreshIRForFile(payload.ToolInput.FilePath)
	return 0
}

// refreshIRForFile is the E6 auto-fresh step: reparse just the edited file and
// roll the repo's "auto" snapshot so the guard's next read reflects this edit.
// It is strictly best-effort observability plumbing — every failure path is a
// silent no-op so the PostToolUse hook can never alter a tool result or block.
// The named return `outcome` carries a short token naming the branch this call
// took; the deferred e6debug appends it to decisions.jsonl under RUNECHO_DEBUG=1.
// Behavior is unchanged — every path is still a silent no-op for the hook — the
// trace is opt-in observability only. Tokens are stable for grepping a dogfood
// log: refreshed / bootstrapped / unchanged / no-repo / <something>-fail.
func refreshIRForFile(filePath string) (outcome string) {
	defer func() { e6debug(filePath, outcome) }()

	storeDir, err := runechoDir()
	if err != nil {
		return "no-store-dir"
	}
	dbPath := filepath.Join(storeDir, "history.db")
	if _, err := os.Stat(dbPath); err != nil {
		return "no-db"
	}
	db, err := snapshot.OpenFast(dbPath)
	if err != nil {
		return "open-fail"
	}
	defer db.Close()

	repo, _, ok := db.ResolveRepo(filepath.Dir(filePath))
	if !ok {
		return "no-repo" // unenrolled repo — expected, not a failure
	}
	srcRoot := repo.EffectiveSourceRoot()
	// In bare-repo + multi-worktree setups (the claudew/codexw pattern) the
	// registered srcRoot is the enrolled worktree (e.g. "master") while edits
	// land in a different linked worktree. UpdateFile normalises the edited
	// file path relative to srcRoot, so a cross-worktree path would fail the
	// "../" prefix check and silently return unchanged. Relative paths are
	// stable across linked worktrees, so swapping to the file's own worktree
	// root makes UpdateFile's path arithmetic correct.
	//
	// Guard: only swap if the file's worktree shares the enrolled repo's
	// common-dir. Without this, a nested .git (submodule, test fixture) in a
	// subdirectory would silently redirect srcRoot to an unrelated repo root.
	if repo.CommonDir != "" {
		if wtRoot, wtErr := gitutil.TopLevel(filepath.Dir(filePath)); wtErr == nil {
			if wcd, cdErr := gitutil.CommonDir(wtRoot); cdErr == nil && filepath.Clean(wcd) == filepath.Clean(repo.CommonDir) {
				srcRoot = filepath.Clean(wtRoot)
			}
		}
	}
	irPath := filepath.Join(srcRoot, ".ai", "ir.json")

	gen := ir.NewGenerator(ir.GeneratorConfig{})
	// Serialize the whole load→update→save (and the store roll that mirrors it)
	// under a cross-process advisory lock: concurrent PostToolUse hooks otherwise
	// interleave load-modify-save on ir.json and the last writer silently drops
	// the other file's refresh (last-writer-wins lost update). Same fail-open
	// flock the learned-allow store uses. The lock file lives in the runecho
	// store dir (keyed by repo ID), NOT beside ir.json — an in-worktree lock
	// file would litter git status on every hook fire, including the
	// unchanged/fail outcomes where nothing is ever saved. Holding the lock
	// across a bootstrap Generate is deliberate: a waiter then takes the cheap
	// UpdateFile path against the fresh IR instead of repeating the full walk.
	store.WithFileLock(store.RefreshLockPath(storeDir, repo.ID), func() {
		// Re-read the repo row now that the lock is held: a concurrent hook may
		// have bootstrapped while we waited, and TouchRepo below must not
		// clobber its fresh full-walk coverage counters with the stale values
		// this call resolved before blocking.
		if r2, rErr := db.GetRepoByName(repo.Name); rErr == nil && r2 != nil {
			repo = r2
		}
		existing, loadErr := ir.Load(irPath)

		var updated *ir.IR
		var changed bool
		bootstrapped := false
		// Coverage counters written back via TouchRepo below. A single-file
		// UpdateFile does not re-walk, so it preserves the repo's existing counters;
		// a bootstrap Generate re-walks the whole tree and yields fresh, authoritative
		// counts that must replace the stale ones (else coverage can exceed 100%).
		parseErrors, supportedSeen := repo.ParseErrors, repo.SupportedSeen
		if loadErr != nil || existing == nil || existing.Version != ir.IRVersion {
			// No usable IR file yet — bootstrap with a full generate (one-time cost).
			full, stats, genErr := gen.Generate(srcRoot)
			if genErr != nil {
				outcome = "generate-fail"
				return
			}
			updated, changed, bootstrapped = full, true, true
			parseErrors, supportedSeen = stats.ParseErrors, stats.SupportedSeen
		} else if updated, changed, err = gen.UpdateFile(existing, srcRoot, filePath); err != nil {
			outcome = "update-fail"
			return
		}
		if !changed {
			outcome = "unchanged" // nothing structural changed; leave the store and ir.json alone
			return
		}

		if err := updated.Save(irPath); err != nil {
			outcome = "save-fail"
			return
		}
		// Roll the single "auto" snapshot: delete the prior one and write the fresh
		// one in ONE transaction, so concurrent PostToolUse hooks can't leave two.
		if _, err := db.RollAutoSnapshot(repo.ID, "", srcRoot, updated); err != nil {
			outcome = "snapshot-roll-fail"
			return
		}
		// Bump last_indexed so the staleness warning stays quiet. The coverage
		// counters are the pre-walk values for a single-file refresh, or the fresh
		// full-walk values when this call bootstrapped (see above).
		_ = db.TouchRepo(repo.ID, time.Now(), parseErrors, supportedSeen)
		if bootstrapped {
			outcome = "bootstrapped"
			return
		}
		outcome = "refreshed"
	})
	return outcome
}

// runHookMode handles --hook-mode (Claude Code PreToolUse). It reads the tool
// edit out from under the user's permission prompts. Exits 0 unconditionally —
// the decision is communicated through the JSON written to out (or its absence).
// in/out are explicit (not os.Stdin/os.Stdout) so the full decision contract is
// testable without a subprocess; main() passes the real streams.
func runHookMode(in io.Reader, out io.Writer) int {
	var payload struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			FilePath  string   `json:"file_path"`
			OldString string   `json:"old_string"` // Edit tool (E1 dangling-refs)
			NewString string   `json:"new_string"` // Edit tool
			Content   string   `json:"content"`    // Write tool
			Edits     []editOp `json:"edits"`      // MultiEdit tool
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(in).Decode(&payload); err != nil {
		hookDefer()
		logDecision(decisionRecord{Mode: "hook", Decision: "defer", Reason: "parse-fail"})
		return 0
	}

	text := hookText(payload.ToolName, payload.ToolInput.NewString, payload.ToolInput.Content, payload.ToolInput.Edits)
	filePath := payload.ToolInput.FilePath
	// removedText is the Edit/MultiEdit text being deleted (cheap, no IO). It is
	// captured before the empty-input guard so a pure-deletion edit (empty
	// new_string) still reaches the E1 dangling-refs check below instead of being
	// dropped here. Empty (and inert) unless E1/dropped-import is enabled. Write
	// deletions are derived later from the on-disk file, not here. E5 does NOT
	// gate on this: it reads the whole pre-edit file itself (wholeFileText), so
	// including duplicateEnabled() here would needlessly keep this fast-path
	// guard from firing on an E5-only pure-deletion edit.
	var removedText string
	if danglingEnabled() || droppedImportEnabled() {
		removedText = hookOldText(payload.ToolName, payload.ToolInput.OldString, payload.ToolInput.Edits)
	}
	// A full-file-deletion Write (empty content) has text=="" and — since Write
	// carries no old_string — removedText=="" too, so it would trip the empty-input
	// bail below. But for Write the DELETED text is the pre-edit on-disk file, read
	// later for the E1/dropped-import checks; wiping a whole file is exactly when a
	// dangling-ref check matters most. So don't drop such a Write as "empty input"
	// while those checks are enabled — provided the on-disk file actually has
	// content to delete. A cheap os.Stat gates this so a Write that CREATES a new or
	// already-empty file (nothing to delete) keeps the fast early-return instead of
	// paying a DB open + two file reads on the ~12ms hook budget.
	emptyInput := text == "" && removedText == ""
	if emptyInput && payload.ToolName == "Write" && (danglingEnabled() || droppedImportEnabled()) {
		if fi, err := os.Stat(filePath); err == nil && fi.Size() > 0 {
			emptyInput = false
		}
	}
	if filePath == "" || emptyInput {
		hookDefer()
		logDecision(decisionRecord{Mode: "hook", File: filePath, Decision: "defer", Reason: "empty-input"})
		return 0
	}
	// Reject null bytes (invalid on all supported OSes) and extreme lengths.
	if strings.ContainsRune(filePath, 0) || len(filePath) > 4096 {
		hookDefer()
		logDecision(decisionRecord{Mode: "hook", Decision: "defer", Reason: "bad-path"})
		return 0
	}

	lang := guard.LangFor(filePath)
	if lang == guard.LangUnknown {
		hookDefer()
		logDecision(decisionRecord{Mode: "hook", File: filePath, Decision: "defer", Reason: "unknown-lang"})
		return 0
	}

	res := lookupSymbolsFor(filepath.Dir(filePath))
	if !res.OK {
		switch {
		case res.Warn != "":
			// Schema-newer: already loud regardless of strict — surfaced always.
			hookDeferContext(out, res.Warn)
			logDecision(decisionRecord{Mode: "hook", Repo: res.RepoName, File: filePath, Lang: string(lang), Decision: "defer", Reason: "schema-newer"})
		case res.NoRepo:
			// Not enrolled — silent skip; strict does not change this.
			hookDefer()
			logDecision(decisionRecord{Mode: "hook", File: filePath, Lang: string(lang), Decision: "defer", Reason: "no-repo"})
		default:
			// Store accessible but degraded (no snapshot, no symbols, etc.).
			// Under strict, surface an advisory so the user knows validation is off.
			if strictMode() {
				hookDeferContext(out, "[runecho-guard] store unavailable or no snapshot — symbol validation is DISABLED for this edit (RUNECHO_GUARD_STRICT=1).")
			} else {
				hookDefer()
			}
			logDecision(decisionRecord{Mode: "hook", Repo: res.RepoName, File: filePath, Lang: string(lang), Decision: "defer", Reason: "store-degraded"})
		}
		return 0
	}
	// Destructure into the locals the rest of the flow already uses.
	symbols, ignorePath, latest, repoName := res.Symbols, res.IgnorePath, res.Latest, res.RepoName

	// An Edit/MultiEdit hunk sees only the changed region, not the rest of the
	// file — so a call to a sibling function (or a nested/local def, or a private
	// `_helper` the IR may not index) elsewhere in the file would falsely read as
	// hallucinated. Fold the current on-disk file's definitions into the known set
	// to suppress that. Best-effort: a missing/oversized file simply adds nothing.
	// Read once here and reuse the parsed lines for the dropped-import check's
	// whole-file bound set below — same snapshot, one read/scan per hook.
	fileLines := readFileLines(filePath)
	addInFileDefs(symbols, fileLines, lang)

	// C3 learned-allow: fold in symbols this repo has approved often enough to
	// trust (count>=N, within TTL) so the guard stops re-asking about them.
	// Gated and read-only — a no-op (no store read) unless RUNECHO_GUARD_LEARN=1.
	if learnEnabled() {
		if dir, err := runechoDir(); err == nil {
			for s := range learnedAllowedSet(dir, repoName, time.Now()) {
				symbols[s] = struct{}{}
			}
		}
	}

	// newLines is the added text as AddedLines — gap-separated per edit for a
	// MultiEdit so stateful scanners reset open-string state at each boundary.
	// Shared by the additive check and the dropped-import check below so both see
	// the same (leak-free) view of a MultiEdit rather than a flat "\n"-join.
	newLines := hookAddedLines(payload.ToolName, payload.ToolInput.NewString, payload.ToolInput.Content, payload.ToolInput.Edits)
	diffs := []guard.FileDiff{{
		Path:       filePath,
		AddedLines: newLines,
		// Seed each block's open-string state from where it sits in the pre-edit
		// file, so an Edit landing inside a docstring or string literal is masked
		// instead of scanned as code. fileLines is the read already done above.
		SeedByLine: hookSeedByLine(payload.ToolName, payload.ToolInput.OldString, payload.ToolInput.Edits, fileLines, lang),
	}}

	violations := guard.Run(symbols, ignorePath, diffs)

	// Same-repo internal-package qualified-call check (RUNECHO_GUARD_QUALIFIED=1,
	// default off). fileLines is the pre-edit whole file (read above); newLines is
	// the proposed added text — passing both lets an in-edit shadow or a newly
	// added same-repo import be seen. The file's own directory anchors go.mod.
	if qualifiedEnabled() && lang == guard.LangGo {
		if modulePath := guard.GoModulePath(filepath.Dir(filePath)); modulePath != "" {
			violations = append(violations, qualifiedViolations(lang, fileLines, newLines, symbols, modulePath, filePath)...)
		}
	}

	// External-dependency qualified-call check for Go (RUNECHO_GUARD_DEPS_GO=1,
	// default off). The edited file's directory anchors go.mod discovery, so a
	// multi-module repo resolves against the module the file actually belongs to.
	if lang == guard.LangGo {
		if goDepIdx := newGoDepIndex(filepath.Dir(filePath)); goDepIdx != nil {
			modulePath := guard.GoModulePath(filepath.Dir(filePath))
			violations = append(violations, goDepQualifiedViolations(lang, fileLines, newLines, modulePath, goDepIdx, filePath)...)
		}
	}

	// Deletion-side checks (both gated OFF by default; dogfood-first). They share
	// the pre-edit text — removedText for Edit/MultiEdit, or the on-disk file for
	// Write, which replaces wholesale so the old file is the only record of what it
	// removes (best-effort read; the hook is PreToolUse, so the file is still old).
	// Both feed the single ask. Fail-open: any error yields no warning — but a
	// check that could NOT give a definitive answer is counted in degraded, so a
	// transient store error or an unreadable/oversized pre-edit file never
	// masquerades as a clean pass (silent by default; an advisory under strict).
	var dangling []danglingWarning
	var droppedImps []guard.DroppedImport
	var duplicates []duplicateWarning
	degraded := 0
	if danglingEnabled() || droppedImportEnabled() || duplicateEnabled() {
		// ONE definitive read of the pre-edit on-disk file, shared by every check
		// that needs it: E1/dropped-import's oldText for Write (the old file is
		// the only record of what a wholesale Write removes) and E5's whole-file
		// prior-definition set (any tool). The missing-vs-unreadable distinction
		// is wholeFileText's: a missing file means "" IS the pre-edit truth; an
		// existing file that is unreadable or over the cap means the pre-edit
		// state is unknown — the checks would run against a fabricated empty old
		// text and silently find nothing, so they are skipped and the single
		// cause counted once in degraded.
		wholeOld, wholeDefinitive := "", true
		if payload.ToolName == "Write" || duplicateEnabled() {
			wholeOld, wholeDefinitive = wholeFileText(filePath)
		}
		if !wholeDefinitive {
			degraded++
		}
		oldText := removedText
		oldTextDefinitive := true
		if payload.ToolName == "Write" {
			oldText, oldTextDefinitive = wholeOld, wholeDefinitive
		}
		// E1: does this edit remove a definition that *other* files still reference?
		if danglingEnabled() && oldTextDefinitive {
			if deleted := deletedDefs(lang, oldText, text); len(deleted) > 0 {
				var qErrs int
				dangling, qErrs = checkDanglingRefs(filepath.Dir(filePath), filePath, deleted)
				degraded += qErrs
			}
		}
		// Dropped-import: does this edit remove an import whose name the new text
		// still uses unqualified? Complements the additive check, which at edit time
		// still sees the old import on disk and so stays silent.
		if droppedImportEnabled() && oldTextDefinitive {
			oldLines := hookOldLines(payload.ToolName, payload.ToolInput.OldString, payload.ToolInput.Edits, oldText)
			// newLines is hunk-only for Edit/MultiEdit, so its bound set can't see a
			// name rebound on an UNTOUCHED line elsewhere in the file (mirrors why
			// addInFileDefs folds whole-file defs into the additive check's known
			// set above). Fold the on-disk file's whole-file binding context in as
			// preBound so such a rebind still suppresses the false positive. Not
			// needed for Write: its newLines already IS the whole file.
			var preBound map[string]struct{}
			if payload.ToolName != "Write" {
				preBound = wholeFileBoundNames(fileLines, lang)
			}
			droppedImps = guard.DroppedImportRefsLinesWithBound(lang, oldLines, newLines, preBound)
		}
		// E5: does this edit introduce a symbol not previously defined anywhere in
		// this file, whose name is already defined in a DIFFERENT file? Uses the
		// whole pre-edit file (wholeOld), not oldText/removedText — see
		// wholeFileText's doc comment for why the hunk-scoped variable above is
		// not reusable here.
		if duplicateEnabled() && wholeDefinitive {
			if added := addedDefs(lang, wholeOld, text); len(added) > 0 {
				var qErrs int
				duplicates, qErrs = checkDuplicateDefs(lang, filepath.Dir(filePath), filePath, added)
				degraded += qErrs
			}
		}
	}

	if len(violations) == 0 && len(dangling) == 0 && len(droppedImps) == 0 && len(duplicates) == 0 {
		// Nothing flagged. A degraded deletion-side check means "found nothing"
		// is not the same as "checked everything" — under strict, say so via
		// additionalContext (the same posture strict already applies to other
		// degraded states); by default stay silent per the fail-open contract.
		// Reason is check-degraded, NOT store-degraded: the store may be fine
		// (an oversized pre-edit file degrades too), and dogfood stats grep
		// decisions.jsonl by reason — conflating the two would skew the store-
		// health signal the un-gating decisions rest on. This intentionally
		// supersedes the stale-IR advisory for this edit (one advisory slot);
		// degraded coverage is the more actionable of the two.
		if degraded > 0 && strictMode() {
			hookDeferContext(out, fmt.Sprintf("[runecho-guard] %d deletion-side/duplicate check(s) could not run to completion (pre-edit file unreadable/oversized, or a store query failed) — coverage was incomplete for this edit.", degraded))
			logDecision(decisionRecord{Mode: "hook", Repo: repoName, File: filePath, Lang: string(lang), Decision: "defer", Reason: "check-degraded"})
			return 0
		}
		// If the IR is stale the check may be incomplete — say so via
		// additionalContext (which informs Claude without forcing an allow/deny).
		staleReason := hookDeferStale(out, latest)
		logDecision(decisionRecord{Mode: "hook", Repo: repoName, File: filePath, Lang: string(lang), Decision: "defer", Reason: staleReason})
		return 0
	}

	var sb strings.Builder
	// syms: every flagged name, for the ask record / guardstats observability.
	// learnSyms: only the hallucination-origin (violations) names — the subset an
	// approval may train the learned-allow store on. See LearnSymbols on
	// decisionRecord for why the other categories must be excluded.
	var syms []string
	var learnSyms []string
	if len(violations) > 0 {
		fmt.Fprintf(&sb, "[runecho-guard] %d symbol reference(s) not found in the indexed code — possible hallucination:\n", len(violations))
		for _, v := range violations {
			// "snippet line N" is honest: in hook mode the guard scans the
			// new_string/content snippet, not the whole file, so the number is
			// relative to the edit hunk — not the file's absolute line number.
			fmt.Fprintf(&sb, "  snippet line %d: %s%s\n", v.Line, v.Symbol, suggestionSuffix(v.Suggestion))
			syms = append(syms, v.Symbol)
			learnSyms = append(learnSyms, v.Symbol)
		}
	}
	if len(dangling) > 0 {
		fmt.Fprintf(&sb, "[runecho-guard] %d symbol(s) being removed are still referenced elsewhere — deleting may break callers:\n", len(dangling))
		for _, d := range dangling {
			fmt.Fprintf(&sb, "  %s — referenced by %s\n", d.Symbol, strings.Join(d.Referrers, ", "))
			syms = append(syms, d.Symbol)
		}
	}
	if len(droppedImps) > 0 {
		fmt.Fprintf(&sb, "[runecho-guard] %d import(s) removed by this edit are still used below — likely a dropped import (will fail at runtime):\n", len(droppedImps))
		for _, di := range droppedImps {
			fmt.Fprintf(&sb, "  %s — still used at snippet line %d\n", di.Name, di.LineNo)
			syms = append(syms, di.Name)
		}
	}
	if len(duplicates) > 0 {
		fmt.Fprintf(&sb, "[runecho-guard] %d new symbol(s) already exist as definitions elsewhere — possible duplicate/reimplementation:\n", len(duplicates))
		for _, d := range duplicates {
			fmt.Fprintf(&sb, "  %s — also defined in %s\n", d.Symbol, strings.Join(d.Locations, ", "))
			syms = append(syms, d.Symbol)
		}
	}
	fmt.Fprintf(&sb, "Approve if these are legitimate (new/local/dynamic, or an intended removal). Silence repeats via .runechoguardignore, or RUNECHO_GUARD_SKIP=1 to disable.")
	hookAsk(out, sb.String())
	logDecision(decisionRecord{Mode: "hook", Repo: repoName, File: filePath, Lang: string(lang), Decision: "ask", Reason: askReason(len(violations) > 0, len(dangling) > 0, len(droppedImps) > 0, len(duplicates) > 0), Symbols: syms, LearnSymbols: learnSyms})
	return 0
}

// maxInFileBytes caps the on-disk file read in readFileLines. Files larger than
// this are skipped — the definition-context gain is not worth the read/scan cost,
// and the SQLite symbol set already covers the file's top-level declarations.
const maxInFileBytes = 2 << 20 // 2 MiB

// readFileLines reads the current on-disk file and returns it as AddedLines, or
// nil if the file can't be read or exceeds maxInFileBytes. Both whole-file folds
// (addInFileDefs for the additive check, wholeFileBoundNames for the
// dropped-import check) consume this so a single hook invocation reads and parses
// the file once instead of once per check, and both see the same snapshot.
func readFileLines(filePath string) []guard.AddedLine {
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) > maxInFileBytes {
		return nil
	}
	return guard.TextToAddedLines(string(data))
}

// addInFileDefs folds every definition the def-extractor finds in fileLines
// (top-level AND indented/nested defs and local arrow consts, since the def
// regexes are `^\s*`-anchored) into the known symbol set. This is the P2
// residual-killer: it makes a hunk-scoped Edit aware of the rest of its own file
// without re-implementing a scope-tracking parser. It mutates symbols in place (a
// fresh per-call map). fileLines is nil for a missing/oversized file (see
// readFileLines), in which case this adds nothing.
func addInFileDefs(symbols map[string]struct{}, fileLines []guard.AddedLine, lang guard.Lang) {
	for _, def := range guard.ExtractDefs(lang, fileLines) {
		symbols[def] = struct{}{}
	}
	// Imported names (`from pathlib import Path`, `import {readFileSync} …`) are
	// real callables bound elsewhere in the file; fold them in too.
	for _, imp := range guard.ExtractImports(lang, fileLines) {
		symbols[imp] = struct{}{}
	}
	// JS binds callables by forms the def/import extractors miss — destructuring
	// (`const [x, setX] = useState()`), object destructure, and computed-assign
	// (`const fn = handlers[k]`). Fold the whole-file declarator binding targets in
	// so a bare call to one is not a false hallucination — crucially the binding
	// line (e.g. a useState destructure) usually sits OUTSIDE the edited hunk, which
	// the hunk-scoped diff never sees. JSDeclaredNames (not the over-inclusive
	// LocallyBoundNames) keeps a param type annotation from leaking a type name and
	// masking a real undefined reference. JS-only: Go skips bare lowercase refs
	// already, and Python's locals are out of scope for this pass.
	if lang == guard.LangJS {
		for _, name := range guard.JSDeclaredNames(fileLines) {
			symbols[name] = struct{}{}
		}
	}
	// Python sibling: a local callable bound by assignment (`handler =
	// HANDLERS[key]; handler(payload)`) is not a hallucination. Fold whole-file
	// assignment targets so a binding on a line outside the edited hunk resolves.
	if lang == guard.LangPython {
		for _, name := range guard.PyDeclaredNames(fileLines) {
			symbols[name] = struct{}{}
		}
		// Parameters used as callables are bound by their signature (a
		// `Callable`-typed param, a lambda arg). Fold the whole file's parameter
		// names — names only, never their type annotations. This was the last
		// surviving Python false-positive class in the live decision log.
		for _, name := range guard.PyParamNames(fileLines) {
			symbols[name] = struct{}{}
		}
	}
}

// wholeFileBoundNames returns the union of the file's locally-bound names
// (LocallyBoundNames) and definitions (ExtractDefs) from fileLines — the same
// whole-file fold-in addInFileDefs does for the additive check, mirrored here for
// the dropped-import check's bound set. Fail-open: nil fileLines (a missing or
// over-maxInFileBytes file, per readFileLines) yields nil (no extra context),
// same as addInFileDefs's own degrade path.
//
// Known limitation (accepted, not a bug): fileLines is read PRE-edit, so for a
// MultiEdit whose own sibling hunk removes a rebind, that rebind still appears
// bound here and can suppress a real dropped-import warning (a false negative).
// This is deliberately on the precision-over-recall side the whole check is
// tuned to (see DroppedImportRefs): a suppressed real drop is recoverable (the
// additive check or the runtime still catches it), whereas a false alarm from
// masking sibling-hunk state imperfectly would train users to ignore the guard.
// Masking each edit's OldString region before the read was considered and
// rejected as complexity that buys recall the design does not prioritize.
func wholeFileBoundNames(fileLines []guard.AddedLine, lang guard.Lang) map[string]struct{} {
	if fileLines == nil {
		return nil
	}
	bound := guard.LocallyBoundNames(lang, fileLines)
	for _, def := range guard.ExtractDefs(lang, fileLines) {
		bound[def] = struct{}{}
	}
	return bound
}

// editOp is one MultiEdit edit. OldString is captured for the E1 dangling-refs
// check (a removed definition); NewString for the additive hallucination check.
type editOp struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// hookText returns the new content to check for the given tool. For MultiEdit it
// concatenates every edit's replacement text so symbols introduced in any edit
// are validated.
func hookText(toolName, newString, content string, edits []editOp) string {
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

// hookAddedLines builds the AddedLine slice the additive hallucination check
// scans. It mirrors hookText, but for MultiEdit it keeps each edit in its own
// line-number block (separated by a gap via AddedLinesWithGap) instead of the
// flat "\n"-joined string hookText returns. That gap makes the stateful literal
// scanner reset open-string/comment state at every edit boundary, so an
// unterminated string in one edit can't silently blank real calls in an
// unrelated later edit (dropping genuine hallucination detections).
func hookAddedLines(toolName, newString, content string, edits []editOp) []guard.AddedLine {
	switch toolName {
	case "MultiEdit":
		var blocks []string
		for _, e := range edits {
			if e.NewString != "" {
				blocks = append(blocks, e.NewString)
			}
		}
		return guard.AddedLinesWithGap(blocks)
	case "Edit":
		return guard.TextToAddedLines(newString)
	case "Write":
		return guard.TextToAddedLines(content)
	default:
		return nil
	}
}

// hookSeedByLine computes, per added-line block, the open-string state in effect
// where that block sits in the PRE-EDIT file — the seed guard.Run needs so a
// block that begins inside a pre-existing docstring or string literal is masked
// rather than scanned as code.
//
// Why this exists: issue #145 fixed exactly this leak for the pre-commit path,
// via FileDiff.AbsPath and real new-file line numbers. The hook path's line
// numbers are synthetic (1..N per block), so that mechanism could not apply and
// the leak survived where all the real traffic is — measured as the largest
// single source of live false positives (prose words followed by a parenthetical
// in a docstring, and SQL keywords like `VALUES (` inside a query string, both
// read as calls).
//
// The block's position is recovered by matching its old_string against the
// pre-edit file in LINE space (fileLines is already in hand; no second read).
// Write is deliberately absent: it replaces the file wholesale, so its content
// genuinely starts outside any string. Every failure to locate a block is a
// silent skip — no entry means "starts outside any string", the previous
// behavior, so a bad match degrades to today's noise rather than to a missed
// hallucination.
func hookSeedByLine(toolName, oldString string, edits []editOp, fileLines []guard.AddedLine, lang guard.Lang) map[int]string {
	if len(fileLines) == 0 {
		return nil
	}
	seeds := make(map[int]string)
	switch toolName {
	case "Edit":
		if idx := blockStartLine(fileLines, oldString); idx >= 0 {
			if open := guard.OpenStateBefore(lang, fileLines, idx); open != "" {
				seeds[1] = open
			}
		}
	case "MultiEdit":
		// Mirror hookAddedLines' block selection AND AddedLinesWithGap's line
		// arithmetic exactly, so each seed lands on the synthetic LineNo that
		// actually starts its block. Drifting from either would silently seed the
		// wrong block.
		no, first := 0, true
		for _, e := range edits {
			if e.NewString == "" {
				continue
			}
			if !first {
				no++ // the gap AddedLinesWithGap inserts between blocks
			}
			start := no + 1
			no += len(strings.Split(e.NewString, "\n"))
			first = false
			if idx := blockStartLine(fileLines, e.OldString); idx >= 0 {
				if open := guard.OpenStateBefore(lang, fileLines, idx); open != "" {
					seeds[start] = open
				}
			}
		}
	}
	if len(seeds) == 0 {
		return nil
	}
	return seeds
}

// blockStartLine returns the 0-based index in fileLines where block's lines
// appear as a consecutive run, or -1 if block is empty, not found, or found in
// MORE THAN ONE place. Matching is done on lines rather than a byte offset
// because fileLines is what the hook already read, and its per-line cap (capLine)
// means a byte-offset search against a reconstructed text could drift.
//
// Ambiguity (two+ matches) returns -1 rather than guessing the first. A plain
// Edit requires old_string to be unique, so a second match cannot arise there —
// but a `replace_all` edit (which the hook payload does not parse) applies with a
// NON-unique old_string, and seeding from the wrong occurrence could compute an
// "open string" state that masks a hallucinated call in the replacement: a false
// negative, the worst class for this guard. Returning -1 on ambiguity means "no
// seed", which fails open toward flagging (a possible false positive) instead —
// the safe direction, and consistent with the uniqueness premise the plain-Edit
// path already assumes.
func blockStartLine(fileLines []guard.AddedLine, block string) int {
	if block == "" {
		return -1
	}
	want := strings.Split(block, "\n")
	found := -1
	for i := 0; i+len(want) <= len(fileLines); i++ {
		hit := true
		for j, w := range want {
			if fileLines[i+j].Text != w {
				hit = false
				break
			}
		}
		if hit {
			if found != -1 {
				return -1 // ambiguous: a second match — do not seed
			}
			found = i
		}
	}
	return found
}

// hookOldLines builds the AddedLines of the text being REMOVED, mirroring
// hookAddedLines: for a MultiEdit each edit's old_string is its own gap-separated
// block, so the dropped-import scan resets open multi-line-string state at each
// edit boundary instead of leaking it across the flat join. For Edit/Write the
// removed text is one contiguous region — singleBlock (the edit's old_string, or
// the on-disk pre-edit file for Write) — so it is converted straight through.
func hookOldLines(toolName, oldString string, edits []editOp, singleBlock string) []guard.AddedLine {
	if toolName == "MultiEdit" {
		var blocks []string
		for _, e := range edits {
			if e.OldString != "" {
				blocks = append(blocks, e.OldString)
			}
		}
		return guard.AddedLinesWithGap(blocks)
	}
	return guard.TextToAddedLines(singleBlock)
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
// any condition that prevents validation. Most failures stay silent (fail-open by
// design), but warn carries the one that must not: a store migrated by a newer
// binary means validation is disabled until this binary is rebuilt — permanent,
// and invisible unless surfaced. noRepo is true when the failure is "not enrolled"
// (an expected silent skip, not a degraded state); callers use it to suppress the
// strict-mode advisory for unenrolled repos. repoName is the enrolled repo's name
// whenever resolution succeeded (even if a later step degraded) — the decision log
// needs it for per-repo analysis (C3 learned-allow).
// lookupResult is the outcome of resolving the symbol set for an edit's repo.
// Symbols/IgnorePath/Latest are meaningful only when OK is true. The other
// fields carry the degraded-state contract the old 7-value return encoded in
// argument position (and only in comments):
//   - Warn != "": a schema-newer advisory that MUST surface even though OK is
//     false (the guard binary is older than the store; validation is disabled).
//   - NoRepo: the repo is not enrolled — an expected silent skip, distinct from
//     a degraded state, so callers suppress the strict-mode advisory.
//   - RepoName: set whenever the repo resolved (even if a later step degraded),
//     so the decision log can attribute the record per-repo.
type lookupResult struct {
	Symbols    map[string]struct{}
	IgnorePath string
	Latest     time.Time
	RepoName   string
	Warn       string
	NoRepo     bool
	OK         bool
}

// ignorePathFor resolves the .runechoguardignore for the working tree containing
// `dir`. The enrolled repoRoot can be a bare-repo CONTAINER: the claudew/codexw
// layout enrols the container (e.g. ".../terse", holding .bare + linked worktrees),
// not the worktree that actually holds the file (".../terse/main"). Joining repoRoot
// with the filename then points at a path that does not exist, loadIgnore silently
// returns nothing, and every false positive fires despite a correct ignore file. The
// ignore file is a per-worktree config, so prefer the git worktree top of `dir`; fall
// back to repoRoot when the worktree has no ignore file (or dir is not inside a tree,
// e.g. TopLevel erroring on a bare root).
func ignorePathFor(dir, repoRoot string) string {
	if top, err := gitutil.TopLevel(dir); err == nil {
		if p := filepath.Join(top, ".runechoguardignore"); fileExists(p) {
			return p
		}
	}
	return filepath.Join(repoRoot, ".runechoguardignore")
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func lookupSymbolsFor(dir string) lookupResult {
	storeDir, err := runechoDir()
	if err != nil {
		return lookupResult{}
	}
	dbPath := filepath.Join(storeDir, "history.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return lookupResult{}
	}
	// OpenFast skips the on-open integrity scan — this read path fires on every
	// edit and must stay cheap; integrity is the writer's concern.
	db, err := snapshot.OpenFast(dbPath)
	if err != nil {
		if errors.Is(err, snapshot.ErrSchemaNewer) {
			return lookupResult{Warn: "runecho-guard is older than the RunEcho store — symbol validation is DISABLED until the guard binary is rebuilt (bash install.sh)."}
		}
		return lookupResult{}
	}
	defer db.Close()

	repo, repoRoot, resolved := db.ResolveRepo(dir)
	if !resolved {
		// Not enrolled — silent skip, not a degraded state.
		return lookupResult{NoRepo: true}
	}

	snaps, err := db.List(repo.ID, 1)
	if err != nil || len(snaps) == 0 {
		return lookupResult{RepoName: repo.Name}
	}

	syms, err := db.SymbolsForLatestSnapshot(repo.ID)
	if err != nil {
		return lookupResult{RepoName: repo.Name}
	}

	return lookupResult{
		Symbols:    syms,
		IgnorePath: ignorePathFor(dir, repoRoot),
		Latest:     snaps[0].Timestamp,
		RepoName:   repo.Name,
		OK:         true,
	}
}

// hookDefer emits no decision, so Claude Code applies its normal permission flow.
func hookDefer() {}

// hookDeferStale defers, but attaches an advisory note when the IR is older than
// the staleness threshold — the symbol check may have missed recently-added names.
// It returns the log reason: "stale-ir" when the advisory fires, "clean" otherwise.
func hookDeferStale(out io.Writer, latest time.Time) string {
	maxAge, err := guard.ParseMaxAge()
	if err != nil {
		return "clean" // bad config — stay silent rather than nag with a broken message
	}
	age := time.Since(latest)
	if age <= maxAge {
		return "clean"
	}
	days := int(age.Hours() / 24)
	hookDeferContext(out, fmt.Sprintf("RunEcho IR is %d day(s) stale; symbol checks may be incomplete — run `runecho-ir repo reindex`.", days))
	return "stale-ir"
}

// hookDeferContext defers (no permission decision) while surfacing an advisory
// note via additionalContext — informs Claude without forcing allow/deny.
func hookDeferContext(out io.Writer, ctx string) {
	_ = json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":     "PreToolUse",
			"additionalContext": ctx,
		},
	})
}

// hookAsk surfaces the flagged symbol(s) for user confirmation (permissionDecision
// "ask", 2026 hookSpecificOutput form) rather than hard-denying. A guard mistake
// (the residual false-positive floor) then costs a single dismissal instead of an
// env-var/ignorefile override — which is what keeps the user reading the reason
// instead of training a reflexive bypass. The guard still never auto-allows.
//
// Posture note: this is the soft posture for every language. The plan's graduation
// rule (see runecho-guard-fp-precision-and-p5.md) is to move a language to a hard
// "deny" only after it has fired correctly ~20 times with zero false blocks in live
// use, reverting to "ask" on any confirmed false block.
func hookAsk(out io.Writer, reason string) {
	_ = json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]string{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "ask",
			"permissionDecisionReason": reason,
		},
	})
}

// runechoDir is the package-local alias to the shared store helper.
func runechoDir() (string, error) { return store.RunechoDir() }

// strictMode reports whether RUNECHO_GUARD_STRICT=1 is set. When true,
// degraded states (store unavailable, schema mismatch, no snapshot, etc.)
// cause pre-commit to exit 1 instead of 0, and hook mode emits an advisory
// via additionalContext instead of silently deferring. Repo-not-enrolled is
// always a silent skip regardless of strict (not a degraded state).
func strictMode() bool { return os.Getenv("RUNECHO_GUARD_STRICT") == "1" }

// degradedExit returns 1 when strict mode is active, 0 otherwise. Used at
// each pre-commit degraded-state early-return so the caller cannot forget to
// apply the strict toggle.
func degradedExit(strict bool) int {
	if strict {
		return 1
	}
	return 0
}

func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[runecho-guard] WARNING: "+format+"\n", args...)
}

func infof(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[runecho-guard] "+format+"\n", args...)
}
