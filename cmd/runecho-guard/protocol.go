package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/guard"
)

// protocol.go — #394's versioned verification protocol: `runecho-guard
// --protocol`, an edit on stdin, a verdict document on stdout.
//
// Why it exists: the guard spoke Claude Code's PreToolUse JSON and nothing else,
// so that schema was also RunEcho's only verification interface. A CI step, a
// different agent surface or an autonomous harness had to impersonate a hook
// payload to ask a question the guard already knew the answer to. Here the hook
// is one renderer over verifyEdit (verify.go) and this is another.
//
// Named --protocol, not --verify: `runecho-ir verify` already means something
// different (diff the session-start snapshot against live ir.json), and one
// toolchain should not spell two unrelated questions the same way. Unrelated to
// internal/mcp's DefaultProtocolVersion, which is an MCP spec revision date
// governed by an external spec; this one is an integer document version RunEcho
// owns.

// protocolVersion is the document version. See TECHNICAL.md for the
// compatibility rule this number promises; in short, additive changes keep it at
// 1 and anything a consumer could be broken by forces 2.
const protocolVersion = 1

// Document-level error tokens. Each means "no document could be produced", which
// is a different thing from a document full of violations — see the exit-code
// note on runProtocolMode.
const (
	errUnsupportedProtocol = "unsupported-protocol"
	errMalformedInput      = "malformed-input"
	errMissingPath         = "missing-path"
	errRelativePath        = "relative-path"
	errBadPath             = "bad-path"
	errAmbiguousEdit       = "ambiguous-edit"
	errMissingEdit         = "missing-edit"
	errEmptyHunks          = "empty-hunks"
	errPanic               = "panic"
)

// protocolInput is the request. Content is a *string so that `"content": ""` —
// a whole-file deletion, which is a real edit with real consequences for
// dangling and dropped-import — is distinguishable from content being absent.
type protocolInput struct {
	Protocol int     `json:"protocol"`
	Path     string  `json:"path"`
	Content  *string `json:"content,omitempty"`
	Hunks    []struct {
		Old string `json:"old"`
		New string `json:"new"`
	} `json:"hunks,omitempty"`
}

// protocolDoc is the response. `results` always carries every check this binary
// knows, exactly once, in checkOrder — a consumer never has to distinguish
// "absent" from "skipped", which is precisely the ambiguity decisions.jsonl's
// checks map does have (a pre-commit record legitimately omits seven).
type protocolDoc struct {
	Protocol int              `json:"protocol"`
	Error    string           `json:"error,omitempty"`
	Path     string           `json:"path,omitempty"`
	Lang     string           `json:"lang,omitempty"`
	Index    *protocolIndex   `json:"index"`
	Results  []protocolResult `json:"results,omitempty"`
}

// protocolIndex is what the verdicts were judged against. It is here because a
// stale snapshot is the one coverage loss no per-check verdict can express: every
// check can run to completion, report ok, and still be answering about code as it
// was a week ago. The guard's own RUNECHO_GUARD_MAX_AGE is deliberately not
// exported — a consumer's staleness policy is the consumer's.
type protocolIndex struct {
	Repo       string `json:"repo"`
	SnapshotAt string `json:"snapshot_at"`
}

type protocolResult struct {
	Check   string `json:"check"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"`
	// Class splits an `unknown` into "gate" (the check saw a candidate and
	// declined it on its own precision gate) and "degraded" (the input or the
	// environment was missing, so coverage was genuinely lost). Both are
	// recorded as unknown in decisions.jsonl, but only the degraded class feeds
	// the hook's strict "coverage was incomplete" advisory — so a consumer told
	// only "unknown" cannot reproduce the guard's own judgement. Present iff
	// verdict is unknown.
	Class    string             `json:"class,omitempty"`
	Evidence []protocolEvidence `json:"evidence,omitempty"`
}

// protocolEvidence is one finding. The common core is `symbol`; everything else
// is per-check and documented in TECHNICAL.md.
//
// This is where findings and verdicts meet — as JSON, in a renderer, NOT in
// CheckResult. checkresult.go's standing decision is that four of the eleven
// checks carry richer shapes than guard.Violation and a shared Go field would
// either lose data or widen guard.Violation for every in-process consumer. That
// argument is about a shared Go type; a wire format has no such constraint,
// because each check renders through its own arm below.
type protocolEvidence struct {
	Symbol string `json:"symbol"`
	Line   int    `json:"line,omitempty"`
	// LineSpace says what Line counts, and is REQUIRED whenever Line is set.
	// "snippet" numbers the edit hunk (line 1 is the first added line, and for a
	// multi-hunk edit the hunks are counted gap-joined, exactly as the hook's ask
	// does); "file" numbers the file as proposed. Omitting it sends a consumer —
	// and then an agent — to the wrong line, which is the DeclLineIsSnippet
	// lesson from callshapecheck.go applied to every check rather than one.
	LineSpace string `json:"line_space,omitempty"`

	Suggestions []string `json:"suggestions,omitempty"`
	Referrers   []string `json:"referrers,omitempty"`
	Locations   []string `json:"locations,omitempty"`

	Keyword       string   `json:"keyword,omitempty"`
	Accepted      []string `json:"accepted,omitempty"`
	DeclLine      int      `json:"decl_line,omitempty"`
	DeclLineSpace string   `json:"decl_line_space,omitempty"`

	Rule    string `json:"rule,omitempty"`
	Message string `json:"message,omitempty"`
}

// lineSpaceFor reports what an added-line number counts for this tool. A Write
// carries the whole proposed file, so its line 1 IS file line 1; an Edit or
// MultiEdit carries only the hunk.
func lineSpaceFor(toolName string) string {
	if toolName == "Write" {
		return "file"
	}
	return "snippet"
}

// abstainClass splits an Unknown's reason into the gate/degraded classes
// countDegradedUnknown already distinguishes internally. Membership is opt-in
// there and an unregistered reason counts as degraded, so this agrees with the
// hook's advisory by construction rather than by a second hand-kept list.
func abstainClass(reason string) string {
	if _, gate := gateAbstainReasons[reason]; gate {
		return "gate"
	}
	return "degraded"
}

// decodeProtocolInput reads and validates one request, returning the edit shape
// the core consumes. The error token is document-level: every case here means no
// verdicts could be produced at all, as opposed to a verdict of "unknown".
func decodeProtocolInput(in io.Reader) (hookEdit, string, string) {
	var pi protocolInput
	if err := json.NewDecoder(in).Decode(&pi); err != nil {
		return hookEdit{}, "", errMalformedInput
	}
	if pi.Protocol != protocolVersion {
		return hookEdit{}, "", errUnsupportedProtocol
	}
	switch {
	case pi.Path == "":
		return hookEdit{}, "", errMissingPath
	case !filepath.IsAbs(pi.Path):
		// Repo resolution, the contract root and ruff's --stdin-filename are all
		// keyed on the path; a relative one would resolve against whatever
		// directory the caller happened to be in.
		return hookEdit{}, "", errRelativePath
	case strings.ContainsRune(pi.Path, 0) || len(pi.Path) > 4096:
		// The hook's own rule, applied at the door rather than as a bail.
		return hookEdit{}, "", errBadPath
	}
	hasContent, hasHunks := pi.Content != nil, pi.Hunks != nil
	switch {
	case hasContent && hasHunks:
		return hookEdit{}, "", errAmbiguousEdit
	case !hasContent && !hasHunks:
		return hookEdit{}, "", errMissingEdit
	case hasHunks && len(pi.Hunks) == 0:
		return hookEdit{}, "", errEmptyHunks
	}
	if hasContent {
		return hookEdit{ToolName: "Write", Content: *pi.Content}, pi.Path, ""
	}
	// One hunk is an Edit, not a one-element MultiEdit. The promise is that the
	// same edit gets the same verdicts on every surface, and a single-hunk edit
	// arriving through Claude Code IS an Edit: editFingerprint, hookOldLines and
	// the empty-new_string filtering all branch on the tool name, so matching it
	// is what makes parity hold by construction rather than by inspection.
	if len(pi.Hunks) == 1 {
		return hookEdit{ToolName: "Edit", OldString: pi.Hunks[0].Old, NewString: pi.Hunks[0].New}, pi.Path, ""
	}
	edits := make([]editOp, 0, len(pi.Hunks))
	for _, h := range pi.Hunks {
		edits = append(edits, editOp{OldString: h.Old, NewString: h.New})
	}
	return hookEdit{ToolName: "MultiEdit", Edits: edits}, pi.Path, ""
}

// degradedReasonFor names why a store-dependent check could not answer, reusing
// the defer-reason vocabulary TECHNICAL.md already documents rather than minting
// protocol-only tokens a reader would have to learn twice.
func degradedReasonFor(res lookupResult) string {
	switch {
	case res.Warn != "":
		return "schema-newer"
	case res.NoRepo:
		return "no-repo"
	default:
		return "store-degraded"
	}
}

// renderProtocol turns a verification into the document. Every path produces all
// of checkOrder: a check that did not report is rendered skipped rather than
// omitted, so "absent" is never something a consumer has to interpret.
func renderProtocol(v verification) protocolDoc {
	doc := protocolDoc{Protocol: protocolVersion, Path: v.Path, Lang: string(v.Lang)}

	byCheck := map[string]protocolResult{}
	add := func(r protocolResult) { byCheck[r.Check] = r }

	switch v.Bail {
	case bailEmptyInput, bailUnknownLang:
		// Nothing ran, and nothing COULD have: there is no text to judge, or no
		// parser for this path. That is skipped, not unknown — no coverage was
		// lost, because there was none to lose.
		for _, name := range checkOrder {
			add(protocolResult{Check: name, Verdict: VerdictSkipped.String(), Reason: v.Bail})
		}
	case bailDegradedStore:
		// The nine store-dependent checks genuinely could not answer, which is
		// exactly the abstention the hook renders as silence. The two store-free
		// ones did answer; asking storeFreeChecks here is why #394 split it out.
		reason := degradedReasonFor(v.Lookup)
		for _, name := range checkOrder {
			add(protocolResult{
				Check:   name,
				Verdict: VerdictUnknown.String(),
				Reason:  reason,
				Class:   abstainClass(reason),
			})
		}
		shapes, lints, results := storeFreeChecks(v.Lookup, v.Edit, v.Path, v.Lang, v.RemovedText)
		for _, r := range results {
			add(protocolResultFor(r, v, shapes, lints))
		}
	default:
		for _, r := range v.Results {
			add(protocolResultFor(r, v, v.CallShapes, v.Lint))
		}
		if v.Lookup.OK {
			doc.Index = &protocolIndex{Repo: v.Lookup.RepoName, SnapshotAt: v.Lookup.Latest.UTC().Format(time.RFC3339)}
		}
	}

	doc.Results = make([]protocolResult, 0, len(checkOrder))
	for _, name := range checkOrder {
		r, ok := byCheck[name]
		if !ok {
			// Structural guarantee, not a fallback anyone expects to hit: the
			// "every check, exactly once" promise must hold even if a future
			// check forgets to append its result.
			r = protocolResult{Check: name, Verdict: VerdictSkipped.String()}
		}
		doc.Results = append(doc.Results, r)
	}
	return doc
}

// protocolResultFor renders one CheckResult plus whatever findings belong to it.
// Evidence is attached only for a violation: an ok verdict has nothing to show,
// and an unknown's information is its reason and class.
func protocolResultFor(r CheckResult, v verification, shapes []guard.CallShapeMismatch, lints []lintFinding) protocolResult {
	out := protocolResult{Check: r.Check, Verdict: r.Verdict.String(), Reason: r.Reason}
	if r.Verdict == VerdictUnknown {
		out.Class = abstainClass(r.Reason)
	}
	if r.Verdict != VerdictViolation {
		return out
	}
	space := lineSpaceFor(v.Edit.ToolName)
	switch r.Check {
	case "violations", "recv-method", "var-type":
		// The merged slice, as the ask renders it. recv-method and var-type
		// append into Violations rather than carrying their own, so all three
		// names project the same source here.
		out.Evidence = violationEvidence(v.Violations, space)
	case "qualified":
		out.Evidence = violationEvidence(v.Qualified, space)
	case "deps-go":
		out.Evidence = violationEvidence(v.DepsGo, space)
	case "file-scope":
		out.Evidence = violationEvidence(v.FileScope, space)
	case "dangling":
		for _, d := range v.Dangling {
			out.Evidence = append(out.Evidence, protocolEvidence{
				Symbol:    d.Symbol,
				Referrers: sanitizeReasonPaths(d.Referrers),
			})
		}
	case "duplicate-symbol":
		for _, d := range v.Duplicates {
			out.Evidence = append(out.Evidence, protocolEvidence{
				Symbol:    d.Symbol,
				Locations: sanitizeReasonPaths(d.Locations),
			})
		}
	case "dropped-import":
		for _, d := range v.Dropped {
			out.Evidence = append(out.Evidence, protocolEvidence{
				Symbol: d.Name, Line: d.LineNo, LineSpace: space,
			})
		}
	case "call-shape":
		for _, m := range shapes {
			e := protocolEvidence{
				Symbol: m.Callee, Line: m.LineNo, LineSpace: space,
				Keyword: m.Keyword, Accepted: m.Accepted,
				DeclLine: m.DeclLine, Suggestions: m.Suggestions,
			}
			// The declaration can live in a different space from the call: it is
			// read from the ADDED text when this edit rewrites the signature, and
			// from the file otherwise. Reporting one space for both is what
			// DeclLineIsSnippet exists to prevent.
			if m.DeclLineIsSnippet {
				e.DeclLineSpace = "snippet"
			} else {
				e.DeclLineSpace = "file"
			}
			out.Evidence = append(out.Evidence, e)
		}
	case "lint":
		for _, l := range lints {
			// Always "file": ruff is handed the proposed content and reports
			// against it, so its line numbers are the file's regardless of tool.
			out.Evidence = append(out.Evidence, protocolEvidence{
				Symbol: l.Symbol, Line: l.Line, LineSpace: "file",
				Rule: l.Rule, Message: l.Message,
			})
		}
	}
	return out
}

func violationEvidence(vs []guard.Violation, space string) []protocolEvidence {
	out := make([]protocolEvidence, 0, len(vs))
	for _, v := range vs {
		out = append(out, protocolEvidence{
			Symbol: v.Symbol, Line: v.Line, LineSpace: space, Suggestions: v.Suggestions,
		})
	}
	return out
}

// protocolErrorDoc is the whole response when no verdicts could be produced.
func protocolErrorDoc(token string) protocolDoc {
	return protocolDoc{Protocol: protocolVersion, Error: token}
}

// writeProtocolDoc emits one document, newline-terminated so a consumer reading
// a stream of them can split on lines.
func writeProtocolDoc(out io.Writer, doc protocolDoc) {
	b, err := json.Marshal(doc)
	if err != nil {
		fmt.Fprintf(out, "{\"protocol\":%d,\"error\":%q,\"index\":null}\n", protocolVersion, errMalformedInput)
		return
	}
	fmt.Fprintf(out, "%s\n", b)
}

// runProtocolMode answers one request on stdin with one document on stdout.
//
// Exit 0 means a document was written; it is NOT a verdict. A document full of
// violations exits 0, because "the guard has an opinion" and "the guard could
// not be asked" are different failures and a consumer's policy is its own. Exit
// 2 means no document of verdicts could be produced and an error document was
// written instead.
//
// It ignores RUNECHO_GUARD_SKIP (that bypasses ENFORCEMENT; this enforces
// nothing, and a question asked deserves an answer) and RUNECHO_GUARD_STRICT
// (there is nothing to escalate — `class` already carries what strict would have
// told a human). It honours every per-check RUNECHO_GUARD_* gate, because a
// verdict that disagreed with the hook on the same machine would be worse than
// no verdict at all.
func runProtocolMode(in io.Reader, out io.Writer) int {
	edit, path, errTok := decodeProtocolInput(in)
	if errTok != "" {
		writeProtocolDoc(out, protocolErrorDoc(errTok))
		return 2
	}
	v := protocolVerifyBody(edit, path, "")
	if v.Bail == bailBadPath {
		// decodeProtocolInput already rejects these, so reaching here means the
		// two disagree. Answering with an error document rather than eleven
		// skipped results keeps that a loud contradiction instead of a quiet one.
		writeProtocolDoc(out, protocolErrorDoc(errBadPath))
		return 2
	}
	writeProtocolDoc(out, renderProtocol(v))
	return 0
}

// protocolVerifyBody indirects verifyEdit through a var so a test can substitute
// a panicking body and prove the barrier is WIRED, not merely present. Same seam
// as runPreCommitBody.
var protocolVerifyBody = verifyEdit

// protocolPanicBarrier is deferOnPanic's counterpart for this mode, and it
// deliberately does the OPPOSITE on panic. deferOnPanic writes nothing and exits
// 0, which is right for a hook (say nothing, block nothing) and wrong here: a
// consumer that asked a question and got an empty stdout with a success code
// would read it as "no findings". Say so instead.
func protocolPanicBarrier(out io.Writer, fn func(io.Writer) int) (code int) {
	defer func() {
		if r := recover(); r != nil {
			warnf("protocol mode panicked: %v", r)
			writeProtocolDoc(out, protocolErrorDoc(errPanic))
			code = 2
		}
	}()
	return fn(out)
}
