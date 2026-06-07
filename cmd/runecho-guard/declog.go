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

	// O_APPEND single-write: on Linux, a write to an O_APPEND file is atomic
	// up to PIPE_BUF (4096 bytes). A JSONL record is well under that limit.
	// No additional locking is needed for this use case.
	_, _ = f.Write(append(line, '\n'))
}

const (
	maxOutcomeAge       = 5 * time.Minute
	maxOutcomeReadBytes = int64(64 * 1024) // 64 KiB — covers ~500 recent entries
)

// logOutcomeForFile appends an approved-outcome record if a recent "ask"
// entry exists for file in decisions.jsonl (within maxOutcomeAge). No-ops
// silently when no matching ask is found or on any I/O error.
func logOutcomeForFile(file string) {
	dir, err := runechoDir()
	if err != nil {
		return
	}
	if !recentAskExists(filepath.Join(dir, "decisions.jsonl"), file) {
		return
	}
	logDecision(decisionRecord{Mode: "hook", File: file, Decision: "outcome", Reason: "approved"})
}

// recentAskExists reports whether decisions.jsonl contains an "ask" entry for
// file within the last maxOutcomeAge. Reads only the tail of the file to keep
// the hot path fast.
func recentAskExists(path, file string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return false
	}
	offset := stat.Size() - maxOutcomeReadBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false
	}

	cutoff := time.Now().UTC().Add(-maxOutcomeAge)
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
			return true
		}
	}
	return false
}
