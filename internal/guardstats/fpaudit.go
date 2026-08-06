package guardstats

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The audit answers a question the approval log structurally cannot.
//
// fpreport's rate is "asks the agent approved anyway ÷ asks", which is only a
// false-positive proxy if approval carries information. Measured against
// transcript ground truth over 30 days — every ask joined to its actual
// Edit/Write tool call across 2,135 Claude Code transcripts — it does not:
// 308 ask-gated edits, 308 approved, 0 denied. A signal with no variance can
// rank nothing, so every per-check and per-language spread fpreport prints is
// a property of the join, not of the guard.
//
// This audit replaces the human with git history, which does vary. For a
// flagged symbol it asks two dated questions instead of one undated one:
// did the symbol exist when we complained, and does it exist now. That splits
// the single "approved anyway" bucket into three verdicts with different fixes:
//
//   - VerdictFP        the symbol was already defined somewhere in the repo at
//                      ask time. A complete resolver would have found it; the
//                      guard's cheap path did not. This is a resolution bug.
//   - VerdictPremature the symbol did not exist at ask time and does now. The
//                      guard was CORRECT and the warning was still worthless —
//                      it interrupted an agent writing a caller before its
//                      callee, which is legitimate authoring order. Not a
//                      resolver bug; a timing one. The fix is to move the check
//                      later, not to widen the known set.
//   - VerdictStands    the symbol never came to resolve. The guard caught a
//                      reference that is still unbacked.
//
// The premature bucket is the one no prior metric could see, and the one that
// motivated this file. Its founding case: `hashToSeed` in frostline's
// track-b-db.ts, logged as an ask at 2026-07-20T02:01:43Z and read for weeks
// afterwards as the cleanest false positive in the log — a plain top-level
// `function hashToSeed(value: string)` in the very file being edited. It is
// not a false positive. ExtractDefs finds that declaration today; the symbol
// first entered the tree ten hours after the ask. Judged against the file as it
// is now, a correct catch looks like a resolver bug.

// AuditVerdict is the classification of one (ask, symbol) pair.
type AuditVerdict string

const (
	VerdictFP        AuditVerdict = "fp"
	VerdictPremature AuditVerdict = "premature"
	VerdictStands    AuditVerdict = "stands"
	// VerdictUnknown covers every case the oracle could not answer: a deleted
	// worktree, an unenrolled or non-git tree, a symbol name too odd to build a
	// safe pattern from. Counted and reported, never folded into another bucket
	// and never dropped — an audit that silently discards what it cannot see
	// reports the coverage it wishes it had.
	VerdictUnknown AuditVerdict = "unknown"
	// VerdictNA marks a flagged symbol the two dated questions cannot judge.
	//
	// Only the hallucination check asserts "this name does not resolve". The
	// duplicate-symbol check flags a name defined TWICE and the dangling-ref
	// check flags one whose definition is being removed — for both, the symbol
	// being defined at ask time is the premise of the finding, not evidence
	// against it. Scoring them here would count every correct duplicate-symbol
	// catch as a guard resolution bug; the first run of this audit did exactly
	// that (27 fp, 0 stands for duplicate-symbol) before this bucket existed.
	//
	// The guard already draws this line for its own learned-allow store, and for
	// the same reason — see decisionRecord.LearnSymbols in declog.go. This reuses
	// that subset rather than re-deriving it.
	VerdictNA AuditVerdict = "n/a"
)

// Oracle answers dated existence questions about a repository. It is an
// interface so the classification below is testable without a git tree, and so
// a stronger implementation (running runecho's own parser over the blob at a
// commit, rather than matching definition patterns) can be swapped in without
// touching Audit.
type Oracle interface {
	// Worktree maps an absolute file path from a decision record to a usable git
	// worktree root AND the file's repo-relative path within it. The recorded
	// path often no longer exists — worktrees created by the claudew/codexw flow
	// are deleted on session exit — so an implementation is expected to fall back
	// to a sibling worktree of the same repository rather than give up, and MUST
	// prove the candidate is that repository before doing so.
	Worktree(file string) (root, rel string, err error)
	// RevAt returns the newest commit in worktree at or before ts.
	RevAt(worktree string, ts time.Time) (string, error)
	// Head returns the current HEAD commit of worktree.
	Head(worktree string) (string, error)
	// Defined reports whether sym was resolvable at rev — declared anywhere in
	// the tree, or bound inside the edited file itself, named by its
	// repo-relative path rel. lang is the decision record's language tag, used to
	// pick patterns; an unrecognised tag should widen rather than narrow, since a
	// missed definition here reads as a guard catch and inflates VerdictStands.
	Defined(worktree, rev, lang, sym, rel string) (bool, error)
}

// defKey memoises one Defined answer. rev is in the key, not just root: the
// whole point of the audit is that the same symbol has different answers at
// different commits. rel is in it because a binding resolves only in the file
// that wrote it, so the same symbol at the same commit legitimately differs
// between two files.
type defKey struct{ root, rev, lang, sym, rel string }

// hallucinationOrigin returns the set of flagged symbols the audit may judge —
// the ones whose ask meant "this name does not resolve". See VerdictNA.
//
// The guard writes that subset as learn_symbols. When the field is absent the
// record predates it (pre-#29), and the reason string is the only evidence left:
// a reason naming ONLY the hallucination check means every symbol on the record
// came from it. A mixed reason with no learn_symbols cannot be split and is not
// guessed at — those symbols are marked n/a rather than attributed.
func hallucinationOrigin(d *Decision) map[string]bool {
	out := make(map[string]bool, len(d.Symbols))
	if d.LearnSymbols != nil {
		for _, s := range d.LearnSymbols {
			out[s] = true
		}
		return out
	}
	if d.Reason == "violations" {
		for _, s := range d.Symbols {
			out[s] = true
		}
	}
	return out
}

// AuditFinding is one classified (ask, symbol) pair, carrying enough context to
// go look at the original edit.
type AuditFinding struct {
	TS      time.Time
	Repo    string
	File    string
	Lang    string
	Reason  string
	GV      string
	Symbol  string
	Verdict AuditVerdict
	// Note explains a VerdictUnknown. Empty otherwise.
	Note string
}

// AuditStats is the aggregate over a window.
type AuditStats struct {
	Since    time.Time
	Until    time.Time
	Asks     int // ask EVENTS considered, after collapsing duplicate hook fires
	Symbols  int // (ask, symbol) pairs classified
	Counts   map[AuditVerdict]int
	ByReason map[string]map[AuditVerdict]int
	ByLang   map[string]map[AuditVerdict]int
	Findings []AuditFinding
}

// Rated is the number of pairs that were both judgeable and answerable.
func (s AuditStats) Rated() int {
	return s.Symbols - s.Counts[VerdictUnknown] - s.Counts[VerdictNA]
}

// Share is verdict's fraction of the RATED pairs. Unknowns and n/a are excluded
// from the denominator rather than counted against any verdict — one is missing
// data and the other is out of scope, and neither is evidence — which is exactly
// why Rated() must be reported alongside.
func (s AuditStats) Share(v AuditVerdict) float64 {
	r := s.Rated()
	if r == 0 {
		return 0
	}
	return float64(s.Counts[v]) / float64(r)
}

// Audit classifies every symbol flagged by every hook-mode ask in the window.
//
// Only hook-mode asks are considered. A precommit ask carries no file field at
// all (the guard reports them per-repo from a staged diff), so it has no path
// to resolve a worktree from — 145 of the 648 asks in the reference 30-day
// window are precommit, and counting them anywhere but their own line would
// repeat exactly the denominator error this audit exists to correct.
//
// Duplicate PreToolUse fires are collapsed first. Claude Code merges hooks from
// the plugin, user settings and project settings and runs every match, so one
// edit routinely writes the same ask two or three times within a second; 103 of
// those same 648 records are duplicates. Counting them would weight a verdict by
// how many hook wirings the machine happened to have.
func Audit(decisions []Decision, since time.Time, o Oracle) AuditStats {
	stats := AuditStats{
		Since:    since,
		Counts:   map[AuditVerdict]int{},
		ByReason: map[string]map[AuditVerdict]int{},
		ByLang:   map[string]map[AuditVerdict]int{},
	}

	// Cache per worktree: resolving a worktree and its HEAD costs a git process
	// each, and a window is dominated by a handful of repos.
	type wtState struct {
		root string
		rel  string
		head string
		err  error
	}
	wts := map[string]*wtState{}
	// Cache Defined answers: the same symbol is re-flagged across many asks in a
	// session (hashToSeed alone accounts for eight records in one morning).
	defs := map[defKey]bool{}

	var prev *Decision
	for i := range decisions {
		d := &decisions[i]
		if d.Decision != "ask" || d.Mode != "hook" || d.TS.Before(since) {
			continue
		}
		if d.TS.After(stats.Until) {
			stats.Until = d.TS
		}
		if prev != nil && isDuplicateFire(*prev, *d) {
			continue
		}
		prev = d
		stats.Asks++
		if len(d.Symbols) == 0 {
			continue
		}

		st, ok := wts[d.File]
		if !ok {
			st = &wtState{}
			if st.root, st.rel, st.err = o.Worktree(d.File); st.err == nil {
				st.head, st.err = o.Head(st.root)
			}
			wts[d.File] = st
		}

		judgeable := hallucinationOrigin(d)
		// One ask can list the same name more than once — the guard emits one
		// entry per violating LINE, so a helper called three times in one hunk
		// arrives three times (7 of the 1,028 ask records in the log do this,
		// one of them 19 entries for 15 distinct names). Every copy resolves
		// identically, so keeping them would just weight that verdict by how
		// often the agent happened to call the symbol.
		for _, sym := range dedupeStrings(d.Symbols) {
			f := AuditFinding{
				TS: d.TS, Repo: d.Repo, File: d.File, Lang: d.Lang,
				Reason: d.Reason, GV: d.GV, Symbol: sym,
			}
			switch {
			case !judgeable[sym]:
				f.Verdict = VerdictNA
			case st.err != nil:
				f.Verdict, f.Note = VerdictUnknown, st.err.Error()
			default:
				f.Verdict, f.Note = classify(o, st.root, st.rel, st.head, d, sym, defs)
			}
			stats.Symbols++
			stats.Counts[f.Verdict]++
			bump(stats.ByReason, d.Reason, f.Verdict)
			bump(stats.ByLang, langLabel(d.Lang), f.Verdict)
			stats.Findings = append(stats.Findings, f)
		}
	}
	// Until tracks the newest ask actually seen. With no asks in the window it
	// stays the zero time and the report header renders "0001-01-01"; fall back
	// to now, matching Aggregate's convention.
	if stats.Until.IsZero() {
		stats.Until = time.Now().UTC()
	}
	return stats
}

// dedupeStrings returns in order, without repeats.
func dedupeStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// classify runs the two dated questions for one symbol.
//
// Order matters and is not an optimisation. "Defined at ask time" is asked
// first because it is the only verdict that indicts the guard's resolver; if it
// is true, whether the symbol also exists now tells us nothing. Asking HEAD
// first and treating "defined now" as the FP signal is the mistake that made
// hashToSeed read as a false positive for weeks.
func classify(o Oracle, root, rel, head string, d *Decision, sym string, defs map[defKey]bool) (AuditVerdict, string) {
	revAt, err := o.RevAt(root, d.TS)
	if err != nil {
		return VerdictUnknown, "no commit at or before ask time: " + err.Error()
	}

	lookup := func(rev string) (bool, error) {
		k := defKey{root, rev, d.Lang, sym, rel}
		if v, ok := defs[k]; ok {
			return v, nil
		}
		v, err := o.Defined(root, rev, d.Lang, sym, rel)
		if err != nil {
			return false, err
		}
		defs[k] = v
		return v, nil
	}

	wasDefined, err := lookup(revAt)
	if err != nil {
		return VerdictUnknown, err.Error()
	}
	if wasDefined {
		return VerdictFP, ""
	}
	isDefined, err := lookup(head)
	if err != nil {
		return VerdictUnknown, err.Error()
	}
	if isDefined {
		return VerdictPremature, ""
	}
	return VerdictStands, ""
}

// duplicateFireWindow is how close two identical ask records must be to be read
// as one edit seen by several hook wirings rather than two edits. Measured on
// the reference window, every duplicate pair landed within 2s (99 of 103 within
// the same second); the nearest genuine re-ask on an unchanged symbol set was
// 61s away, so the gap this sits in is two orders of magnitude wide.
const duplicateFireWindow = 5 * time.Second

// isDuplicateFire reports whether cur is the same ask as prev, re-logged by
// another hook wiring. Deliberately strict — same file, same reason, same symbol
// list, within the window — so a genuine repeat ask after an unsuccessful fix is
// still counted.
func isDuplicateFire(prev, cur Decision) bool {
	return prev.File == cur.File &&
		prev.Reason == cur.Reason &&
		sameSymbols(prev.Symbols, cur.Symbols) &&
		cur.TS.Sub(prev.TS) >= 0 &&
		cur.TS.Sub(prev.TS) <= duplicateFireWindow
}

func sameSymbols(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func bump(m map[string]map[AuditVerdict]int, key string, v AuditVerdict) {
	if m[key] == nil {
		m[key] = map[AuditVerdict]int{}
	}
	m[key][v]++
}

func langLabel(l string) string {
	if l == "" {
		return "(none)"
	}
	return l
}

// FormatAudit renders the audit for a terminal.
func FormatAudit(s AuditStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Guard verdict audit (%s → %s)\n\n",
		s.Since.Format("2006-01-02"), s.Until.Format("2006-01-02"))

	b.WriteString("Each flagged symbol is asked two dated questions against git history:\n")
	b.WriteString("was it defined anywhere in the repo at ask time, and is it defined now.\n")
	b.WriteString("No human judgement is involved — approval carries no signal (see below).\n\n")

	if s.Symbols == 0 {
		b.WriteString("No hook-mode asks with symbols in this window.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "%d ask event(s), %d flagged symbol(s), %d rated.\n", s.Asks, s.Symbols, s.Rated())
	fmt.Fprintf(&b, "(%d n/a — only the hallucination check asserts a name does not resolve;\n"+
		" %d unanswerable by the oracle.)\n\n", s.Counts[VerdictNA], s.Counts[VerdictUnknown])

	rows := []struct {
		v    AuditVerdict
		what string
	}{
		{VerdictFP, "already defined at ask time — the guard's resolver missed it"},
		{VerdictPremature, "defined only afterwards — correct, but fired too early"},
		{VerdictStands, "still undefined — the guard caught a real unbacked reference"},
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-10s %5d  %5.1f%%   %s\n",
			r.v, s.Counts[r.v], 100*s.Share(r.v), r.what)
	}
	if u := s.Counts[VerdictUnknown]; u > 0 {
		fmt.Fprintf(&b, "  %-10s %5d      -    oracle could not answer (see --json for reasons)\n",
			VerdictUnknown, u)
	}
	if n := s.Counts[VerdictNA]; n > 0 {
		fmt.Fprintf(&b, "  %-10s %5d      -    not a resolution claim (duplicate-symbol, dangling, ...)\n",
			VerdictNA, n)
	}

	b.WriteString("\nBy check:\n")
	writeVerdictTable(&b, s.ByReason)
	b.WriteString("\nBy language:\n")
	writeVerdictTable(&b, s.ByLang)

	b.WriteString("\nA high 'premature' share is not a resolver bug. It means the check fires at\n")
	b.WriteString("the wrong moment — an agent writing a caller before its callee — and the fix\n")
	b.WriteString("is to move the check later, not to widen the known-symbol set.\n")
	return b.String()
}

func writeVerdictTable(b *strings.Builder, m map[string]map[AuditVerdict]int) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Busiest first, name as tiebreak, so the table is stable across runs.
	sort.Slice(keys, func(i, j int) bool {
		ti, tj := total(m[keys[i]]), total(m[keys[j]])
		if ti != tj {
			return ti > tj
		}
		return keys[i] < keys[j]
	})
	width := 0
	for _, k := range keys {
		if len(k) > width {
			width = len(k)
		}
	}
	fmt.Fprintf(b, "  %-*s  %6s %10s %7s %8s %5s\n", width, "", "fp", "premature", "stands", "unknown", "n/a")
	for _, k := range keys {
		c := m[k]
		fmt.Fprintf(b, "  %-*s  %6d %10d %7d %8d %5d\n", width, k,
			c[VerdictFP], c[VerdictPremature], c[VerdictStands], c[VerdictUnknown], c[VerdictNA])
	}
}

func total(m map[AuditVerdict]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// PayloadAudit is the --json shape.
func PayloadAudit(s AuditStats) map[string]any {
	findings := make([]map[string]any, 0, len(s.Findings))
	for _, f := range s.Findings {
		m := map[string]any{
			"ts": f.TS.UTC().Format(time.RFC3339), "repo": f.Repo, "file": f.File,
			"lang": f.Lang, "reason": f.Reason, "gv": f.GV,
			"symbol": f.Symbol, "verdict": string(f.Verdict),
		}
		if f.Note != "" {
			m["note"] = f.Note
		}
		findings = append(findings, m)
	}
	counts := map[string]int{}
	for v, n := range s.Counts {
		counts[string(v)] = n
	}
	return map[string]any{
		"since": s.Since.UTC().Format(time.RFC3339),
		"until": s.Until.UTC().Format(time.RFC3339),
		"asks":  s.Asks, "symbols": s.Symbols, "rated": s.Rated(),
		"counts":    counts,
		"by_reason": nestedPayload(s.ByReason),
		"by_lang":   nestedPayload(s.ByLang),
		"findings":  findings,
	}
}

func nestedPayload(m map[string]map[AuditVerdict]int) map[string]map[string]int {
	out := map[string]map[string]int{}
	for k, inner := range m {
		out[k] = map[string]int{}
		for v, n := range inner {
			out[k][string(v)] = n
		}
	}
	return out
}
