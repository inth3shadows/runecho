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
// METHOD. Reuses Audit's own VerdictPremature findings unchanged (same
// Oracle, same window, same classify() rules — nothing about the audit itself
// is touched), then for each one walks forward from the ask-time commit to
// HEAD via git log + bisection (assuming definedness is monotonic across the
// window — a spike-quality approximation, not a guarantee: a symbol
// introduced, deleted, then reintroduced would misattribute the LATER
// introduction) to find the commit that first satisfies Defined, and buckets
// (that commit's date) - (the ask's timestamp).
//
// KNOWN GAP, surfaced by this spike rather than hidden: Decision (declog.go)
// carries no session or task id, so "resolved within the same agent session"
// cannot be measured directly — only elapsed wall-clock time can. A short
// elapsed time is consistent with same-session resolution but is not proof of
// it; a long one is stronger evidence the agent's task had already ended.
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
	var resolved, unresolved, anomalies int
	var examples []string
	var elapsedList []time.Duration

	for _, f := range premature {
		root, rel, err := oracle.Worktree(f.File)
		if err != nil {
			unresolved++
			continue
		}
		revAt, err := oracle.RevAt(root, f.TS)
		if err != nil {
			unresolved++
			continue
		}
		head, err := oracle.Head(root)
		if err != nil {
			unresolved++
			continue
		}
		sha, definedAt, ok := findDefiningCommit(oracle, root, rel, f.Lang, f.Symbol, revAt, head)
		if !ok {
			unresolved++
			continue
		}
		elapsed := definedAt.Sub(f.TS)
		if elapsed < 0 {
			// The defining commit predates the ask — inconsistent with classify()
			// having already proven "not defined at ask time" via the same Oracle.
			// Likely a pathspec/worktree mismatch between the two calls rather
			// than a real time-travel; logged, not silently bucketed.
			anomalies++
			continue
		}
		resolved++
		elapsedList = append(elapsedList, elapsed)
		counts[bucketFor(elapsed)]++
		if len(examples) < 10 {
			examples = append(examples, fmt.Sprintf("%s %s: asked %s -> defined %s (%v later) @ %s",
				f.File, f.Symbol, f.TS.Format(time.RFC3339), definedAt.Format(time.RFC3339), elapsed.Round(time.Second), sha[:12]))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n=== premature-latency spike ===\n")
	fmt.Fprintf(&b, "premature findings: %d total, %d resolved (defining commit found), %d unresolved (oracle/git could not answer), %d anomalies (defined-before-ask)\n",
		len(premature), resolved, unresolved, anomalies)
	if resolved > 0 {
		order := []string{"<2min", "<30min", "<1h", "<1day", "<1week", ">=1week"}
		for _, k := range order {
			if n := counts[k]; n > 0 {
				fmt.Fprintf(&b, "  %-8s %4d  %5.1f%%\n", k, n, 100*float64(n)/float64(resolved))
			}
		}
		sort.Slice(elapsedList, func(i, j int) bool { return elapsedList[i] < elapsedList[j] })
		fmt.Fprintf(&b, "median elapsed: %v\n", elapsedList[len(elapsedList)/2].Round(time.Second))
	}
	fmt.Fprintf(&b, "KNOWN GAP: elapsed wall-clock only — Decision carries no session id, so this is a proxy for \"resolved within the same task,\" not proof of it.\n")
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
	case elapsed < 24*time.Hour:
		return "<1day"
	case elapsed < 7*24*time.Hour:
		return "<1week"
	default:
		return ">=1week"
	}
}

// findDefiningCommit bisects the commit range (revAt, head] for the first
// commit where Defined(sym) is true, assuming monotonic definedness across
// the range (see file header). Returns ok=false if the walk cannot be
// completed (no commits in range, a git error, or head itself disagrees with
// Audit's own already-proven "defined at head" verdict — a same-run
// consistency check, not a new claim).
func findDefiningCommit(o GitOracle, root, rel, lang, sym, revAt, head string) (sha string, definedAt time.Time, ok bool) {
	out, err := o.git(root, "log", "--reverse", "--format=%H%x09%cI", revAt+".."+head)
	if err != nil {
		return "", time.Time{}, false
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
		if err != nil {
			continue
		}
		commits = append(commits, commitInfo{sha: parts[0], ts: ts})
	}
	if len(commits) == 0 {
		return "", time.Time{}, false
	}

	hi := len(commits) - 1
	if defined, err := o.Defined(root, commits[hi].sha, lang, sym, rel); err != nil || !defined {
		return "", time.Time{}, false // disagrees with Audit's own head verdict — skip rather than guess
	}
	lo := 0
	for lo < hi {
		mid := (lo + hi) / 2
		defined, err := o.Defined(root, commits[mid].sha, lang, sym, rel)
		if err != nil {
			lo = mid + 1 // treat an oracle error as "not yet defined", search later half
			continue
		}
		if defined {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return commits[lo].sha, commits[lo].ts, true
}
