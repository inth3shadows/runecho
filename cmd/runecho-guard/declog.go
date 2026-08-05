package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/inth3shadows/runecho/internal/store"
	"github.com/inth3shadows/runecho/internal/version"
)

// decisionRecord is one JSONL line appended to decisions.jsonl.
// mode distinguishes Claude Code hook fires ("hook") from git pre-commit fires
// ("precommit"). decision is "ask" or "defer" in both modes — pre-commit blocks
// instead of asking, but keeping the same two-value enum lets log consumers
// correlate ask-rate across surfaces without schema forks.
// symbols is only populated on ask (the flagged symbol names, across all ask
// categories: hallucination violations, dangling, dropped-import, duplicate).
//
// learnSymbols is the HALLUCINATION-ORIGIN subset of symbols — the only names an
// approval may fold into the learned-allow store (recordApprovals). It exists
// because the learned-allow set feeds guard.Run's hallucination known-set: a name
// approved from a dangling/dropped/duplicate ask does NOT mean "this reference
// legitimately resolves," so training the hallucination check on it would blind
// the guard to a later genuine hallucination of that same name. Only violations
// carry that "name resolves" meaning, so only they populate this field.
//
// GV is the guard BINARY version that wrote the record — distinct from V, which
// is the record SCHEMA version and has always been 1. Without it, an aggregate
// over any window longer than the gap between two installs silently pools the
// behaviour of different programs: measured on the real log, a 30-day window
// reported a 70% approval rate while the trailing 2 days reported 19%, because
// the installed binary had been six releases stale (#207). Records written
// before this field existed carry no version and are reported as "unknown"
// rather than being attributed to whatever is installed now.
type decisionRecord struct {
	V            int      `json:"v"`
	GV           string   `json:"gv,omitempty"`
	TS           string   `json:"ts"`
	Mode         string   `json:"mode"`
	Repo         string   `json:"repo,omitempty"`
	File         string   `json:"file,omitempty"`
	Lang         string   `json:"lang,omitempty"`
	Decision     string   `json:"decision"`
	Reason       string   `json:"reason"`
	Symbols      []string `json:"symbols,omitempty"`
	LearnSymbols []string `json:"learn_symbols,omitempty"`
	// contract/contractHash are set only on an edit-scope contract ask (#12 D2).
	// The hash is the contract's content hash AT ACTIVATION, not its hash now:
	// that is what makes an ask replayable against the exact text that produced
	// it, and a contract edited mid-session would otherwise leave a log entry
	// nothing can be checked against. Deliberately NOT carried onto the outcome
	// record by logOutcomeForFile — approving an out-of-scope edit says the edit
	// was fine, never that the scope should widen, and there is no learned-allow
	// analogue here on purpose (a scope that trains itself wider is not a scope).
	Contract     string `json:"contract,omitempty"`
	ContractHash string `json:"contract_hash,omitempty"`
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
	// Always overwrite: the writing binary's own version is the only value that
	// can be true here, so a caller-supplied GV would only ever be wrong.
	// Canonical so an install.sh build (v0.17.4) and a goreleaser build (0.17.4)
	// of the SAME release stamp one label, not two (#233).
	rec.GV = version.Canonical(version.Version)
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format(time.RFC3339)
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return
	}

	path := filepath.Join(dir, "decisions.jsonl")
	// 0600: the decision log records repo paths, filenames, and symbol names;
	// keep it owner-only (defense-in-depth alongside the 0700 store dir).
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
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
// The dedupe below is a read-check-write across two calls, and logDecision is
// deliberately lock-free O_APPEND — so without serialization two hooks firing
// for the SAME edit can both read "no outcome yet" and both write one. Measured
// at 5 duplicates in 30 trials with two concurrent --outcome-mode processes,
// which is exactly the user-settings + project-settings wiring.
//
// Same primitive and same reasoning as recordApprovals one function below. The
// two locks are distinct files and only this function takes both, always in this
// order, so they cannot deadlock. recordApprovals is called AFTER the lock is
// released, and only when this process actually wrote the record — otherwise a
// suppressed duplicate would still train learned-allow, which is the half of the
// double-count that matters most (it halves the effective approval threshold).
//
// Honest scope: store.WithFileLock is fail-open (runs unlocked if the lock can't
// be taken) and a no-op on non-Unix. So this closes the race on Unix and remains
// best-effort elsewhere — it is not an absolute guarantee, and the comments and
// docs must not claim one.
func logOutcomeForFile(file string) {
	dir, err := runechoDir()
	if err != nil {
		return
	}

	var ask decisionRecord
	var wrote bool
	store.WithFileLock(filepath.Join(dir, "decisions.jsonl.lock"), func() {
		rec, ok := recentUnrecordedAsk(filepath.Join(dir, "decisions.jsonl"), file)
		if !ok {
			return
		}
		logDecision(decisionRecord{
			Mode:         "hook",
			Repo:         rec.Repo,
			File:         file,
			Lang:         rec.Lang,
			Decision:     "outcome",
			Reason:       "approved",
			Symbols:      rec.Symbols,
			LearnSymbols: rec.LearnSymbols,
		})
		ask, wrote = rec, true
	})
	if !wrote {
		return
	}

	// Train learned-allow only on the hallucination-origin subset — see the
	// LearnSymbols doc on decisionRecord for why dangling/dropped/duplicate
	// approvals must not populate the hallucination known-set. Records written
	// before this field existed have a nil LearnSymbols, so they simply train
	// nothing (fail-safe: under-trains rather than mis-trains).
	recordApprovals(dir, ask.Repo, ask.LearnSymbols, time.Now())
}

// recentUnrecordedAsk returns the MOST RECENT "ask" record for file in
// decisions.jsonl within the last maxOutcomeAge, and whether one was found that
// has NOT already had an outcome recorded against it. Reads only the tail of the
// file to keep the hot path fast. The full record is returned (not just a bool)
// so callers can copy its Symbols/Repo forward onto the outcome record.
//
// The "unrecorded" half exists because a single edit can fire the PostToolUse
// hook more than once. Claude Code merges hooks from the plugin, the user's
// settings.json and the project's settings.json, and runs EVERY matching entry;
// the plugin invokes `guard.sh --outcome-mode` while a hand-merged snippet
// invokes `runecho-guard --outcome-mode`, so the command strings differ and no
// dedupe upstream is even possible. Both are documented wirings, so a user with
// two of them configured is following instructions, not misconfiguring.
//
// Without this check each fire appends its own outcome record for one real
// approval, and both consumers are corrupted rather than merely noisy:
//
//   - recordApprovals does e.Count++ per fire, so RUNECHO_GUARD_LEARN reaches
//     its two-approval threshold on a SINGLE approval — an auto-allow that
//     trains twice as fast as designed.
//   - fpreport's `consumed` map is keyed per OUTCOME, not per ask (see the note
//     at internal/guardstats/fpreport.go:341), so a surplus outcome is claimed
//     by a later, genuinely-unapproved ask on the same file. The approval rate
//     comes out INFLATED — silently wrong rather than obviously empty, which is
//     worse than the missing-recorder bug that made it empty.
//
// Deduping here rather than in the configs makes double-wiring harmless however
// the user arrived at it. It is deliberately keyed on the same (file, window)
// pair the ask lookup already uses: a second fire for one edit sees the outcome
// the first fire wrote and no-ops.
//
// Ordering is what makes the single pass correct. The log is append-ordered, so
// resetting the flag on each newly-seen ask means an outcome only suppresses the
// ask it FOLLOWS — an outcome for a previous edit to the same file cannot mask a
// genuine new ask.
func recentUnrecordedAsk(path, file string) (decisionRecord, bool) {
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
	// recorded tracks whether an outcome has already been written for `match`.
	// Reset whenever a newer ask supersedes it — see the ordering note above.
	var recorded bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec decisionRecord
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if rec.File != file {
			continue
		}
		switch rec.Decision {
		case "ask":
			ts, err := time.Parse(time.RFC3339, rec.TS)
			if err != nil {
				continue
			}
			if ts.After(cutoff) {
				// Keep scanning: the log is append-ordered, so the last in-window
				// match is the most recent ask for this file.
				match, found, recorded = rec, true, false
			}
		case "outcome":
			// An outcome for this file after the ask above means some other
			// PostToolUse fire already recorded that approval. Not time-filtered
			// on purpose: it only matters that it came AFTER the ask we matched,
			// which its position in the append-ordered log already establishes.
			recorded = true
		}
	}
	if recorded {
		return decisionRecord{}, false
	}
	return match, found
}
