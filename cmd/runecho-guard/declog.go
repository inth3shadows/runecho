package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// decisionRecord is one JSONL line appended to decisions.jsonl.
// mode distinguishes Claude Code hook fires ("hook") from git pre-commit fires
// ("precommit"). decision is "ask" or "defer" in both modes — pre-commit blocks
// instead of asking, but keeping the same two-value enum lets log consumers
// correlate ask-rate across surfaces without schema forks.
// symbols is only populated on ask (the violating symbol names).
type decisionRecord struct {
	V        int      `json:"v"`
	TS       string   `json:"ts"`
	Mode     string   `json:"mode"`
	Repo     string   `json:"repo,omitempty"`
	File     string   `json:"file,omitempty"`
	Lang     string   `json:"lang,omitempty"`
	Decision string   `json:"decision"`
	Reason   string   `json:"reason"`
	Symbols  []string `json:"symbols,omitempty"`
}

// logDecision appends one JSONL line to <storeDir>/decisions.jsonl.
//
// Why fail-open and why after the response: the log is observability, not
// correctness. A write error (disk full, bad RUNECHO_HOME, permission) must
// never alter the hook's decision, output, or exit code. Callers write their
// JSON response to out (or to stderr for pre-commit) before calling logDecision,
// so the append cannot touch the latency budget of the decision itself.
// All errors from this function are silently discarded by design.
func logDecision(rec decisionRecord) {
	dir, err := runechoDir()
	if err != nil {
		return
	}
	rec.V = 1
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format(time.RFC3339)
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return
	}

	path := filepath.Join(dir, "decisions.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	// O_APPEND single-write: on Linux, a write(2) to an O_APPEND regular file is
	// atomic with no documented byte ceiling (PIPE_BUF is a pipe/FIFO guarantee,
	// not a regular-file one); it holds in practice for single-page JSONL records
	// like these. No additional locking is needed for this use case.
	_, _ = f.Write(append(line, '\n'))
}

// e6debug appends one E6 auto-refresh trace record to decisions.jsonl, but only
// when RUNECHO_DEBUG=1. The E6 refresh path (refreshIRForFile) is otherwise
// fully silent and fail-open by design, which makes it un-dogfoodable: "no
// complaints" is indistinguishable from "never ran" or "failed silently every
// time". This opt-in trace records which branch the refresh took (refreshed,
// unchanged, no-repo, or a specific failure token) so a dogfood session can
// confirm the feature actually fires and rolls the auto snapshot. It is gated
// (not always-on) so the normal hot path writes nothing extra and the decision
// log stays clean for its primary consumer (the C3 learned-allow analysis).
// outcome is a short token, not free text, so the log stays greppable.
func e6debug(file, outcome string) {
	if os.Getenv("RUNECHO_DEBUG") != "1" {
		return
	}
	logDecision(decisionRecord{Mode: "e6", File: file, Decision: "refresh", Reason: outcome})
}

const (
	maxOutcomeAge       = 5 * time.Minute
	maxOutcomeReadBytes = int64(64 * 1024) // 64 KiB — covers ~500 recent entries
)

// logOutcomeForFile appends an approved-outcome record if a recent "ask"
// entry exists for file in decisions.jsonl (within maxOutcomeAge). No-ops
// silently when no matching ask is found or on any I/O error.
//
// C3 enrichment: the ask record carries the violating Symbols (and Repo); copy
// them forward onto the outcome record so a later analysis (or recordApprovals
// below) can attribute the approval to specific symbols without re-joining the
// log. When the learned-allow feature is enabled, the approval is also folded
// into the per-repo learned-allow store.
func logOutcomeForFile(file string) {
	dir, err := runechoDir()
	if err != nil {
		return
	}
	ask, ok := recentAsk(filepath.Join(dir, "decisions.jsonl"), file)
	if !ok {
		return
	}
	logDecision(decisionRecord{
		Mode:     "hook",
		Repo:     ask.Repo,
		File:     file,
		Lang:     ask.Lang,
		Decision: "outcome",
		Reason:   "approved",
		Symbols:  ask.Symbols,
	})
	recordApprovals(dir, ask.Repo, ask.Symbols, time.Now())
}

// recentAsk returns the MOST RECENT "ask" record for file in decisions.jsonl
// within the last maxOutcomeAge (and whether one was found). Reads only the tail
// of the file to keep the hot path fast. The full record is returned (not just a
// bool) so callers can copy its Symbols/Repo forward onto the outcome record.
func recentAsk(path, file string) (decisionRecord, bool) {
	f, err := os.Open(path)
	if err != nil {
		return decisionRecord{}, false
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return decisionRecord{}, false
	}
	offset := stat.Size() - maxOutcomeReadBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return decisionRecord{}, false
	}

	cutoff := time.Now().UTC().Add(-maxOutcomeAge)
	var match decisionRecord
	var found bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec decisionRecord
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if rec.Decision != "ask" || rec.File != file {
			continue
		}
		ts, err := time.Parse(time.RFC3339, rec.TS)
		if err != nil {
			continue
		}
		if ts.After(cutoff) {
			// Keep scanning: the log is append-ordered, so the last in-window
			// match is the most recent ask for this file.
			match, found = rec, true
		}
	}
	return match, found
}
