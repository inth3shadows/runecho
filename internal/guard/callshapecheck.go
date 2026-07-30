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
	// DeclLine is the 1-based line of the `def` this was compared against, in the
	// same file, so the reader can go look at the signature.
	DeclLine int
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
)

// rePyDefNameTmpl matches a Python `def NAME(` (optionally `async`, any
// indentation) so the check can tell that a name's SIGNATURE is being touched by
// this very edit, as opposed to merely being called by it.
const rePyDefNameTmpl = `(?m)^[ \t]*(?:async[ \t]+)?def[ \t]+%s[ \t]*\(`

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
func PyCallShapeMismatches(lang Lang, wholeFile []AddedLine, fd FileDiff, removed []AddedLine, addedIsWholeFile bool) []CallShapeMismatch {
	if lang != LangPython {
		return nil
	}
	added := fd.AddedLines
	if len(added) == 0 {
		return nil
	}
	// The declaration source. For Write, the added lines are the whole post-edit
	// file, so they are strictly better than the pre-edit copy: a signature this
	// very Write changes is already reflected. For Edit/MultiEdit the hunk is
	// partial, so the pre-edit file is the only whole-file view available.
	declLines := wholeFile
	if addedIsWholeFile {
		declLines = added
	}
	if len(declLines) == 0 {
		return nil
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
	for _, c := range ExtractCallShapes(lang, added, seedFunc(lang, fd)) {
		switch {
		case len(c.Kwargs) == 0:
			// Nothing to compare — positional arity is a later slice.
		case c.HasLambda, c.Unreliable:
			// The extractor says its own Pos/Kwargs are not trustworthy here.
		default:
			candidates = append(candidates, c)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	// ONE masked pass over the declaration source yields everything the whole file
	// has to say: where each column-zero def sits, every def's parameter names, and
	// whether the file contains a name-rebinding construct. Doing it in one pass is a
	// latency requirement — see pyDeclIndex.
	declIndex := newPyDeclIndex(declLines)
	if declIndex.dynamic {
		return nil
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
			return nil
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
			shadowed = pyNonDefBindings(declLines, declIndex)
			if addedIndex != nil {
				for n := range pyNonDefBindings(added, addedIndex) {
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
	}
	checked := make(map[string]resolved, len(candidates))
	for _, c := range candidates {
		if len(out) >= callShapeMaxMismatches {
			break
		}
		r, seen := checked[c.Name]
		if !seen {
			r.shape, r.ok = resolveDeclShape(c.Name, shadowedSet, declIndex, addedIndex, addedText, declLines, removedText, addedIsWholeFile)
			checked[c.Name] = r
		}
		if !r.ok {
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
				Callee:   c.Name,
				Keyword:  kw,
				LineNo:   c.LineNo,
				DeclLine: r.shape.Line,
				Accepted: capNames(r.shape.Keywords, callShapeMaxAccepted),
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
	return out
}

// resolveDeclShape picks the single declaration shape for name that the call can
// be compared against, or reports ok=false to abstain. Every abstain path is a
// case where more than one answer is defensible, and the check's contract is that
// it never guesses between them.
func resolveDeclShape(
	name string,
	shadowedSet func() map[string]struct{},
	declIndex, addedIndex *pyDeclIndex,
	addedText string,
	declLines []AddedLine,
	removedText string,
	addedIsWholeFile bool,
) (pyDeclShape, bool) {
	var zero pyDeclShape
	if _, bad := shadowedSet()[name]; bad {
		return zero, false
	}
	// When the edit itself declares this name, its version of the signature is the
	// one the post-edit file will have — the indexed/on-disk copy is stale by
	// exactly this edit. Adding a parameter and using it in the same edit is a
	// routine agent move, so getting this wrong would be a recurring false
	// positive rather than a corner case.
	if !addedIsWholeFile && matchesPyDef(addedText, name) {
		s, ok := soleShape(addedIndex.shapesFor(name))
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
			for _, pre := range declIndex.shapesFor(name) {
				if pre.Decorated {
					s.Decorated = true
					break
				}
			}
		}
		if !ok {
			// The hunk shows a `def name(` but no single parseable signature (a
			// truncated parameter list, or two disagreeing branches). Its true shape
			// is unknown and the on-disk copy is known-stale, so there is nothing to
			// compare against.
			return zero, false
		}
		return s, s.usable()
	}
	s, ok := soleShape(declIndex.shapesFor(name))
	if !ok {
		// No module-level def of this name in the file (a cross-file or third-party
		// callee — the existence and file-scope checks own that), or two
		// conditional branches with different accepted sets.
		return zero, false
	}
	if !s.usable() {
		return zero, false
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
	if removedText != "" && removesSignatureLine(declLines, s.Line, removedText) {
		return zero, false
	}
	return s, true
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
	if !strings.Contains(text, name) {
		return false // cheap reject before compiling anything
	}
	re, err := regexp.Compile(strings.ReplaceAll(rePyDefNameTmpl, "%s", regexp.QuoteMeta(name)))
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
func removesSignatureLine(declLines []AddedLine, declLine int, removedText string) bool {
	sig := pySignatureLines(declLines, declLine)
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

// pySignatureLines returns the trimmed, non-empty lines spanning the parameter
// list of the def at declLine (1-based), from the `def` line through the line
// whose closing paren balances it. Returns nil if declLine is out of range or the
// parens never balance within pySignatureMaxLines.
func pySignatureLines(lines []AddedLine, declLine int) []string {
	const pySignatureMaxLines = 60 // a signature longer than this abstains rather than scanning on
	idx := -1
	for i, l := range lines {
		if l.LineNo == declLine {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	var out []string
	depth := 0
	started := false
	for i := idx; i < len(lines) && i-idx < pySignatureMaxLines; i++ {
		text := lines[i].Text
		if t := strings.TrimSpace(text); t != "" {
			out = append(out, t)
		}
		for j := 0; j < len(text); j++ {
			switch text[j] {
			case '(':
				depth++
				started = true
			case ')':
				depth--
			}
		}
		if started && depth <= 0 {
			return out
		}
	}
	return nil
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
func pyNonDefBindings(lines []AddedLine, idx *pyDeclIndex) map[string]struct{} {
	out := make(map[string]struct{})
	for _, n := range ExtractImports(LangPython, lines) {
		out[n] = struct{}{}
	}
	for _, n := range PyDeclaredNames(lines) {
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
