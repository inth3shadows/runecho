package main

import (
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/inth3shadows/runecho/internal/doctor"
)

// The scheduled reindex rotates its own log (#554). The cron line and the
// LaunchAgent plist redirect both periodic jobs into
// $RUNECHO_HOME/logs/reindex.log and nothing else ever bounded it: one box
// reached 43 MB in 83 days. logrotate/newsyslog would be a per-OS install
// step; the job itself is already on every install that has the log.
//
// How it works: the scheduler opens the log (cron `>>`, launchd
// StandardOutPath) BEFORE the process starts, so renaming it at start is safe —
// this run's fd follows the inode into reindex.log.1, and the next spawn
// creates a fresh reindex.log. Until then there is no active log, and the
// newest freshen line may be in .1: under cron freshen runs 30 minutes later,
// but the launchd agents share StartInterval 3600 with no offset, so freshen can
// even be mid-run on the old inode. doctor's freshen check falls back to .1.
//
// Only the reindex job rotates: it writes >99.9% of the bytes and is the entry
// every `install --periodic` writes (freshen exists only on from-source
// installs). It rotates only when its stdout IS the log (os.SameFile), so an
// interactive `repo reindex --all`, or any custom redirect, never renames a
// file. The same check is repeated right before the rename, so a run that lost
// a race backs off instead of renaming a freshly created log over the .1 just
// rotated; only a recreation inside that last microsecond can still slip by.

// reindexLogCap is the size above which the scheduled reindex rotates its log.
// One generation at 10 MiB keeps ~20 MiB on disk and, at ~10 KB an hour after
// #554's missing-root summary, weeks of history. A var only so tests can lower
// it.
var reindexLogCap int64 = 10 << 20

// Seams for tests.
var (
	rotateGOOS = runtime.GOOS
	rotateHook = func() {} // runs between the checks and the rename; tests interleave a racer here
)

type rotateOutcome int

const (
	rotateNotOurLog      rotateOutcome = iota // stdout is not the log, the log is absent, or a stat failed
	rotateBelowCap                            // size <= cap
	rotateRefusedSymlink                      // the log or its .1 is a symlink
	rotateRenameFailed                        // fail-open: the job runs on, writing to the old file
	rotateUnsupportedOS                       // windows
	rotated
)

// rotateJobLog renames logPath to logPath+doctor.RotatedLogSuffix when out is
// logPath's file and it is larger than capBytes, replacing any older rotated
// file. It never creates a file and never renames through or onto a symlink.
// Every failure returns an outcome (refusals and rename failures also print one
// Warning line to warn); the caller carries on regardless.
func rotateJobLog(logPath string, out *os.File, capBytes int64, warn io.Writer) rotateOutcome {
	// Windows refuses to rename a file another handle (our own stdout) has open
	// without FILE_SHARE_DELETE; freshen skips Windows for the same family of
	// reasons.
	if rotateGOOS == "windows" {
		return rotateUnsupportedOS
	}
	outInfo, err := out.Stat()
	if err != nil {
		return rotateNotOurLog
	}
	// Stat, not Lstat, for identity: a symlinked log then matches stdout and
	// reaches the refusal below, which says so, instead of silently reading as
	// "not ours".
	logInfo, err := os.Stat(logPath)
	if err != nil || !os.SameFile(outInfo, logInfo) {
		return rotateNotOurLog
	}
	// Below the cap there is nothing to do, and nothing to refuse: a symlinked
	// log is only worth a warning once it actually needs rotating.
	if outInfo.Size() <= capBytes {
		return rotateBelowCap
	}
	if li, err := os.Lstat(logPath); err != nil {
		return rotateNotOurLog
	} else if li.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintf(warn, "Warning: not rotating %s: it is a symlink\n", logPath)
		return rotateRefusedSymlink
	}
	rotatedPath := logPath + doctor.RotatedLogSuffix
	if ri, err := os.Lstat(rotatedPath); err == nil && ri.Mode()&os.ModeSymlink != 0 {
		fmt.Fprintf(warn, "Warning: not rotating %s: %s is a symlink\n", logPath, rotatedPath)
		return rotateRefusedSymlink
	}
	rotateHook()
	// Re-check: another run may have rotated since the checks above, and the
	// scheduler may already have created a fresh log at logPath. Renaming that
	// over .1 would discard the generation just rotated.
	if cur, err := os.Stat(logPath); err != nil || !os.SameFile(outInfo, cur) {
		return rotateNotOurLog
	}
	if err := os.Rename(logPath, rotatedPath); err != nil {
		fmt.Fprintf(warn, "Warning: could not rotate %s: %v\n", logPath, err)
		return rotateRenameFailed
	}
	return rotated
}

// rotateScheduledLog rotates this process's $RUNECHO_HOME/logs/reindex.log
// when stdout is that file. If the job runs with a different $RUNECHO_HOME than
// install time, it misses — but such a job is already reading a different
// store, so its log barely grows.
func rotateScheduledLog() {
	dir, err := runechoDir()
	if err != nil {
		return
	}
	_ = rotateJobLog(reindexLogFile(dir), os.Stdout, reindexLogCap, os.Stderr)
}
