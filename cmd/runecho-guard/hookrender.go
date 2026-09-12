package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/inth3shadows/runecho/internal/guard"
)

// hookrender.go — turning a verification into Claude Code's PreToolUse answer.
//
// Split from runHookMode by #394 so the hook is one renderer over the shared
// core (verify.go) rather than the architecture itself. Everything here is about
// what to SAY and what to LOG; nothing here decides anything about the code.
//
// The body below was moved verbatim, for the same mutation-catalog reason
// verify.go records. The hook emits EXACTLY ONE decision on every path, and the
// order — write to out, then logDecision — is load-bearing: the decision log is
// the dogfood instrument, and a record written before a failed write would
// count a decision the user never saw.

// renderHookDecision emits the hook's single decision for v and logs it,
// returning the process exit code (always 0 — the guard defers, it never
// blocks from here).
func renderHookDecision(out io.Writer, v verification) int {
	// The four bail arms answer without any check having run. Each keeps the
	// exact log reason, the exact fields, and the exact contract-first ordering
	// its arm had when it lived inline: bad-path deliberately logs NO file (the
	// path is what was rejected), while the other three carry it.
	switch v.Bail {
	case bailEmptyInput:
		if askContractOnly(out, v.Contract, v.Path, v.Lang, editFingerprint(v.Edit), nil, nil) {
			return 0
		}
		hookDefer()
		logDecision(decisionRecord{Mode: "hook", File: v.Path, Decision: "defer", Reason: bailEmptyInput})
		return 0
	case bailBadPath:
		hookDefer()
		logDecision(decisionRecord{Mode: "hook", Decision: "defer", Reason: bailBadPath})
		return 0
	case bailUnknownLang:
		if askContractOnly(out, v.Contract, v.Path, v.Lang, editFingerprint(v.Edit), nil, nil) {
			return 0
		}
		hookDefer()
		logDecision(decisionRecord{Mode: "hook", File: v.Path, Decision: "defer", Reason: bailUnknownLang})
		return 0
	case bailDegradedStore:
		answerDegradedStore(out, v.Lookup, v.Edit, v.Path, v.Lang, v.RemovedText)
		return 0
	}

	// Destructured back into the names the moved body uses, so that body stays a
	// literal move and its comments stay literally true.
	edit, filePath, lang := v.Edit, v.Path, v.Lang
	cw := v.Contract
	repoName, latest := v.Lookup.RepoName, v.Lookup.Latest
	results, violations, learnEligible := v.Results, v.Violations, v.LearnEligible
	fsv, qualifiedV, depsGoV := v.FileScope, v.Qualified, v.DepsGo
	dangling, droppedImps, duplicates := v.Dangling, v.Dropped, v.Duplicates
	callShapes, lintFindingsList := v.CallShapes, v.Lint

	fired := firedChecksFrom(results)
	// Degraded-class Unknowns only (#359): a check that declined one candidate on
	// its own precision gate is recorded in decisions.jsonl but does not raise the
	// strict advisory — see countDegradedUnknown for why the two questions parted
	// ways.
	degraded := countDegradedUnknown(results)

	// Gated on BOTH len(violations) and fired.anyNonViolation(): violations
	// covers additive/recv-method/var-type (still merged into that slice), and
	// anyNonViolation covers everything else — file-scope, qualified, deps-go,
	// dangling, dropped, duplicate, call-shape all report through their own
	// slice/flag and were never (or, since #269, are no longer) visible to
	// len(violations) alone. Dropping either half of this condition would let a
	// real finding from the half it drops read as clean. See
	// firedChecks.anyNonViolation for the full history of why.
	if len(violations) == 0 && !fired.anyNonViolation() {
		// Every FACT check passed. The contract question is independent of all of
		// them — a perfectly correct edit to a file the session said it would not
		// touch is precisely the case this check exists for — so it is answered
		// here, ahead of the degraded and stale advisories, because an ask is a
		// stronger signal than either and the hook emits only one decision.
		if askContractOnly(out, cw, filePath, lang, editFingerprint(edit), checkStatusMap(results), checkReasonMap(results)) {
			return 0
		}
		// Nothing flagged. A degraded check means "found nothing" is not the
		// same as "checked everything" — under strict, say so via
		// additionalContext (the same posture strict already applies to other
		// degraded states); by default stay silent per the fail-open contract.
		// Reason is check-degraded, NOT store-degraded: the store may be fine
		// (an oversized pre-edit file degrades too), and dogfood stats grep
		// decisions.jsonl by reason — conflating the two would skew the store-
		// health signal the un-gating decisions rest on. This intentionally
		// supersedes the stale-IR advisory for this edit (one advisory slot);
		// degraded coverage is the more actionable of the two.
		//
		// degraded now counts a VerdictUnknown from ANY of the eleven checks
		// (#330), not just the three deletion-side ones — a qualified/deps-go/
		// file-scope abstain (no module path, go.work, a star-import, …) that
		// used to log identically to a clean pass now surfaces here too.
		//
		// DEGRADED-class only since #359: a check that saw a candidate and
		// declined it on its own precision gate is recorded in decisions.jsonl
		// but says nothing here. Measured on golang.org/x/text, gate abstains
		// are ~100% of all abstains and hit 17.8% of files for recv-method, so
		// counting them would put this advisory on roughly one in five Go edits
		// — the noise that trains a user to stop reading it.
		if degraded > 0 && strictMode() {
			hookDeferContext(out, fmt.Sprintf("[runecho-guard] %d check(s) could not run to completion (pre-edit file unreadable/oversized, a store query failed, or a check abstained on degraded input) — coverage was incomplete for this edit.", degraded))
			logDecision(decisionRecord{Mode: "hook", Repo: repoName, File: filePath, Lang: string(lang), Decision: "defer", Reason: "check-degraded", Checks: checkStatusMap(results), CheckReasons: checkReasonMap(results)})
			return 0
		}
		// If the IR is stale the check may be incomplete — say so via
		// additionalContext (which informs Claude without forcing an allow/deny).
		staleReason := hookDeferStale(out, latest)
		logDecision(decisionRecord{Mode: "hook", Repo: repoName, File: filePath, Lang: string(lang), Decision: "defer", Reason: staleReason, Checks: checkStatusMap(results), CheckReasons: checkReasonMap(results)})
		return 0
	}

	var sb strings.Builder
	// syms: every flagged name, for the ask record / guardstats observability.
	// learnSyms: only the hallucination-origin (violations) names — the subset an
	// approval may train the learned-allow store on. See LearnSymbols on
	// decisionRecord for why the other categories must be excluded.
	var syms []string
	var learnSyms []string
	// claimSyms: check name -> the flagged names whose assertion fpaudit's dated
	// oracle can judge (#393). Deliberately NOT a subset of learnSyms and
	// deliberately NOT gated on cw — see ClaimSymbols on decisionRecord: one
	// field records what an approval licenses, the other what the check claimed,
	// and a contract-merged ask still made its resolution claim.
	claimSyms := map[string][]string{}
	// Contract first: "should you be editing this file at all" precedes "do these
	// names resolve", and reading it the other way round invites fixing the
	// symbol and re-submitting the same out-of-scope edit.
	if cw != nil {
		sb.WriteString(cw.section())
	}
	if len(violations) > 0 {
		fmt.Fprintf(&sb, "[runecho-guard] %d symbol reference(s) not found in the indexed code — possible hallucination:\n", len(violations))
		for _, v := range violations {
			// "snippet line N" is honest: in hook mode the guard scans the
			// new_string/content snippet, not the whole file, so the number is
			// relative to the edit hunk — not the file's absolute line number.
			fmt.Fprintf(&sb, "  snippet line %d: %s%s\n", v.Line, v.Symbol, suggestionSuffix(v.Suggestions))
			syms = append(syms, v.Symbol)
			// NOT trained on when a contract also fired. An approval answers the
			// whole ask, and a merged ask asks two different questions — a user who
			// approves because the out-of-scope edit was legitimate has said nothing
			// about whether the symbol resolves. Folding it into learned-allow would
			// permanently blind the hallucination check to that name on the strength
			// of a scope decision. Same reasoning that excludes dangling, dropped and
			// duplicate approvals (see LearnSymbols on decisionRecord); contracts are
			// a fourth category that comment did not anticipate.
			//
			// claimSyms takes the same additive subset for a different reason:
			// only the additive check asserts "resolves nowhere in the repo".
			// recv-method and var-type merged their findings into `violations`
			// above, and theirs is a MEMBER claim ("no such method on this
			// receiver") — a tree-wide grep would find that name declared on some
			// other type and score the correct catch as a false positive, exactly
			// as it would for qualified.
			if _, additive := learnEligible[v.Symbol]; additive {
				if cw == nil {
					learnSyms = append(learnSyms, v.Symbol)
				}
				claimSyms["violations"] = append(claimSyms["violations"], v.Symbol)
			}
		}
	}
	// file-scope, qualified and deps-go each get their own header instead of
	// folding into the block above: the symbol above genuinely does not exist
	// anywhere the guard knows about, but these three found something real and
	// are only flagging where/how it's reachable (#269's fourth confound —
	// reusing "not found in the indexed code" for these was factually false and
	// made an approve-anyway ambiguous between "wrong finding" and "right finding,
	// wrong explanation"). snippetLineFmt matches every other hook-mode section:
	// hook mode scans the edit's own hunk, so the line number is relative to the
	// snippet, not the file.
	snippetLineFmt := func(v guard.Violation) string { return fmt.Sprintf("snippet line %d: %s", v.Line, v.Symbol) }
	writeCheckSection(&sb, &syms, fileScopeAskHeader, fsv, snippetLineFmt)
	// file-scope is rateable, but only against a FILE-scoped oracle question:
	// its finding is "this name is not reachable here", and the name is usually
	// declared elsewhere in the repo — which is why it needs claimScope, not
	// just a place in this map. qualified and deps-go get no entry: both assert
	// package membership, and the oracle strips the qualifier.
	for _, v := range fsv {
		claimSyms["file-scope"] = append(claimSyms["file-scope"], v.Symbol)
	}
	writeCheckSection(&sb, &syms, qualifiedAskHeader, qualifiedV, snippetLineFmt)
	writeCheckSection(&sb, &syms, depsGoAskHeader, depsGoV, snippetLineFmt)
	if len(dangling) > 0 {
		fmt.Fprintf(&sb, "[runecho-guard] %d symbol(s) being removed are still referenced elsewhere — deleting may break callers:\n", len(dangling))
		for _, d := range dangling {
			fmt.Fprintf(&sb, "  %s — referenced by %s\n", d.Symbol, strings.Join(sanitizeReasonPaths(d.Referrers), ", "))
			syms = append(syms, d.Symbol)
		}
	}
	if len(droppedImps) > 0 {
		fmt.Fprintf(&sb, "[runecho-guard] %d import(s) removed by this edit are still used below — likely a dropped import (will fail at runtime):\n", len(droppedImps))
		for _, di := range droppedImps {
			fmt.Fprintf(&sb, "  %s — still used at snippet line %d\n", di.Name, di.LineNo)
			syms = append(syms, di.Name)
		}
	}
	if len(duplicates) > 0 {
		fmt.Fprintf(&sb, "[runecho-guard] %d new symbol(s) already exist as definitions elsewhere — possible duplicate/reimplementation:\n", len(duplicates))
		for _, d := range duplicates {
			fmt.Fprintf(&sb, "  %s — also defined in %s\n", d.Symbol, strings.Join(sanitizeReasonPaths(d.Locations), ", "))
			syms = append(syms, d.Symbol)
		}
	}
	syms = append(syms, callShapeSection(&sb, callShapes)...)
	syms = append(syms, lintSection(&sb, lintFindingsList)...)
	if ls := lintClaimSymbols(lintFindingsList); len(ls) > 0 {
		claimSyms["lint"] = ls
	}
	// Trailer built from the checks that actually fired (#267): .runechoguardignore
	// reaches only the additive check, so offering it for a dangling/call-shape/lint
	// finding sent the user to a file that would not change the answer. See remedy.go.
	sb.WriteString(askTrailer(fired))
	hookAsk(out, sb.String())
	rec := decisionRecord{Mode: "hook", Repo: repoName, File: filePath, Lang: string(lang), Decision: "ask", Reason: contractReason(cw != nil, askReason(fired)), Symbols: syms, LearnSymbols: learnSyms, ClaimSymbols: claimSyms, Edit: editFingerprint(edit), Checks: checkStatusMap(results), CheckReasons: checkReasonMap(results)}
	if cw != nil {
		rec.Contract, rec.ContractHash = cw.Name, shortHash(cw.ActivatedHash)
	}
	logDecision(rec)
	return 0
}
