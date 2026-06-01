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
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

const version = "0.1.0"

func main() {
	os.Exit(run())
}

func run() int {
	dryRun := flag.Bool("dry-run", false, "report violations but exit 0")
	verbose := flag.Bool("verbose", false, "print every checked symbol")
	hookMode := flag.Bool("hook-mode", false, "Claude Code PreToolUse hook mode — reads JSON from stdin, writes JSON to stdout")
	flag.Parse()

	// Bypass check after flag parsing so hook mode can emit a proper JSON approve.
	if os.Getenv("RUNECHO_GUARD_SKIP") == "1" {
		if *hookMode {
			hookApprove()
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

	// Resolve repo root.
	repoRoot, err := gitTopLevel()
	if err != nil {
		warnf("cannot determine repo root: %v", err)
		return 0
	}

	// Look up enrolled repo.
	repo, err := db.GetRepoByPath(repoRoot)
	if err != nil {
		warnf("store error: %v", err)
		return 0
	}
	if repo == nil {
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
	maxAge, err := parseMaxAge()
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
		fmt.Fprintf(os.Stderr, "  %s:%d: %s\n", v.File, v.Line, v.Symbol)
	}
	fmt.Fprintf(os.Stderr, "\nNote: only bare calls are checked (method calls x.Foo() are skipped).\n")
	fmt.Fprintf(os.Stderr, "Add false positives to .runechoguardignore, or bypass with RUNECHO_GUARD_SKIP=1.\n")

	if *dryRun {
		return 0
	}
	return 1
}

// runHookMode implements the Claude Code PreToolUse hook contract.
// Reads JSON from stdin, writes {"decision":"approve"|"block","reason":"..."} to stdout.
// Exits 0 unconditionally — the decision is communicated through the JSON output.
func runHookMode() int {
	var payload struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			FilePath  string `json:"file_path"`
			NewString string `json:"new_string"` // Edit tool
			Content   string `json:"content"`    // Write tool
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		hookApprove()
		return 0
	}

	if payload.ToolName != "Edit" && payload.ToolName != "Write" {
		hookApprove()
		return 0
	}

	text := payload.ToolInput.NewString
	if text == "" {
		text = payload.ToolInput.Content
	}
	filePath := payload.ToolInput.FilePath
	if text == "" || filePath == "" {
		hookApprove()
		return 0
	}

	lang := guard.LangFor(filePath)
	if lang == guard.LangUnknown {
		hookApprove()
		return 0
	}

	symbols, ignorePath, ok := lookupSymbols()
	if !ok {
		hookApprove()
		return 0
	}

	diffs := []guard.FileDiff{{
		Path:       filePath,
		AddedLines: textToAddedLines(text),
	}}

	violations := guard.Run(symbols, ignorePath, diffs)
	if len(violations) == 0 {
		hookApprove()
		return 0
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "[runecho-guard] %d unresolved symbol(s) in new content:\n", len(violations))
	for _, v := range violations {
		fmt.Fprintf(&sb, "  line %d: %s\n", v.Line, v.Symbol)
	}
	fmt.Fprintf(&sb, "Add false positives to .runechoguardignore or set RUNECHO_GUARD_SKIP=1 to bypass.")
	hookBlock(sb.String())
	return 0
}

// lookupSymbols loads the symbol set for the repo containing the current directory.
// Returns ok=false on any degradation condition (no DB, not enrolled, no snapshot).
func lookupSymbols() (symbols map[string]struct{}, ignorePath string, ok bool) {
	dir, err := runechoDir()
	if err != nil {
		return nil, "", false
	}
	dbPath := filepath.Join(dir, "history.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, "", false
	}
	db, err := snapshot.Open(dbPath)
	if err != nil {
		return nil, "", false
	}
	defer db.Close()

	repoRoot, err := gitTopLevel()
	if err != nil {
		return nil, "", false
	}

	repo, err := db.GetRepoByPath(repoRoot)
	if err != nil || repo == nil {
		return nil, "", false
	}

	snaps, err := db.List(repo.ID, 1)
	if err != nil || len(snaps) == 0 {
		return nil, "", false
	}

	syms, err := db.SymbolsForLatestSnapshot(repo.ID)
	if err != nil {
		return nil, "", false
	}

	return syms, filepath.Join(repoRoot, ".runechoguardignore"), true
}

// textToAddedLines converts a multi-line string into AddedLine entries (1-based).
func textToAddedLines(text string) []guard.AddedLine {
	raw := strings.Split(text, "\n")
	lines := make([]guard.AddedLine, len(raw))
	for i, l := range raw {
		lines[i] = guard.AddedLine{LineNo: i + 1, Text: l}
	}
	return lines
}

func hookApprove() {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"decision": "approve"})
}

func hookBlock(reason string) {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"decision": "block", "reason": reason})
}

func gitTopLevel() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func runechoDir() (string, error) {
	if h := os.Getenv("RUNECHO_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".runecho"), nil
}

func parseMaxAge() (time.Duration, error) {
	if s := os.Getenv("RUNECHO_GUARD_MAX_AGE"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("bad RUNECHO_GUARD_MAX_AGE %q: %w", s, err)
		}
		return d, nil
	}
	return 24 * time.Hour, nil
}

func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[runecho-guard] WARNING: "+format+"\n", args...)
}

func infof(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[runecho-guard] "+format+"\n", args...)
}
