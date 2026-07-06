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
			return "generate-fail"
		}
		updated, changed, bootstrapped = full, true, true
		parseErrors, supportedSeen = stats.ParseErrors, stats.SupportedSeen
	} else if updated, changed, err = gen.UpdateFile(existing, srcRoot, filePath); err != nil {
		return "update-fail"
	}
	if !changed {
		return "unchanged" // nothing structural changed; leave the store and ir.json alone
	}

	if err := updated.Save(irPath); err != nil {
		return "save-fail"
	}
	// Roll the single "auto" snapshot: delete the prior one and write the fresh
	// one in ONE transaction, so concurrent PostToolUse hooks can't leave two.
	if _, err := db.RollAutoSnapshot(repo.ID, "", srcRoot, updated); err != nil {
		return "snapshot-roll-fail"
	}
	// Bump last_indexed so the staleness warning stays quiet. The coverage
	// counters are the pre-walk values for a single-file refresh, or the fresh
	// full-walk values when this call bootstrapped (see above).
	_ = db.TouchRepo(repo.ID, time.Now(), parseErrors, supportedSeen)
	if bootstrapped {
		return "bootstrapped"
	}
	return "refreshed"
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
	// dangling-ref check matters most. So don't drop a Write as "empty input" while
	// those deletion-side checks are enabled — let the on-disk read downstream
	// decide. (A Write that creates a genuinely empty file simply finds nothing to
	// delete there and falls through to the normal no-violation defer.)
	emptyInput := text == "" && removedText == ""
	if payload.ToolName == "Write" && (danglingEnabled() || droppedImportEnabled()) {
		emptyInput = false
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
	addInFileDefs(symbols, filePath, lang)

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

	diffs := []guard.FileDiff{{
		Path:       filePath,
		AddedLines: hookAddedLines(payload.ToolName, payload.ToolInput.NewString, payload.ToolInput.Content, payload.ToolInput.Edits),
	}}

	violations := guard.Run(symbols, ignorePath, diffs)

	// Deletion-side checks (both gated OFF by default; dogfood-first). They share
	// the pre-edit text — removedText for Edit/MultiEdit, or the on-disk file for
	// Write, which replaces wholesale so the old file is the only record of what it
	// removes (best-effort read; the hook is PreToolUse, so the file is still old).
	// Both feed the single ask. Fail-open: any error yields no warning.
	var dangling []danglingWarning
	var droppedImps []guard.DroppedImport
	var duplicates []duplicateWarning
	if danglingEnabled() || droppedImportEnabled() || duplicateEnabled() {
		oldText := removedText
		if payload.ToolName == "Write" {
			if data, err := os.ReadFile(filePath); err == nil && len(data) <= maxInFileBytes {
				oldText = string(data)
			}
		}
		// E1: does this edit remove a definition that *other* files still reference?
		if danglingEnabled() {
			if deleted := deletedDefs(lang, oldText, text); len(deleted) > 0 {
				dangling = checkDanglingRefs(filepath.Dir(filePath), filePath, deleted)
			}
		}
		// Dropped-import: does this edit remove an import whose name the new text
		// still uses unqualified? Complements the additive check, which at edit time
		// still sees the old import on disk and so stays silent.
		if droppedImportEnabled() {
			droppedImps = guard.DroppedImportRefs(lang, oldText, text)
		}
		// E5: does this edit introduce a symbol not previously defined anywhere in
		// this file, whose name is already defined in a DIFFERENT file? Uses the
		// whole on-disk pre-edit file (wholeFileText), not oldText/removedText —
		// see wholeFileText's doc comment for why the hunk-scoped variable above
		// is not reusable here. Skips the check entirely (rather than treating ""
		// as "nothing defined") when wholeFileText can't give a definitive answer
		// — see wholeFileText's doc comment for why that distinction matters.
		if duplicateEnabled() {
			if wholeOld, definitive := wholeFileText(filePath); definitive {
				if added := addedDefs(lang, wholeOld, text); len(added) > 0 {
					duplicates = checkDuplicateDefs(filepath.Dir(filePath), filePath, added)
				}
			}
		}
	}

	if len(violations) == 0 && len(dangling) == 0 && len(droppedImps) == 0 && len(duplicates) == 0 {
		// Nothing flagged. Defer to the normal permission flow, but if the IR is
		// stale the check may be incomplete — say so via additionalContext (which
		// informs Claude without forcing an allow/deny).
		staleReason := hookDeferStale(out, latest)
		logDecision(decisionRecord{Mode: "hook", Repo: repoName, File: filePath, Lang: string(lang), Decision: "defer", Reason: staleReason})
		return 0
	}

	var sb strings.Builder
	var syms []string
	if len(violations) > 0 {
		fmt.Fprintf(&sb, "[runecho-guard] %d symbol reference(s) not found in the indexed code — possible hallucination:\n", len(violations))
		for _, v := range violations {
			// "snippet line N" is honest: in hook mode the guard scans the
			// new_string/content snippet, not the whole file, so the number is
			// relative to the edit hunk — not the file's absolute line number.
			fmt.Fprintf(&sb, "  snippet line %d: %s%s\n", v.Line, v.Symbol, suggestionSuffix(v.Suggestion))
			syms = append(syms, v.Symbol)
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
	logDecision(decisionRecord{Mode: "hook", Repo: repoName, File: filePath, Lang: string(lang), Decision: "ask", Reason: askReason(len(violations) > 0, len(dangling) > 0, len(droppedImps) > 0, len(duplicates) > 0), Symbols: syms})
	return 0
}

// maxInFileBytes caps the on-disk file read in addInFileDefs. Files larger than
// this are skipped — the definition-context gain is not worth the read/scan cost,
// and the SQLite symbol set already covers the file's top-level declarations.
const maxInFileBytes = 2 << 20 // 2 MiB

// addInFileDefs reads the current on-disk file and folds every definition the
// def-extractor finds (top-level AND indented/nested defs and local arrow
// consts, since the def regexes are `^\s*`-anchored) into the known symbol set.
// This is the P2 residual-killer: it makes a hunk-scoped Edit aware of the rest
// of its own file without re-implementing a scope-tracking parser. It mutates
// symbols in place (a fresh per-call map). Degrades silently on any read error
// (e.g. a brand-new file being created), adding nothing.
func addInFileDefs(symbols map[string]struct{}, filePath string, lang guard.Lang) {
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) > maxInFileBytes {
		return
	}
	fileLines := guard.TextToAddedLines(string(data))
	for _, def := range guard.ExtractDefs(lang, fileLines) {
		symbols[def] = struct{}{}
	}
	// Imported names (`from pathlib import Path`, `import {readFileSync} …`) are
	// real callables bound elsewhere in the file; fold them in too.
	for _, imp := range guard.ExtractImports(lang, fileLines) {
		symbols[imp] = struct{}{}
	}
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
		IgnorePath: filepath.Join(repoRoot, ".runechoguardignore"),
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
