package guard

import (
	"regexp"
	"strings"
)

// The call-shape AGREEMENT check (#243 slice 1): a call passes a keyword argument
// the declaration does not accept. callshape.go extracts the call-side shape,
// callshapedecl.go the declaration-side shape; this file is the comparison.
//
// SCOPE: Python, keyword names only, and only against a declaration in the SAME
// FILE as the call. The same-file restriction is the whole reason this can be
// stated as a fact rather than a guess:
//
//   - An unqualified `foo(...)` whose `def foo` is in this same file needs no
//     import resolution to know which foo it means.
//   - A cross-file match would be NAME-keyed only. The index cannot say whether
//     this file's `foo` is the repo's `foo` or one imported from a third-party
//     package that happens to share the name, and answering that is import/scope
//     resolution — out of scope for #243 per the plan, and the same hard ceiling
//     the step-2 spike recorded.
//
// Measured cost of that restriction, CPython-ast oracle over three indexed repos:
// same-file covers 21.2% / 3.3% / 15.0% of kwarg-bearing unqualified call sites,
// which is 53% / 13% / 75% of the population a full (cross-file) checker could
// reach. Cross-file reach is deliberately left on the table until import
// resolution exists to earn it.
//
// Everything ambiguous abstains. This check's credibility dies on its first false
// positive faster than it grows on its tenth true one.

// CallShapeMismatch is one keyword argument that a call passes and the
// declaration it resolves to does not accept.
type CallShapeMismatch struct {
	// Callee is the unqualified function name.
	Callee string
	// Keyword is the offending keyword-argument name.
	Keyword string
	// LineNo is the 1-based added-line number of the call's callee identifier.
	LineNo int
	// DeclLine is the 1-based line of the `def` this was compared against.
	DeclLine int
	// DeclLineIsSnippet reports that DeclLine numbers the EDIT HUNK, not the file:
	// it is set when the signature was read from the added text (because this edit
	// rewrites it), where line 1 is the first added line. Printing both cases as a
	// file line sent the reader to the wrong place — the call site is already
	// labelled "snippet line", and the declaration now says which it is too.
	DeclLineIsSnippet bool
	// Accepted are the keyword names the declaration does accept, in source order
	// and capped — enough to fix the call from the message alone.
	Accepted []string
	// Suggestions are the accepted names closest to Keyword by edit distance,
	// nearest first, when near enough to be a likely typo (nil if none). Same
	// model-free suggester the additive check uses, so `timeuot=` points at
	// `timeout`.
	Suggestions []string
}

const (
	// callShapeMaxMismatches caps reported mismatches per edit. A single ask that
	// lists more than a handful stops being actionable, and the same cap keeps a
	// pathological diff from building an unbounded message.
	callShapeMaxMismatches = 5
	// callShapeMaxAccepted caps how many accepted keyword names are listed in one
	// finding. A 30-parameter signature would otherwise bury the point.
	callShapeMaxAccepted = 8
	// callShapeMaxUnreliableNames caps how many DISTINCT unreadable callee names
	// are kept for the abstain-reason test (#359). Each costs one regex match
	// against the added text, and one is enough to record the reason.
	callShapeMaxUnreliableNames = 8
)

// rePyDefNameTmpl matches a Python `def NAME(` (optionally `async`, any
// indentation) so the check can tell that a name's SIGNATURE is being touched by
// this very edit, as opposed to merely being called by it.
const rePyDefNameTmpl = `(?m)^[ \t]*(?:async[ \t]+)?def[ \t]+%s[ \t]*\(`

// rePyTopLevelDefNameTmpl is rePyDefNameTmpl restricted to COLUMN ZERO — the
// only defs an unqualified call can reach, and the same set pyDeclIndex.shapesFor
// indexes.
//
// The two are not interchangeable and the difference is load-bearing. Control
// flow uses the indented-permitting form: a hunk that rewrites a nested or
// method signature makes the on-disk copy stale, and judging the call against
// it would risk a false positive. The ABSTAIN REASON uses this one: a hunk that
// merely adds `class C:` with a method named `notify` must not make an
// unqualified `notify()` — which resolves to an import — look like a candidate
// this check declined. Round one of #363's review closed that hole for
// module-level declarations; round two found it still open through indentation.
const rePyTopLevelDefNameTmpl = `(?m)^(?:async[ \t]+)?def[ \t]+%s[ \t]*\(`

// PyCallShapeMismatches reports keyword arguments passed by calls in fd's added
// lines that the same file's module-level declaration does not accept.
//
// wholeFile is the pre-edit on-disk file as lines (nil when unreadable — the
// check then returns nothing, since the declaration is invisible). removed is the
// text this edit deletes, needed to detect that the declaration's own signature
// is being rewritten by this edit. addedIsWholeFile must be true only when fd's
// added lines ARE the complete post-edit file (the Write tool), in which case
// they are the authoritative declaration source and wholeFile is not consulted.
//
// Returns nil for any language other than Python.
//
// Thin wrapper over PyCallShapeMismatchesWithReason, discarding the abstain
// reason — kept so every existing caller is untouched by #359.
func PyCallShapeMismatches(lang Lang, wholeFile []AddedLine, fd FileDiff, removed []AddedLine, addedIsWholeFile bool) []CallShapeMismatch {
	ms, _ := PyCallShapeMismatchesWithReason(lang, wholeFile, fd, removed, addedIsWholeFile)
	return ms
}

// PyCallShapeMismatchesWithReason is PyCallShapeMismatches plus the reason a
// candidate call was declined. A candidate is a kwarg-bearing call in the added
// lines — the only thing this check can adjudicate — so an edit with no such
// call yields no reason no matter what else it contains:
//
//   - "oversized-pre-edit-file" / "dynamic-binding" (degraded class): the
//     declaration source was unreadable, or the file rebinds names in a way the
//     decl index cannot follow.
//   - "unreliable-call-shape": the extractor says its own kwarg parse for this
//     call is not trustworthy (a lambda argument, an unbalanced span).
//   - "shadowed-callee", "nested-def-shadow", "ambiguous-decl-shapes",
//     "unusable-decl-shape", "unparseable-added-signature",
//     "decl-edited-in-hunk": resolveDeclShape found more than one defensible
//     answer for the callee's signature and refused to guess between them.
//
// A callee with NO module-level declaration in this file is not a decline: it
// is a cross-file or third-party call, owned by the existence and file-scope
// checks, and it is the common case for kwarg-bearing calls. Counting it would
// make this reason fire on most Python edits while saying nothing about this
// check's coverage.
//
// First reason encountered wins, matching GoDepQualifiedViolationsWithReason.
func PyCallShapeMismatchesWithReason(lang Lang, wholeFile []AddedLine, fd FileDiff, removed []AddedLine, addedIsWholeFile bool) ([]CallShapeMismatch, string) {
	if lang != LangPython {
		return nil, ""
	}
	added := fd.AddedLines
	if len(added) == 0 {
		return nil, ""
	}
	// The declaration source. For Write, the added lines are the whole post-edit
	// file, so they are strictly better than the pre-edit copy: a signature this
	// very Write changes is already reflected. For Edit/MultiEdit the hunk is
	// partial, so the pre-edit file is the only whole-file view available.
	declLines := wholeFile
	if addedIsWholeFile {
		declLines = added
	}
	// Narrow to calls that could possibly be checked BEFORE anything touches the
	// whole file. Ordering matters for latency, not just tidiness: this scan reads
	// only the hunk, while both the abstain scan and the declaration parse below
	// read every line of the file. Most edits have no candidate at all, and those
	// must cost the hunk scan and nothing more. Measured: reversing this order made
	// a candidate-free edit to a 2677-line file cost 0.37 ms instead of 0.02 ms.
	// A `**splat` at the call is deliberately NOT an abstention here, though the
	// first draft made it one. `foo(bogus=1, **cfg)` is a TypeError in exactly the
	// same way `foo(bogus=1)` is: a splat ADDS keywords, it never excuses an
	// explicitly named one. Abstaining on it cost reach and bought nothing, and the
	// mutation harness is what exposed it — the abstain had no fixture that could
	// fail, because a call whose only argument is `**cfg` is already covered by the
	// no-kwargs arm below. A splat DOES matter to an arity or required-argument
	// check, so this reasoning does not carry to a later slice.
	var candidates []CallShape
	var reason string
	// unreliableNames are kwarg-bearing calls the extractor could not read.
	// Their NAMES are kept, not just a bool: whether such a call is a candidate
	// this check declined depends on whether this file declares the callee at
	// all, and that question cannot be answered until the declaration index
	// exists further down. Capped because it feeds a per-name regex below and a
	// pathological diff must not turn that into an unbounded scan.
	//
	// DISTINCT names, via seenUnreliable: the cap counting duplicates let eight
	// repeats of one imported callee fill the list and crowd out the single
	// declared-in-file name that actually earns the reason, which is the only
	// name in it that can (found by review of #363).
	var unreliableNames []string
	seenUnreliable := map[string]struct{}{}
	for _, c := range ExtractCallShapes(lang, added, seedFunc(lang, fd)) {
		switch {
		case len(c.Kwargs) == 0:
			// Nothing to compare — positional arity is a later slice.
		case c.HasLambda, c.Unreliable:
			// The extractor says its own Pos/Kwargs are not trustworthy here.
			if _, dup := seenUnreliable[c.Name]; !dup && len(unreliableNames) < callShapeMaxUnreliableNames {
				seenUnreliable[c.Name] = struct{}{}
				unreliableNames = append(unreliableNames, c.Name)
			}
		default:
			candidates = append(candidates, c)
		}
	}
	// An edit with no readable candidate returns here even when it holds an
	// unreliable one. Deciding whether that unreliable call was ours needs the
	// whole-file declaration index, and building it costs a full-file pass —
	// the exact cost the candidate scan above is ordered to avoid (0.02 ms
	// becomes 0.37 ms on a 2677-line file). A reason is diagnostic; it does not
	// get to buy itself a scan the check would not otherwise run. Consequence,
	// stated rather than implied: an edit whose ONLY kwarg call is unreadable
	// records no reason.
	if len(candidates) == 0 {
		return nil, ""
	}
	// Checked here rather than beside declLines' assignment above so a degraded
	// declaration source is reported only when this edit actually had something
	// to check — an edit with no kwarg-bearing call is not "coverage lost", it
	// is out of shape. The test itself is O(1), so the latency ordering the
	// candidate scan protects is unaffected.
	if len(declLines) == 0 {
		return nil, "oversized-pre-edit-file"
	}

	// ONE masked pass over the declaration source yields everything the whole file
	// has to say: where each column-zero def sits, every def's parameter names, and
	// whether the file contains a name-rebinding construct. Doing it in one pass is a
	// latency requirement — see pyDeclIndex.
	declIndex := newPyDeclIndex(declLines)
	if declIndex.dynamic {
		return nil, "dynamic-binding"
	}
	// The added text is the fresher declaration when this edit rewrites one. It is a
	// hunk here (the whole-file case is addedIsWholeFile, already folded into
	// declLines), so a second index over it is cheap.
	var addedIndex *pyDeclIndex
	addedText := ""
	if !addedIsWholeFile {
		addedText = linesText(added)
		addedIndex = newPyDeclIndex(added)
		if addedIndex.dynamic {
			return nil, "dynamic-binding"
		}
	}
	removedText := linesText(removed)

	// Any binding of the callee name OTHER than a `def` makes the call's target
	// ambiguous: an import shadowing a same-named local def, a reassignment to a
	// wrapper, a parameter of an enclosing function.
	//
	// Built on FIRST USE, not here. It is the most expensive thing this check does
	// (two whole-file extractor passes, 3.6 ms on a 3000-line file), and it is only
	// needed once a candidate call actually resolves to a same-file declaration —
	// which most diffs never do, because most kwarg-bearing calls go to imported or
	// third-party functions this file does not declare.
	var shadowed map[string]struct{}
	shadowedSet := func() map[string]struct{} {
		if shadowed == nil {
			// declLines is always whole-file-from-line-1 (see its own comment
			// above) — nil is correct there. added is the hunk when it isn't
			// the whole file, and needs PyDeclaredNames' own bracket-depth
			// seed (#294) or a kwarg on a wrapped call whose opener sits in
			// unchanged context above the hunk misreads as a rebind, silently
			// widening (not narrowing) the shadow set this check relies on.
			shadowed = pyNonDefBindings(declLines, declIndex, nil)
			if addedIndex != nil {
				for n := range pyNonDefBindings(added, addedIndex, bracketDepthSeedFunc(lang, fd)) {
					shadowed[n] = struct{}{}
				}
			}
		}
		return shadowed
	}

	var out []CallShapeMismatch
	// checked memoises the per-name resolution so N calls to one function cost one
	// resolution. ok=false means "abstain", cached so the abstain reasoning (and
	// its regex work) is not repeated either.
	type resolved struct {
		shape pyDeclShape
		ok    bool
		// fromAdded records that shape came from the hunk, so its Line numbers the
		// hunk rather than the file.
		fromAdded bool
		// why is the abstain reason when ok is false, "" when it resolved or
		// when the callee simply is not declared in this file (#359).
		why string
	}
	checked := make(map[string]resolved, len(candidates))
	for _, c := range candidates {
		if len(out) >= callShapeMaxMismatches {
			break
		}
		r, seen := checked[c.Name]
		if !seen {
			r.shape, r.ok, r.fromAdded, r.why = resolveDeclShape(c.Name, shadowedSet, declIndex, addedIndex, addedText, removedText, addedIsWholeFile)
			checked[c.Name] = r
		}
		if !r.ok {
			if r.why != "" && reason == "" {
				reason = r.why
			}
			continue
		}
		accepted := make(map[string]struct{}, len(r.shape.Keywords))
		for _, k := range r.shape.Keywords {
			accepted[k] = struct{}{}
		}
		reported := make(map[string]struct{}, len(c.Kwargs))
		for _, kw := range c.Kwargs {
			if _, okKw := accepted[kw]; okKw {
				continue
			}
			// A repeated bad keyword (`foo(bad=1, bad=2)`) is one problem, not two.
			if _, dup := reported[kw]; dup {
				continue
			}
			reported[kw] = struct{}{}
			m := CallShapeMismatch{
				Callee:            c.Name,
				Keyword:           kw,
				LineNo:            c.LineNo,
				DeclLine:          r.shape.Line,
				DeclLineIsSnippet: r.fromAdded,
				Accepted:          capNames(r.shape.Keywords, callShapeMaxAccepted),
			}
			if sug, found := Suggest(kw, accepted); found {
				m.Suggestions = sug
			}
			out = append(out, m)
			if len(out) >= callShapeMaxMismatches {
				break
			}
		}
	}
	// The unreadable-call reason is a FALLBACK, evaluated only once no candidate
	// produced one of its own. Two deliberate choices here:
	//
	//   - It is gated on declaredInFile, the same test resolveDeclShape uses.
	//     `notify(text=f"hi {name}")` where notify is imported sets Unreliable
	//     (an f-string prefix survives the mask), and reporting that would make
	//     this reason fire on ordinary Python for a callee this check never
	//     claimed — the failure the reasonIf gate exists to prevent, arrived at
	//     from the other direction.
	//   - It loses to a per-candidate decline rather than winning on scan order,
	//     because "this file's own declaration was ambiguous" says more about
	//     coverage than "one argument list was unreadable".
	if reason == "" {
		for _, name := range unreliableNames {
			if declaredInFile(name, declIndex, addedText, addedIsWholeFile) {
				reason = "unreliable-call-shape"
				break
			}
		}
	}
	return out, reason
}

// declaredInFile reports whether the declaration source declares name at module
// level, or this edit's own hunk declares it at module level. It is the "was
// this ever our candidate" test — every #359 abstain reason in this check is
// gated on it, so an imported or third-party callee (most kwarg-bearing calls)
// records nothing.
//
// Module level on BOTH halves: shapesFor is column-zero-only, and the hunk half
// uses matchesPyTopLevelDef for the reason given on rePyTopLevelDefNameTmpl.
func declaredInFile(name string, declIndex *pyDeclIndex, addedText string, addedIsWholeFile bool) bool {
	if len(declIndex.shapesFor(name)) > 0 {
		return true
	}
	return !addedIsWholeFile && matchesPyTopLevelDef(addedText, name)
}

// resolveDeclShape picks the single declaration shape for name that the call can
// be compared against, or reports ok=false to abstain. Every abstain path is a
// case where more than one answer is defensible, and the check's contract is that
// it never guesses between them.
//
// The fourth return names which abstain fired (#359), or "" when the shape
// resolved. It is also "" for every ok=false path on a name this file does not
// declare at all: an imported or third-party callee is not a candidate this
// check declined, it is one it never claimed — and since most kwarg-bearing
// calls go to imported functions, reporting those would leave the reason firing
// on nearly every Python edit while saying nothing about coverage.
func resolveDeclShape(
	name string,
	shadowedSet func() map[string]struct{},
	declIndex, addedIndex *pyDeclIndex,
	addedText string,
	removedText string,
	addedIsWholeFile bool,
) (shape pyDeclShape, ok bool, fromAdded bool, why string) {
	var zero pyDeclShape
	// Does this file declare the name at all? Every abstain below is reported
	// only when it does — see the doc comment. Computed from data the paths
	// below already need (the module-level shapes; whether the hunk declares
	// it), so it costs no extra scan.
	declShapes := declIndex.shapesFor(name)
	// Control flow: any indentation. A hunk rewriting a nested or method
	// signature still makes the on-disk copy stale for this edit.
	editDeclares := !addedIsWholeFile && matchesPyDef(addedText, name)
	// reasonIf keeps an abstain silent for a callee this file does not declare.
	// Deliberately a NARROWER test than editDeclares above — module-level only,
	// exactly declaredInFile's (which the unreadable-call fallback uses). An
	// indented def named like an imported callee is not a declaration this call
	// could ever reach, so it must not make the call look like our candidate.
	reasonIf := func(r string) string {
		if len(declShapes) > 0 || declaredInFile(name, declIndex, addedText, addedIsWholeFile) {
			return r
		}
		return ""
	}
	if _, bad := shadowedSet()[name]; bad {
		return zero, false, false, reasonIf("shadowed-callee")
	}
	// A nested or conditionally-declared def of the same name means the column-zero
	// signature may not be the one a call reaches. Methods are excluded — an
	// unqualified call cannot reach one. Checked on the declaration source only: the
	// hunk's own indentation is not a reliable guide to its enclosing block.
	if declIndex.shadowedByNestedDef(name) {
		return zero, false, false, reasonIf("nested-def-shadow")
	}
	// When the edit itself declares this name, its version of the signature is the
	// one the post-edit file will have — the indexed/on-disk copy is stale by
	// exactly this edit. Adding a parameter and using it in the same edit is a
	// routine agent move, so getting this wrong would be a recurring false
	// positive rather than a corner case.
	if editDeclares {
		added, ok := soleShape(addedIndex.shapesFor(name))
		if ok {
			// The hunk shows the parameter list but CANNOT show what sits above the
			// `def`. An Edit whose old_string begins at the def line leaves the
			// decorator in the unchanged part of the file, so decoratedAbove finds
			// nothing to walk and reports "undecorated" for a function a wrapper may
			// have replaced — a false positive, and on the intersection of two shapes
			// this code calls routine (editing a signature; decorated functions).
			// Fold the PRE-EDIT file's answer in, which is where the decorator still
			// is. Decoratedness is the only property that lives outside the signature,
			// so nothing else needs this treatment.
			//
			// An edit that REMOVES a decorator therefore over-abstains for one edit.
			// That is the safe direction and the rarer shape.
			for _, pre := range declShapes {
				if pre.Decorated {
					added.Decorated = true
					break
				}
			}
		}
		if !ok {
			// The hunk shows a `def name(` but no single parseable signature (a
			// truncated parameter list, or two disagreeing branches). Its true shape
			// is unknown and the on-disk copy is known-stale, so there is nothing to
			// compare against.
			// reasonIf, not a bare string: editDeclares above permits an
			// indented def, so this arm is reachable for a callee this file
			// does not declare at module level — which is not our candidate.
			return zero, false, false, reasonIf("unparseable-added-signature")
		}
		if !added.usable() {
			return added, false, true, reasonIf("unusable-decl-shape")
		}
		return added, true, true, ""
	}
	s, ok := soleShape(declShapes)
	if !ok {
		// No module-level def of this name in the file (a cross-file or third-party
		// callee — the existence and file-scope checks own that), or two
		// conditional branches with different accepted sets. Only the second is an
		// abstain; reasonIf collapses the first to "" (see the doc comment).
		return zero, false, false, reasonIf("ambiguous-decl-shapes")
	}
	if !s.usable() {
		// `**kwargs` accepts anything, a decorator can rewrite the signature, and
		// an unbalanced span was never read — none can be argued against.
		return zero, false, false, "unusable-decl-shape"
	}
	// Last abstain: this edit may be changing the declaration WITHOUT the hunk
	// containing a whole `def` line — a MultiEdit that rewrites one line of a
	// multi-line parameter list and then adds a call using the new keyword, or one
	// that deletes the declaration outright. The shape read above is pre-edit in
	// both cases, and would flag a valid call.
	//
	// A separate `matchesPyDef(removedText, name)` test stood here and was removed
	// as redundant, on the mutation harness's evidence: deleting or rewriting a
	// declaration necessarily removes a line of its own signature, so
	// removesSignatureLine already fires wherever that test would have, and no input
	// could distinguish them. The two remaining ways it could have mattered are both
	// unreachable — a shape whose signature span does not balance is already
	// Unknowable and returned above, and s.Line always exists in declLines because s
	// came from an index built over them.
	if removedText != "" && removesSignatureLine(declIndex, s.Line, removedText) {
		return zero, false, false, "decl-edited-in-hunk"
	}
	return s, true, false, ""
}

// soleShape returns the one shape in shapes, treating "several declarations that
// all accept the same keywords" as one — conditional `def` branches under
// `if TYPE_CHECKING:` or a try/except import fallback commonly repeat a signature
// verbatim, and abstaining on those would cost reach for no ambiguity. Branches
// that genuinely disagree return ok=false.
func soleShape(shapes []pyDeclShape) (pyDeclShape, bool) {
	if len(shapes) == 0 {
		return pyDeclShape{}, false
	}
	first := shapes[0]
	key := shapeKey(first)
	for _, s := range shapes[1:] {
		if shapeKey(s) != key {
			return pyDeclShape{}, false
		}
	}
	return first, true
}

// shapeKey renders the parts of a shape that decide whether two declarations
// agree. Line is excluded: two branches at different lines with the same accepted
// set are not an ambiguity.
func shapeKey(s pyDeclShape) string {
	return strings.Join(s.Keywords, ",") + "|" + boolChar(s.HasKwStar) + boolChar(s.Decorated) + boolChar(s.Unknowable)
}

func boolChar(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// matchesPyDef reports whether text contains a `def name(` at statement position.
// regexp.QuoteMeta is applied because name comes from repo text, which the #212
// red-team pass established is attacker-influenced input, not a trusted literal.
func matchesPyDef(text, name string) bool {
	return matchesPyDefTmpl(rePyDefNameTmpl, text, name)
}

// matchesPyTopLevelDef is matchesPyDef restricted to a column-zero `def` — the
// declaration set an unqualified call can actually reach. See
// rePyTopLevelDefNameTmpl for why the two are kept apart.
func matchesPyTopLevelDef(text, name string) bool {
	return matchesPyDefTmpl(rePyTopLevelDefNameTmpl, text, name)
}

func matchesPyDefTmpl(tmpl, text, name string) bool {
	if !strings.Contains(text, name) {
		return false // cheap reject before compiling anything
	}
	re, err := regexp.Compile(strings.ReplaceAll(tmpl, "%s", regexp.QuoteMeta(name)))
	if err != nil {
		return true // unexpected; abstain rather than compare against a stale shape
	}
	return re.MatchString(text)
}

// removesSignatureLine reports whether removedText deletes a line belonging to
// the parameter list of the declaration starting at declLine — evidence that this
// edit is rewriting that signature even though no whole `def` line appears in the
// hunk.
//
// Trimmed whole-line equality is deliberately coarse: it will also fire when a
// removed line elsewhere happens to look like `    b,`. That is over-abstention,
// which costs reach; the alternative direction costs a false positive.
func removesSignatureLine(idx *pyDeclIndex, declLine int, removedText string) bool {
	sig := idx.signatureLinesAt(declLine)
	if len(sig) == 0 {
		return false
	}
	removedSet := make(map[string]struct{})
	for _, l := range strings.Split(removedText, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			removedSet[t] = struct{}{}
		}
	}
	for _, s := range sig {
		if _, hit := removedSet[s]; hit {
			return true
		}
	}
	return false
}

// pyNonDefBindings collects every name these lines bind EXCEPT by `def`. A def is
// the thing being resolved TO, while any other binding of the same name — an
// import, a reassignment to a wrapper, a parameter of an enclosing function —
// means the call site may not reach that def at all.
//
// It is pyFileScope minus ExtractDefs, and deliberately minus LocallyBoundNames
// as well. LocallyBoundNames is over-inclusive by design (it exists to SUPPRESS
// dropped-import false positives, where binding too much is the safe direction);
// here over-inclusion is not conservative, it is fatal. Its generic assignLHS has
// no Python bracket-depth tracking, so `return fetch("u", timeout=5)` reads the
// kwarg's `=` as an assignment and binds `return`, `fetch` and `timeout` — which
// makes the callee "shadowed" at its own call site and silences the check
// everywhere. The three precise extractors below are the same choice
// DroppedImportRefs made for JS (JSDeclaredNames, not LocallyBoundNames).
func pyNonDefBindings(lines []AddedLine, idx *pyDeclIndex, bracketDepthSeed func(lineNo int) int) map[string]struct{} {
	out := make(map[string]struct{})
	for _, n := range ExtractImports(LangPython, lines) {
		out[n] = struct{}{}
	}
	for _, n := range PyDeclaredNames(lines, bracketDepthSeed) {
		out[n] = struct{}{}
	}
	// PyDeclaredNames sees only plain `=` assignments. A `for` target (statement or
	// comprehension), a `with`/`except ... as`, and a walrus all rebind a name too,
	// and an adversarial review reproduced a false positive for each.
	for n := range idx.rebindings() {
		out[n] = struct{}{}
	}
	// Parameter names come from the index's already-located signature spans rather
	// than from PyParamNames, which re-threads every line of the file: measured at
	// 5.6 ms on a 3000-line file against a ~12 ms budget for the whole hook.
	for n := range idx.params() {
		out[n] = struct{}{}
	}
	return out
}

// linesText rejoins AddedLines into source text for a whole-file parse. Faithful
// for a contiguous read (the pre-edit file, or a Write's content); for a hunk it
// yields the hunk's own text, which is all the added-text parse needs.
func linesText(lines []AddedLine) string {
	if len(lines) == 0 {
		return ""
	}
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, l.Text)
	}
	return strings.Join(parts, "\n")
}

// capNames returns at most n names, appending an ellipsis marker when truncated
// so the reader knows the list is partial rather than complete.
func capNames(names []string, n int) []string {
	if len(names) <= n {
		return names
	}
	out := make([]string, 0, n+1)
	out = append(out, names[:n]...)
	return append(out, "…")
}
