package guardstats

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/version"
)

// OutcomeJoinWindow is how long after an ask an "approved" outcome may arrive
// and still be attributed to it. It matches cmd/runecho-guard's own
// maxOutcomeAge (declog.go): the PostToolUse recorder only writes an approved
// outcome when a matching ask exists within that window, so joining over a
// wider one here would pair an ask with an unrelated later approval.
const OutcomeJoinWindow = 5 * time.Minute

// KeyedOutcomeJoinWindow bounds the edit-fingerprint join track (#300): an
// outcome whose Edit field matches an ask's Edit field is joined to it directly,
// bypassing the symbol+time-window guess OutcomeJoinWindow exists for. It is
// wider than OutcomeJoinWindow on purpose — the fingerprint already carries the
// precision, so this only bounds fingerprint REUSE (an agent re-issuing a
// byte-identical edit long after the first), not human deliberation time, which
// is exactly the term OutcomeJoinWindow was silently truncating.
//
// Must equal cmd/runecho-guard's maxKeyedOutcomeAge (declog.go) —
// cmd/runecho-guard/declog_window_test.go asserts the two stay in sync.
const KeyedOutcomeJoinWindow = 24 * time.Hour

// FPBucket is an ask/approved tally for one grouping key (a reason or a
// language). Approved is the count of asks that were followed by a
// symbol-exact "approved" outcome for the same file within OutcomeJoinWindow.
//
// Unrated counts asks in this bucket that carry NO symbols and therefore have no
// join key to any outcome (#254). They are real asks — the guard interrupted
// real work — but no approve-anyway verdict is computable for them, so they are
// deliberately kept OUT of Asks and out of Rate(). Before #254 they were dropped
// entirely, which let a bucket print a confident rate over a fraction of its own
// asks with nothing saying so. Reading Rate() without reading Unrated is reading
// a subset.
type FPBucket struct {
	Asks     int `json:"asks"`
	Approved int `json:"approved"`
	Unrated  int `json:"unrated"`
	// Latency is ask→approval latency over this bucket's MATCHED asks only
	// (the Approved count, not Asks — an ask with no approved outcome
	// contributes no sample). See LatencyStats.
	Latency LatencyStats `json:"latency"`
}

// LatencyStats summarizes ask→approval latency for one bucket, in seconds.
// It exists to measure the "friction" term of #316's value model
// (value = P(error) × (recovery_cost − next_turn_cost) − friction): how long
// an ask sat before the matching approval, as a proxy for how much it
// actually slowed the session down.
//
// Zero value (N==0) means no matched ask in this bucket — the same
// "nothing to report" convention FPBucket.Rate() uses for Asks==0. This is
// latency CONDITIONED ON approval: it says nothing about asks that were
// denied, abandoned, or whose session ended before an outcome was recorded.
type LatencyStats struct {
	N       int     `json:"n"`
	MinS    float64 `json:"min_s"`
	MedianS float64 `json:"median_s"`
	MeanS   float64 `json:"mean_s"`
	MaxS    float64 `json:"max_s"`
}

// latencyStats computes LatencyStats over ask→approval durations. It sorts a
// copy and does not mutate durs. An empty input is a valid, zero-value result
// — not an error — matching latencyStats' callers, which never have a sample
// for a bucket with zero matched asks.
func latencyStats(durs []time.Duration) LatencyStats {
	if len(durs) == 0 {
		return LatencyStats{}
	}
	sorted := append([]time.Duration(nil), durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}
	return LatencyStats{
		N:       len(sorted),
		MinS:    sorted[0].Seconds(),
		MedianS: median.Seconds(),
		MeanS:   (sum / time.Duration(len(sorted))).Seconds(),
		MaxS:    sorted[len(sorted)-1].Seconds(),
	}
}

// formatLatency renders a seconds figure as a compact human string (e.g.
// "2m14s"), matching time.Duration's own String(). LatencyStats stores
// float64 seconds rather than a Duration so it marshals to JSON directly;
// this is the one place that needs the Duration formatting back.
func formatLatency(seconds float64) string {
	return time.Duration(seconds * float64(time.Second)).Round(time.Second).String()
}

// Rate returns Approved/Asks in [0,1], or 0 when there were no asks. It is a
// rate over RATEABLE asks only — see Coverage.
func (b FPBucket) Rate() float64 {
	if b.Asks == 0 {
		return 0
	}
	return float64(b.Approved) / float64(b.Asks)
}

// Total is every ask in the bucket, rateable or not. It is the honest volume
// figure; Asks alone understates how often this check fired.
func (b FPBucket) Total() int { return b.Asks + b.Unrated }

// Coverage is the fraction of this bucket's asks that Rate() is computed over,
// in [0,1]. It is 1 when nothing is unrated and 0 when a bucket has no rateable
// ask at all. A low coverage means Rate() describes a non-random subset — the
// asks that happened to co-fire with a symbol-bearing check — and must not be
// quoted as the check's rate.
func (b FPBucket) Coverage() float64 {
	if b.Total() == 0 {
		return 1
	}
	return float64(b.Asks) / float64(b.Total())
}

// unratedCoverageFloor is the coverage below which a bucket's rate is called out
// as unrepresentative rather than merely annotated. Half is not a tuned
// threshold: below it the unseen asks outnumber the seen ones, so the rate is
// a minority report about its own bucket.
const unratedCoverageFloor = 0.5

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
	want := version.Canonical(gv)
	if gv == UnknownVersion {
		want = ""
	}
	out := make([]Decision, 0, len(decisions))
	for _, d := range decisions {
		// Normalize both sides so a --gv in either stamp form matches records
		// written in either form (#233): --gv=0.17.4 selects v0.17.4 records too.
		if version.Canonical(d.GV) == want {
			out = append(out, d)
		}
	}
	return out
}

// RepoCount is one entry in the loudest-repos ranking.
//
// Asks is the RATEABLE ask count, so the field reconciles with overall.asks
// under the same name; Unrated is the rest of the volume, and the ranking is by
// their sum. Folding both into one "asks" would have given the same JSON key two
// different denominators in one document (#254 review).
type RepoCount struct {
	Repo    string `json:"repo"`
	Asks    int    `json:"asks"`
	Unrated int    `json:"unrated"`
}

// Total is every ask the repo raised, rateable or not — the ranking key.
func (r RepoCount) Total() int { return r.Asks + r.Unrated }

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
//
// Not every ask can be rated. An ask with no symbols has nothing to join an
// outcome on, so it lands in each bucket's Unrated and in none of the rates
// (#254). Any report of this struct that quotes Rate() without Coverage() is
// quoting a subset — for the contract check on the author's log, a 19% one.
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
	//
	// A per-check rate is only as good as its coverage. The contract check
	// (#12 D2) fires on a PATH and stamps no symbols, so most of its asks are
	// unrateable and its Rate() is computed over the minority that co-fired with
	// a symbol-bearing check — the opposite of a random sample. Read
	// FPBucket.Coverage before acting on any row here; #209's suppression design
	// and the keep/close call on #12 are the decisions this warning exists for.
	ByCheck map[string]FPBucket
	ByLang  map[string]FPBucket
	// ByVersion buckets asks by the guard binary that wrote them (UnknownVersion
	// for records predating the gv stamp). More than one key with a RATEABLE ask
	// means the headline rate above pools the behaviour of different programs and
	// must not be read as a measurement of the current guard — see MixedVersions,
	// which counts those and not keys: a key holding only unrated asks contributed
	// to no rate and so cannot make one an average.
	ByVersion    map[string]FPBucket
	TopSymbols   []SymbolCount // symbols on APPROVED asks (the FP suspects), ranked
	LoudestRepos []RepoCount
	// UnmatchedOutcomes counts "approved" outcome records with no ask they could
	// join to in-window. Causes, in rough order of likelihood:
	//
	//  1. The outcome recorder pairs on FILE only (declog.go's recentUnrecordedAsk), so a
	//     later tool call on the same file inside maxOutcomeAge re-emits an
	//     approval carrying the earlier ask's symbols. Those extra outcomes never
	//     had a distinct ask.
	//  2. Ask records collapsed as hook re-invocations (#252) release the extra
	//     outcomes their duplicates had claimed.
	//  3. The log really is missing asks — rotated, or written by an older guard
	//     that did not stamp symbols.
	//
	// Only (3) is a data-integrity problem, so a large value is a caveat on the
	// rates above, not by itself evidence of a damaged log.
	UnmatchedOutcomes int
}

// MixedVersions reports whether the window's RATEABLE asks came from more than
// one guard binary. When true, every pooled figure in the report — the headline
// rate, the by-check and by-language tables, and the most-approved-symbols
// ranking — is an average across different programs, and a fix shipped partway
// through the window is invisible in it.
//
// It counts versions with a rateable ask, not map keys. Since #254 an
// unrateable ask also creates a ByVersion bucket, and a bucket that contributed
// nothing to any rate cannot make a rate a pooled average. Counting keys would
// let one symbol-less contract ask from a different build — or a single
// pre-symbol-stamping record, which lands under UnknownVersion — flip this to
// true and, through cmd/runecho-ir's gateEligible, silently skip the --max-rate
// gate on a window whose rate is in fact single-version. It would also print
// "the rate above pools 2 different builds" about a rate drawn from one.
func (s FPStats) MixedVersions() bool {
	n := 0
	for _, b := range s.ByVersion {
		if b.Asks > 0 {
			n++
		}
	}
	return n > 1
}

// approvedOutcome is one physical approved-outcome record. Both join tracks
// (symbol+window and, since #300, edit-fingerprint) index into one flat
// []approvedOutcome slice by position rather than keeping separate per-track
// stamp lists, so a single outcome consumed via either track cannot also be
// claimed by the other — see the shared `consumed` map in FPReport.
type approvedOutcome struct {
	ts   time.Time
	edit string
}

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

// askEventKey identifies the tool call an ask record describes, so repeated
// records written for one call collapse to one event (#252).
//
// Every field that could differ between two genuinely distinct asks is in the
// key. Timestamps are second-resolution as logged, which is what makes this work
// at all: a re-invocation lands in the same second, so identical records pair up
// without a tolerance window to tune. It also bounds the damage — an over-eager
// key would erase real asks, and the only way two distinct events collide here is
// if they share a second, a file, a repo, a language, a guard version, a reason
// AND a symbol set.
//
// Deliberately NOT applied to outcome records. They measured 1.000 records per
// event on the reference log — the recorder is not on the re-invoked path — and
// collapsing them would risk cutting the numerator to fix the denominator.
func askEventKey(d Decision) string {
	return strings.Join([]string{
		d.TS.UTC().Format(time.RFC3339),
		d.Repo,
		d.Lang,
		d.GV,
		d.Reason,
		symbolKey(d.File, d.Symbols),
	}, "\x02")
}

// FPReport joins ask records to their approved outcomes and summarizes the
// observed approval (upper-bound false-positive) rate over decisions at or after
// since. topN caps the flagged-symbol and loudest-repo rankings.
//
// Only hook-mode ask records participate: pre-commit asks block rather than
// prompt, so they have no approve/deny outcome to join to. Records with a
// timestamp before since are dropped first (there is no upper bound — a
// future-dated record from clock skew is kept, and moves Until).
//
// Hook asks with no symbols are counted but never joined — they land in each
// bucket's Unrated and in no rate (#254). Window.Asks is therefore still the
// rateable-ask count and the --max-rate gate's denominator is unchanged by this;
// Window.Total is every ask the guard raised.
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
	// approval rate. A symbol-less OUTCOME is dropped outright: it has no join
	// signal, and unlike a symbol-less ask there is no volume question it could
	// answer — an outcome exists only to be attributed to an ask. Keeping it
	// would collide every symbol-less ask and outcome on one file into a false
	// pairing (see UnmatchedOutcomes' note about pre-symbol-stamping guards).
	//
	// The ask side and the outcome side must stay in this configuration together:
	// counting symbol-less asks (#254) is safe only because no symbol-less
	// outcome is ever indexed for them to pair with. Lifting this drop without
	// giving both sides a real key re-creates exactly that collision.
	var allApproved []approvedOutcome
	approvedByKey := map[string][]int{}  // symbolKey -> indices into allApproved
	approvedByEdit := map[string][]int{} // edit fingerprint -> indices into allApproved
	var asks []Decision
	// unratedAsks are hook asks with no symbols: counted everywhere asks are
	// counted, joined nowhere. Kept in a second slice rather than flagged in
	// `asks` so no later change can accidentally hand one to matchOutcome.
	var unratedAsks []Decision
	eventSeen := map[string]bool{}
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
			// A symbol-less ask has no join signal: symbolKey degenerates to the
			// file, so pairing it against outcomes would collide every symbol-less
			// ask and outcome on that file into one false approval. It is therefore
			// never joined — but it IS counted, in Unrated (#254).
			//
			// This matters because it is not a rare malformed record: the contract
			// check (#12 D2) asks about a PATH, not about identifiers, so EVERY
			// contract-only ask lands here. Measured on the author's log 2026-07-29
			// (--days=90; it is append-only, so re-measure before quoting): 16
			// contract ask EVENTS, of which 13 carry no symbols. Dropping them made
			// `fpreport` print {"check":"contract","asks":3,"approved":0,"rate":0} —
			// "the contract check is never approved anyway", over the 19% of contract
			// asks that happened to co-fire with a symbol check, which is the least
			// representative subset available, presented with no hint that it was a
			// subset at all. Same failure class as #227.
			//
			// Counting-not-joining is the deliberate stopping point, not a first
			// step. Giving contract asks a real `(file, contract)` join was piloted
			// on the reference log before any code was written and rejected: a
			// contract ask has no symbols, so that key collapses to one axis, and
			// the outcome recorder already pairs on file alone (declog.go's
			// recentUnrecordedAsk) with posture always "ask", never "deny". "Edited
			// again" and "approved" become the same event — less completely so since
			// the recorder began skipping an ask that already has an outcome, but the
			// pilot's verdict stands on the collapsed key alone. The pilot bore it out: both
			// ask events from the one genuine dogfood contract read approved, a rate
			// of 100% by construction. It would also have had to forward
			// decisionRecord.Contract onto the outcome record, which declog.go
			// deliberately does not do (a scope that trains itself wider is not a
			// scope). Neither price buys a number worth reading.
			unrated := len(d.Symbols) == 0
			// Collapse re-invocations of one tool call (#252). The agent harness can
			// run the PreToolUse hook more than once for a single Edit/Write, and the
			// guard writes a record each time — byte-identical apart from nothing.
			//
			// This has to happen here, not at the reporting end, because Window.Asks++
			// fires per RECORD: every duplicate enlarges the denominator.
			//
			// It moves the numerator too, and in the same direction — do not read this
			// as a denominator-only fix. `consumed` below is keyed per OUTCOME, not per
			// ask, so N duplicate asks at one timestamp can each claim a DIFFERENT
			// outcome within the match window. Outcomes are plentiful enough for that to
			// bite because the recorder pairs on file only (declog.go's recentUnrecordedAsk), so a
			// later edit to the same file re-emits an approval carrying the earlier
			// symbol set. Collapsing the asks therefore also releases the extra approvals
			// their duplicates had claimed.
			//
			// The denominator falls faster than the numerator, so the net effect is that
			// duplication DEFLATES the reported rate and flatters the guard. Measured on
			// the author's 20,799-record log: 632 asks / 457 approved = 72.3% raw versus
			// 552 / 436 = 79.0% collapsed — a 6.7 point understatement, which the
			// `--max-rate` gate inherits as a bias toward passing.
			//
			// A smaller ask count also shrinks gate ELIGIBILITY: a window near
			// gateMinAsks can drop below it and skip the gate (with the stderr note, not
			// silently). On this log `--days=3` goes 33 asks to 20 — right at the floor.
			//
			// Two genuine edits to the same file inside one second are indistinguishable
			// from a re-invocation and collapse too. That is the right trade: it moves
			// counts slightly, whereas the alternative moves the rate the report exists
			// to state.
			// Symbol-less asks collapse on the same key and for the same reason.
			// #254 is what makes that observable: askEventKey never ran on them
			// before, because the drop above `continue`d first. It is exactly the
			// duplication that matters most here — the reference log has one Write
			// producing three identical contract asks, so an uncollapsed volume
			// figure would treble the contract check's apparent noise.
			if k := askEventKey(d); eventSeen[k] {
				continue
			} else {
				eventSeen[k] = true
			}
			if unrated {
				unratedAsks = append(unratedAsks, d)
				continue
			}
			asks = append(asks, d)
		case "outcome":
			// Same eligibility gate as before #300: a symbol-less outcome (a
			// contract-only approval — see the note above unratedAsks) has no
			// symbolKey signal and is dropped outright, exactly as it always was.
			// It is NOT given an edit-hash entry either, even though it may carry
			// one now: no rateable ask (the only asks matchOutcome ever consults)
			// can share its edit hash by construction — one hook fire emits
			// exactly one ask decision, so a contract-only ask's fingerprint never
			// coincides with a violations ask's. Indexing it would only grow
			// totalOutcomes with an entry nothing can ever match, inflating
			// UnmatchedOutcomes for a population this report was never rating.
			if d.Reason != "approved" || len(d.Symbols) == 0 {
				continue
			}
			idx := len(allApproved)
			allApproved = append(allApproved, approvedOutcome{ts: d.TS, edit: d.Edit})
			k := symbolKey(d.File, d.Symbols)
			approvedByKey[k] = append(approvedByKey[k], idx)
			if d.Edit != "" {
				// Scoped by FILE too, not just the fingerprint: the fingerprint hashes
				// only the tool call's edit content (declog.go's editFingerprint), so
				// two different files patched with byte-identical text — the same
				// one-line fix applied twice, a generated boilerplate block — collide
				// on the same hash. Without the file component here, file A's ask could
				// be credited with file B's approval (and vice versa), corrupting
				// exactly the approve-anyway rate #314's dogfood-gating decisions read.
				// declog.go's own hash track does not need this: it already filters to
				// `cur.File == file` before ever comparing Edit.
				ek := d.File + "\x00" + d.Edit
				approvedByEdit[ek] = append(approvedByEdit[ek], idx)
			}
		}
	}
	for k := range approvedByKey {
		sort.Slice(approvedByKey[k], func(i, j int) bool {
			return allApproved[approvedByKey[k][i]].ts.Before(allApproved[approvedByKey[k][j]].ts)
		})
	}
	for k := range approvedByEdit {
		sort.Slice(approvedByEdit[k], func(i, j int) bool {
			return allApproved[approvedByEdit[k][i]].ts.Before(allApproved[approvedByEdit[k][j]].ts)
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

	// consumed marks an allApproved INDEX already paired, so two asks — whether
	// they matched via the same symbolKey or the edit and window tracks
	// respectively finding the same physical outcome — can't both claim it.
	consumed := map[int]bool{}
	usedOutcomes := 0
	approvedSymbolFreq := map[string]int{}
	repoAsks := map[string]int{}
	repoUnrated := map[string]int{}

	// Raw ask→approval durations per dimension, reduced to LatencyStats after
	// the loop (mirrors every other bucket field: accumulate first, summarize
	// once every match is in). Keyed identically to s.ByReason/ByCheck/ByLang/
	// ByVersion so the merge below is a straight lookup.
	var windowLatency []time.Duration
	reasonLatency := map[string][]time.Duration{}
	checkLatency := map[string][]time.Duration{}
	langLatency := map[string][]time.Duration{}
	verLatency := map[string][]time.Duration{}

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
		gv := version.Canonical(a.GV)
		if gv == "" {
			gv = UnknownVersion
		}
		verBucket := s.ByVersion[gv]
		verBucket.Asks++
		repoAsks[a.Repo]++

		k := symbolKey(a.File, a.Symbols)
		// Edit-fingerprint track first (#300): it identifies THIS edit precisely,
		// however long the human took to decide, so it must win whenever it finds
		// anything. Falling back to the symbol+window guess only when it doesn't —
		// an ask from a pre-#300 guard (no Edit) or whose matching outcome fell
		// outside KeyedOutcomeJoinWindow — keeps every existing pairing exactly as
		// it was.
		matchIdx := -1
		if a.Edit != "" {
			matchIdx = matchOutcome(allApproved, approvedByEdit[a.File+"\x00"+a.Edit], consumed, a.TS, KeyedOutcomeJoinWindow)
		}
		if matchIdx < 0 {
			matchIdx = matchOutcome(allApproved, approvedByKey[k], consumed, a.TS, OutcomeJoinWindow)
		}
		if matchIdx >= 0 {
			consumed[matchIdx] = true
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

			lat := allApproved[matchIdx].ts.Sub(a.TS)
			windowLatency = append(windowLatency, lat)
			reasonLatency[a.Reason] = append(reasonLatency[a.Reason], lat)
			for _, c := range checks {
				checkLatency[c] = append(checkLatency[c], lat)
			}
			langLatency[a.Lang] = append(langLatency[a.Lang], lat)
			verLatency[gv] = append(verLatency[gv], lat)
		}
		s.ByReason[a.Reason] = reasonBucket
		s.ByLang[a.Lang] = langBucket
		s.ByVersion[gv] = verBucket
	}

	s.Window.Latency = latencyStats(windowLatency)
	for reason, durs := range reasonLatency {
		b := s.ByReason[reason]
		b.Latency = latencyStats(durs)
		s.ByReason[reason] = b
	}
	for check, durs := range checkLatency {
		b := s.ByCheck[check]
		b.Latency = latencyStats(durs)
		s.ByCheck[check] = b
	}
	for lang, durs := range langLatency {
		b := s.ByLang[lang]
		b.Latency = latencyStats(durs)
		s.ByLang[lang] = b
	}
	for gv, durs := range verLatency {
		b := s.ByVersion[gv]
		b.Latency = latencyStats(durs)
		s.ByVersion[gv] = b
	}

	// Unrated asks (#254): tallied into every bucket the rateable asks are, but
	// into Unrated rather than Asks, and never offered to matchOutcome. That
	// keeps Rate() and the --max-rate gate's denominator (Window.Asks) meaning
	// exactly what they meant before this change, while making the asks the
	// report cannot rate visible instead of absent.
	//
	// repoAsks counts them: "loudest repos" is a volume question, and answering a
	// volume question by silently discarding real asks is the same defect at a
	// different address.
	for _, a := range unratedAsks {
		s.Window.Unrated++
		reasonBucket := s.ByReason[a.Reason]
		reasonBucket.Unrated++
		s.ByReason[a.Reason] = reasonBucket
		for _, c := range splitChecks(a.Reason) {
			bkt := s.ByCheck[c]
			bkt.Unrated++
			s.ByCheck[c] = bkt
		}
		langBucket := s.ByLang[a.Lang]
		langBucket.Unrated++
		s.ByLang[a.Lang] = langBucket
		gv := version.Canonical(a.GV)
		if gv == "" {
			gv = UnknownVersion
		}
		verBucket := s.ByVersion[gv]
		verBucket.Unrated++
		s.ByVersion[gv] = verBucket
		repoUnrated[a.Repo]++
	}

	// Any approved outcome not consumed by an ask had no in-window ask to pair to.
	// allApproved is the flat, deduplicated population (#300) — summing per-track
	// index lists here would double-count an outcome present in both.
	s.UnmatchedOutcomes = len(allApproved) - usedOutcomes

	s.TopSymbols = topSymbolCounts(approvedSymbolFreq, topN)
	s.LoudestRepos = topRepoCounts(repoAsks, repoUnrated, topN)
	// No in-window record leaves Until at its zero value, which renders/marshals
	// as a date before Since (an inverted window). Pin it to Since so an empty
	// report shows a zero-width window rather than a nonsensical one.
	if s.Until.Before(since) {
		s.Until = since
	}
	return s
}

// matchOutcome returns the allApproved index of the earliest unconsumed
// candidate outcome that is at or after askTS and STRICTLY within window of it,
// or -1 if none. candidates holds indices into all, sorted ascending by
// all[idx].ts. The bound is strict (< window, not <=) to mirror the recorder:
// cmd/runecho-guard writes an outcome only when now-ask < maxOutcomeAge or
// maxKeyedOutcomeAge (declog.go), so it never emits a record at exactly the
// window edge, and the join must not admit one either.
func matchOutcome(all []approvedOutcome, candidates []int, consumed map[int]bool, askTS time.Time, window time.Duration) int {
	for _, idx := range candidates {
		if consumed[idx] {
			continue
		}
		ts := all[idx].ts
		if ts.Before(askTS) {
			continue
		}
		if ts.Sub(askTS) >= window {
			break // sorted: no later candidate is closer
		}
		return idx
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

func topRepoCounts(rated, unrated map[string]int, topN int) []RepoCount {
	repos := map[string]bool{}
	for r := range rated {
		repos[r] = true
	}
	for r := range unrated {
		repos[r] = true
	}
	out := make([]RepoCount, 0, len(repos))
	for repo := range repos {
		out = append(out, RepoCount{Repo: repo, Asks: rated[repo], Unrated: unrated[repo]})
	}
	// Rank on total volume: "loudest" is how often the guard interrupted, and an
	// unrateable interruption is just as loud.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Total() != out[j].Total() {
			return out[i].Total() > out[j].Total()
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

	// The headline obeys the same rule as fpRow: with nothing rateable there is
	// no rate, and printing "0% approval rate" over zero rateable asks reads as
	// "the guard was never approved" — the exact misreading this change exists to
	// stop, one level up from the table it was fixed in.
	if s.Window.Asks == 0 {
		fmt.Fprintf(&b, "Overall: 0 rateable ask(s)  →  no approval rate\n")
	} else {
		fmt.Fprintf(&b, "Overall: %d ask(s), %d approved  →  %.0f%% approval rate\n",
			s.Window.Asks, s.Window.Approved, 100*s.Window.Rate())
		if s.Window.Latency.N > 0 {
			fmt.Fprintf(&b, "         median time to decision: %s (n=%d)\n",
				formatLatency(s.Window.Latency.MedianS), s.Window.Latency.N)
		}
	}

	// Total, not Asks: an unrated ask is still an ask, and a window that consists
	// entirely of them must not report itself as empty (#254).
	if s.Window.Total() == 0 {
		fmt.Fprintf(&b, "\nNo hook-mode asks in window.\n")
		return b.String()
	}

	// Directly under the headline for the same reason as the mixed-versions
	// banner: a reader who stops at the first number has to see what it omits.
	if s.Window.Unrated > 0 {
		// "of the N asks", not "N FURTHER asks": when every ask is unrated there
		// is no prior count for them to be further than.
		fmt.Fprintf(&b, "\n!! %d of the %d ask(s) in this window carry no symbols, so no outcome can be\n",
			s.Window.Unrated, s.Window.Total())
		fmt.Fprintf(&b, "   joined to them and no approve-anyway rate exists for them. They are counted\n")
		fmt.Fprintf(&b, "   as \"unrated\" below and are in NO rate on this page. A row marked ! has under\n")
		fmt.Fprintf(&b, "   half its asks rated — its rate describes a minority of its own bucket.\n")
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
		fpRow(&b, gv, 14, s.ByVersion[gv])
	}

	fmt.Fprintf(&b, "\nBy check (reason):\n")
	for _, reason := range sortedFPKeys(s.ByReason) {
		fpRow(&b, reason, 28, s.ByReason[reason])
	}

	fmt.Fprintf(&b, "\nBy check (split, one ask counted once per check it fired):\n")
	for _, c := range sortedFPKeys(s.ByCheck) {
		fpRow(&b, c, 28, s.ByCheck[c])
	}

	fmt.Fprintf(&b, "\nBy language:\n")
	for _, lang := range sortedFPKeys(s.ByLang) {
		name := lang
		if name == "" {
			name = "(none)"
		}
		fpRow(&b, name, 10, s.ByLang[lang])
	}

	if len(s.TopSymbols) > 0 {
		fmt.Fprintf(&b, "\nMost-approved symbols (false-positive suspects):\n")
		for _, sc := range s.TopSymbols {
			fmt.Fprintf(&b, "  %4d  %s\n", sc.Count, sc.Name)
		}
	}

	if len(s.LoudestRepos) > 0 {
		fmt.Fprintf(&b, "\nLoudest repos (by ask count, rateable + unrated):\n")
		for _, rc := range s.LoudestRepos {
			if rc.Unrated > 0 {
				fmt.Fprintf(&b, "  %4d  %s  (%d rated, %d unrated)\n",
					rc.Total(), rc.Repo, rc.Asks, rc.Unrated)
				continue
			}
			fmt.Fprintf(&b, "  %4d  %s\n", rc.Total(), rc.Repo)
		}
	}

	if s.UnmatchedOutcomes > 0 {
		fmt.Fprintf(&b, "\nNote: %d approved outcome(s) had no matching ask in-window. Usually benign:\n",
			s.UnmatchedOutcomes)
		fmt.Fprintf(&b, "outcomes are recorded per FILE, so repeat edits re-emit one; and asks collapsed\n")
		fmt.Fprintf(&b, "as hook re-invocations release theirs. Only suspect a missing/rotated log if\n")
		fmt.Fprintf(&b, "this is large relative to the ask count.\n")
	}
	return b.String()
}

// fpRow renders one bucket line, padding the label to width.
//
// A bucket with unrated asks never prints a bare rate. Either the rate is
// followed by the share of the bucket it actually covers, or — when NOTHING in
// the bucket could be rated — there is no rate to print and the row says so
// instead of printing a 0% that reads as "never approved" (#254). The contract
// check is the motivating case: filtered to a window with no co-firing symbol
// check, its bucket is entirely unrated.
func fpRow(b *strings.Builder, name string, width int, bkt FPBucket) {
	// The "ask" column is ALWAYS the rateable count — the same quantity the
	// headline and the --max-rate gate use — including when it is 0. Printing
	// Total here instead would silently change what the column means between
	// rows, so a reader comparing two rows would be comparing two quantities.
	fmt.Fprintf(b, "  %-*s %4d ask  ", width, name, bkt.Asks)
	if bkt.Asks == 0 {
		fmt.Fprintf(b, "  no rate (no ask here carries a join key)")
	} else {
		fmt.Fprintf(b, "%4d approved  %5.0f%%", bkt.Approved, 100*bkt.Rate())
		if bkt.Latency.N > 0 {
			fmt.Fprintf(b, "  (median %s)", formatLatency(bkt.Latency.MedianS))
		}
	}
	if bkt.Unrated > 0 {
		mark := " "
		if bkt.Coverage() < unratedCoverageFloor {
			mark = "!"
		}
		fmt.Fprintf(b, "  %s +%d unrated (rate covers %s)",
			mark, bkt.Unrated, coveragePct(bkt))
	}
	fmt.Fprint(b, "\n")
}

// coveragePct renders a coverage as a percentage that never rounds to a lie.
// %.0f turns 0.9978 into "100%", so a row would admit an unrated ask and claim
// full coverage in the same breath. Truncating toward zero keeps "<100%" honest;
// the ceiling case is symmetric — a coverage just above 0 must not read as "0%"
// on a row that does have rateable asks.
func coveragePct(b FPBucket) string {
	pct := 100 * b.Coverage()
	switch {
	case b.Unrated > 0 && pct > 99:
		return ">99%"
	case b.Asks > 0 && pct < 1:
		return "<1%"
	default:
		return fmt.Sprintf("%.0f%%", math.Floor(pct))
	}
}

// sortedFPKeys orders buckets by TOTAL asks (rated plus unrated), so a check
// whose asks are all unrated sorts by how loud it actually is rather than
// sinking to the bottom on a zero rateable count.
func sortedFPKeys(m map[string]FPBucket) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]].Total() != m[keys[j]].Total() {
			return m[keys[i]].Total() > m[keys[j]].Total()
		}
		return keys[i] < keys[j]
	})
	return keys
}

// PayloadFP renders an FPStats as a JSON-serializable map (parity with Payload).
//
// Every bucket carries "unrated" and "coverage" alongside "rate" (#254). They
// are not optional decoration: a consumer that reads "rate" without "coverage"
// can read 1.0 off a bucket where that rate covers 14% of the asks. "asks" keeps
// its old meaning — rateable asks, the denominator of "rate" — and "total" is
// the honest volume, so no existing consumer's arithmetic changes silently.
func PayloadFP(s FPStats) map[string]any {
	bucket := func(b FPBucket) map[string]any {
		// null, not 0, when nothing is rateable. FormatFP refuses to print a
		// percentage there precisely because 0% reads as "never approved"; handing
		// the same 0 to a jq filter or a dashboard would keep the defect on the
		// machine path that this payload's doc names as the one at risk. A
		// consumer that plots null plots a gap; one that plots 0 plots a claim.
		var rate any
		if b.Asks > 0 {
			rate = b.Rate()
		}
		return map[string]any{
			"asks": b.Asks, "approved": b.Approved, "rate": rate,
			"unrated": b.Unrated, "total": b.Total(), "coverage": b.Coverage(),
			"latency": latencyPayload(b.Latency),
		}
	}
	reasons := make([]map[string]any, 0, len(s.ByReason))
	for _, r := range sortedFPKeys(s.ByReason) {
		m := bucket(s.ByReason[r])
		m["reason"] = r
		reasons = append(reasons, m)
	}
	checks := make([]map[string]any, 0, len(s.ByCheck))
	for _, c := range sortedFPKeys(s.ByCheck) {
		m := bucket(s.ByCheck[c])
		m["check"] = c
		checks = append(checks, m)
	}
	langs := make([]map[string]any, 0, len(s.ByLang))
	for _, l := range sortedFPKeys(s.ByLang) {
		m := bucket(s.ByLang[l])
		m["lang"] = l
		langs = append(langs, m)
	}
	versions := make([]map[string]any, 0, len(s.ByVersion))
	for _, v := range sortedFPKeys(s.ByVersion) {
		m := bucket(s.ByVersion[v])
		m["gv"] = v
		versions = append(versions, m)
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
		"overall":            bucket(s.Window),
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

// latencyPayload mirrors bucket()'s null-not-zero convention above: no
// matched ask means no latency to report, not a zero-second one that a
// consumer could plot as "instant decisions."
func latencyPayload(l LatencyStats) any {
	if l.N == 0 {
		return nil
	}
	return map[string]any{
		"n": l.N, "min_s": l.MinS, "median_s": l.MedianS, "mean_s": l.MeanS, "max_s": l.MaxS,
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
