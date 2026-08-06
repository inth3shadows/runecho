package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/inth3shadows/runecho/internal/guardstats"
)

// runFPAudit classifies every symbol the guard flagged against git history,
// splitting fpreport's single "approved anyway" number into three verdicts that
// have different fixes: fp (resolver missed a definition that already existed),
// premature (the guard was right and fired too early), and stands (the reference
// is still unbacked).
//
// It exists because fpreport's rate cannot be a false-positive proxy on this
// data. Joined to transcript ground truth over 30 days, 308 ask-gated edits
// produced 308 approvals and 0 denials, so the approval signal has no variance
// and its per-check and per-language spreads describe the join's failure modes.
// See internal/guardstats/fpaudit.go for the full argument.
//
// Read-only: it runs `git rev-list`, `git rev-parse` and `git grep` against
// commits, checks nothing out, and writes nothing anywhere.
//
// Exit codes match fpreport: ExitOK(0), ExitNoData(1) for a missing or empty
// log (a fresh checkout — CI should skip), ExitError(2) for a bad flag.
func runFPAudit(args []string) int {
	fs := flag.NewFlagSet("fpaudit", flag.ContinueOnError)
	days := fs.Int("days", 30, "audit window in days (1..36500)")
	asJSON := fs.Bool("json", false, "machine-readable JSON, including every per-symbol verdict")
	gv := fs.String("gv", "", "only audit decisions written by this guard version (\"unknown\" = records predating version stamping)")
	timeout := fs.Duration("git-timeout", guardstats.DefaultGitTimeout, "per-git-invocation timeout")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}
	if *days <= 0 || *days > maxReportDays {
		fmt.Fprintf(os.Stderr, "fpaudit: --days must be between 1 and %d\n", maxReportDays)
		return ExitError
	}
	if *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "fpaudit: --git-timeout must be positive")
		return ExitError
	}

	dir, err := runechoDir()
	if err != nil {
		return printErr(err)
	}
	decisions, err := guardstats.Load(filepath.Join(dir, "decisions.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "No decisions recorded yet.")
			return ExitNoData
		}
		return printErr(err)
	}

	since := time.Now().Add(-time.Duration(*days) * 24 * time.Hour)
	stats := guardstats.Audit(
		guardstats.FilterVersion(decisions, *gv),
		since,
		guardstats.GitOracle{Timeout: *timeout},
	)

	if *asJSON {
		out, err := json.MarshalIndent(guardstats.PayloadAudit(stats), "", "  ")
		if err != nil {
			return printErr(err)
		}
		fmt.Println(string(out))
	} else {
		fmt.Print(guardstats.FormatAudit(stats))
	}
	if stats.Symbols == 0 {
		return ExitNoData
	}
	return ExitOK
}
