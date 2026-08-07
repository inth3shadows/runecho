// Spike: how long does a VerdictPremature finding stay wrong?
//
// WHY THIS EXISTS. Every seat on an adversarial review of the "phase-aware
// pending-proof enforcement" proposal (deferring premature asks to a
// Stop/pre-commit recheck instead of interrupting immediately) independently
// flagged the same gap: fpaudit.go's 32.7%-premature share (see
// TestGoResolveFalseNegativesAgainstCompiler's sibling, the compiler-oracle
// differential this file borrows its spirit from) says HOW OFTEN a finding
// was premature, never HOW LONG it stayed premature. A finding resolved 90
// seconds later by the same edit sequence is a non-problem an agent barely
// notices; one resolved three days later in an unrelated commit is a real
// interruption cost. Building a deferral mechanism before knowing which of
// those two pictures is true is guessing at the one parameter — the deferral
// window — that decides whether the feature works at all.
//
// METHOD, v2 — corrected after a real measurement artifact. v1 walked
// revAt..head on a single branch (effectively `master`) via bisection. That
// is wrong for a repo that squash-merges PRs (this one does, routinely):
// a squash-merge creates a BRAND-NEW commit at merge time, so the "defining
// commit" v1 found was usually the merge, not the original fix — the
// bucketed elapsed time was measuring PR review-to-merge latency, not agent
// fix latency. Caught via the same discipline KB principle #71 names: check
// whether the measurement instrument shares state with the system under
// test. Here it did — the same squash-merge process runs on both sides.
//
// v2 instead searches `git log --all` (every reachable ref, not just one
// branch — a still-live short-lived claudew/codexw feature branch is a
// separate ref and IS included) for the EARLIEST commit BY AUTHOR DATE (not
// committer date, which squash resets to merge time) where Defined flips
// true, scanning candidates oldest-first and stopping at the first hit. This
// is a linear scan, not a bisection: monotonicity cannot be assumed across
// branches the way it can within one. Capped at maxLatencyScan commits per
// finding to bound worst-case runtime; a finding that hits the cap is
// reported as capped, not silently folded into "unresolved".
//
// RESIDUAL LIMITATION, not solved by v2: once a feature branch is deleted
// (this repo's own aftercare flow offers exactly that after a merge) and its
// commits are garbage-collected, only the squash-merge commit remains
// reachable at all — v2 then falls back to the same merge-latency number v1
// always produced, correctly but unavoidably. The reported bucket counts
// should be read as a mix of "true fix latency" (branch still alive) and
// "merge latency" (branch gone), not a clean measurement of either.
//
// KNOWN GAP, unrelated to the above and still unresolved: Decision
// (declog.go) carries no session or task id, so "resolved within the same
// agent session" cannot be measured directly even with a correct commit
// timestamp — only elapsed wall-clock time can. Given this project's own
// workflow (short claudew/codexw sessions, often well under an hour), two
// SEPARATE short sessions on the same repo can easily land inside the same
// elapsed-time bucket as one continuous session would — so even a clean
// timestamp does not by itself prove same-task self-correction.
//
// Env-gated, zero CI impact, reads real ~/.runecho/decisions.jsonl.
package guardstats

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSpikePrematureLatency(t *testing.T) {
	if os.Getenv("RUNECHO_SPIKE_PREMATURE_LATENCY") != "1" {
		t.Skip("set RUNECHO_SPIKE_PREMATURE_LATENCY=1 to run the premature-latency spike against a real decision log")
	}

	logPath := os.Getenv("RUNECHO_SPIKE_DECISIONS_LOG")
	if logPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("resolve home dir: %v", err)
		}
		logPath = filepath.Join(home, ".runecho", "decisions.jsonl")
	}
	decisions, err := Load(logPath)
	if err != nil {
		t.Skipf("no decision log at %s: %v", logPath, err)
	}

	days := 30
	if v := os.Getenv("RUNECHO_SPIKE_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("RUNECHO_SPIKE_DAYS must be a positive integer, got %q", v)
		}
		days = n
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	oracle := GitOracle{}
	stats := Audit(decisions, since, oracle)

	var premature []AuditFinding
	for _, f := range stats.Findings {
		if f.Verdict == VerdictPremature {
			premature = append(premature, f)
		}
	}
	t.Logf("window=%dd asks=%d symbols=%d rated=%d premature=%d (%.1f%% of rated)",
		days, stats.Asks, stats.Symbols, stats.Rated(), len(premature), 100*stats.Share(VerdictPremature))
	if len(premature) == 0 {
		t.Skip("no premature findings in this window")
	}

	counts := map[string]int{}
	var resolved, unresolved, capped int
	var examples []string
	var elapsedList []time.Duration

	for _, f := range premature {
		root, rel, err := oracle.Worktree(f.File)
		if err != nil {
			unresolved++
			continue
		}
		sha, definedAt, status := findDefiningCommit(oracle, root, rel, f.Lang, f.Symbol, f.TS)
		switch status {
		case scanCapped:
			capped++
			continue
		case scanNotFound:
			unresolved++
			continue
		}
		elapsed := definedAt.Sub(f.TS)
		resolved++
		elapsedList = append(elapsedList, elapsed)
		counts[bucketFor(elapsed)]++
		if len(examples) < 10 {
			examples = append(examples, fmt.Sprintf("%s %s: asked %s -> defined %s (%v later) @ %s",
				f.File, f.Symbol, f.TS.Format(time.RFC3339), definedAt.Format(time.RFC3339), elapsed.Round(time.Second), sha[:12]))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n=== premature-latency spike (v2: author-date, all-refs) ===\n")
	fmt.Fprintf(&b, "premature findings: %d total, %d resolved, %d unresolved (no matching commit found on any live ref), %d capped (scan limit hit)\n",
		len(premature), resolved, unresolved, capped)
	if resolved > 0 {
		order := []string{"<2min", "<30min", "<1h", "<5h", "<1day", "<1week", ">=1week"}
		for _, k := range order {
			if n := counts[k]; n > 0 {
				fmt.Fprintf(&b, "  %-8s %4d  %5.1f%%\n", k, n, 100*float64(n)/float64(resolved))
			}
		}
		ls := latencyStats(elapsedList)
		fmt.Fprintf(&b, "elapsed: min=%s median=%s mean=%s max=%s (n=%d)\n",
			formatLatency(ls.MinS), formatLatency(ls.MedianS), formatLatency(ls.MeanS), formatLatency(ls.MaxS), ls.N)

		within1h, within5h := 0, 0
		for _, e := range elapsedList {
			if e <= time.Hour {
				within1h++
			}
			if e <= 5*time.Hour {
				within5h++
			}
		}
		fmt.Fprintf(&b, "practical-session proxy: resolved within 1h = %d/%d (%.1f%%), within 5h = %d/%d (%.1f%%)\n",
			within1h, resolved, 100*float64(within1h)/float64(resolved),
			within5h, resolved, 100*float64(within5h)/float64(resolved))
	}
	fmt.Fprintf(&b, "KNOWN GAP: elapsed wall-clock only — Decision carries no session id, so this is a proxy for \"resolved within the same task,\" not proof of it. Two separate short sessions can land in the same bucket as one continuous one.\n")
	fmt.Fprintf(&b, "RESIDUAL LIMITATION: once a feature branch is deleted post-merge, only the squash-merge commit remains reachable, so its entry reflects merge latency, not fix latency — see file header.\n")
	for _, e := range examples {
		fmt.Fprintf(&b, "  %s\n", e)
	}
	t.Log(b.String())
}

func bucketFor(elapsed time.Duration) string {
	switch {
	case elapsed < 2*time.Minute:
		return "<2min"
	case elapsed < 30*time.Minute:
		return "<30min"
	case elapsed < time.Hour:
		return "<1h"
	case elapsed < 5*time.Hour:
		return "<5h"
	case elapsed < 24*time.Hour:
		return "<1day"
	case elapsed < 7*24*time.Hour:
		return "<1week"
	default:
		return ">=1week"
	}
}

// maxLatencyScan bounds how many candidate commits findDefiningCommit will
// call Defined against for one finding. A repo with many active branches
// since askTS can otherwise turn one finding into hundreds of `git grep`
// calls; this is a spike, not a service, so a finding that would need more
// than this is reported as capped rather than left to run unbounded.
const maxLatencyScan = 500

type scanStatus int

const (
	scanFound scanStatus = iota
	scanNotFound
	scanCapped
)

// findDefiningCommit searches every commit reachable from ANY ref (`git log
// --all`) authored at or after askTS, oldest AUTHOR DATE first, for the
// first one where Defined(sym) is true. Author date, not committer date,
// because a squash-merge stamps committer date at merge time regardless of
// when the original fix was written — see the file header for why this
// replaced a single-branch bisection. --all rather than one branch so a
// still-live short-lived feature branch (a separate ref) is searched too,
// not just whatever root's default branch happens to be.
//
// This is a linear scan, not a bisection: with commits drawn from multiple
// branches, "defined" is not guaranteed monotonic across the scan order the
// way it is within one branch's own history.
func findDefiningCommit(o GitOracle, root, rel, lang, sym string, askTS time.Time) (sha string, definedAt time.Time, status scanStatus) {
	// No --since here, deliberately: git's --since prunes the graph walk based
	// on an assumption of chronological order down each branch, and can stop
	// early (silently dropping a later-dated commit) when a descendant has an
	// earlier committer date than an ancestor on the same path — reproduced in
	// a scratch repo during review. The date filter below is the sole
	// correctness gate; --since would only have been a prefetch optimization,
	// and an incorrect one.
	out, err := o.git(root, "log", "--all", "--format=%H%x09%aI")
	if err != nil {
		return "", time.Time{}, scanNotFound
	}
	type commitInfo struct {
		sha string
		ts  time.Time
	}
	var commits []commitInfo
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		ts, err := time.Parse(time.RFC3339, parts[1])
		if err != nil || ts.Before(askTS) {
			continue // the only date filter — see the no-`--since` note above
		}
		commits = append(commits, commitInfo{sha: parts[0], ts: ts})
	}
	if len(commits) == 0 {
		return "", time.Time{}, scanNotFound
	}
	sort.Slice(commits, func(i, j int) bool { return commits[i].ts.Before(commits[j].ts) })

	limit := len(commits)
	wasCapped := false
	if limit > maxLatencyScan {
		limit = maxLatencyScan
		wasCapped = true
	}
	for i := 0; i < limit; i++ {
		defined, err := o.Defined(root, commits[i].sha, lang, sym, rel)
		if err != nil {
			continue // this commit's answer is unavailable; keep scanning forward
		}
		if defined {
			return commits[i].sha, commits[i].ts, scanFound
		}
	}
	if wasCapped {
		return "", time.Time{}, scanCapped
	}
	return "", time.Time{}, scanNotFound
}
