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

// runFPReport reports the guard's observed false-positive rate from
// decisions.jsonl: the fraction of asks the agent approved anyway, joined
// symbol-exact to their outcomes, broken down by check and language, with the
// most-approved symbols and loudest repos. Complements guard-stats (which
// reports ask VOLUME) by answering the FP question — "how often was the guard
// wrong?" — that the volume report cannot.
func runFPReport(args []string) int {
	fs := flag.NewFlagSet("fpreport", flag.ContinueOnError)
	days := fs.Int("days", 30, "report window in days (decisions older than this are excluded)")
	top := fs.Int("top", 15, "number of top symbols / repos to show")
	asJSON := fs.Bool("json", false, "machine-readable JSON")
	maxRate := fs.Float64("max-rate", -1, "exit non-zero if the overall approval rate exceeds this fraction (0..1); for CI gating. -1 disables")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}
	if *days <= 0 {
		fmt.Fprintln(os.Stderr, "Usage: runecho-ir fpreport [--days=N] [--top=N] [--json] [--max-rate=F] (--days must be positive)")
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
	stats := guardstats.FPReport(decisions, since, *top)

	if *asJSON {
		out, err := json.MarshalIndent(guardstats.PayloadFP(stats), "", "  ")
		if err != nil {
			return printErr(err)
		}
		fmt.Println(string(out))
	} else {
		fmt.Print(guardstats.FormatFP(stats))
	}

	// CI gate: a rising approval rate means the guard is interrupting more
	// legitimate work — a regression worth failing on. Only meaningful with
	// enough asks to be a rate rather than noise; below that, never gate.
	if *maxRate >= 0 && stats.Window.Asks >= 20 && stats.Window.Rate() > *maxRate {
		fmt.Fprintf(os.Stderr, "\nFAIL: approval rate %.1f%% exceeds --max-rate %.1f%% (%d asks)\n",
			100*stats.Window.Rate(), 100**maxRate, stats.Window.Asks)
		return ExitNoData
	}
	return ExitOK
}
