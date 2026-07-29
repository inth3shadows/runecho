package guard

import "strings"

// CallShape is one call site's argument SHAPE as observed in a diff — the
// call-side half of issue #243's call-shape agreement check. It records how many
// arguments a call passes and under what names, never their types: RunEcho owns
// shape agreement deterministically, and type agreement belongs to a language
// server (KB decision #291).
//
// Kwargs preserves source order and may contain duplicates; a repeated keyword is
// itself a defect, so collapsing it here would destroy signal the consumer needs.
type CallShape struct {
	// Name is the unqualified callee identifier. Qualified calls (obj.method())
	// are never emitted — the same scope rule the existence check follows.
	Name string
	// LineNo is the 1-based new-file line the callee identifier sits on. A call
	// whose arguments span lines reports the line of the name, not of the
	// closing paren, so a finding points at the call the reader recognizes.
	LineNo int
	// Pos counts positional arguments. A *unpacking is NOT counted (its width is
	// unknowable statically) — HasStar records it instead.
	Pos int
	// Kwargs are the explicit `name=value` keyword names, in source order.
	Kwargs []string
	// HasStar reports a `*args` unpacking among the arguments. Any consumer
	// checking arity must abstain when it is set: the true positional count is
	// unknown.
	HasStar bool
	// HasKwStar reports a `**kwargs` unpacking. A consumer checking keyword names
	// must abstain when it is set: unnamed keywords may be supplied at runtime.
	HasKwStar bool
	// HasLambda reports a `lambda` at argument depth zero, whose parameter commas
	// are indistinguishable from argument commas without a full parse:
	// `foo(key=lambda a, b: a)` passes ONE argument but splits into two. Pos and
	// Kwargs are therefore unreliable when it is set and a consumer must abstain
	// on the call entirely. A lambda nested inside brackets is safe and does not
	// set this — its commas are not at depth zero.
	HasLambda bool
	// Unreliable reports that at least one argument could not be classified, so Pos
	// and Kwargs undercount and a consumer must abstain on the call.
	//
	// It is the residual default for a segment whose masked text is empty while its
	// source text is not, once the two known causes — a comment and an
	// interpolation-nested literal — have been accounted for. No input is currently
	// known to reach it; it exists so that an unanticipated masking case degrades to
	// an abstention rather than to a silent undercount, and it is deliberately not
	// pinned by a fixture because pinning would require inventing an input that
	// cannot occur.
	Unreliable bool
}

const (
	// callShapeMaxLookahead bounds how many contiguous added lines are consumed
	// looking for the paren that closes one argument list. A call list longer
	// than this abstains rather than scanning unboundedly — the guard runs inside
	// an agent's edit loop on attacker-influenced repo text, so every scan needs a
	// ceiling (see the #212 red-team pass).
	callShapeMaxLookahead = 40
	// callShapeMaxArgBytes bounds the accumulated argument text for one call.
	callShapeMaxArgBytes = 8 << 10 // 8 KiB
	// callShapeMaxSites bounds the returned slice. A generated or minified file can
	// hold enormous numbers of call sites; the check's value does not scale with
	// them, and the hook's latency budget does not tolerate them.
	callShapeMaxSites = 4096
	// callShapeMaxTotalScan bounds the bytes examined looking for closing parens
	// ACROSS every candidate in one call. The per-call ceilings above bound each
	// attempt, and callShapeMaxSites bounds successes — but an abstention costs a
	// full lookahead and produces no site, so neither ceiling bounds the work a
	// file of unbalanced parens can force. `foo(` repeated 60 times per line over
	// 4000 lines measured 19 s before this budget existed, and maxDiffBytes is
	// 64 MiB, so the input is reachable inside an agent's edit loop (#212).
	callShapeMaxTotalScan = 4 << 20 // 4 MiB
)

// ExtractCallShapes extracts the argument shape of every unqualified call site in
// the added lines whose argument list is FULLY VISIBLE within them.
//
// Python only. Other languages return nil: the argument-shape grammar (and what
// counts as "keyword") differs per language, and a shared code path that silently
// half-worked on Go or JS would be worse than an explicit gap. Go return-arity is
// the documented next slice (#243), not a fall-through of this one.
//
// Abstention is the whole design. A call is SKIPPED — never guessed at — when its
// argument list does not balance inside the added lines (arguments continuing into
// unchanged context above or below the hunk are unknowable from a diff), when a
// hunk gap interrupts it, or when it exceeds the byte/line ceilings above. The
// consumer therefore sees only call sites whose shape is certain, which is what
// lets a mismatch be reported as fact rather than suspicion.
//
// openSeed mirrors extractRefs': openSeed(lineNo) returns the unterminated
// multi-line string delimiter in effect at the START of new-file line lineNo, so a
// hunk that begins inside a pre-existing docstring is scanned masked instead of as
// code. Without it, prose reading `Runs COUNT(x) and calls helper(a, b).` inside an
// existing docstring yields two confident call shapes — the #145 class the existence
// check already fixed. A nil seed means every contiguous run starts outside any
// string, which is correct only for a whole-file scan.
func ExtractCallShapes(lang Lang, lines []AddedLine, openSeed func(lineNo int) string) []CallShape {
	if lang != LangPython {
		return nil
	}

	var out []CallShape
	// scanned accumulates bytes examined by collectArgText across every candidate —
	// see callShapeMaxTotalScan. Successful and abandoned attempts both count, since
	// abandonment is the expensive case.
	scanned := 0

	// Masked scan text per line, indexed the same as lines. Literal content is
	// blanked (length-preserving) so a paren or `=` inside a string cannot alter
	// the parse, and a comment cannot close an argument list.
	scans := make([]string, len(lines))
	// runEnd[i] is one past the last line of i's contiguous run. Argument lists are
	// only followed within a run: a diff hunk's added lines may not be contiguous,
	// and neither bracket nor string continuity survives a gap.
	runEnd := make([]int, len(lines))

	open := ""
	prevNo := 0
	runFrom := 0
	for i, l := range lines {
		if i == 0 || l.LineNo != prevNo+1 {
			// Close out the previous run, then start a new one. Carried string
			// state resets — see the same reasoning in extractRefs.
			for j := runFrom; j < i; j++ {
				runEnd[j] = i
			}
			runFrom = i
			if openSeed != nil {
				open = openSeed(l.LineNo)
			} else {
				open = ""
			}
		}
		prevNo = l.LineNo
		if open == "" && isCommentLine(lang, l.Text) {
			// A whole-line comment contributes no code, but it does not break a
			// run: a call list may legitimately span it.
			scans[i] = ""
			continue
		}
		scan, newOpen := stripLiteralsStateful(lang, l.Text, open)
		open = newOpen
		scans[i] = scan
	}
	for j := runFrom; j < len(lines); j++ {
		runEnd[j] = len(lines)
	}

	builtins := builtinsFor(lang)

	for i, l := range lines {
		scan := scans[i]
		if scan == "" {
			continue
		}
		// Names this line DEFINES. `def foo(a=1)` matches the call regex on `foo(`
		// but is a declaration, and its `a=1` is a default, not a keyword
		// argument — reading it as a call would invent a caller that does not
		// exist. Skipping per-name (not per-line) keeps a genuine call sharing the
		// line, e.g. `def f(x=g(y=1))`.
		defs := defNames(lang, l.Text)

		for _, idx := range reCallIdent.FindAllStringSubmatchIndex(scan, -1) {
			fullStart := idx[0]
			name := scan[idx[2]:idx[3]]

			// Qualified call, or a match landing mid-identifier: out of scope, the
			// same boundary the existence check draws.
			if fullStart > 0 {
				if prev := scan[fullStart-1]; prev == '.' || isWordByte(prev) {
					continue
				}
			}
			if _, ok := builtins[name]; ok {
				continue
			}
			if _, isDef := defs[name]; isDef {
				continue
			}

			if scanned >= callShapeMaxTotalScan {
				// Whole-input scan budget spent; abandon the rest rather than let a
				// file of unbalanced parens dictate how long the hook runs.
				return out
			}
			// idx[1] is one past the '(' the regex matched.
			masked, orig, ok, used := collectArgText(scans, lines, i, runEnd[i], idx[1])
			scanned += used
			if !ok {
				continue // unbalanced within the visible lines — abstain
			}
			cs := parseArgShape(masked, orig)
			cs.Name = name
			cs.LineNo = l.LineNo
			out = append(out, cs)
			if len(out) >= callShapeMaxSites {
				return out
			}
		}
	}
	return out
}

// collectArgText returns the argument text between a call's opening paren and its
// matching close, accumulating across contiguous lines [start, end) beginning at
// byte offset from in scans[start]. ok is false when the paren does not balance
// inside those lines or a ceiling is hit — the caller must abstain, not guess.
//
// TWO parallel strings are returned, and both are needed:
//
//   - masked, from the literal-blanked scan, drives every structural decision.
//     Parens, commas and `=` inside a string or comment are already blanked there
//     and so cannot skew depth, argument splitting, or keyword detection.
//   - orig, the same span of the raw source, answers only "was there an argument
//     here at all". A string-literal argument masks to pure whitespace, which is
//     byte-identical to the nothing after a trailing comma — `_fmt(gap, '+.4f')`
//     read as ONE argument until orig was carried alongside. Masking is
//     length-preserving, so the two strings stay index-aligned and a range taken
//     from one applies exactly to the other.
func collectArgText(scans []string, lines []AddedLine, start, end, from int) (masked, orig string, ok bool, used int) {
	var mb, ob strings.Builder
	depth := 1
	limit := start + callShapeMaxLookahead
	if limit > end {
		limit = end
	}
	for i := start; i < limit; i++ {
		s := scans[i]
		raw := lines[i].Text
		if len(raw) != len(s) {
			// Index alignment between masked and orig is the premise of this pairing.
			// stripLiteralsStateful is length-preserving, so the only expected
			// mismatch is a line this function's caller emptied (a whole-line
			// comment), which contributes no bytes at all. Anything else means the
			// invariant broke: abstain rather than mis-slice.
			if s != "" {
				return "", "", false, used
			}
			raw = ""
		}
		j := 0
		if i == start {
			j = from
		} else {
			// Joining a continuation line: a newline inside parens is whitespace in
			// Python, and the separator matters — without it `foo(a\nb)` would
			// read as the single token `ab`. Written to both builders to keep them
			// aligned.
			mb.WriteByte(' ')
			ob.WriteByte(' ')
		}
		for ; j < len(s); j++ {
			switch s[j] {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				depth--
				if depth == 0 {
					return mb.String(), ob.String(), true, used
				}
			}
			mb.WriteByte(s[j])
			ob.WriteByte(raw[j])
			used++
			if mb.Len() > callShapeMaxArgBytes {
				return "", "", false, used
			}
		}
	}
	return "", "", false, used
}

// parseArgShape classifies one balanced argument list. masked drives every
// structural decision; orig answers only whether a segment holds an argument at
// all. The two must be index-aligned — see collectArgText.
func parseArgShape(masked, orig string) CallShape {
	var cs CallShape
	cs.HasLambda = hasTopLevelLambda(masked)
	for _, r := range splitTopLevel(masked) {
		mseg := strings.TrimSpace(masked[r[0]:r[1]])
		if mseg == "" {
			// The masked segment holds no code. Three things look like this, and only
			// the source text tells them apart:
			//   - nothing at all — the trailing comma of `f(x,)`. Punctuation.
			//   - a string-literal argument — `_fmt(gap, '+.4f')`. Masking blanks its
			//     interior, so without this branch the call read as one argument.
			//   - a comment — `f(\n  a,  # why\n)`. Punctuation when it is the whole
			//     segment, but a comment sitting between a comma and a value on the
			//     NEXT line is indistinguishable once lines are joined, so the shape
			//     is marked unreliable rather than guessed at.
			switch oseg := strings.TrimSpace(orig[r[0]:r[1]]); {
			case oseg == "":
				// punctuation
			case oseg[0] == '#':
				// A pure comment. It cannot be shadowing a value on the next line:
				// masking blanks only the comment text, so a value there would still
				// appear in masked and this branch would not be reached — which is
				// what TestExtractCallShapes_CommentLineBeforeValueIsReliable pins.
				// Ordinary Black-formatted Python with a magic trailing comma and
				// per-argument comments lands here.
			case oseg[0] == '\'' || oseg[0] == '"':
				// A literal whose QUOTES were blanked too, which happens only inside an
				// f-string interpolation (maskNestedLiteral). In plain context
				// stripLiteralsStateful keeps the quote bytes, so such a segment is
				// non-empty in masked and never reaches here.
				cs.Pos++
			default:
				cs.Unreliable = true
			}
			continue
		}
		switch {
		case strings.HasPrefix(mseg, "**"):
			cs.HasKwStar = true
		case strings.HasPrefix(mseg, "*"):
			cs.HasStar = true
		default:
			if kw := keywordName(mseg); kw != "" {
				cs.Kwargs = append(cs.Kwargs, kw)
			} else {
				cs.Pos++
			}
		}
	}
	return cs
}

// hasTopLevelLambda reports a `lambda` keyword at bracket depth zero in an
// argument list, which makes the comma split unreliable — see CallShape.HasLambda.
// Word boundaries are required so an identifier merely containing the letters
// (`lambda_fn`, `my_lambda`) does not trip it.
func hasTopLevelLambda(s string) bool {
	const kw = "lambda"
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
			continue
		case ')', ']', '}':
			depth--
			continue
		}
		if depth != 0 || s[i] != 'l' || !strings.HasPrefix(s[i:], kw) {
			continue
		}
		if i > 0 && isWordByte(s[i-1]) {
			continue
		}
		if j := i + len(kw); j < len(s) && isWordByte(s[j]) {
			continue
		}
		return true
	}
	return false
}

// splitTopLevel returns the [start, end) byte ranges of an argument list's
// segments, splitting on commas at bracket depth zero. Ranges rather than
// substrings, so a caller can apply the same split to the index-aligned original
// text — see parseArgShape.
//
// It does not attempt to resolve the lambda ambiguity: `foo(key=lambda a, b: a)`
// passes one argument but splits into two, and hasTopLevelLambda flags the call so
// the consumer abstains.
func splitTopLevel(s string) [][2]int {
	var out [][2]int
	depth, last := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, [2]int{last, i})
				last = i + 1
			}
		}
	}
	return append(out, [2]int{last, len(s)})
}

// keywordName returns the keyword name a segment binds, or "" when the segment is
// positional.
//
// The rule is Python's: a keyword argument is `NAME = value` where NAME is a bare
// identifier and `=` is at bracket depth zero. Everything that merely CONTAINS an
// `=` is positional, and the discriminations below are the ones that bite:
//
//   - comparison and augmented operators — `a == b`, `a != b`, `a <= b`, `a >= b`
//   - the walrus `n := len(x)`, whose `=` follows a colon
//   - a nested default inside a lambda — `key=lambda a=1: a` binds `key`, the
//     FIRST depth-zero `=`, never `a`
//   - an `=` inside brackets — `foo(d[k] == 1)` is one positional argument
//
// A misread here is a false positive on working code, which costs the check its
// credibility far faster than a missed defect does; every ambiguous shape resolves
// to positional.
func keywordName(seg string) string {
	depth := 0
	for i := 0; i < len(seg); i++ {
		switch c := seg[i]; c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case '=':
			if depth != 0 {
				continue
			}
			// `==` is a comparison; skip both bytes.
			if i+1 < len(seg) && seg[i+1] == '=' {
				i++
				continue
			}
			// `!=`, `<=`, `>=` are comparisons; `:=` is the walrus. In all four the
			// byte before `=` disqualifies it, and none can be a keyword binding.
			if i > 0 {
				switch seg[i-1] {
				case '!', '<', '>', ':', '=':
					continue
				}
			}
			name := strings.TrimSpace(seg[:i])
			if !isIdent(name) {
				return ""
			}
			return name
		}
	}
	return ""
}

// isIdent reports whether s is a single bare Python identifier. Anything else —
// an attribute path, a subscript, an expression — means the `=` was not a keyword
// binding.
func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			if i == 0 {
				return false
			}
			continue
		}
		if !isWordByte(c) {
			return false
		}
	}
	return true
}
