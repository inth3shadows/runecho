package guard

import (
	"regexp"
	"strings"
)

// Declaration-side resolution for the call-shape check, sized for the HOOK.
//
// This began as a tree-sitter parse — a real Python AST is the obviously correct
// way to read a parameter list, and internal/parser already had the grammar
// loaded. It was measured and removed. On this project's pure-Go (CGO-free)
// tree-sitter runtime a parse costs about 8 ms of FIXED overhead regardless of
// input size, and 180 ms for a 3037-line file. The hook's whole budget for every
// check it runs is about 12 ms. One parse of one four-line signature would have
// consumed two thirds of it.
//
// So the declaration side is read the same way the CALL side already is: over
// masked lines, splitting a bracketed list at top-level commas, with the shared
// splitTopLevel / isIdent primitives from callshape.go. That symmetry is the point
// — both halves of the comparison now use one set of rules and one set of
// abstentions, and TestCallShapeDeclDifferential adjudicates the result against
// CPython's own `ast` over real corpora, which is the only evidence that matters
// for a hand-written parameter reader.
//
// Two narrowings are recorded rather than hidden:
//
//   - Only declarations at COLUMN ZERO are found. A module-level function nested
//     inside a wrapper (`if TYPE_CHECKING:`, `try:`/`except ImportError:`) is
//     indented, so the call abstains. A column test is the only way to tell a
//     module-level function from a method without a full-file parse, and silence is
//     the safe direction.
//   - A parameter list containing a top-level `lambda` abstains, because the
//     lambda's own parameter commas are indistinguishable from the enclosing list's
//     — the same hazard CallShape.HasLambda records on the call side.

var (
	// rePyDefAtColumnZero matches a module-level `def`/`async def` and captures the
	// name. Anchored at column 0: an indented def is a method, a nested def, or a
	// conditionally-declared one, and none is what an unqualified call resolves to.
	rePyDefAtColumnZero = regexp.MustCompile(`^(?:async[ \t]+)?def[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`)
	// rePyDefAnyIndent is the shadow-set counterpart: EVERY def, at any indentation,
	// because a nested def's parameters shadow an outer name just as a top-level
	// one's do. Captures the leading whitespace and the name — both are needed to
	// tell a nested function (which shadows) from a method (which cannot be reached
	// by an unqualified call, so does not).
	rePyDefAnyIndent = regexp.MustCompile(`^([ \t]*)(?:async[ \t]+)?def[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`)
	// rePyAnyForTarget is deliberately NOT anchored, unlike dropped_import.go's
	// rePyForTarget: a comprehension puts the `for` mid-line
	// (`[f(x) for f in fns]`), and an anchored pattern misses exactly that.
	rePyAnyForTarget = regexp.MustCompile(`\bfor\s+([A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*)\s+in\b`)
	// rePyClassAnyIndent identifies an enclosing `class` header by its indentation.
	rePyClassAnyIndent = regexp.MustCompile(`^([ \t]*)class[ \t]+`)
)

const (
	// pyDeclMaxSignatureLines bounds the lines consumed looking for the paren that
	// closes one parameter list. A longer signature abstains rather than scanning on
	// — the guard runs on attacker-influenced repo text, so every scan needs a
	// ceiling (#212).
	pyDeclMaxSignatureLines = 60
	// pyDeclMaxDecoratorLines bounds the upward walk looking for decorators above a
	// def. Beyond it the walk gives up and reports the declaration as DECORATED,
	// which abstains — the safe direction, since a missed decorator is a false
	// positive.
	pyDeclMaxDecoratorLines = 40
)

// pyDeclShape is one module-level Python `def`'s parameter shape: which names it
// accepts as keyword arguments, and the facts that make the accepted set
// unknowable. No types, no defaults — RunEcho owns shape agreement, a language
// server owns type agreement (KB decision #291).
type pyDeclShape struct {
	// Name is the module-level function name.
	Name string
	// Line is the 1-based file line of the `def` keyword (not of a decorator above
	// it), so a finding points at the signature the reader compares against.
	Line int
	// Keywords are the parameter names accepted by keyword, in source order.
	// Positional-only parameters (before a `/`) are excluded — passing one by
	// keyword is an error whether or not the name matches. Keyword-only parameters
	// (after `*` or `*args`) ARE included: they are the most keyword-callable
	// parameters there are. The `*args`/`**kwargs` names themselves are excluded.
	Keywords []string
	// HasKwStar reports a `**kwargs` parameter, which accepts every keyword name.
	HasKwStar bool
	// Decorated reports a decorator on the definition. A wrapper may accept a
	// different set — functools.wraps makes the visible signature a lie.
	Decorated bool
	// Unknowable reports that the signature could not be read with certainty (an
	// unclosed list, a top-level lambda, a segment that is not a parameter). It is
	// kept as a distinct field rather than folded into HasKwStar so the abstain
	// reason stays legible; every consumer treats it the same way.
	Unknowable bool
}

// usable reports whether this declaration's accepted-keyword set is knowable.
func (s pyDeclShape) usable() bool { return !s.HasKwStar && !s.Decorated && !s.Unknowable }

// pyDeclIndex scans one file's lines ONCE — masking string state as it goes — and
// records everything the check needs from the whole file: where each column-zero
// def sits, every def's parameter names (the shadow set), and whether the file
// contains a construct that can rebind a module-level name. Signatures are parsed
// lazily, per name asked for, and memoised.
//
// One pass matters. Each of the guard's whole-file extractors costs 1–6 ms on a
// 3000-line file, and the earlier draft of this check ran four of them plus a
// separate abstain scan.
type pyDeclIndex struct {
	lines []AddedLine
	// masked[i] is lines[i].Text with string and comment content blanked, so a
	// `def foo(` inside a docstring is not read as a declaration — the #145
	// false-positive class.
	masked []string
	// defLines maps a column-zero declared name to indices into lines.
	defLines map[string][]int
	// defOpens indexes EVERY def at any indentation, feeding params(); a nested
	// def's parameters shadow an outer name just as a top-level one's do.
	defOpens []int
	// indentedDefs maps a name declared by an INDENTED def to those line indices.
	// A nested function redefines the name for its own body, so a call there does
	// not reach the column-zero def of the same name — the wrong-signature
	// comparison an adversarial review reproduced.
	indentedDefs map[string][]int
	// rebound is rebindings()' memo; nil until first use.
	rebound map[string]struct{}
	// paramNames is params()' memo; nil until first use.
	paramNames map[string]struct{}
	// dynamic reports a star-import or a name-rebinding construct anywhere in the
	// file, which makes every resolution in it a guess.
	dynamic bool
	cache   map[string]pyDeclShape
	// hasCache distinguishes "resolved to an abstaining shape" from "not yet
	// resolved"; the zero pyDeclShape is itself a meaningful (usable, no-keywords)
	// value.
	hasCache map[string]bool
}

// newPyDeclIndex builds the index. O(bytes), no parsing.
func newPyDeclIndex(lines []AddedLine) *pyDeclIndex {
	idx := &pyDeclIndex{
		lines:        lines,
		masked:       make([]string, 0, len(lines)),
		defLines:     make(map[string][]int),
		indentedDefs: make(map[string][]int),
		cache:        make(map[string]pyDeclShape),
		hasCache:     make(map[string]bool),
	}
	// scanStripped threads multi-line string state across lines and resets it on a
	// line-number gap, which is what keeps prose inside a docstring from reading as
	// code. It is the primitive every other Python check extracts through, so the
	// masking rules cannot drift per-feature.
	var defOpens []int
	scanStripped(LangPython, lines, func(s string, l AddedLine) {
		i := len(idx.masked)
		idx.masked = append(idx.masked, s)
		if !idx.dynamic && lineIsDynamic(l.Text, s) {
			idx.dynamic = true
		}
		// Cheap substring reject before either regex: running both over every line of
		// a 3000-line file cost 4.4 ms of a ~12 ms hook budget for nothing. The
		// needle is "def" and NOT "def " — both regexes accept `def[ \t]+`, so a
		// tab-separated `def\tname(` would have been invisible to the tighter
		// prefilter while remaining a real declaration, silently dropping it from
		// both the resolvable set and the parameter shadow set.
		if !strings.Contains(s, "def") {
			return
		}
		if m := rePyDefAtColumnZero.FindStringSubmatch(s); m != nil {
			idx.defLines[m[1]] = append(idx.defLines[m[1]], i)
		}
		if m := rePyDefAnyIndent.FindStringSubmatch(s); m != nil {
			defOpens = append(defOpens, i)
			if m[1] != "" {
				idx.indentedDefs[m[2]] = append(idx.indentedDefs[m[2]], i)
			}
		}
	})
	idx.defOpens = defOpens
	return idx
}

// params returns every parameter name of every def in the file, at any
// indentation, computed on first use. It is deferred because the shadow set it
// feeds is only needed once a candidate call actually resolves to a same-file
// declaration, which most diffs never do.
//
// The sweep reuses the signature spans the scan already located, so it costs
// O(signatures) rather than the O(file) of the general PyParamNames extractor —
// 5.8 ms on a 3000-line file, against a ~12 ms budget for the whole hook.
func (idx *pyDeclIndex) params() map[string]struct{} {
	if idx.paramNames != nil {
		return idx.paramNames
	}
	idx.paramNames = make(map[string]struct{})
	for _, i := range idx.defOpens {
		content, _, ok := idx.signatureContent(i)
		if !ok {
			continue
		}
		for _, seg := range splitTopLevel(content) {
			if name := pyParamSegmentName(content[seg[0]:seg[1]]); name != "" {
				idx.paramNames[name] = struct{}{}
			}
		}
	}
	return idx.paramNames
}

// rebindings returns names bound by a non-`def`, non-assignment construct: a
// `for` target (statement OR comprehension), a `with`/`except ... as`, or a
// walrus. Computed on first use, off the already-masked lines.
//
// PyDeclaredNames sees only plain `=` assignments, so without this every one of
// these forms left a callee looking unshadowed and the check compared against the
// module-level def. An adversarial review reproduced five false positives on valid
// code that way — `for handler in fns: handler(item, verbose=True)` beside a
// module-level `def handler(x, dry=False)`, and the with/except/comprehension/
// walrus equivalents.
func (idx *pyDeclIndex) rebindings() map[string]struct{} {
	if idx.rebound != nil {
		return idx.rebound
	}
	idx.rebound = make(map[string]struct{})
	for _, line := range idx.masked {
		if strings.Contains(line, "for ") {
			for _, m := range rePyAnyForTarget.FindAllStringSubmatch(line, -1) {
				for _, part := range strings.Split(m[1], ",") {
					if n := strings.TrimSpace(part); n != "" {
						idx.rebound[n] = struct{}{}
					}
				}
			}
		}
		if strings.Contains(line, "as ") {
			for _, m := range reAsBind.FindAllStringSubmatch(line, -1) {
				idx.rebound[m[1]] = struct{}{}
			}
		}
		if strings.Contains(line, ":=") {
			for _, m := range reWalrus.FindAllStringSubmatch(line, -1) {
				idx.rebound[m[1]] = struct{}{}
			}
		}
	}
	return idx.rebound
}

// shadowedByNestedDef reports whether name is also declared by an INDENTED def
// that is NOT a class member. A nested function rebinds the name for its enclosing
// function's body, and a conditionally-declared one (`if TYPE_CHECKING:`) is a
// second module-level declaration — in both cases the column-zero signature may
// not be what a call reaches.
//
// A METHOD is excluded, and that exclusion is the whole reason this is not simply
// "any indented def": an unqualified `run(...)` cannot reach `C.run`, so treating
// every class member as a shadow would abstain on the very common case of a method
// and a module function sharing a name. The test is the nearest enclosing header
// at strictly smaller indentation: `class` means method, anything else means the
// def is reachable as a rebinding.
func (idx *pyDeclIndex) shadowedByNestedDef(name string) bool {
	for _, i := range idx.indentedDefs[name] {
		if !idx.enclosedByClass(i) {
			return true
		}
	}
	return false
}

// enclosedByClass reports whether the nearest header above line index i with
// strictly smaller indentation is a `class`.
func (idx *pyDeclIndex) enclosedByClass(i int) bool {
	own := leadingIndent(idx.masked[i])
	for j := i - 1; j >= 0; j-- {
		line := idx.masked[j]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if leadingIndent(line) >= own {
			continue
		}
		return rePyClassAnyIndent.MatchString(line)
	}
	return false
}

// leadingIndent counts leading whitespace bytes, a tab weighing the same as a
// space. Exact column arithmetic is not needed — only "strictly less than", and
// Python forbids mixing the two inconsistently within one block.
func leadingIndent(s string) int {
	n := 0
	for n < len(s) && (s[n] == ' ' || s[n] == '\t') {
		n++
	}
	return n
}

// signatureLinesAt returns the trimmed ORIGINAL lines spanning the parameter list
// of the def at file line declLine, or nil when there is no such def or its list
// does not balance.
//
// Balancing runs over MASKED text via signatureContent. The earlier version
// counted parens on raw text, so `sep="("` in a default made the span never
// balance — silently switching off the "this edit rewrites the declaration"
// abstain and reporting a correct parameter rename as a mismatch. Reproduced by an
// adversarial review against the shipped fixture plus one extra parameter.
func (idx *pyDeclIndex) signatureLinesAt(declLine int) []string {
	start := -1
	for _, sites := range idx.defLines {
		for _, i := range sites {
			if idx.lines[i].LineNo == declLine {
				start = i
			}
		}
	}
	if start < 0 {
		return nil
	}
	_, end, ok := idx.signatureContent(start)
	if !ok {
		return nil
	}
	var out []string
	for j := start; j <= end && j < len(idx.lines); j++ {
		if t := strings.TrimSpace(idx.lines[j].Text); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// lineIsDynamic reports whether one line contains a star-import or a construct
// that can rebind a module-level NAME at runtime.
//
// The marker list is deliberately NARROWER than the file-scope check's
// pyDynamicBinding, which also abstains on `setattr(`, `vars(`, `importlib` and
// `__import__`. Those matter to a check asking "is this name bound anywhere in the
// file"; they cannot matter to this one, which asks only whether a particular
// def's parameter list accepts a keyword. `setattr`/`vars` bind object
// ATTRIBUTES, and dynamic module loading yields a module object — neither rewrites
// a signature, and neither rebinds a bare local name without an assignment that
// PyDeclaredNames already sees.
//
// Reusing the wider list was measured, not assumed: it abstained on 218 of 283
// checkable sites in the CPython 3.12 stdlib and 109 of 303 in NFLsignal, almost
// all from an `importlib` or `setattr` in an otherwise ordinary file. One `getattr`
// in a 2000-line module must not silence the whole file.
//
// The star-import test reads the ORIGINAL line because rePyStarImport anchors on
// `from … import *`, and masking leaves that intact; the marker test reads the
// MASKED line so a marker named inside a docstring does not count.
func lineIsDynamic(original, masked string) bool {
	if rePyStarImport.MatchString(original) {
		return true
	}
	for _, marker := range callShapeDynamicBinding {
		if containsCallAt(masked, marker) {
			return true
		}
	}
	return false
}

// containsCallAt reports whether masked contains marker (an identifier plus its
// opening paren, e.g. "eval(") as a CALL of that builtin, rather than as the tail
// of some other name.
//
// A plain strings.Contains was wrong in a way that silently switched the whole
// check off for a file: `def get_locals(a, b)` contains "locals(", so an ordinary
// helper name darkened every call site in its module. A preceding `.` is excluded
// too — `self.eval(...)` is a method call, which cannot rebind a module-level
// name, and that is the only reason these markers abstain at all.
func containsCallAt(masked, marker string) bool {
	for i := 0; ; {
		j := strings.Index(masked[i:], marker)
		if j < 0 {
			return false
		}
		at := i + j
		if at == 0 {
			return true
		}
		if prev := masked[at-1]; !isWordByte(prev) && prev != '.' {
			return true
		}
		i = at + 1
	}
}

// callShapeDynamicBinding lists constructs that can rebind a module-level name.
var callShapeDynamicBinding = []string{"globals(", "locals(", "exec(", "eval("}

// shapesFor returns the shape of every column-zero declaration of name in this
// file. An empty result means "no module-level declaration found here", which the
// caller reads as an abstention.
func (idx *pyDeclIndex) shapesFor(name string) []pyDeclShape {
	sites := idx.defLines[name]
	if len(sites) == 0 {
		return nil
	}
	if len(sites) == 1 {
		if idx.hasCache[name] {
			return []pyDeclShape{idx.cache[name]}
		}
		s := idx.shapeAt(sites[0], name)
		idx.cache[name] = s
		idx.hasCache[name] = true
		return []pyDeclShape{s}
	}
	out := make([]pyDeclShape, 0, len(sites))
	for _, i := range sites {
		out = append(out, idx.shapeAt(i, name))
	}
	return out
}

// shapeAt reads the declaration whose `def` is at line index i.
func (idx *pyDeclIndex) shapeAt(i int, name string) pyDeclShape {
	s := pyDeclShape{Name: name, Line: idx.lines[i].LineNo}
	content, end, ok := idx.signatureContent(i)
	if !ok {
		s.Unknowable = true
		return s
	}
	s.Keywords, s.HasKwStar, s.Unknowable = pyKeywordParams(content)
	s.Decorated = idx.decoratedAbove(i)
	_ = end
	return s
}

// signatureContent returns the MASKED text between the paren opened on the def
// line at index i and its matching close, plus the line index where it closes.
// Masked text is correct for this: a parameter NAME can never be inside a string
// literal, while a paren or comma inside a default value's string must not be
// counted — which is exactly what masking removes.
func (idx *pyDeclIndex) signatureContent(i int) (content string, end int, ok bool) {
	open := strings.IndexByte(idx.masked[i], '(')
	if open < 0 {
		return "", 0, false
	}
	var b strings.Builder
	depth := 0
	for j := i; j < len(idx.masked) && j-i < pyDeclMaxSignatureLines; j++ {
		// A hunk's lines can be non-contiguous; a signature cannot span a gap, so
		// stop rather than joining unrelated text across one.
		if j > i && idx.lines[j].LineNo != idx.lines[j-1].LineNo+1 {
			return "", 0, false
		}
		line := idx.masked[j]
		from := 0
		if j == i {
			from = open
		}
		for k := from; k < len(line); k++ {
			switch line[k] {
			case '(', '[', '{':
				depth++
				if depth == 1 && j == i && k == open {
					continue // the opening paren itself is not content
				}
			case ')', ']', '}':
				depth--
				if depth == 0 {
					return b.String(), j, true
				}
			}
			b.WriteByte(line[k])
		}
		// A parameter list broken across lines joins with a space, not nothing:
		// `def f(a\n, b)` and `def f(a,\n b)` must both split into `a` and `b`.
		b.WriteByte(' ')
	}
	return "", 0, false
}

// pyKeywordParams reads a parameter list's MASKED content into the set of names
// callable by keyword.
//
// The rules are Python's:
//
//   - `/` ends the positional-only region. Every name collected before it is
//     dropped: `def f(a, /, b)` rejects `f(a=1)`, so keeping `a` would let a
//     genuinely invalid call pass.
//   - a bare `*` and `*args` both begin the keyword-ONLY region. Names after them
//     stay; neither splat's own name is keyword-callable.
//   - `**kwargs` means every keyword name is accepted.
//   - anything else is `NAME`, `NAME: T`, `NAME = v` or `NAME: T = v`, and NAME is
//     the keyword.
//
// unknowable is set when a segment cannot be read as any of those, or when a
// top-level `lambda` makes the comma split itself unreliable. A misread here is a
// false positive on working code, which costs this check its credibility far faster
// than a missed defect does — so every ambiguity resolves to abstention.
func pyKeywordParams(content string) (keywords []string, hasKwStar, unknowable bool) {
	if strings.TrimSpace(content) == "" {
		return nil, false, false
	}
	// A lambda default's own parameter commas sit at the same bracket depth as the
	// enclosing list's: `def f(key=lambda a, b: a)` declares ONE parameter but
	// splits into two segments. Neither half is readable, so abstain on the list.
	if hasTopLevelKeyword(content, "lambda") {
		return nil, false, true
	}
	for _, span := range splitTopLevel(content) {
		seg := strings.TrimSpace(content[span[0]:span[1]])
		switch {
		case seg == "":
			// A trailing comma, or an empty list. Not a parameter.
		case seg == "/":
			keywords = nil // everything before is positional-only
		case seg == "*":
			// Start of the keyword-only region; binds no name.
		case strings.HasPrefix(seg, "**"):
			hasKwStar = true
		case strings.HasPrefix(seg, "*"):
			// `*args` — its own name is not keyword-callable.
		default:
			name := pyParamSegmentName(seg)
			if name == "" {
				return nil, false, true
			}
			keywords = append(keywords, name)
		}
	}
	return keywords, hasKwStar, false
}

// pyParamSegmentName returns the parameter name a single list segment declares, or
// "" when the segment is not a plain named parameter (a separator, a splat, or
// something unreadable). The name is the text before the first top-level `:`
// (annotation) or `=` (default).
func pyParamSegmentName(seg string) string {
	seg = strings.TrimSpace(seg)
	if seg == "" || seg == "/" || seg == "*" || strings.HasPrefix(seg, "*") {
		return ""
	}
	depth := 0
	cut := len(seg)
	for i := 0; i < len(seg); i++ {
		switch seg[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ':', '=':
			if depth == 0 {
				cut = i
				i = len(seg) // break out
			}
		}
	}
	name := strings.TrimSpace(seg[:cut])
	if !isIdent(name) {
		return ""
	}
	return name
}

// decoratedAbove reports whether the def at line index i carries a decorator.
//
// The subtle case is a MULTI-LINE decorator (`@app.route(\n    "/x",\n)`), whose
// last line is a bare `)`. It is handled by bracket balance rather than by pattern:
// walking upward, a line leaving more closers than openers means the statement
// continues above, so the walk keeps going until the balance returns to zero. Only
// then is the line the statement's first, and only then is `@` tested.
//
// Missing a decorator is the false-positive direction — it compares a call against
// a signature a wrapper has replaced — so running out of budget reports TRUE.
// Blank lines and full-line comments between a decorator and its def are legal
// Python and are skipped.
func (idx *pyDeclIndex) decoratedAbove(i int) bool {
	depth := 0
	for j := i - 1; j >= 0; j-- {
		if i-j > pyDeclMaxDecoratorLines {
			return true // budget exhausted mid-walk: abstain rather than guess
		}
		// A decorator cannot be separated from its def by a hunk gap.
		if idx.lines[j].LineNo != idx.lines[j+1].LineNo-1 {
			return false
		}
		t := strings.TrimSpace(idx.masked[j])
		if depth == 0 && (t == "" || strings.HasPrefix(t, "#")) {
			continue
		}
		for k := 0; k < len(t); k++ {
			switch t[k] {
			case ')', ']', '}':
				depth++
			case '(', '[', '{':
				depth--
			}
		}
		if depth > 0 {
			continue // still inside a bracketed statement that began above
		}
		depth = 0
		if strings.HasPrefix(t, "@") {
			return true
		}
		return false
	}
	return false
}
