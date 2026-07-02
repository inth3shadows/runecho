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

// runGuardStats summarizes cmd/runecho-guard's decisions.jsonl: ask/defer
// counts by repo and language, the most-frequently-flagged symbols, and
// defer-reason frequency, over a trailing --days window (issue #87).
func runGuardStats(args []string) int {
	fs := flag.NewFlagSet("guard-stats", flag.ContinueOnError)
	days := fs.Int("days", 30, "report window in days (decisions older than this are excluded)")
	top := fs.Int("top", 10, "number of top flagged symbols to show")
	asJSON := fs.Bool("json", false, "machine-readable JSON")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}
	if *days <= 0 {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir guard-stats [--days=N] [--top=N] [--json] (--days must be positive)")
		return ExitError
	}

	dir, err := runechoDir()
	if err != nil {
		return printErr(err)
	}
	path := filepath.Join(dir, "decisions.jsonl")

	decisions, err := guardstats.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "No decisions recorded yet.")
			return ExitNoData
		}
		return printErr(err)
	}

	since := time.Now().Add(-time.Duration(*days) * 24 * time.Hour)
	stats := guardstats.Aggregate(decisions, since, *top)

	if *asJSON {
		out, err := json.MarshalIndent(guardstats.Payload(stats), "", "  ")
		if err != nil {
			return printErr(err)
		}
		fmt.Println(string(out))
	} else {
		fmt.Print(guardstats.Format(stats))
	}
	return ExitOK
}
