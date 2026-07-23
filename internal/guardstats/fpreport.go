package guardstats

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// OutcomeJoinWindow is how long after an ask an "approved" outcome may arrive
// and still be attributed to it. It matches cmd/runecho-guard's own
// maxOutcomeAge (declog.go): the PostToolUse recorder only writes an approved
// outcome when a matching ask exists within that window, so joining over a
// wider one here would pair an ask with an unrelated later approval.
const OutcomeJoinWindow = 5 * time.Minute

// FPBucket is an ask/approved tally for one grouping key (a reason or a
// language). Approved is the count of asks that were followed by a
// symbol-exact "approved" outcome for the same file within OutcomeJoinWindow.
type FPBucket struct {
	Asks     int `json:"asks"`
	Approved int `json:"approved"`
}

// Rate returns Approved/Asks in [0,1], or 0 when there were no asks.
func (b FPBucket) Rate() float64 {
	if b.Asks == 0 {
		return 0
	}
	return float64(b.Approved) / float64(b.Asks)
}

// UnknownVersion labels asks from a guard that predates the gv stamp (#207).
// It is deliberately not inferred from the timestamp: guessing which binary
// wrote a record and presenting the guess as data is the failure this whole
// breakdown exists to prevent.
const UnknownVersion = "unknown"

// FilterVersion returns only the decisions written by guard version gv.
// UnknownVersion selects records with no version stamp. An empty gv is a no-op
// (returns decisions unchanged), so callers can pass a flag value straight
// through without branching.
//
// Both asks and outcomes are filtered. An ask and its outcome are written by the
// same process in the same session, so they always carry the same version and no
// join is broken by this; the only exception is a binary swapped mid-session,
// where dropping the orphaned half is the correct conservative result.
func FilterVersion(decisions []Decision, gv string) []Decision {
	if gv == "" {
		return decisions
	}
	want := gv
	if want == UnknownVersion {
		want = ""
	}
	out := make([]Decision, 0, len(decisions))
	for _, d := range decisions {
		if d.GV == want {
			out = append(out, d)
		}
	}
	return out
}

// RepoCount is one entry in the loudest-repos ranking.
type RepoCount struct {
	Repo string `json:"repo"`
	Asks int    `json:"asks"`
}

// FPStats is the observed false-positive report over a decision-log window.
//
// The headline number is the APPROVAL RATE: the fraction of the guard's asks
// that the agent then approved anyway. An approved ask means the guard
// interrupted work the user judged legitimate — a false positive from the
// user's standpoint. It is an UPPER BOUND on the true FP rate, not the rate
// itself: some approvals are the user approving a genuine fix to the flagged
// symbol in the same file within the window, not a dismissal of a wrong alarm.
// The complement (asks with no approved outcome) mixes true positives the user
// rejected with asks whose session simply ended before an outcome was recorded.
type FPStats struct {
	Since    time.Time
	Until    time.Time
	Window   FPBucket
	ByReason map[string]FPBucket
	// ByCheck is ByReason with compound reasons split on "+" and each term
	// tallied separately, so it answers "what is THIS check's approve-anyway
	// rate" — which ByReason cannot.
	//
	// ByReason buckets the exact logged string, so one ask that fired two checks
	// lands in its own bucket: "violations", "violations+dangling" and
	// "contract+violations" are three keys, and the hallucination check's real
	// rate is spread across all three. contractReason's own doc note says the
	// arithmetic "has to split on '+' and count terms" — nothing did, and a
	// gating decision (#209) now depends on the contract check's rate in
	// isolation. Deriving it by hand across buckets is exactly the kind of step
	// that gets skipped or done wrong.
	//
	// Both are kept: ByReason shows which checks CO-FIRE (a real signal about
	// noise), ByCheck shows per-check rates. Their ask totals deliberately
	// disagree — a compound ask counts once per term here — so ByCheck must
	// never be summed to a window total.
	ByCheck map[string]FPBucket
	ByLang  map[string]FPBucket
	// ByVersion buckets asks by the guard binary that wrote them (UnknownVersion
	// for records predating the gv stamp). More than one key means the headline
	// rate above pools the behaviour of different programs and must not be read
	// as a measurement of the current guard — see MixedVersions.
	ByVersion    map[string]FPBucket
	TopSymbols   []SymbolCount // symbols on APPROVED asks (the FP suspects), ranked
	LoudestRepos []RepoCount
	// UnmatchedOutcomes counts "approved" outcome records with no ask they could
	// join to in-window. A large value means the log is missing asks (rotated,
	// or written by an older guard that did not stamp symbols) — a caveat on the
	// rates above, surfaced rather than hidden.
	UnmatchedOutcomes int
}

// MixedVersions reports whether the window's asks came from more than one guard
// binary. When true, every pooled figure in the report — the headline rate, the
// by-check and by-language tables, and the most-approved-symbols ranking — is an
// average across different programs, and a fix shipped partway through the window
// is invisible in it.
func (s FPStats) MixedVersions() bool { return len(s.ByVersion) > 1 }

// symbolKey is the join key: the file plus the ask's symbol set, order-
// independent. The outcome recorder copies the ask's symbols forward verbatim,
// so an exact set match on the same file is a precise pairing — far tighter than
// a file-and-time-window guess (the join the plan's 63% was built on, and warned
// against).
func symbolKey(file string, symbols []string) string {
	sorted := append([]string(nil), symbols...)
	sort.Strings(sorted)
	return file + "\x00" + strings.Join(sorted, "\x01")
}

// FPReport joins ask records to their approved outcomes and summarizes the
// observed approval (upper-bound false-positive) rate over decisions at or after
// since. topN caps the flagged-symbol and loudest-repo rankings.
//
// Only hook-mode ask records participate: pre-commit asks block rather than
// prompt, so they have no approve/deny outcome to join to. Records with a
// timestamp before since are dropped first (there is no upper bound — a
// future-dated record from clock skew is kept, and moves Until).
func FPReport(decisions []Decision, since time.Time, topN int) FPStats {
	s := FPStats{
		Since:     since,
		ByReason:  map[string]FPBucket{},
		ByCheck:   map[string]FPBucket{},
		ByLang:    map[string]FPBucket{},
		ByVersion: map[string]FPBucket{},
	}

	// Index approved outcomes by join key. A file may see the same symbol set
	// approved more than once over a long window; keep every outcome timestamp so
	// an ask matches one at or after itself, within the window.
	//
	// Only an outcome whose reason is "approved" counts. The guard writes exactly
	// that today (declog.go), but decisions.jsonl is a user-writable file, so a
	// hand-edited or future non-approved "outcome" must not silently inflate the
	// approval rate. A record with no join signal (empty symbol set) is skipped:
	// symbolKey("",nil) would otherwise collide every symbol-less ask and outcome
	// on the same/empty file into one false pairing (see UnmatchedOutcomes' note
	// about pre-symbol-stamping guards).
	type stamp = time.Time
	approvedByKey := map[string][]stamp{}
	var asks []Decision
	for _, d := range decisions {
		if d.TS.Before(since) {
			continue
		}
		if d.TS.After(s.Until) {
			s.Until = d.TS
		}
		switch d.Decision {
		case "ask":
			if d.Mode != "hook" {
				continue // pre-commit asks block; no outcome to join
			}
			if len(d.Symbols) == 0 {
				continue // no join signal — would collide with other symbol-less records
			}
			asks = append(asks, d)
		case "outcome":
			if d.Reason != "approved" || len(d.Symbols) == 0 {
				continue
			}
			k := symbolKey(d.File, d.Symbols)
			approvedByKey[k] = append(approvedByKey[k], d.TS)
		}
	}
	for k := range approvedByKey {
		sort.Slice(approvedByKey[k], func(i, j int) bool {
			return approvedByKey[k][i].Before(approvedByKey[k][j])
		})
	}

	// Match asks in ASCENDING timestamp order. matchOutcome greedily takes the
	// earliest unconsumed outcome at or after an ask, which is optimal only when
	// asks are processed oldest-first. decisions.jsonl is append-ordered in
	// practice, but concurrent worktree writers or clock skew can interleave it,
	// and an out-of-order ask could otherwise steal the outcome an earlier ask
	// needed — undercounting approvals. Sorting removes that dependence on input
	// order. Stable so equal-timestamp asks keep their log order.
	sort.SliceStable(asks, func(i, j int) bool {
		return asks[i].TS.Before(asks[j].TS)
	})

	// consumed marks an (key,index) outcome already paired, so two asks with the
	// same file+symbols don't both claim one approval.
	consumed := map[string]map[int]bool{}
	usedOutcomes := 0
	approvedSymbolFreq := map[string]int{}
	repoAsks := map[string]int{}

	for _, a := range asks {
		s.Window.Asks++
		reasonBucket := s.ByReason[a.Reason]
		reasonBucket.Asks++
		// One ask can fire several checks; askReason joins them with "+".
		// Tally each term so a per-check rate exists without hand arithmetic.
		checks := splitChecks(a.Reason)
		for _, c := range checks {
			bkt := s.ByCheck[c]
			bkt.Asks++
			s.ByCheck[c] = bkt
		}
		langBucket := s.ByLang[a.Lang]
		langBucket.Asks++
		gv := a.GV
		if gv == "" {
			gv = UnknownVersion
		}
		verBucket := s.ByVersion[gv]
		verBucket.Asks++
		repoAsks[a.Repo]++

		k := symbolKey(a.File, a.Symbols)
		if idx := matchOutcome(approvedByKey[k], consumed[k], a.TS); idx >= 0 {
			if consumed[k] == nil {
				consumed[k] = map[int]bool{}
			}
			consumed[k][idx] = true
			usedOutcomes++
			s.Window.Approved++
			reasonBucket.Approved++
			for _, c := range checks {
				bkt := s.ByCheck[c]
				bkt.Approved++
				s.ByCheck[c] = bkt
			}
			langBucket.Approved++
			verBucket.Approved++
			for _, sym := range a.Symbols {
				approvedSymbolFreq[sym]++
			}
		}
		s.ByReason[a.Reason] = reasonBucket
		s.ByLang[a.Lang] = langBucket
		s.ByVersion[gv] = verBucket
	}

	// Any approved outcome not consumed by an ask had no in-window ask to pair to.
	totalOutcomes := 0
	for _, stamps := range approvedByKey {
		totalOutcomes += len(stamps)
	}
	s.UnmatchedOutcomes = totalOutcomes - usedOutcomes

	s.TopSymbols = topSymbolCounts(approvedSymbolFreq, topN)
	s.LoudestRepos = topRepoCounts(repoAsks, topN)
	// No in-window record leaves Until at its zero value, which renders/marshals
	// as a date before Since (an inverted window). Pin it to Since so an empty
	// report shows a zero-width window rather than a nonsensical one.
	if s.Until.Before(since) {
		s.Until = since
	}
	return s
}

// matchOutcome returns the index of the earliest unconsumed outcome stamp that
// is at or after askTS and STRICTLY within OutcomeJoinWindow of it, or -1 if
// none. stamps is sorted ascending. The bound is strict (< window, not <=) to
// mirror the recorder: cmd/runecho-guard writes an outcome only when now-ask <
// maxOutcomeAge (declog.go's ts.After(cutoff)), so it never emits a record at
// exactly the window edge, and the join must not admit one either.
func matchOutcome(stamps []time.Time, used map[int]bool, askTS time.Time) int {
	for i, ts := range stamps {
		if used[i] {
			continue
		}
		if ts.Before(askTS) {
			continue
		}
		if ts.Sub(askTS) >= OutcomeJoinWindow {
			break // sorted: no later stamp is closer
		}
		return i
	}
	return -1
}

func topSymbolCounts(freq map[string]int, topN int) []SymbolCount {
	out := make([]SymbolCount, 0, len(freq))
	for name, c := range freq {
		out = append(out, SymbolCount{Name: name, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return out
}

func topRepoCounts(freq map[string]int, topN int) []RepoCount {
	out := make([]RepoCount, 0, len(freq))
	for repo, c := range freq {
		out = append(out, RepoCount{Repo: repo, Asks: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Asks != out[j].Asks {
			return out[i].Asks > out[j].Asks
		}
		return out[i].Repo < out[j].Repo
	})
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return out
}

// FormatFP renders an FPStats as a human-readable report.
func FormatFP(s FPStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Guard false-positive report (%s → %s)\n",
		s.Since.UTC().Format("2006-01-02"), s.Until.UTC().Format("2006-01-02"))
	fmt.Fprintf(&b, "\nApproval rate = asks the agent approved anyway ÷ asks. An upper bound\n")
	fmt.Fprintf(&b, "on the false-positive rate (some approvals are genuine fixes, not dismissals).\n\n")

	fmt.Fprintf(&b, "Overall: %d ask(s), %d approved  →  %.0f%% approval rate\n",
		s.Window.Asks, s.Window.Approved, 100*s.Window.Rate())

	if s.Window.Asks == 0 {
		fmt.Fprintf(&b, "\nNo hook-mode asks in window.\n")
		return b.String()
	}

	// Placed immediately under the headline, before any breakdown: a reader who
	// stops after the first number must still see that the number is pooled.
	if s.MixedVersions() {
		fmt.Fprintf(&b, "\n!! MIXED GUARD VERSIONS — the rate above pools %d different builds.\n", len(s.ByVersion))
		fmt.Fprintf(&b, "   Every table below is an average across them, so a fix shipped mid-window\n")
		fmt.Fprintf(&b, "   is invisible here. Re-run with --gv=<version> before acting on any of it.\n")
	}

	fmt.Fprintf(&b, "\nBy guard version:\n")
	for _, gv := range sortedFPKeys(s.ByVersion) {
		bkt := s.ByVersion[gv]
		fmt.Fprintf(&b, "  %-14s %4d ask  %4d approved  %5.0f%%\n",
			gv, bkt.Asks, bkt.Approved, 100*bkt.Rate())
	}

	fmt.Fprintf(&b, "\nBy check (reason):\n")
	for _, reason := range sortedFPKeys(s.ByReason) {
		bkt := s.ByReason[reason]
		fmt.Fprintf(&b, "  %-28s %4d ask  %4d approved  %5.0f%%\n",
			reason, bkt.Asks, bkt.Approved, 100*bkt.Rate())
	}

	fmt.Fprintf(&b, "\nBy check (split, one ask counted once per check it fired):\n")
	for _, c := range sortedFPKeys(s.ByCheck) {
		bkt := s.ByCheck[c]
		fmt.Fprintf(&b, "  %-28s %4d ask  %4d approved  %5.0f%%\n",
			c, bkt.Asks, bkt.Approved, 100*bkt.Rate())
	}

	fmt.Fprintf(&b, "\nBy language:\n")
	for _, lang := range sortedFPKeys(s.ByLang) {
		bkt := s.ByLang[lang]
		name := lang
		if name == "" {
			name = "(none)"
		}
		fmt.Fprintf(&b, "  %-10s %4d ask  %4d approved  %5.0f%%\n",
			name, bkt.Asks, bkt.Approved, 100*bkt.Rate())
	}

	if len(s.TopSymbols) > 0 {
		fmt.Fprintf(&b, "\nMost-approved symbols (false-positive suspects):\n")
		for _, sc := range s.TopSymbols {
			fmt.Fprintf(&b, "  %4d  %s\n", sc.Count, sc.Name)
		}
	}

	if len(s.LoudestRepos) > 0 {
		fmt.Fprintf(&b, "\nLoudest repos (by ask count):\n")
		for _, rc := range s.LoudestRepos {
			fmt.Fprintf(&b, "  %4d  %s\n", rc.Asks, rc.Repo)
		}
	}

	if s.UnmatchedOutcomes > 0 {
		fmt.Fprintf(&b, "\nNote: %d approved outcome(s) had no matching ask in-window — the\n",
			s.UnmatchedOutcomes)
		fmt.Fprintf(&b, "log may be missing asks (rotated, or from a guard that did not stamp symbols).\n")
	}
	return b.String()
}

func sortedFPKeys(m map[string]FPBucket) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]].Asks != m[keys[j]].Asks {
			return m[keys[i]].Asks > m[keys[j]].Asks
		}
		return keys[i] < keys[j]
	})
	return keys
}

// PayloadFP renders an FPStats as a JSON-serializable map (parity with Payload).
func PayloadFP(s FPStats) map[string]any {
	reasons := make([]map[string]any, 0, len(s.ByReason))
	for _, r := range sortedFPKeys(s.ByReason) {
		b := s.ByReason[r]
		reasons = append(reasons, map[string]any{
			"reason": r, "asks": b.Asks, "approved": b.Approved, "rate": b.Rate(),
		})
	}
	checks := make([]map[string]any, 0, len(s.ByCheck))
	for _, c := range sortedFPKeys(s.ByCheck) {
		b := s.ByCheck[c]
		checks = append(checks, map[string]any{
			"check": c, "asks": b.Asks, "approved": b.Approved, "rate": b.Rate(),
		})
	}
	langs := make([]map[string]any, 0, len(s.ByLang))
	for _, l := range sortedFPKeys(s.ByLang) {
		b := s.ByLang[l]
		langs = append(langs, map[string]any{
			"lang": l, "asks": b.Asks, "approved": b.Approved, "rate": b.Rate(),
		})
	}
	versions := make([]map[string]any, 0, len(s.ByVersion))
	for _, v := range sortedFPKeys(s.ByVersion) {
		b := s.ByVersion[v]
		versions = append(versions, map[string]any{
			"gv": v, "asks": b.Asks, "approved": b.Approved, "rate": b.Rate(),
		})
	}
	topSymbols := s.TopSymbols
	if topSymbols == nil {
		topSymbols = []SymbolCount{}
	}
	loudest := s.LoudestRepos
	if loudest == nil {
		loudest = []RepoCount{}
	}
	return map[string]any{
		"since":              s.Since,
		"until":              s.Until,
		"overall":            map[string]any{"asks": s.Window.Asks, "approved": s.Window.Approved, "rate": s.Window.Rate()},
		"by_reason":          reasons,
		"by_check":           checks,
		"by_lang":            langs,
		"by_version":         versions,
		"mixed_versions":     s.MixedVersions(),
		"top_symbols":        topSymbols,
		"loudest_repos":      loudest,
		"unmatched_outcomes": s.UnmatchedOutcomes,
	}
}

// splitChecks breaks a logged ask reason into the individual checks that fired.
// askReason (cmd/runecho-guard/dangling.go) joins them with "+", and
// contractReason prefixes "contract+", so "contract+violations+dangling" is
// three checks, not one exotic one.
//
// Empty terms are dropped rather than becoming a "" bucket: a malformed reason
// should lose a term, never invent a nameless check that shows up in a report
// someone is about to make a decision from.
func splitChecks(reason string) []string {
	if reason == "" {
		return nil
	}
	parts := strings.Split(reason, "+")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
