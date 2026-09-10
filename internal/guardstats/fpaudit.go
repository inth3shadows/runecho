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
// the single "approved anyway" bucket into four verdicts with different fixes:
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
//   - VerdictStands    the symbol never came to resolve, AND it is a name that
//                      could legitimately have been declared in the flagged
//                      language. The guard caught a real unbacked reference.
//   - VerdictNotIdent  the symbol could never have resolved, in any repo, at
//                      any commit — it is not a code identifier in the
//                      flagged language at all (a reserved word like JS
//                      `NaN`, or text from inside a string/comment the
//                      masking pass missed). This is still a false positive,
//                      but the fix is masking, not resolution — a different
//                      bug from VerdictFP, so it gets its own bucket instead
//                      of being folded into one that would send the fix to
//                      the wrong file. See classify's not-an-identifier gate.
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
	// VerdictNotIdent marks a flagged symbol that cannot be a declaration in
	// the flagged language at all — a reserved word/literal (JS `NaN`,
	// `Infinity`; Go `nil`, `iota`; Python `None`) that will NEVER resolve no
	// matter what commit is inspected, or SQL/prose text a masking gap let
	// through (`SUM`, `SQLite`). Without this, both fell to VerdictStands and
	// were scored "the guard was right" — measured at ~31% of one language's
	// stands bucket (issue #360) — which understates the guard's real FP rate
	// on exactly the number used to gate default-on decisions.
	//
	// The membership test below is deliberately NOT a SQL-keyword list: `SUM`,
	// `COALESCE`, `EXISTS` are not JS keywords, and calling them
	// not-an-identifier IN JS would misclassify a real JS function legitimately
	// named `SUM`. Only names that are reserved words/literals of the flagged
	// language itself qualify — a fact about that language's grammar, not an
	// inference about surrounding context. Table names and SQL-keyword leaks
	// belong to the masking fix (see extract.go's string/comment stripping),
	// not to this gate.
	//
	// Counted inside Rated() (see below) — this is a JUDGED, wrong flag, not a
	// coverage gap like VerdictUnknown/VerdictNA. Excluding it would move the
	// symbols out of `stands` and leave the reported FP rate exactly as
	// understated as before, wearing a different label.
	VerdictNotIdent AuditVerdict = "not-an-identifier"
	// VerdictUnknown covers every case the oracle could not answer: a deleted
	// worktree, an unenrolled or non-git tree, a symbol name too odd to build a
	// safe pattern from. Counted and reported, never folded into another bucket
	// and never dropped — an audit that silently discards what it cannot see
	// reports the coverage it wishes it had.
	VerdictUnknown AuditVerdict = "unknown"
	// VerdictNA marks a flagged symbol no dated question can judge. The Note on the
	// finding says which kind — see the NAReason* constants; they mean opposite
	// things and must not be read as one bucket.
	//
	// Only the hallucination check asserts "this name does not resolve". The
	// duplicate-symbol check flags a name defined TWICE and the dangling-ref
	// check flags one whose definition is being removed — for both, the symbol
	// being defined at ask time is the premise of the finding, not evidence
	// against it. Scoring them here would count every correct duplicate-symbol
	// catch as a guard resolution bug; the first run of this audit did exactly
	// that (27 fp, 0 stands for duplicate-symbol) before this bucket existed.
	//
	// Which symbols ARE judgeable, and under which question, comes from the
	// guard's own claim_symbols — see decisionRecord.ClaimSymbols in declog.go.
	// Deliberately NOT learn_symbols: that field is gated on what an approval
	// licenses, which is a different question and excludes contract-merged asks.
	VerdictNA AuditVerdict = "n/a"
)

// ClaimScope names WHICH dated existence question a check's finding actually
// makes. It exists because "was this symbol defined at ask time" is not one
// question — a hallucination flag and a file-scope flag mean different things by
// "defined", and answering the second with the first reports every correct catch
// as a false positive.
type ClaimScope string

const (
	// ScopeRepo — "this name resolves nowhere in the repository". The additive
	// hallucination check and ruff F821. This is the only question the oracle
	// asked before #393.
	ScopeRepo ClaimScope = "repo"
	// ScopeFile — "this name is not reachable in THIS file". The file-scope
	// check. Its findings name symbols that usually ARE declared elsewhere in the
	// repo; that is the premise of the finding, not evidence against it.
	ScopeFile ClaimScope = "file"
)

// claimScope maps a check name (decisionRecord.ClaimSymbols' keys, which are
// checkOrder's vocabulary) to the question the oracle must ask for it.
//
// A check absent from this map is one the guard should never have recorded a
// claim for. That is treated as a data error rather than defaulted to ScopeRepo:
// defaulting is how a check with the wrong question silently starts producing
// confident, wrong verdicts, which is the failure this whole type exists to stop.
var claimScope = map[string]ClaimScope{
	"violations": ScopeRepo,
	// lint is ScopeFile, not ScopeRepo. Ruff's F821 is pyflakes: "undefined
	// name" means unbound in an enclosing scope of THIS FILE, and says nothing
	// about the rest of the repo. The guard also runs suppressAlreadyReported
	// against `violations` before recording, which strips exactly the findings
	// the repo-wide check already caught — so what survives is dominated by names
	// that ARE declared elsewhere in the repo and merely unreachable here. Asked
	// the repo-wide question, near enough all of them would score `fp` and
	// falsely indict the check.
	"lint":       ScopeFile,
	"file-scope": ScopeFile,
}

// NA reasons. n/a is not one thing, and pooling the kinds is what let #393 sit
// unnoticed for 95 days: a check that is genuinely out of scope and a check with
// missing coverage both rendered as a bare "n/a".
const (
	// NAReasonNotResolutionClaim — the question does not apply. duplicate-symbol
	// and dangling flag names that ARE defined; call-shape flags a callee that
	// resolves by construction; lint F811 is the duplicate shape. Nothing to fix.
	NAReasonNotResolutionClaim = "not-a-resolution-claim"
	// NAReasonNoOracleQuestion — a real resolution claim this oracle cannot ask.
	// qualified and deps-go assert package membership, and resolving a qualifier
	// to a package is not something GitOracle does. Honest missing coverage.
	NAReasonNoOracleQuestion = "no-oracle-question"
	// NAReasonLegacyRecord — written by a guard older than claim_symbols, with a
	// mixed reason string that cannot be split. Shrinks on its own as the window
	// moves forward; never a reason to change a check.
	NAReasonLegacyRecord = "legacy-record"
	// NAReasonMixedReason — the ask's reason names BOTH an inapplicable check and
	// an unrateable-real-claim one, and the record does not say which flagged
	// this symbol. Reported as its own bucket rather than folded into either:
	// no-oracle-question is the actionable "missing coverage" figure, and
	// inflating it with symbols that may be duplicate-symbol's would make the one
	// number this split exists to expose the least trustworthy one in the table.
	NAReasonMixedReason = "mixed-reason"
)

// noOracleQuestionChecks and notResolutionClaimChecks are the two kinds of
// unrateable check, by the name they contribute to decisionRecord.Reason. Only
// checks that can appear in a reason string belong here — lint is absent because
// its two rules split across both kinds and the reason cannot say which fired.
var (
	// recv-method and var-type are here for the reason the guard gives for
	// excluding them from claim_symbols in the first place: theirs is a MEMBER
	// claim ("no such method on this receiver"), which a tree-wide grep cannot
	// ask — exactly as for qualified. Labelling them not-a-resolution-claim would
	// read as "correct and permanent" for what is really missing coverage.
	noOracleQuestionChecks   = []string{"qualified", "deps-go", "recv-method", "var-type"}
	notResolutionClaimChecks = []string{"duplicate-symbol", "dangling", "dropped-import", "call-shape"}
)

// naReasonForCheck classifies why an unrateable symbol is unrateable, from the
// ask's reason string.
//
// The reason can be merged ("contract+violations", "violations+dangling"), and
// the record attributes only the RATEABLE symbols to a check — so for the rest
// this is the only evidence there is. A merged reason naming both kinds is
// reported as mixed rather than guessed at, for the same discipline
// claimedSymbols applies to a pre-#29 mixed reason: an attribution that cannot
// be established is not invented.
func naReasonForCheck(reason string) string {
	namesAny := func(names []string) bool {
		for _, c := range names {
			if strings.Contains(reason, c) {
				return true
			}
		}
		return false
	}
	noQuestion, notClaim := namesAny(noOracleQuestionChecks), namesAny(notResolutionClaimChecks)
	switch {
	case noQuestion && notClaim:
		return NAReasonMixedReason
	case noQuestion:
		return NAReasonNoOracleQuestion
	default:
		// Includes the plain not-a-resolution-claim reasons and anything
		// unrecognised. Defaulting here is safe in the way defaulting in
		// claimScope is not: this picks a LABEL for a pair already excluded from
		// every rate, where claimScope would pick a question and emit a verdict.
		return NAReasonNotResolutionClaim
	}
}

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
	// Defined reports whether sym was resolvable at rev under scope — see
	// ClaimScope. rel is the edited file's repo-relative path. lang is the
	// decision record's language tag, used to pick patterns; an unrecognised tag
	// should widen rather than narrow, since a missed definition here reads as a
	// guard catch and inflates VerdictStands.
	//
	// scope is a required parameter rather than a second method on purpose: a
	// call site can forget to switch methods, but it cannot forget to pass an
	// argument, and asking the wrong one of these two questions produces a
	// confident wrong verdict rather than an error.
	Defined(worktree, rev, lang, sym, rel string, scope ClaimScope) (bool, error)
}

// defKey memoises one Defined answer. rev is in the key, not just root: the
// whole point of the audit is that the same symbol has different answers at
// different commits. rel is in it because a binding resolves only in the file
// that wrote it, so the same symbol at the same commit legitimately differs
// between two files.
type defKey struct {
	root, rev, lang, sym, rel string
	// scope is in the key because the same symbol at the same commit
	// legitimately answers differently to the two questions — that is the entire
	// point of ClaimScope. Omitting it would serve a repo-scoped answer to a
	// file-scoped question from the memo, silently.
	scope ClaimScope
}

// claimedSymbols returns, for each flagged symbol the audit may judge, the dated
// question to ask about it. A symbol absent from the result is n/a.
//
// Three record generations, newest first:
//
//   - claim_symbols present (#393). The guard named the check behind each
//     rateable symbol, so the question comes straight from claimScope. A check
//     the guard recorded but claimScope does not know is DROPPED, not defaulted —
//     see claimScope's doc.
//   - claim_symbols absent, learn_symbols present (#29..#393). learn_symbols is
//     the hallucination-origin subset, which is exactly the repo-scoped question.
//     Note this is a coincidence of overlap, not a definition: learn_symbols is
//     gated on what an APPROVAL licenses, so a contract-merged ask carries an
//     empty one and its violations stay unrateable on these old records. That is
//     the coverage gap #393 measured, and it is unfixable retroactively.
//   - neither (pre-#29). The reason string is the only evidence left: a reason
//     naming ONLY the hallucination check means every symbol on the record came
//     from it. A mixed reason cannot be split and is not guessed at.
func claimedSymbols(d *Decision) map[string]ClaimScope {
	out := make(map[string]ClaimScope, len(d.Symbols))
	if d.ClaimSymbols != nil {
		// Two checks CAN claim the same name on one edit, and first-writer-wins
		// over Go's randomised map iteration made the verdict depend on which one
		// the runtime happened to visit first. Measured on a single unmodified
		// record where file-scope and lint both flagged `render`: ten fpaudit runs
		// returned `stands` seven times and `fp` three times. An oracle whose
		// headline rate is not reproducible on an unchanged log is not an oracle.
		//
		// So: narrowest scope wins, ties broken by nothing (the scopes are equal).
		// ScopeFile asks strictly less than ScopeRepo — a name reachable in the
		// edited file is reachable in the repo, never the reverse — so preferring
		// it can only move a pair from `fp` toward `stands`/`premature`. That is
		// the conservative direction: it declines to indict the resolver on a
		// question the check did not ask, rather than manufacturing a false
		// positive from a scope mismatch.
		//
		// ScopeFile and ScopeRepo are also the ONLY two scopes; a third would need
		// a real ordering here rather than this two-value rule.
		for _, check := range sortedKeys(d.ClaimSymbols) {
			scope, known := claimScope[check]
			if !known {
				continue
			}
			for _, sym := range d.ClaimSymbols[check] {
				if prev, dup := out[sym]; dup && prev == ScopeFile {
					continue
				}
				out[sym] = scope
			}
		}
		return out
	}
	if d.LearnSymbols != nil {
		for _, sym := range d.LearnSymbols {
			out[sym] = ScopeRepo
		}
		return out
	}
	if d.Reason == "violations" {
		for _, sym := range d.Symbols {
			out[sym] = ScopeRepo
		}
	}
	return out
}

// sortedKeys returns m's keys in a stable order, so an audit over an unchanged
// log produces an unchanged result. Go randomises map iteration deliberately;
// an offline measurement instrument is exactly the place that must not inherit
// it.
func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// naReasonFor explains why sym on d is unrateable. Split out so the reason is
// derived in one place for both the finding and the report.
func naReasonFor(d *Decision) string {
	if d.ClaimSymbols == nil && d.LearnSymbols == nil && d.Reason != "violations" {
		return NAReasonLegacyRecord
	}
	return naReasonForCheck(d.Reason)
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
	// Scope is the dated question this pair WOULD be judged by (#393). Set
	// whenever the record marked the symbol rateable — which includes the
	// not-an-identifier and unknown pairs, where classify short-circuits before
	// asking anything, so a populated Scope does not imply the oracle ran. Empty
	// only on n/a, where no check claimed the symbol at all.
	//
	// Carried on the finding so a downstream consumer — the premature-latency
	// scan, the JSON — re-asks the SAME question rather than defaulting to
	// repo-wide and silently measuring a different thing.
	Scope ClaimScope
	// Note explains a VerdictUnknown (the oracle's error) or a VerdictNA (one of
	// the NAReason* constants — n/a is not one thing, see #393). Empty otherwise.
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
	// NAReasons counts the n/a pairs by NAReason* (#393). Reported separately
	// from Counts[VerdictNA] because the two kinds mean opposite things:
	// not-a-resolution-claim is correct and permanent, no-oracle-question is
	// missing coverage. A single pooled n/a hid the second for 95 days.
	NAReasons map[string]int
	Findings  []AuditFinding
}

// Rated is the number of pairs that were both judgeable and answerable.
// VerdictNotIdent deliberately stays IN this count: it is a judged, wrong
// flag (a masking bug, not a resolution one), so subtracting it here like
// VerdictUnknown/VerdictNA would move it out of `stands` and leave the
// reported FP rate exactly as understated as before this verdict existed.
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
		Since:     since,
		Counts:    map[AuditVerdict]int{},
		ByReason:  map[string]map[AuditVerdict]int{},
		ByLang:    map[string]map[AuditVerdict]int{},
		NAReasons: map[string]int{},
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
	// RevAt is per (root, ask timestamp), not per symbol. Without this every
	// symbol on an ask spawned its own `git rev-list` — ~150 redundant processes
	// on the reference window, each carrying the full git timeout.
	type revKey struct {
		root string
		ts   time.Time
	}
	revs := map[revKey]struct {
		rev string
		err error
	}{}
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

		judgeable := claimedSymbols(d)
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
			scope, rateable := judgeable[sym]
			if rateable {
				f.Scope = scope
			}
			switch {
			case !rateable:
				f.Verdict, f.Note = VerdictNA, naReasonFor(d)
			case st.err != nil:
				f.Verdict, f.Note = VerdictUnknown, st.err.Error()
			default:
				rk := revKey{st.root, d.TS}
				r, seen := revs[rk]
				if !seen {
					r.rev, r.err = o.RevAt(st.root, d.TS)
					revs[rk] = r
				}
				f.Verdict, f.Note = classify(o, st.root, st.rel, st.head, r.rev, r.err, d, sym, scope, defs)
			}
			stats.Symbols++
			stats.Counts[f.Verdict]++
			if f.Verdict == VerdictNA {
				stats.NAReasons[f.Note]++
			}
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
// The not-an-identifier gate runs first and short-circuits both dated
// questions: a name that can never be a declaration in the flagged language
// needs no git lookup at all — asking "was NaN defined at ask time" cannot
// come back true, in this or any repo, so skipping straight past the oracle
// is not an optimisation shortcut, it is the only answer the two dated
// questions could ever produce.
//
// Order matters for the rest and is not an optimisation. "Defined at ask time"
// is asked first because it is the only verdict that indicts the guard's
// resolver; if it is true, whether the symbol also exists now tells us
// nothing. Asking HEAD first and treating "defined now" as the FP signal is
// the mistake that made hashToSeed read as a false positive for weeks.
func classify(o Oracle, root, rel, head, revAt string, revErr error, d *Decision, sym string, scope ClaimScope, defs map[defKey]bool) (AuditVerdict, string) {
	if isNotIdentifier(d.Lang, sym) {
		return VerdictNotIdent, ""
	}
	if revErr != nil {
		return VerdictUnknown, "no commit at or before ask time: " + revErr.Error()
	}

	lookup := func(rev string) (bool, error) {
		k := defKey{root: root, rev: rev, lang: d.Lang, sym: sym, rel: rel, scope: scope}
		if v, ok := defs[k]; ok {
			return v, nil
		}
		v, err := o.Defined(root, rev, d.Lang, sym, rel, scope)
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

// notIdentSets is the per-language set of reserved words and literal globals
// that can NEVER be a declaration, keyed on the same "go"/"js"/"py" tag the
// guard stamps into Decision.Lang (js already covers ts/jsx/tsx/gs — see
// LangJS's doc comment in internal/guard/extract.go — so there is no separate
// ts/jsx/tsx entry to keep in sync). "js" is deliberately a SHORTER list than
// jsBuiltins in internal/guard/extract.go: that list also carries callable
// globals like `fetch`/`Promise`, which ARE legal identifiers a user could
// shadow or (in principle) a resolver could find defined — only true reserved
// words/literals belong here, because this gate's whole justification is
// that git can never answer "was it declared" any other way for these names.
//
// A word only reserved in STRICT-mode JS (`static`, `let`, `yield`, `await`,
// `implements`, `interface`, `package`, `private`, `protected`, `public`) is
// deliberately EXCLUDED, even though it looks tempting to add: `.gs` (Apps
// Script) and CommonJS `.js` both run sloppy-mode by default, where
// `function static(){}` is a legal declaration. Gating on one of these words
// would short-circuit the oracle for a symbol that genuinely could be
// declared, turning a real VerdictFP (resolver miss) into an unfalsifiable
// VerdictNotIdent — reviewed 2026-09-01, verified live: a `.gs` fixture
// declaring `function static(){}` reached VerdictNotIdent with the oracle's
// Defined never called, before this list was narrowed to unconditional
// keywords/literals only.
//
// nil is a Go builtin but a legal Python variable name, and vice versa for
// None — this is why the map is per-language rather than one shared set.
var notIdentSets = map[string]map[string]struct{}{
	"js": setOfNames(
		// Global properties, not syntactically reserved — `function NaN(){}`
		// and `function undefined(){}` are both legal JS in every mode
		// (verified on node: typeof both is "function" afterwards) — but
		// treated as unshadowable in practice, the same tradeoff jsBuiltins
		// already makes for callable globals like `console`/`Object`. NaN and
		// Infinity are the pair issue #360's corpus actually measured (a JS
		// file's SQL template referencing SUM/COALESCE alongside a literal
		// NaN/Infinity reference); undefined joins them for the same reason,
		// not because it is grammatically reserved like null/true/false below.
		"NaN", "Infinity", "undefined",
		// Unconditionally reserved in every JS mode (ECMA-262 ReservedWord —
		// not the FutureReservedWord subset, which is strict-mode only and
		// excluded above).
		"null", "true", "false", "this", "super",
		"break", "case", "catch", "class", "const", "continue", "debugger",
		"default", "delete", "do", "else", "enum", "export", "extends",
		"finally", "for", "function", "if", "import", "in", "instanceof",
		"new", "return", "switch", "throw", "try", "typeof", "var", "void",
		"while", "with",
	),
	"go": setOfNames(
		// Predeclared identifiers, not keywords — `var nil = 5` compiles (it
		// shadows the predeclared nil within that scope). Treated as
		// unshadowable in practice for the same reason as JS's NaN/Infinity
		// above: no real Go code names a function `nil`/`iota`.
		"nil", "iota", "true", "false",
		// Keywords — a syntax error as an identifier in every Go version.
		"break", "case", "chan", "const", "continue", "default", "defer",
		"else", "fallthrough", "for", "func", "go", "goto", "if", "import",
		"interface", "map", "package", "range", "return", "select", "struct",
		"switch", "type", "var",
	),
	"py": setOfNames(
		// literals/keyword-constants — never bindable (SyntaxError to assign to).
		"None", "True", "False",
		// keywords — a syntax error as an identifier.
		"and", "as", "assert", "async", "await", "break", "class", "continue",
		"def", "del", "elif", "else", "except", "finally", "for", "from",
		"global", "if", "import", "in", "is", "lambda", "nonlocal", "not",
		"or", "pass", "raise", "return", "try", "while", "with", "yield",
	),
}

func setOfNames(names ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

// isNotIdentifier reports whether sym is a reserved word or literal of lang —
// a name that cannot be a declaration in that language at any commit, so the
// two dated git questions cannot judge it. Zero inference: membership is a
// fact about the flagged language's own grammar, not a guess about what kind
// of code the name showed up in. An unrecognised lang tag reports false
// (widens rather than narrows, matching Oracle.Defined's convention — a miss
// here reads as VerdictStands, same failure direction as before this gate
// existed, never a new one).
func isNotIdentifier(lang, sym string) bool {
	set, ok := notIdentSets[lang]
	if !ok {
		return false
	}
	_, isReserved := set[sym]
	return isReserved
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
	fmt.Fprintf(&b, "(%d n/a — the guard recorded no rateable claim for them;\n"+
		" %d unanswerable by the oracle.)\n\n", s.Counts[VerdictNA], s.Counts[VerdictUnknown])

	rows := []struct {
		v    AuditVerdict
		what string
	}{
		{VerdictFP, "already defined at ask time — the guard's resolver missed it"},
		{VerdictPremature, "defined only afterwards — correct, but fired too early"},
		{VerdictStands, "still undefined — the guard caught a real unbacked reference"},
		{VerdictNotIdent, "not a code identifier at all — reserved word or masking gap"},
	}
	// "not-an-identifier" is 17 chars, the longest verdict label; width 18
	// gives it a 1-char margin. Widen this if a longer verdict is ever added,
	// or the row it belongs to shifts the count/percent columns out of
	// alignment with the rest.
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-18s %5d  %5.1f%%   %s\n",
			r.v, s.Counts[r.v], 100*s.Share(r.v), r.what)
	}
	if u := s.Counts[VerdictUnknown]; u > 0 {
		fmt.Fprintf(&b, "  %-18s %5d      -    oracle could not answer (see --json for reasons)\n",
			VerdictUnknown, u)
	}
	if n := s.Counts[VerdictNA]; n > 0 {
		fmt.Fprintf(&b, "  %-18s %5d      -    no rateable claim — see the n/a breakdown below\n",
			VerdictNA, n)
	}

	b.WriteString("\nBy check:\n")
	writeVerdictTable(&b, s.ByReason)
	b.WriteString("\nBy language:\n")
	writeVerdictTable(&b, s.ByLang)

	if len(s.NAReasons) > 0 {
		b.WriteString("\nn/a by reason:\n")
		for _, r := range []struct{ key, what string }{
			{NAReasonNotResolutionClaim, "the question does not apply — duplicate-symbol/dangling/\n" +
				"                              dropped-import flag names that ARE defined, call-shape\n" +
				"                              flags a callee that resolves by construction, and lint\n" +
				"                              F811 is the duplicate shape. Correct and permanent."},
			{NAReasonNoOracleQuestion, "a real resolution claim this oracle cannot ask — qualified and\n" +
				"                              deps-go assert PACKAGE MEMBERSHIP, and resolving a\n" +
				"                              qualifier to a package is not something it does. This is\n" +
				"                              missing coverage, not out of scope."},
			{NAReasonMixedReason, "the ask names both kinds of unrateable check and does not say\n" +
				"                              which flagged this symbol. Not folded into either — the\n" +
				"                              missing-coverage figure has to stay trustworthy."},
			{NAReasonLegacyRecord, "written before claim_symbols existed (#393); the reason string\n" +
				"                              cannot be split. Shrinks as the window moves forward."},
		} {
			if n := s.NAReasons[r.key]; n > 0 {
				fmt.Fprintf(&b, "  %-26s %5d  %s\n", r.key, n, r.what)
			}
		}
	}
	b.WriteString("\nEach rated check is asked the question it actually made (#393): violations\n")
	b.WriteString("asserts 'resolves nowhere in the repo' and is answered tree-wide; file-scope and\n")
	b.WriteString("ruff F821 assert 'not reachable in THIS file' and are answered against the edited\n")
	b.WriteString("file alone. Asking the repo-wide question of either would report every correct\n")
	b.WriteString("catch as a false positive — the name they flag is usually declared elsewhere,\n")
	b.WriteString("which is the premise of the finding, not evidence against it.\n")
	b.WriteString("\nA high 'premature' share is not a resolver bug. It means the check fires at\n")
	b.WriteString("the wrong moment — an agent writing a caller before its callee — and the fix\n")
	b.WriteString("is to move the check later, not to widen the known-symbol set.\n")
	b.WriteString("\n'not-an-identifier' is also a false positive, but a masking bug, not a\n")
	b.WriteString("resolver bug — fp + not-an-identifier is the true 'guard was wrong' total,\n")
	b.WriteString("with two different fixes in two different files (see VerdictNotIdent).\n")
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
	fmt.Fprintf(b, "  %-*s  %6s %10s %7s %10s %8s %5s\n", width, "", "fp", "premature", "stands", "not-ident", "unknown", "n/a")
	for _, k := range keys {
		c := m[k]
		fmt.Fprintf(b, "  %-*s  %6d %10d %7d %10d %8d %5d\n", width, k,
			c[VerdictFP], c[VerdictPremature], c[VerdictStands], c[VerdictNotIdent], c[VerdictUnknown], c[VerdictNA])
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
		if f.Scope != "" {
			m["scope"] = string(f.Scope)
		}
		findings = append(findings, m)
	}
	counts := map[string]int{}
	for v, n := range s.Counts {
		counts[string(v)] = n
	}
	// na_reasons is emitted even when empty: a consumer reading it as "which
	// checks are unrateable and why" needs to tell "no n/a in this window" from
	// "this build does not report the breakdown".
	naReasons := map[string]int{}
	for r, n := range s.NAReasons {
		naReasons[r] = n
	}
	return map[string]any{
		"since": s.Since.UTC().Format(time.RFC3339),
		"until": s.Until.UTC().Format(time.RFC3339),
		"asks":  s.Asks, "symbols": s.Symbols, "rated": s.Rated(),
		"counts":     counts,
		"na_reasons": naReasons,
		"by_reason":  nestedPayload(s.ByReason),
		"by_lang":    nestedPayload(s.ByLang),
		"findings":   findings,
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
