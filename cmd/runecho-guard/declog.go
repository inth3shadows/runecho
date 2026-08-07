package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	// Edit is a fingerprint of the tool call's edit content (see
	// editFingerprint), stamped on ask records so the matching PostToolUse
	// outcome can be joined precisely instead of by a (file, time-window) guess
	// — see #300 and the join-basis comment on recentUnrecordedAsk. Empty on
	// records written by older guards and on pre-commit asks, which have no
	// outcome to join.
	Edit string `json:"edit,omitempty"`
	// Join records which track matched an outcome to its ask: "edit" (fingerprint,
	// precise) or "window" (file+time fallback, the pre-#300 behavior). Outcome
	// records only. This is a diagnostic, not a policy input — its only purpose is
	// making a silent regression to the fallback path observable via
	// `jq -r 'select(.decision=="outcome")|.join'` rather than invisible.
	Join string `json:"join,omitempty"`
}

// editFingerprint returns a 12-hex-character fingerprint of a tool call's edit
// content, hashing the extracted hookEdit fields rather than raw JSON bytes so
// the same edit fingerprints identically whether it was parsed from the
// PreToolUse payload (ask side) or the differently-shaped PostToolUse payload
// (outcome side) — key order and whitespace are not stable across the two.
// Empty ToolName (no edit content at all, e.g. a pre-commit ask) returns "".
func editFingerprint(e hookEdit) string {
	if e.ToolName == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(e.ToolName))
	h.Write([]byte{0})
	h.Write([]byte(e.OldString))
	h.Write([]byte{0})
	h.Write([]byte(e.NewString))
	h.Write([]byte{0})
	h.Write([]byte(e.Content))
	h.Write([]byte{0})
	for _, op := range e.Edits {
		h.Write([]byte(op.OldString))
		h.Write([]byte{0})
		h.Write([]byte(op.NewString))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
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
	maxOutcomeAge = 5 * time.Minute
	// maxKeyedOutcomeAge bounds the fingerprint-join track (#300). It exists only
	// to bound hash reuse (an agent re-issuing a byte-identical edit days apart);
	// the fingerprint itself carries the precision, unlike the window track's
	// (file, time) guess. 24h is a judgment call, not a measured optimum: the one
	// real case that motivated this (#300) was 1h45m, and the ask-to-outcome
	// latency distribution observed on the live log is censored at the OLD
	// maxOutcomeAge-derived read window, so any number derived from current data
	// is a lower bound by construction. Keep this equal to
	// guardstats.KeyedOutcomeJoinWindow — declog_window_test.go asserts it.
	maxKeyedOutcomeAge = 24 * time.Hour
	// maxOutcomeReadBytes bounds how far back recentUnrecordedAsk reads. 1 MiB is
	// ~4,700 records — hours at peak logged volume, days at median — versus the
	// old 64 KiB (~500 records, ~15 min at peak: too narrow to ever reach a
	// maxKeyedOutcomeAge-old ask on a busy log). A busy-enough log can still miss
	// an ask older than this window; that is now a bounded, documented gap
	// instead of a silent 5-minute cliff.
	maxOutcomeReadBytes = int64(1 << 20)
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
func logOutcomeForFile(file, editHash string) {
	dir, err := runechoDir()
	if err != nil {
		return
	}

	var ask decisionRecord
	var wrote bool
	store.WithFileLock(filepath.Join(dir, "decisions.jsonl.lock"), func() {
		rec, join, ok := recentUnrecordedAsk(filepath.Join(dir, "decisions.jsonl"), file, editHash)
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
			Edit:         editHash,
			Join:         join,
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
// decisions.jsonl that has NOT already had an outcome recorded against it, plus
// which join basis found it ("edit" or "window", for the Join field). Reads
// only the tail of the file to keep the hot path fast. The full record is
// returned (not just a bool) so callers can copy its Symbols/Repo forward onto
// the outcome record.
//
// Two join tracks run in the same pass, and the hash track always wins when it
// finds anything (#300):
//
//   - hash track — the latest ask whose Edit fingerprint equals editHash,
//     within maxKeyedOutcomeAge. Precise: it identifies THIS edit regardless of
//     how long the human took to decide, which is the whole point — a
//     5-minute cutoff was silently discarding every outcome recorded after a
//     considered (rather than reflex) approval.
//   - window track — the pre-#300 behavior unchanged: latest ask for this file
//     within maxOutcomeAge, no fingerprint involved. This is what a record
//     written by an older guard (no Edit field) or a caller with no editHash
//     (editHash == "") falls back to.
//
// The "unrecorded" half of each track exists because a single edit can fire the
// PostToolUse hook more than once. Claude Code merges hooks from the plugin,
// the user's settings.json and the project's settings.json, and runs EVERY
// matching entry; the plugin invokes `guard.sh --outcome-mode` while a
// hand-merged snippet invokes `runecho-guard --outcome-mode`, so the command
// strings differ and no dedupe upstream is even possible. Both are documented
// wirings, so a user with two of them configured is following instructions,
// not misconfiguring.
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
// The hash track's "already recorded" check accepts an outcome whose Edit is
// editHash OR empty as proof this ask was already answered — an unattributable
// outcome (older guard, or a caller that passed no editHash) must be assumed to
// be ours. The failure direction that matters is under-recording (a lost
// outcome, same as before #300), never double-counting recordApprovals.
//
// Ordering is what makes the single pass correct for each track independently.
// The log is append-ordered, so resetting a track's recorded flag on each
// newly-seen matching ask means an outcome only suppresses the ask it FOLLOWS —
// an outcome for a previous edit cannot mask a genuine new ask.
func recentUnrecordedAsk(path, file, editHash string) (rec decisionRecord, join string, found bool) {
	f, err := os.Open(path)
	if err != nil {
		return decisionRecord{}, "", false
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return decisionRecord{}, "", false
	}
	offset := stat.Size() - maxOutcomeReadBytes
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return decisionRecord{}, "", false
	}

	windowCutoff := time.Now().UTC().Add(-maxOutcomeAge)
	keyedCutoff := time.Now().UTC().Add(-maxKeyedOutcomeAge)

	var hashMatch, winMatch decisionRecord
	var hashFound, winFound, hashRecorded, winRecorded bool

	// needle is the JSON-encoded form of file, so the prefilter below matches
	// exactly what rec.File would decode to without unmarshalling every line —
	// this repo's decision log runs to hundreds of records at peak volume per
	// hour, and most lines are for other files.
	needleBytes, _ := json.Marshal(file)
	needle := string(needleBytes)

	r := bufio.NewReader(f)
	for {
		// bufio.Reader.ReadString, not bufio.Scanner: a single oversized line
		// (a pre-commit ask can carry hundreds of symbols) makes Scanner.Scan
		// return false forever and silently truncates every record after it —
		// see internal/guardstats/guardstats.go for the same fix applied there,
		// and docs/focus-hunt-2026-07-04-candidate-bugs.md for the hazard. The
		// 16x wider read window here makes hitting it 16x likelier, so it must
		// be fixed in the same change that widens maxOutcomeReadBytes.
		line, readErr := r.ReadString('\n')
		if len(line) > 0 && !strings.Contains(line, needle) {
			if readErr != nil {
				break
			}
			continue
		}
		var cur decisionRecord
		if len(line) > 0 && json.Unmarshal([]byte(line), &cur) == nil && cur.File == file {
			switch cur.Decision {
			case "ask":
				ts, tsErr := time.Parse(time.RFC3339, cur.TS)
				if tsErr == nil {
					if editHash != "" && cur.Edit == editHash && ts.After(keyedCutoff) {
						hashMatch, hashFound, hashRecorded = cur, true, false
					}
					if ts.After(windowCutoff) {
						winMatch, winFound, winRecorded = cur, true, false
					}
				}
			case "outcome":
				if editHash != "" && (cur.Edit == editHash || cur.Edit == "") {
					hashRecorded = true
				}
				winRecorded = true
			}
		}
		if readErr != nil {
			break
		}
	}

	if hashFound {
		if hashRecorded {
			return decisionRecord{}, "", false
		}
		return hashMatch, "edit", true
	}
	if winRecorded {
		return decisionRecord{}, "", false
	}
	return winMatch, "window", winFound
}
