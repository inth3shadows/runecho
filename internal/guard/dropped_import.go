package guard

import (
	"regexp"
	"sort"
	"strings"
)

// DroppedImport names an imported symbol whose import binding an edit removes
// while the new text still uses it unqualified and does not re-define it. This is
// the dropped-import bug seen in real transcripts (an agent rewrites a file,
// drops `from ulid import ULID`, but still calls `ULID()`), which fails at
// runtime (NameError / "X is not defined").
//
// It is the file-scoped mirror of the E1 dangling-refs check (removed *definition*
// still referenced cross-file): here it is a removed *import* still referenced
// in-file. It complements the additive hallucination check rather than
// duplicating it: at edit time the on-disk file still carries the old import, so
// the additive check resolves the name and stays silent — only by diffing the
// edit's old vs new text does the removal become visible.
type DroppedImport struct {
	Name   string
	LineNo int // first surviving-use line within newText (1-based)
}

// DroppedImportRefs returns imported names that are bound in oldText, no longer
// bound in newText, still used unqualified somewhere in newText, and not
// re-provided by a local definition in newText. Deterministic (sorted by name).
//
// Go is excluded: Go imports are used package-qualified (pkg.Foo), so a dropped
// Go import surfaces as a qualified reference, which the guard handles elsewhere
// (and ExtractImports already excludes Go for the same reason).
// DroppedImportRefs is the single-block convenience form (Edit/Write, where the
// old and new text are each one contiguous region). For a MultiEdit — whose edits
// are unrelated regions that must not share open multi-line-string state — call
// DroppedImportRefsLines with AddedLinesWithGap so the scan resets at each edit
// boundary; a flat "\n"-join here would leak an unterminated string from one edit
// into the next and silently drop (or falsely raise) a detection.
func DroppedImportRefs(lang Lang, oldText, newText string) []DroppedImport {
	return DroppedImportRefsLines(lang, TextToAddedLines(oldText), TextToAddedLines(newText))
}

// DroppedImportRefsLines is the structured form of DroppedImportRefs: it scans the
// pre-built AddedLines directly, so a caller can pass gap-separated lines
// (AddedLinesWithGap) for a MultiEdit to reset multi-line-string state at each
// edit boundary. All of its scans (ExtractImports' inPyParen state,
// firstUnqualifiedUseLines, LocallyBoundNames) honor those gaps.
func DroppedImportRefsLines(lang Lang, oldLines, newLines []AddedLine) []DroppedImport {
	return DroppedImportRefsLinesWithBound(lang, oldLines, newLines, nil, nil)
}

// DroppedImportRefsLinesWithBound is DroppedImportRefsLines with an extra
// preBound set of names unioned into the new-text binding set before dropped
// imports are computed. This lets a caller fold in binding context that isn't
// visible in newLines at all — e.g. a hunk-only Edit/MultiEdit can't see a
// name rebound on an untouched line elsewhere in the file, which would
// otherwise false-positive as a dropped import. Pass nil for no extra context
// (identical to DroppedImportRefsLines).
//
// defSigDepthSeed is threaded straight to LocallyBoundNames(lang, newLines) —
// nil is correct for a self-contained, contiguous-from-line-1 slice; a
// hunk-only newLines caller (the guard hook) should pass its real seed
// (#294), or a parameter added mid-signature in a hunk whose opener sits in
// unchanged context is never recognized as a rebind of the dropped import.
func DroppedImportRefsLinesWithBound(lang Lang, oldLines, newLines []AddedLine, preBound map[string]struct{}, defSigDepthSeed func(lineNo int) int) []DroppedImport {
	if lang != LangPython && lang != LangJS {
		return nil
	}
	oldImps := nameSet(ExtractImports(lang, oldLines))
	if len(oldImps) == 0 {
		return nil // nothing was imported in the removed text; no work
	}
	newImps := nameSet(ExtractImports(lang, newLines))

	// bound = every name the new text re-provides locally: top-level definitions
	// PLUS any binding form (assignment LHS, for/comprehension target, with/except
	// `as`, walrus, function params, JS const/let/var + destructuring + catch). A
	// dropped import whose name is rebound here is NOT a bug. This is the
	// false-positive guard, and it is deliberately OVER-inclusive: an over-
	// suppressed real drop is a recoverable miss (the additive check or the runtime
	// still catches it), whereas a false alarm trains users to ignore the guard —
	// the adoption-killer. Precision over recall.
	bound := LocallyBoundNames(lang, newLines, defSigDepthSeed)
	for _, d := range ExtractDefs(lang, newLines) {
		bound[d] = struct{}{}
	}
	for name := range preBound {
		bound[name] = struct{}{}
	}

	// Collect the imports that were actually dropped (removed and not rebound)
	// before touching the new text. Most edits drop nothing, so this keeps the
	// common case at zero identifier scans — the per-name check used to be lazy,
	// and we preserve that fast path rather than eagerly indexing every edit.
	var dropped []string
	for name := range oldImps {
		if _, still := newImps[name]; still {
			continue // the import survived in the new text
		}
		if _, b := bound[name]; b {
			continue // the name is now provided by a local definition or binding
		}
		dropped = append(dropped, name)
	}
	if len(dropped) == 0 {
		return nil
	}

	// Index every unqualified identifier use in one pass so each dropped name is an
	// O(1) lookup below. Rescanning the whole new text per dropped import was
	// O(distinct-imports × text-length) — quadratic on a crafted diff.
	uses := firstUnqualifiedUseLines(lang, newLines)
	var out []DroppedImport
	for _, name := range dropped {
		if ln := uses[name]; ln > 0 {
			out = append(out, DroppedImport{Name: name, LineNo: ln})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func nameSet(names []string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

// firstUnqualifiedUseLines returns, for every identifier used as an unqualified
// (non-attribute) whole word anywhere in lines, the 1-based number of its FIRST
// such use. Import lines and string/comment content are ignored (the latter
// blanked by the same stateful literal stripper the reference extractor uses);
// the import-line skip keeps a binding line from counting as a "use". This is the
// single-pass form: callers resolve many candidate names in O(1) each instead of
// rescanning the text per name, which was quadratic in distinct-imports × length.
//
// A maximal run of identifier bytes already satisfies the word-boundary
// conditions the old per-name check enforced, so the only extra test is that the
// run is not preceded by '.' (which would make it a qualified attribute, e.g.
// `x.Name`) — semantics identical to the former containsUnqualifiedWord.
func firstUnqualifiedUseLines(lang Lang, lines []AddedLine) map[string]int {
	uses := make(map[string]int)
	scanStripped(lang, lines, func(scan string, l AddedLine) {
		if isImportLine(lang, l.Text) {
			// A single physical line can pack import clauses and real statements,
			// ';'-separated (`import re; x = Foo()`), and isImportLine classifies
			// the WHOLE line as import (its regexes are end-anchored/prefix-based,
			// so a trailing statement is swallowed into the "import list" match).
			// Skipping the whole line here would silently miss a genuine use in a
			// non-import segment (e.g. a dropped import's only remaining call). So
			// blank EVERY ';'-segment that is itself an import clause — wherever it
			// sits on the line — and scan only the genuine non-import segments.
			// Blanking import clauses in ANY position (not just a leading run)
			// matters: a reimport packed after a statement
			// (`import a; x=1; import {Dropped} from './b'`) must stay blanked, else
			// its name is scanned as a use — and ExtractImports' first-clause-only
			// parse won't have put it in newImps to offset that, so it would
			// false-positive as dropped. Segment on the stripped `scan` (not raw
			// text) so a ';' inside a string literal is not a boundary; scan and
			// l.Text share indices (stripLiteralsStateful preserves length). A
			// plain import line (no ';') blanks entirely, as the old skip did.
			buf := []byte(scan)
			pos := 0
			for pos < len(scan) {
				rel := strings.IndexByte(scan[pos:], ';')
				segEnd := len(scan)
				if rel >= 0 {
					segEnd = pos + rel
				}
				if isImportLine(lang, l.Text[pos:segEnd]) {
					for k := pos; k < segEnd; k++ {
						buf[k] = ' '
					}
				}
				if rel < 0 {
					break
				}
				pos = segEnd + 1
			}
			scan = string(buf)
		}
		for j := 0; j < len(scan); {
			if !isWordByte(scan[j]) {
				j++
				continue
			}
			start := j
			for j < len(scan) && isWordByte(scan[j]) {
				j++
			}
			if start > 0 && scan[start-1] == '.' {
				continue // qualified attribute, e.g. x.Name
			}
			if id := scan[start:j]; uses[id] == 0 {
				uses[id] = l.LineNo
			}
		}
	})
	return uses
}

func isWordByte(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// Binding-form patterns for LocallyBoundNames. Each captures the binding TARGET
// region; identifiers within are extracted with reIdentAll. Definition-position
// only — none match call-argument parens, so a dropped import passed as an
// argument is still flagged, not suppressed.
var (
	reIdentAll    = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
	rePyForTarget = regexp.MustCompile(`^\s*for\s+(.+?)\s+in\b`)
	reAsBind      = regexp.MustCompile(`\bas\s+([A-Za-z_]\w*)`) // with/except ... as x
	reWalrus      = regexp.MustCompile(`([A-Za-z_]\w*)\s*:=`)
	// rePyDefOpen matches a def header through its opening `(`. The parameter
	// region is then accumulated across lines by paren depth (pyConsumeParens),
	// so a PEP8-wrapped signature `def foo(\n  config,\n):` binds its params too —
	// the old single-line-only `\(([^)]*)\)` false-positived every wrapped param.
	rePyDefOpen     = regexp.MustCompile(`^\s*(?:async\s+)?def\s+\w+\s*\(`)
	reJSDeclList    = regexp.MustCompile(`\b(?:const|let|var)\s+(.+)$`)
	reJSFnParams    = regexp.MustCompile(`function\b[^(]*\(([^)]*)\)`)
	reJSArrowParams = regexp.MustCompile(`\(([^)]*)\)\s*=>`)
	// reJSArrowParamsBare catches the unparenthesized single-arg arrow form
	// (`x => x*2`), which reJSArrowParams — parenthesized-only — never matches.
	// No left anchor by design: a `\b` sits AFTER a leading `$` (a non-word byte),
	// so `$x =>` would capture `x` — the wrong name — and leave the real binding
	// `$x` unbound, false-positiving a rebound `$`-prefixed import as dropped.
	// Leftmost matching already starts the identifier at its true start (a `.`- or
	// space-preceded token can't match a suffix, since an earlier start position is
	// tried first), and a leading anchor that consumes a boundary byte would break
	// no-space chains (`x=>y=>z` — the byte before `y` was eaten by the `x` match).
	// Requiring `=>` immediately (only whitespace between) after the identifier
	// still keeps this from firing inside an already-parenthesized form like
	// `(a, b) => …` (the identifier there is followed by `)`, not `=>`).
	reJSArrowParamsBare = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*=>`)
	reJSForDecl         = regexp.MustCompile(`\bfor\s*\(\s*(?:const|let|var)\s+([A-Za-z_$][\w$]*)`)
	reJSCatch           = regexp.MustCompile(`\bcatch\s*\(\s*([A-Za-z_$][\w$]*)`)
)

// LocallyBoundNames collects names the given lines bind locally by any common
// construct, so a dropped import that is actually rebound (replaced by a local
// assignment, loop variable, with-as, parameter, destructure, …) does not
// false-positive. Over-inclusive by design (see DroppedImportRefs). Exported so
// a caller (e.g. the guard hook) can compute the same binding set over whole-
// file context and fold it into DroppedImportRefsLinesWithBound's preBound.
//
// defSigDepthSeed supplies the def-signature paren depth in effect at the
// START of the given line — PyDefSigDepthBefore's caller-facing form (#294),
// shared with PyParamNames' own seed. Nil (or a gap with no seed, or any
// non-Python lang, which never consults it) means "no signature open",
// matching the previous unseeded behavior — correct for a genuinely
// whole-file, contiguous-from-line-1 caller, and the previously-accepted gap
// for a hunk-only caller.
func LocallyBoundNames(lang Lang, lines []AddedLine, defSigDepthSeed func(lineNo int) int) map[string]struct{} {
	m := make(map[string]struct{})
	add := func(s string) {
		for _, id := range reIdentAll.FindAllString(s, -1) {
			m[id] = struct{}{}
		}
	}
	// State for accumulating a Python def signature's parameter region across
	// lines (see rePyDefOpen). depth>0 means we are inside an unclosed signature.
	pyDefDepth := 0
	var pyDefParams strings.Builder
	pyDefPrevNo := 0
	pyFirst := true
	// JS counterpart of pyDefDepth/pyDefParams: a parameter list whose parens
	// span lines. No seed callback (contrast the Python arm's defSigDepthSeed):
	// that machinery exists for #294's hunk-only callers, and adding an
	// untested resumable-depth rule for JS would be surface without a caller.
	jsSigDepth := 0
	jsSigIsFn := false
	jsSigPrevNo := 0
	var jsSig strings.Builder
	// scanStripped threads multi-line string state (so a `x = Foo()` example inside
	// a multi-line docstring/template is not read as a real binding) AND resets it
	// on a line-number gap (so an unterminated string in one MultiEdit block does
	// not leak a spurious binding into the next). Both matter: the former is why
	// stripping must be stateful, the latter is why gap-separated lines
	// (AddedLinesWithGap) reset correctly.
	scanStripped(lang, lines, func(s string, l AddedLine) {
		// A signature cannot be assumed to continue across a diff-hunk gap.
		// The Python arm reseeds below; JS simply drops what it had.
		if jsSigDepth > 0 && l.LineNo != jsSigPrevNo+1 {
			jsSigDepth = 0
			jsSig.Reset()
		}
		jsSigPrevNo = l.LineNo
		if lhs := assignLHS(s); lhs != "" {
			add(lhs)
		}
		switch lang {
		case LangPython:
			if mm := rePyForTarget.FindStringSubmatch(s); mm != nil {
				add(mm[1])
			}
			for _, mm := range reAsBind.FindAllStringSubmatch(s, -1) {
				m[mm[1]] = struct{}{}
			}
			for _, mm := range reWalrus.FindAllStringSubmatch(s, -1) {
				m[mm[1]] = struct{}{}
			}
			// Bind def parameters, single- or multi-line. Accumulate the param
			// region by paren depth from the header's `(` until it balances, then
			// add() every identifier in it (same as the old single-line form).
			// Reseed (not just reset) from the real file state at a gap OR at
			// the very first line (#294) — without this a hunk beginning
			// partway through a multi-line signature (opener in unchanged
			// context above the hunk) never recognized itself as inside one,
			// and a parameter added on the hunk's own lines was never bound.
			if pyFirst || l.LineNo != pyDefPrevNo+1 {
				if defSigDepthSeed != nil {
					pyDefDepth = defSigDepthSeed(l.LineNo)
				} else {
					pyDefDepth = 0
				}
				pyDefParams.Reset()
			}
			pyFirst = false
			if pyDefDepth > 0 {
				part, closed := pyConsumeParens(s, &pyDefDepth)
				pyDefParams.WriteString(part + " ")
				if closed {
					add(pyDefParams.String())
					pyDefParams.Reset()
				}
			} else if loc := rePyDefOpen.FindStringIndex(s); loc != nil {
				pyDefDepth = 1
				part, closed := pyConsumeParens(s[loc[1]:], &pyDefDepth)
				pyDefParams.Reset()
				pyDefParams.WriteString(part + " ")
				if closed {
					add(pyDefParams.String())
					pyDefParams.Reset()
				}
			}
			pyDefPrevNo = l.LineNo
		case LangJS:
			// Capture EVERY declarator of a const/let/var statement, not just the
			// first — `const a = f(), Ulid = () => …` rebinds Ulid as the second
			// declarator, which a first-`=`-only match would miss (false positive).
			if mm := reJSDeclList.FindStringSubmatch(s); mm != nil {
				for _, decl := range splitTopLevelCommas(mm[1]) {
					if lhs := assignLHS(decl); lhs != "" {
						add(lhs)
					} else {
						add(decl) // declarator with no initializer (`let a, b;`)
					}
				}
			}
			if mm := reJSFnParams.FindStringSubmatch(s); mm != nil {
				add(mm[1])
			}
			if mm := reJSArrowParams.FindStringSubmatch(s); mm != nil {
				add(mm[1])
			}
			// Both regexes above are single-line: their `[^)]*` cannot cross a
			// newline, so a wrapped signature — the standard React component
			// shape — bound nothing. Python's twin had exactly this bug and was
			// fixed (see rePyDefOpen's comment); the JS pair was left behind.
			//
			// This accumulator is purely ADDITIVE to them rather than a
			// replacement: LocallyBoundNames is deliberately over-inclusive
			// (it only SUPPRESSES dropped-import warnings, so a name too many
			// costs nothing while a name too few is a false positive), and
			// swapping in the precise JSParamNames would shrink the set.
			// It therefore fires only where the regexes cannot reach — a
			// parameter list that does not close on the line that opens it —
			// and add()s the raw region, identically to them.
			if jsSigDepth > 0 {
				jsSig.WriteByte(' ')
				jsSig.WriteString(s)
				if idx, closed := jsConsumeParens(s, &jsSigDepth); closed {
					full := jsSig.String()
					inner := full[:len(full)-(len(s)-idx)]
					// Same arrow/function test the regexes encode: without it
					// a multi-line CALL's arguments would be bound as if they
					// were parameters, which would suppress a genuine dropped
					// import passed as an argument.
					if jsSigIsFn || jsArrowFollows(s[idx+1:]) {
						add(inner)
					}
					jsSigDepth = 0
					jsSig.Reset()
				}
			} else if open, isFn := jsOpenParamListLoose(s); open >= 0 {
				d := 1
				rest := s[open+1:]
				if _, closed := jsConsumeParens(rest, &d); !closed {
					jsSigDepth, jsSigIsFn = d, isFn
					jsSig.Reset()
					jsSig.WriteString(rest)
				}
			}
			// Bare single-arg arrow(s) (`x => …`) — an INDEPENDENT check (not an
			// `else if` off the parenthesized form) over ALL matches, not just the
			// first. A single line can carry a parenthesized arrow alongside one or
			// more bare arrows (`(a, b) => …; arr.map(Dropped => …)`) or chain them
			// (`a => Dropped => …`); binding only the first bare param would miss a
			// later one and false-positive that name as a dropped import. add() is
			// a set insert, so overlap with the parenthesized form is harmless.
			// (reJSArrowParamsBare's `… \s*=>` can't fire inside a parenthesized
			// arrow: the ident there is followed by `)`, not `=>`.)
			for _, mm := range reJSArrowParamsBare.FindAllStringSubmatch(s, -1) {
				add(mm[1])
			}
			if mm := reJSForDecl.FindStringSubmatch(s); mm != nil {
				m[mm[1]] = struct{}{}
			}
			if mm := reJSCatch.FindStringSubmatch(s); mm != nil {
				m[mm[1]] = struct{}{}
			}
		}
	})
	return m
}

// pyConsumeParens scans s tracking paren nesting from *depth (>0 = inside an open
// def signature), returning the param text before the balancing ')' and whether
// the signature closed on this line. Nested parens (default-value calls like
// `x=g()`) are balanced, so the signature closes only at its real end. Operates
// on the literal-stripped scan, so parens inside strings don't count.
func pyConsumeParens(s string, depth *int) (string, bool) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			*depth++
		case ')':
			*depth--
			if *depth == 0 {
				return s[:i], true
			}
		}
	}
	return s, false
}

// PyDefSigDepthBefore returns the depth of an open `def(...)` parameter
// signature in effect at the START of fileLines[idx] — 0 if no signature is
// open there. LocallyBoundNames' seed counterpart to
// PyBraceDepthBefore/PyBracketDepthBefore (#294): without it, a hunk
// beginning partway through a multi-line def signature (opener in unchanged
// context above the hunk) starts as if no signature were open, so a
// parameter added on that hunk's own lines (e.g. `    timeout: int = 30,`)
// is never recognized as a bound parameter.
//
// Whole-file scan using the exact same rePyDefOpen/pyConsumeParens state
// machine LocallyBoundNames itself steps incrementally, on the same code
// scan (stripLiteralsStateful) LocallyBoundNames already uses — no
// additional f-string neutralization here, matching that existing tracker's
// scope rather than widening it as part of a seeding fix.
//
// LocallyBoundNames ONLY: pyConsumeParens tracks `(`/`)` alone, matching
// LocallyBoundNames' own pre-existing internal rule exactly. PyParamNames'
// own live advance tracks ALL of ()/[]/{} via pyBracketDelta (a multi-line
// default value's own `[`/`{` must stay "open" from the signature's point of
// view too — `b=[\n 1,\n],` only truly closes the signature at its outer
// `)`, not at the list's `]`) — seeding it from THIS function desyncs the
// two rules the moment a default value spans a bracket this doesn't count,
// so PyParamNames has its own PyParamSigDepthBefore below. Do not reuse this
// one for PyParamNames.
//
// Like PyParamNames, this cannot recover a signature whose CLOSE also sits
// outside the hunk: seeding fixes the depth at hunk start, not the missing
// text a hunk-only scanner never saw.
func PyDefSigDepthBefore(fileLines []AddedLine, idx int) int {
	if idx <= 0 || len(fileLines) == 0 {
		return 0
	}
	if idx > len(fileLines) {
		idx = len(fileLines)
	}
	open := ""
	depth := 0
	for _, l := range fileLines[:idx] {
		var scan string
		scan, open = stripLiteralsStateful(LangPython, l.Text, open)
		if depth > 0 {
			pyConsumeParens(scan, &depth)
		} else if loc := rePyDefOpen.FindStringIndex(scan); loc != nil {
			depth = 1
			pyConsumeParens(scan[loc[1]:], &depth)
		}
	}
	return depth
}

// assignLHS returns the substring left of the first plain assignment '=' on a
// line, or "" if there is none. It excludes comparison and arrow operators
// (==, !=, <=, >=, =>) and the walrus ':=' so only true assignment targets are
// captured.
func assignLHS(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != '=' {
			continue
		}
		if i+1 < len(s) && (s[i+1] == '=' || s[i+1] == '>') {
			i++ // ==, =>
			continue
		}
		if i > 0 {
			switch s[i-1] {
			case '=', '!', '<', '>', ':':
				continue // second char of ==, !=, <=, >=, :=
			}
		}
		return s[:i]
	}
	return ""
}

// splitTopLevelCommas splits s on commas that are not nested inside (), [], or
// {}, so a multi-declarator statement (`a = f(x, y), b = g()`) splits into its
// declarators without breaking on commas inside call args, arrays, or objects.
// Operates on literal-stripped text, so no string/comment commas remain.
func splitTopLevelCommas(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}
