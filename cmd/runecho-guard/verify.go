package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/guard"
)

// verify.go — the guard's verification CORE, split out of runHookMode by #394.
//
// The split exists because Claude Code's PreToolUse schema had become the only
// way to ask RunEcho anything. Everything from "what does this edit say" to
// "what did the eleven checks conclude" lives here and speaks no hook JSON;
// deciding what to SAY about that, and emitting it, is renderHookDecision's job
// (hookrender.go). A second surface — the #394 stdin/stdout protocol — is then
// another renderer over the same core rather than an impersonation of a hook.
//
// This file's body was MOVED here verbatim, comments included. That is
// deliberate and worth preserving on future edits: `bench/hookmutate` locates
// its mutations by literal source text, and a rewrite-while-moving would have
// silently unhooked mutations from the code they score. The only changes were
// the four bail sites, which now return a Bail token instead of emitting, and
// filePath/sessionID becoming parameters instead of payload field reads.

// bail tokens name the four early returns that answer WITHOUT running the
// checks. They are not log reasons by accident — each maps to the exact reason
// string its arm has always logged (renderHookDecision), and the vocabulary is
// the one TECHNICAL.md documents and guardstats buckets on.
const (
	bailEmptyInput    = "empty-input"
	bailBadPath       = "bad-path"
	bailUnknownLang   = "unknown-lang"
	bailDegradedStore = "degraded-store"
)

// verification is everything the core concluded about one edit: the verdicts,
// the findings behind them, and the context a renderer needs to talk about
// either. It is the value #394's protocol renders as JSON and the hook renders
// as an ask or a defer.
//
// Findings are kept in their OWN typed slices rather than folded into
// CheckResult. That is checkresult.go's standing decision, not a shortcut here:
// four of the eleven checks carry richer shapes than guard.Violation, and a
// shared field would either lose data or widen guard.Violation for everyone.
// CheckResult answers "could this check answer"; these slices answer "what did
// it find", and only a renderer needs both at once.
type verification struct {
	// Bail is "" when the checks ran. Otherwise it names the early return, and
	// every field below except Edit/Path/Lang/Contract/Lookup/RemovedText is zero.
	Bail string

	Edit     hookEdit
	Path     string
	Lang     guard.Lang
	Lookup   lookupResult
	Contract *contractWarning

	// RemovedText is only carried for the degraded-store bail, whose renderer
	// re-derives the store-free checks from it.
	RemovedText string

	// Results holds one CheckResult per check that reported, in checkOrder.
	Results []CheckResult

	// Violations is the MERGED additive + recv-method + var-type slice, exactly
	// as the ask has always rendered it. LearnEligible is the additive-only
	// subset captured before the other two were appended — the distinction
	// decisionRecord.LearnSymbols depends on.
	Violations    []guard.Violation
	LearnEligible map[string]struct{}

	FileScope  []guard.Violation
	Qualified  []guard.Violation
	DepsGo     []guard.Violation
	Dangling   []danglingWarning
	Dropped    []guard.DroppedImport
	Duplicates []duplicateWarning
	CallShapes []guard.CallShapeMismatch
	Lint       []lintFinding
}

// verifyEdit runs every applicable check against one edit and returns what they
// concluded. It writes nothing and logs nothing — a caller that wants either
// calls a renderer. sessionID binds the edit-scope contract (#12 D2) and is read
// for no other purpose.
func verifyEdit(edit hookEdit, filePath, sessionID string) verification {
	text := hookText(edit.ToolName, edit.NewString, edit.Content, edit.Edits)
	// removedText is the Edit/MultiEdit text being deleted (cheap, no IO). It is
	// captured before the empty-input guard so a pure-deletion edit (empty
	// new_string) still reaches the E1 dangling-refs check below instead of being
	// dropped here. Empty (and inert) unless E1/dropped-import is enabled. Write
	// deletions are derived later from the on-disk file, not here. E5 does NOT
	// gate on this: it reads the whole pre-edit file itself (wholeFileText), so
	// including duplicateEnabled() here would needlessly keep this fast-path
	// guard from firing on an E5-only pure-deletion edit.
	// The call-shape check needs it too, for a different reason: an edit that
	// rewrites a declaration's parameter list makes the on-disk signature stale by
	// exactly this edit, and comparing a call against the stale one is a false
	// positive (see resolveDeclShape).
	var removedText string
	if danglingEnabled() || droppedImportEnabled() || callShapeEnabled() {
		removedText = hookOldText(edit.ToolName, edit.OldString, edit.Edits)
	}
	// A full-file-deletion Write (empty content) has text=="" and — since Write
	// carries no old_string — removedText=="" too, so it would trip the empty-input
	// bail below. But for Write the DELETED text is the pre-edit on-disk file, read
	// later for the E1/dropped-import checks; wiping a whole file is exactly when a
	// dangling-ref check matters most. So don't drop such a Write as "empty input"
	// while those checks are enabled — provided the on-disk file actually has
	// content to delete. A cheap os.Stat gates this so a Write that CREATES a new or
	// already-empty file (nothing to delete) keeps the fast early-return instead of
	// paying a DB open + two file reads on the ~12ms hook budget.
	emptyInput := text == "" && removedText == ""
	if emptyInput && edit.ToolName == "Write" && (danglingEnabled() || droppedImportEnabled()) {
		if fi, err := os.Stat(filePath); err == nil && fi.Size() > 0 {
			emptyInput = false
		}
	}
	if filePath == "" || emptyInput {
		// Contracts (#12 D2) get one last look before the fast return. Every gate
		// above turns on the edit's TEXT, because every check above is about the
		// text; a contract is about the PATH and never reads a byte of either
		// side. Leaving it behind those gates produced two silent misses — a
		// pure-deletion Edit (new_string "") and a Write creating a new
		// out-of-scope file — and whether the first one fired depended on
		// RUNECHO_GUARD_DANGLING happening to be set, since that is what
		// populates removedText. An unrelated flag deciding whether this check
		// runs is not a defensible gate.
		//
		// Asking HERE rather than clearing emptyInput is what keeps the cost
		// honest. Clearing it reopened the whole pipeline — store open, snapshot
		// list, symbol load, file read and parse — and rewrote the logged reason
		// from "empty-input" to "clean"/"stale-ir" for every session on a machine
		// that exports the flag globally, including the ones that activated no
		// contract at all. This path costs a single store open, only for a
		// session that named a contract, and leaves the log alone when it
		// abstains.
		return verification{
			Bail:     bailEmptyInput,
			Edit:     edit,
			Path:     filePath,
			Lang:     guard.LangFor(filePath),
			Contract: contractWarningFor(filePath, sessionID),
		}
	}
	// Reject null bytes (invalid on all supported OSes) and extreme lengths.
	if strings.ContainsRune(filePath, 0) || len(filePath) > 4096 {
		return verification{Bail: bailBadPath, Edit: edit, Path: filePath}
	}

	lang := guard.LangFor(filePath)
	if lang == guard.LangUnknown {
		// Edit-scope contracts (RUNECHO_GUARD_CONTRACT=1, default off; #12 D2)
		// are the one check that is not about code: they ask whether this file
		// should be touched at all, which is as answerable for a Markdown doc or
		// a CI YAML as for a .go file — and scope drift lands in those at least
		// as often as it lands in source. So this is the single place the check
		// pays for its own store open; every other edit picks it up from
		// lookupSymbolsFor below. nil (abstain) unless the flag is on AND this
		// session explicitly activated a contract AND the path fell outside it.
		return verification{
			Bail:     bailUnknownLang,
			Edit:     edit,
			Path:     filePath,
			Lang:     lang,
			Contract: contractWarningFor(filePath, sessionID),
		}
	}

	res := lookupSymbolsFor(filepath.Dir(filePath), filePath, sessionID)
	cw := res.Contract
	if !res.OK {
		return verification{
			Bail:        bailDegradedStore,
			Edit:        edit,
			Path:        filePath,
			Lang:        lang,
			Lookup:      res,
			Contract:    cw,
			RemovedText: removedText,
		}
	}
	// Destructure into the locals the rest of the flow already uses.
	// latest and repoName are the RENDERER's (it reads them off v.Lookup); the
	// core needs only the two the checks consume.
	symbols, ignorePath, repoName := res.Symbols, res.IgnorePath, res.RepoName

	// The file-scope check's firewall means "this name is a real symbol in the REPO
	// index", so it must see the symbol set BEFORE the in-file and learned-allow
	// folds below widen it in place. Learned-allow especially: those are names the
	// user taught the guard to accept, and re-raising one as out-of-scope would
	// undo that. Snapshot only when the check is on, so the default-off path costs
	// nothing on the hook's latency budget.
	var repoSymbols map[string]struct{}
	if fileScopeEnabled() && lang == guard.LangPython {
		repoSymbols = snapshotSymbols(symbols)
	}

	// An Edit/MultiEdit hunk sees only the changed region, not the rest of the
	// file — so a call to a sibling function (or a nested/local def, or a private
	// `_helper` the IR may not index) elsewhere in the file would falsely read as
	// hallucinated. Fold the current on-disk file's definitions into the known set
	// to suppress that. Best-effort: a missing/oversized file simply adds nothing.
	// Read once here and reuse the parsed lines for the dropped-import check's
	// whole-file bound set below — same snapshot, one read/scan per hook.
	fileLines := readFileLines(filePath)
	addInFileDefs(symbols, fileLines, lang)

	// One answer to "was the pre-edit file's context available to the checks that
	// need it", shared by every check below (#359). readFileLines returns nil for
	// BOTH "the file does not exist yet" and "it exists but is oversized or
	// unreadable", and only the second is degraded coverage — a brand-new file
	// this edit creates is DEFINITIVELY empty, so an ordinary new-file Write must
	// not report lost coverage (the same distinction the file-scope arm below
	// used to re-derive inline).
	//
	// Write is excluded even when the pre-edit copy is unreadable: its newLines
	// ARE the whole proposed file, so every check that concatenates
	// fileLines+newLines (qualified, recv-method, var-type) or reads
	// addedIsWholeFile (call-shape) still sees complete context. Only a
	// hunk-scoped Edit/MultiEdit actually loses anything.
	//
	// file-scope deliberately does NOT use this and keeps its own inline
	// re-derivation below: fileLines is its only whole-file source (it never
	// concatenates newLines), so an unreadable pre-edit copy degrades it for a
	// Write too. Same root cause, different reach — folding them into one rule
	// would either under-report file-scope or over-report the other four.
	preEditReason := ""
	if len(fileLines) == 0 && edit.ToolName != "Write" {
		// os.IsNotExist, not err == nil: ONLY a file that does not exist is
		// definitively empty. Any other stat failure (EACCES on a parent
		// directory, ELOOP, ENOTDIR) means readFileLines failed for a reason
		// that IS lost coverage, and testing err == nil silently classified
		// those as clean — while the file-scope arm below, using this same
		// distinction, correctly reported them as degraded. Two spellings of
		// one question that disagreed on everything but ENOENT (found by
		// review of #363).
		if _, err := os.Stat(filePath); !os.IsNotExist(err) {
			preEditReason = "oversized-pre-edit-file"
		}
	}

	// C3 learned-allow: fold in symbols this repo has approved often enough to
	// trust (count>=N, within TTL) so the guard stops re-asking about them.
	// Gated and read-only — a no-op (no store read) unless RUNECHO_GUARD_LEARN=1.
	if learnEnabled() {
		if dir, err := runechoDir(); err == nil {
			for s := range learnedAllowedSet(dir, repoName, time.Now()) {
				symbols[s] = struct{}{}
			}
		}
	}

	// newLines is the added text as AddedLines — gap-separated per edit for a
	// MultiEdit so stateful scanners reset open-string state at each boundary.
	// Shared by the additive check and the dropped-import check below so both see
	// the same (leak-free) view of a MultiEdit rather than a flat "\n"-join.
	newLines := hookAddedLines(edit.ToolName, edit.NewString, edit.Content, edit.Edits)
	// Computed ONCE and shared by every *ByLine seed builder below (code-review
	// finding on PR #334): each independently called hookBlockIndices with the
	// exact same arguments, re-walking blockStartLine's matching logic once per
	// builder on every hook invocation.
	blockIndices := hookBlockIndices(edit.ToolName, edit.OldString, edit.Edits, fileLines)
	diffs := []guard.FileDiff{{
		Path:       filePath,
		AddedLines: newLines,
		// Seed each block's open-string state from where it sits in the pre-edit
		// file, so an Edit landing inside a docstring or string literal is masked
		// instead of scanned as code. fileLines is the read already done above.
		SeedByLine: hookSeedByLineFromIndices(blockIndices, fileLines, lang),
	}}
	if lang == guard.LangPython {
		// Same idea for pyBraceDepth (#289): an Edit that adds a dict key without
		// touching the literal's opening `{` line — the opener is unchanged context
		// above the block — must not start scanning at depth 0 regardless of the
		// file's real state there, or the key reads as a definition instead of a
		// reference. Python-only, matching the wrapped functions' own gate.
		diffs[0].PyBraceDepthByLine = hookBraceDepthByLineFromIndices(blockIndices, fileLines)
		// PyDeclaredNames/PyParamNames/LocallyBoundNames' own seeds (#294) — same
		// rationale as PyBraceDepthByLine, for the general bracket depth and the
		// two def-signature-specific depths respectively.
		diffs[0].PyBracketDepthByLine = hookBracketDepthByLineFromIndices(blockIndices, fileLines)
		diffs[0].PyDefSigDepthByLine = hookDefSigDepthByLineFromIndices(blockIndices, fileLines)
		diffs[0].PyParamSigDepthByLine = hookParamSigDepthByLineFromIndices(blockIndices, fileLines)
	}

	violations := guard.Run(symbols, ignorePath, diffs)
	// results accumulates one CheckResult per check (#330's typed epistemic
	// status — OK/Violation/Unknown/Skipped, replacing firedChecks/degraded's
	// untyped bools+counter), in checkOrder's canonical order. firedChecksFrom
	// derives the untouched firedChecks/askReason from it at the bottom of this
	// function — see checkresult.go for why askReason itself is not
	// reimplemented against this type.
	//
	// "violations" captured before recv-method/var-type below append (the only
	// two checks still merging into `violations` — qualified/deps-go/
	// file-scope stopped as of #269; see qualifiedV/depsGoV/fsv further down).
	results := []CheckResult{classifyResult("violations", len(violations) > 0, "")}
	// learnEligible is the additive check's OWN finding set, captured here for the
	// same reason firedChecks is: recv-method and var-type below append into
	// `violations` too, and reading learn-eligibility back off the merged slice
	// is the bug this pattern exists to stop. LearnSymbols must stay the
	// hallucination-origin subset (see declog.go) because learned-allow feeds
	// guard.Run's known-set — approving a file-scope ask on `render` (a real
	// symbol, not imported here) would otherwise teach the guard that `render`
	// resolves, and keep it silent on a genuine hallucination of that name until
	// the TTL expires.
	learnEligible := make(map[string]struct{}, len(violations))
	for _, v := range violations {
		learnEligible[v.Symbol] = struct{}{}
	}

	// Same-repo internal-package qualified-call check (default on since #314;
	// RUNECHO_GUARD_QUALIFIED=0 disables it). fileLines is the pre-edit whole
	// file (read above); newLines is the proposed added text — passing both lets
	// an in-edit shadow or a newly added same-repo import be seen. The file's
	// own directory anchors go.mod.
	//
	// qualifiedV is kept OUT of `violations` on purpose (see #269's message-text
	// finding): a Violation means "this name does not resolve", and pkg.Foo here
	// means the opposite — pkg resolves and Foo does not exist as one of its
	// exports. Folding it into the additive check's merged slice made the ask's
	// shared header ("not found in the indexed code") false for this finding.
	// It gets its own section below, like dangling and duplicate do.
	var qualifiedV []guard.Violation
	qualifiedResult := CheckResult{Check: "qualified", Verdict: VerdictSkipped}
	if qualifiedEnabled() && lang == guard.LangGo {
		if modulePath := guard.GoModulePath(filepath.Dir(filePath)); modulePath != "" {
			var reason string
			qualifiedV, reason = qualifiedViolationsWithReason(lang, fileLines, newLines, symbols, modulePath, filePath)
			qualifiedResult = classifyResult("qualified", len(qualifiedV) > 0, foldAbstainReason(reason, preEditReason))
		} else {
			// No go.mod anywhere upward: this check validates a call against
			// the repo's OWN module IR, so with no module there is no
			// module-scoped question to ask — not applicable (Skipped), not a
			// transient failure to answer one (Unknown). Distinct from
			// deps-go's go.work/not-in-cache abstains below, where a module
			// DOES exist and the environment just can't confirm an answer.
			qualifiedResult = CheckResult{Check: "qualified", Verdict: VerdictSkipped, Reason: "no-module-path"}
		}
	}
	results = append(results, qualifiedResult)

	// Go receiver-method check (RUNECHO_GUARD_RECVMETHOD=1, default off). Takes
	// the pre-edit file plus the added text for the same reason the qualified
	// check does: a receiver declaration or a rebinding introduced by THIS edit
	// has to be visible, or the check judges the call against a stale file.
	recvMethodResult := CheckResult{Check: "recv-method", Verdict: VerdictSkipped}
	if recvMethodEnabled() && lang == guard.LangGo {
		rv, reason := recvMethodViolationsWithReason(lang, fileLines, newLines, symbols, filePath)
		recvMethodResult = classifyResult("recv-method", len(rv) > 0, foldAbstainReason(reason, preEditReason))
		violations = append(violations, rv...)
	}
	results = append(results, recvMethodResult)

	// Go local-variable-type method check (RUNECHO_GUARD_VARTYPE=1, default
	// off). Same family as the receiver check above and the same reason for
	// taking both fileLines and newLines; kept as its own flag — see
	// vartype.go for why it is not folded into RECVMETHOD.
	varTypeResult := CheckResult{Check: "var-type", Verdict: VerdictSkipped}
	if varTypeEnabled() && lang == guard.LangGo {
		vv, reason := varTypeViolationsWithReason(lang, fileLines, newLines, symbols, filePath)
		varTypeResult = classifyResult("var-type", len(vv) > 0, foldAbstainReason(reason, preEditReason))
		violations = append(violations, vv...)
	}
	results = append(results, varTypeResult)

	// External-dependency qualified-call check for Go (RUNECHO_GUARD_DEPS_GO=1,
	// default off). The edited file's directory anchors go.mod discovery, so a
	// multi-module repo resolves against the module the file actually belongs to.
	//
	// depsGoV stays out of `violations` for the same reason qualifiedV does above:
	// the dependency package resolves and IS imported — only the specific selector
	// is absent from its exports, which is not what "not found in the indexed
	// code" says. Own section below.
	var depsGoV []guard.Violation
	depsGoResult := CheckResult{Check: "deps-go", Verdict: VerdictSkipped}
	if lang == guard.LangGo {
		if goDepIdx := newGoDepIndex(filepath.Dir(filePath)); goDepIdx != nil {
			modulePath := guard.GoModulePath(filepath.Dir(filePath))
			var reason string
			depsGoV, reason = goDepQualifiedViolationsWithReason(lang, fileLines, newLines, modulePath, goDepIdx, filePath)
			// preEditReason folds in here for the same reason it does for
			// qualified/recv-method/var-type: this check builds its context by
			// concatenating fileLines with newLines, so an unreadable pre-edit
			// file leaves it with the hunk alone — no import block, therefore no
			// aliases, therefore "found nothing" from a check that never saw the
			// imports. It was the one context-concatenating check still missing
			// the fold (found by review of #363).
			depsGoResult = classifyResult("deps-go", len(depsGoV) > 0, foldAbstainReason(reason, preEditReason))
		}
	}
	results = append(results, depsGoResult)

	// File-scope resolution check (RUNECHO_GUARD_FILESCOPE=1, default off): a name
	// that resolves repo-wide but not inside THIS file — a helper used without
	// importing it, a module function called without its qualifier. fileLines is
	// the pre-edit whole file, newLines the proposed text; both are needed so a
	// binding introduced by this very edit still resolves. repoSymbols is the
	// pre-fold snapshot taken above.
	//
	// fsv stays out of `violations` too: the symbol resolves repo-wide, just not
	// in this file's scope — the opposite of "not found in the indexed code".
	// Own section below.
	fsv, fsReason := fileScopeViolationsWithReason(lang, fileLines, diffs[0], repoSymbols, filePath)
	if fsReason == "oversized-pre-edit-file" {
		// readFileLines (unlike wholeFileText, duplicate.go) collapses "file
		// doesn't exist yet" and "file exists but is oversized/unreadable"
		// into the same nil, so fileScopeViolationsWithReason can't tell them
		// apart either. A brand-new file (this edit's Write creates it) is
		// DEFINITIVELY empty, not degraded — re-derive which case this was
		// so an ordinary new-file Write doesn't spuriously surface the
		// strict-mode "coverage was incomplete" advisory (#330 code review).
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			fsReason = ""
		}
	}
	fileScopeResult := CheckResult{Check: "file-scope", Verdict: VerdictSkipped}
	if fileScopeEnabled() && lang == guard.LangPython {
		fileScopeResult = classifyResult("file-scope", len(fsv) > 0, fsReason)
	}
	results = append(results, fileScopeResult)

	// Call-shape agreement (RUNECHO_GUARD_CALLSHAPE=1, default off): a keyword
	// argument the declaration does not accept. Kept out of `violations` on purpose
	// — a Violation means "this name does not resolve", and folding a
	// resolves-but-misused finding into that list would make the ask's first line
	// ("not found in the indexed code") false. It gets its own section below, like
	// dangling and duplicate do. Store-free: it resolves against the same file's own
	// declarations, so nothing here touches the index or the ~12 ms budget beyond one
	// tree-sitter parse, and only when the diff has a kwarg-bearing candidate call.
	callShapes, callShapeReason := callShapeMismatchesWithReason(lang, fileLines, diffs[0], edit.ToolName, removedText)
	callShapeResult := CheckResult{Check: "call-shape", Verdict: VerdictSkipped}
	if callShapeEnabled() && lang == guard.LangPython {
		// preEditReason is NOT folded in here: this check reads its own
		// declaration source (added lines for a Write, fileLines otherwise) and
		// already reports "oversized-pre-edit-file" itself when that source is
		// empty AND the edit had a candidate — which is the narrower, more
		// accurate answer than the shared per-edit one.
		//
		// It IS consulted for the one thing the guard package cannot see: the
		// check receives a nil declaration source for a file that does not
		// exist and for one that is unreadable alike (readFileLines collapses
		// them), and only the second is lost coverage. preEditReason has
		// already made that os.Stat call, so an empty preEditReason on a
		// non-Write edit means the file is definitively empty — the same
		// re-derivation the file-scope arm above does, reusing the one stat
		// rather than making a third.
		if callShapeReason == "oversized-pre-edit-file" && preEditReason == "" {
			callShapeReason = ""
		}
		callShapeResult = classifyResult("call-shape", len(callShapes) > 0, callShapeReason)
	}

	// Pre-write ruff lint substrate (RUNECHO_GUARD_LINT=1, default off; #333):
	// F821 (undefined name) / F811 (redefinition), the same two questions the
	// additive check already answers, run against a TRUSTED whole-file linter
	// instead of the guard's own symbol resolution. Write-only — an
	// Edit/MultiEdit hunk would need a disk read spliced with new_string,
	// which races another session's concurrent write (this repo's own
	// working pattern) and would poison fpaudit's git-history oracle with a
	// file state that never existed on disk. Write's content already IS the
	// whole proposed file (see hookText above), so no splice is needed.
	var lintFindingsList []lintFinding
	lintResult := CheckResult{Check: "lint", Verdict: VerdictSkipped}
	if lintEnabled() && edit.ToolName == "Write" && lang == guard.LangPython {
		if _, err := exec.LookPath("ruff"); err != nil {
			// Stays Skipped — the tool isn't available, not that it failed to answer.
		} else {
			var reason string
			lintFindingsList, reason = lintFindingsWithReason(filePath, text)
			// Firewall against the additive check, the same shape file-scope
			// got in #269 and for the same reason: lint and guard.Run ask
			// overlapping questions (F821 IS "this name does not resolve"), so
			// a genuine hallucination is found by BOTH. Without this the ask
			// prints the name twice under two different headers and
			// decisionRecord.Symbols carries it twice — double-counting one
			// finding in the very telemetry the un-gating decision reads.
			// The additive check wins because it is the default-on one whose
			// false-positive rate is being measured; lint keeps only what the
			// additive check did NOT already report, which is exactly the
			// increment this substrate is meant to demonstrate.
			lintFindingsList = suppressAlreadyReported(lintFindingsList, violations)
			lintResult = classifyResult("lint", len(lintFindingsList) > 0, reason)
		}
	}
	results = append(results, lintResult)

	// Deletion-side checks (both gated OFF by default; dogfood-first). They share
	// the pre-edit text — removedText for Edit/MultiEdit, or the on-disk file for
	// Write, which replaces wholesale so the old file is the only record of what it
	// removes (best-effort read; the hook is PreToolUse, so the file is still old).
	// Both feed the single ask. Fail-open: any error yields no warning — but a
	// check that could NOT give a definitive answer is counted in degraded, so a
	// transient store error or an unreadable/oversized pre-edit file never
	// masquerades as a clean pass (silent by default; an advisory under strict).
	var dangling []danglingWarning
	var droppedImps []guard.DroppedImport
	var duplicates []duplicateWarning
	danglingResult := CheckResult{Check: "dangling", Verdict: VerdictSkipped}
	droppedResult := CheckResult{Check: "dropped-import", Verdict: VerdictSkipped}
	duplicateResult := CheckResult{Check: "duplicate-symbol", Verdict: VerdictSkipped}
	if danglingEnabled() || droppedImportEnabled() || duplicateEnabled() {
		// ONE definitive read of the pre-edit on-disk file, shared by every check
		// that needs it: E1/dropped-import's oldText for Write (the old file is
		// the only record of what a wholesale Write removes) and E5's whole-file
		// prior-definition set (any tool). The missing-vs-unreadable distinction
		// is wholeFileText's: a missing file means "" IS the pre-edit truth; an
		// existing file that is unreadable or over the cap means the pre-edit
		// state is unknown — the checks would run against a fabricated empty old
		// text and silently find nothing, so they are skipped and classified
		// Unknown individually below (#330 — previously a single shared
		// `degraded` counter, which could not say which check was affected).
		wholeOld, wholeDefinitive := "", true
		if edit.ToolName == "Write" || duplicateEnabled() {
			wholeOld, wholeDefinitive = wholeFileText(filePath)
		}
		oldText := removedText
		oldTextDefinitive := true
		if edit.ToolName == "Write" {
			oldText, oldTextDefinitive = wholeOld, wholeDefinitive
		}
		// E1: does this edit remove a definition that *other* files still reference?
		if danglingEnabled() {
			switch {
			case !oldTextDefinitive:
				danglingResult = CheckResult{Check: "dangling", Verdict: VerdictUnknown, Reason: "oversized-pre-edit-file"}
			default:
				var qErrs int
				if deleted := deletedDefs(lang, oldText, text); len(deleted) > 0 {
					dangling, qErrs = checkDanglingRefs(filepath.Dir(filePath), filePath, deleted)
				}
				danglingResult = classifyResult("dangling", len(dangling) > 0, storeQueryReason(qErrs))
			}
		}
		// Dropped-import: does this edit remove an import whose name the new text
		// still uses unqualified? Complements the additive check, which at edit time
		// still sees the old import on disk and so stays silent.
		if droppedImportEnabled() {
			if !oldTextDefinitive {
				droppedResult = CheckResult{Check: "dropped-import", Verdict: VerdictUnknown, Reason: "oversized-pre-edit-file"}
			} else {
				oldLines := hookOldLines(edit.ToolName, edit.OldString, edit.Edits, oldText)
				// newLines is hunk-only for Edit/MultiEdit, so its bound set can't see a
				// name rebound on an UNTOUCHED line elsewhere in the file (mirrors why
				// addInFileDefs folds whole-file defs into the additive check's known
				// set above). Fold the on-disk file's whole-file binding context in as
				// preBound so such a rebind still suppresses the false positive. Not
				// needed for Write: its newLines already IS the whole file.
				var preBound map[string]struct{}
				if edit.ToolName != "Write" {
					preBound = wholeFileBoundNames(fileLines, lang)
				}
				// newLines is the same slice diffs[0].AddedLines was built from, so
				// diffs[0].PyDefSigDepthByLine's synthetic-line-number keys (#294)
				// apply directly here — reused rather than recomputed via
				// hookDefSigDepthByLine a second time.
				defSigSeed := func(lineNo int) int { return diffs[0].PyDefSigDepthByLine[lineNo] }
				droppedImps = guard.DroppedImportRefsLinesWithBound(lang, oldLines, newLines, preBound, defSigSeed)
				// No check-specific reason: every decline inside
				// DroppedImportRefsLinesWithBound is definitive (the import
				// survived in the new text, or the name is rebound there), so
				// this check has no candidate-level abstain to report (#359).
				// preEditReason still applies: for an Edit/MultiEdit, preBound
				// above is built from fileLines, so an unreadable pre-edit file
				// leaves a rebind on an untouched line invisible.
				droppedResult = classifyResult("dropped-import", len(droppedImps) > 0, preEditReason)
			}
		}
		// E5: does this edit introduce a symbol not previously defined anywhere in
		// this file, whose name is already defined in a DIFFERENT file? Uses the
		// whole pre-edit file (wholeOld), not oldText/removedText — see
		// wholeFileText's doc comment for why the hunk-scoped variable above is
		// not reusable here.
		if duplicateEnabled() {
			switch {
			case !wholeDefinitive:
				duplicateResult = CheckResult{Check: "duplicate-symbol", Verdict: VerdictUnknown, Reason: "oversized-pre-edit-file"}
			default:
				var qErrs int
				if added := addedDefs(lang, wholeOld, text); len(added) > 0 {
					duplicates, qErrs = checkDuplicateDefs(lang, filepath.Dir(filePath), filePath, added,
						goBuildConstrained(filePath, wholeOld, text))
				}
				duplicateResult = classifyResult("duplicate-symbol", len(duplicates) > 0, storeQueryReason(qErrs))
			}
		}
	}
	results = append(results, danglingResult, droppedResult, duplicateResult, callShapeResult)
	return verification{
		Edit:          edit,
		Path:          filePath,
		Lang:          lang,
		Lookup:        res,
		Contract:      cw,
		Results:       results,
		Violations:    violations,
		LearnEligible: learnEligible,
		FileScope:     fsv,
		Qualified:     qualifiedV,
		DepsGo:        depsGoV,
		Dangling:      dangling,
		Dropped:       droppedImps,
		Duplicates:    duplicates,
		CallShapes:    callShapes,
		Lint:          lintFindingsList,
	}
}
