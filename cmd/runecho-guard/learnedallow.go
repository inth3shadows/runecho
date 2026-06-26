package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// C3 learned-allow: per-repo symbols the user has approved enough times that the
// guard stops asking about them. The whole feature is gated behind
// RUNECHO_GUARD_LEARN=1 — when unset, nothing here runs and no store file is
// ever written (the enriched decision log still accumulates the raw approvals,
// so counts can be rebuilt later if the gate is flipped on).
//
// Design notes:
//   - The store lives next to decisions.jsonl in RUNECHO_HOME, NOT in the repo's
//     .runechoguardignore, so a PostToolUse hook never silently mutates a
//     git-tracked working-tree file.
//   - Every entry carries last_seen so trust DECAYS: an entry not re-approved
//     within the TTL is pruned, turning a one-way ratchet into a sliding window.
//     Without this, a symbol approved once and later deleted would stay allowed
//     forever — exactly the stale blind spot the guard exists to catch.
//   - Pruning happens on the WRITE path (recordApprovals, fired from PostToolUse,
//     off the decision-latency budget). The READ path (learnedAllowedSet, fired
//     from the PreToolUse hot loop) only filters and never writes, so it stays
//     cheap.

const (
	// learnedThresholdDefault is the approval count at which a symbol is trusted.
	// Override with RUNECHO_GUARD_LEARN_N. Defaults low (2) so the feature actually
	// fires at single-user dogfood edit volume; raise once trusted.
	learnedThresholdDefault = 2
	// learnedTTLDaysDefault is how many days an entry survives without being
	// re-approved. Override with RUNECHO_GUARD_LEARN_TTL_DAYS.
	learnedTTLDaysDefault = 14
)

// learnEnabled reports whether the learned-allow feature is active. Default OFF
// (honors the dogfood gate on issue #9 and "guard never auto-allows reflexively").
func learnEnabled() bool { return os.Getenv("RUNECHO_GUARD_LEARN") == "1" }

// learnedThreshold returns the approval count required to trust a symbol,
// honoring RUNECHO_GUARD_LEARN_N (must be a positive integer, else the default).
func learnedThreshold() int {
	if v := os.Getenv("RUNECHO_GUARD_LEARN_N"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return learnedThresholdDefault
}

// learnedTTL returns the decay window, honoring RUNECHO_GUARD_LEARN_TTL_DAYS
// (must be a positive integer number of days, else the default).
func learnedTTL() time.Duration {
	if v := os.Getenv("RUNECHO_GUARD_LEARN_TTL_DAYS"); v != "" {
		if d, err := strconv.Atoi(v); err == nil && d > 0 {
			return time.Duration(d) * 24 * time.Hour
		}
	}
	return learnedTTLDaysDefault * 24 * time.Hour
}

// learnedAllowFile is the store filename inside RUNECHO_HOME.
const learnedAllowFile = "learned-allow.json"

// learnedEntry is the per-symbol record: how many times approved and when last
// seen (for decay).
type learnedEntry struct {
	Count    int    `json:"count"`
	LastSeen string `json:"last_seen"` // RFC3339 UTC
}

// learnedAllow is the on-disk store: repo name -> symbol -> entry.
type learnedAllow struct {
	V     int                                `json:"v"`
	Repos map[string]map[string]learnedEntry `json:"repos"`
}

// loadLearnedAllow reads the store from dir. Fail-open: a missing or corrupt
// file yields an empty (usable) store, never an error — a bad store must not
// alter guard behavior.
func loadLearnedAllow(dir string) learnedAllow {
	la := learnedAllow{V: 1, Repos: map[string]map[string]learnedEntry{}}
	b, err := os.ReadFile(filepath.Join(dir, learnedAllowFile))
	if err != nil {
		return la
	}
	var parsed learnedAllow
	if json.Unmarshal(b, &parsed) != nil || parsed.Repos == nil {
		return la
	}
	parsed.V = 1
	return parsed
}

// saveLearnedAllow writes the store atomically (temp file + rename) so a crashed
// or concurrent write can never leave a half-written, unparseable file. Fail-open:
// any error is silently discarded — persistence is best-effort.
func saveLearnedAllow(dir string, la learnedAllow) {
	la.V = 1
	b, err := json.Marshal(la)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, learnedAllowFile+".*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, learnedAllowFile)); err != nil {
		_ = os.Remove(tmpPath)
	}
}

// recordApprovals folds one approval event (repo + the just-approved symbols)
// into the store: increment each symbol's count and stamp last_seen=now. It also
// prunes entries that have decayed past the TTL — doing it here keeps the prune
// on the PostToolUse write path, off the PreToolUse decision-latency budget.
//
// No-op when the feature is disabled or repo/symbols are empty, so no store file
// appears while the gate is off.
func recordApprovals(dir, repo string, symbols []string, now time.Time) {
	if !learnEnabled() || repo == "" || len(symbols) == 0 {
		return
	}
	// Serialize the whole load-modify-save under a cross-process advisory lock:
	// two PostToolUse hooks firing concurrently would otherwise each load, each
	// increment, and the second save would clobber the first's increments
	// (last-writer-wins). The atomic rename in saveLearnedAllow prevents a torn
	// file but not lost updates — only the lock closes that window.
	withFileLock(filepath.Join(dir, learnedAllowFile+".lock"), func() {
		la := loadLearnedAllow(dir)
		if la.Repos == nil {
			la.Repos = map[string]map[string]learnedEntry{}
		}
		bucket := la.Repos[repo]
		if bucket == nil {
			bucket = map[string]learnedEntry{}
			la.Repos[repo] = bucket
		}
		nowStr := now.UTC().Format(time.RFC3339)
		for _, s := range symbols {
			if s == "" {
				continue
			}
			e := bucket[s]
			e.Count++
			e.LastSeen = nowStr
			bucket[s] = e
		}
		pruneLearnedAllow(&la, now)
		saveLearnedAllow(dir, la)
	})
}

// pruneLearnedAllow drops entries whose last_seen is older than the TTL (or
// unparseable), and removes any repo bucket left empty. Mutates la in place.
func pruneLearnedAllow(la *learnedAllow, now time.Time) {
	cutoff := now.UTC().Add(-learnedTTL())
	for repo, bucket := range la.Repos {
		for sym, e := range bucket {
			ts, err := time.Parse(time.RFC3339, e.LastSeen)
			if err != nil || ts.Before(cutoff) {
				delete(bucket, sym)
			}
		}
		if len(bucket) == 0 {
			delete(la.Repos, repo)
		}
	}
}

// learnedAllowedSet returns the symbols currently trusted for repo: count at or
// above the threshold AND last_seen within the TTL. It is READ-ONLY (no prune,
// no write) so the PreToolUse hot path stays cheap; stale entries are filtered
// out here and physically removed later by recordApprovals.
//
// Returns an empty set when the feature is disabled, so callers can merge it
// unconditionally.
func learnedAllowedSet(dir, repo string, now time.Time) map[string]struct{} {
	out := map[string]struct{}{}
	if !learnEnabled() || repo == "" {
		return out
	}
	la := loadLearnedAllow(dir)
	bucket := la.Repos[repo]
	if bucket == nil {
		return out
	}
	threshold := learnedThreshold()
	cutoff := now.UTC().Add(-learnedTTL())
	for sym, e := range bucket {
		if e.Count < threshold {
			continue
		}
		ts, err := time.Parse(time.RFC3339, e.LastSeen)
		if err != nil || ts.Before(cutoff) {
			continue
		}
		out[sym] = struct{}{}
	}
	return out
}
