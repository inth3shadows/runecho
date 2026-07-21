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

// gateMinAsks is the smallest ask count at which --max-rate will gate. Below it
// the "rate" is noise (one approval swings it by whole percentage points), so the
// gate is skipped — with a stderr note, never silently, so a low-traffic repo is
// told its gate did not run rather than believing it passed.
const gateMinAsks = 20

// maxReportDays caps --days. time.Duration(days)*24h overflows int64 near
// ~106752 days and wraps negative, which would push `since` into the future and
// silently report zero records; reject well before that.
const maxReportDays = 36500 // ~100 years

// runFPReport reports the guard's observed false-positive rate from
// decisions.jsonl: the fraction of asks the agent approved anyway, joined
// symbol-exact to their outcomes, broken down by check and language, with the
// most-approved symbols and loudest repos. Complements guard-stats (which
// reports ask VOLUME) by answering the FP question — "how often was the guard
// wrong?" — that the volume report cannot.
//
// Exit codes are chosen so a CI gate is unambiguous: ExitOK(0) = passed (or no
// gate), ExitNoData(1) = no log / nothing to gate (a fresh checkout — CI should
// SKIP, not fail), ExitError(2) = a tripped gate OR a bad flag (CI should FAIL).
// The gate deliberately does NOT reuse ExitNoData, so "rate exceeded" is never
// confused with "no data".
func runFPReport(args []string) int {
	fs := flag.NewFlagSet("fpreport", flag.ContinueOnError)
	days := fs.Int("days", 30, "report window in days (1..36500)")
	top := fs.Int("top", 15, "number of top symbols / repos to show (must be >= 1)")
	asJSON := fs.Bool("json", false, "machine-readable JSON")
	maxRate := fs.Float64("max-rate", -1, "FAIL (exit 2) if approval rate exceeds this fraction 0..1; needs >=20 asks; -1 disables")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}
	if *days <= 0 || *days > maxReportDays {
		fmt.Fprintf(os.Stderr, "fpreport: --days must be between 1 and %d\n", maxReportDays)
		return ExitError
	}
	if *top < 1 {
		fmt.Fprintln(os.Stderr, "fpreport: --top must be >= 1")
		return ExitError
	}
	// -1 is the "disabled" sentinel; any other value must be a real fraction. A
	// user who writes --max-rate=5 meaning "5%" would otherwise get a gate that
	// can never trip (Rate() is bounded to [0,1]) and false confidence.
	if *maxRate != -1 && (*maxRate < 0 || *maxRate > 1) {
		fmt.Fprintln(os.Stderr, "fpreport: --max-rate must be a fraction in [0,1] (or -1 to disable)")
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

	gateOn := *maxRate >= 0
	gateEligible := gateOn && stats.Window.Asks >= gateMinAsks
	tripped := gateEligible && stats.Window.Rate() > *maxRate

	if *asJSON {
		payload := guardstats.PayloadFP(stats)
		if gateOn {
			payload["gate"] = map[string]any{
				"max_rate":  *maxRate,
				"asks":      stats.Window.Asks,
				"min_asks":  gateMinAsks,
				"evaluated": gateEligible,
				"tripped":   tripped,
			}
		}
		out, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return printErr(err)
		}
		fmt.Println(string(out))
	} else {
		fmt.Print(guardstats.FormatFP(stats))
	}

	// CI gate. A rising approval rate means the guard is interrupting more
	// legitimate work — a regression worth failing on. Gate messages go to stderr
	// so a --json stdout stays clean for a `| jq` pipeline (which also reads the
	// gate result from the "gate" object above, not just the exit code).
	if gateOn && !gateEligible {
		fmt.Fprintf(os.Stderr,
			"\nfpreport: gate skipped — %d ask(s), need %d to evaluate --max-rate\n",
			stats.Window.Asks, gateMinAsks)
	}
	if tripped {
		fmt.Fprintf(os.Stderr, "\nFAIL: approval rate %.1f%% exceeds --max-rate %.1f%% (%d asks)\n",
			100*stats.Window.Rate(), 100**maxRate, stats.Window.Asks)
		return ExitError
	}
	return ExitOK
}
