package guard

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Local-variable-type method check (Go): a call on a local variable of a
// repo-defined type whose declared or literal type has no such method.
//
// WHY THIS SHAPE. recvmethod.go closed the one v.Method() binding form that
// needs no type inference — a method receiver's type is written on the line
// that opens the method. #312 asked whether the rest of v.Method() (3/60 on
// the compiler-oracle differential) is worth a type-aware pass or a
// documented non-goal. This is the next-smallest slice after receivers: a
// local variable whose type is written EXPLICITLY, in the same file, in one
// of two forms Go itself makes unambiguous to read without a type checker:
//
//	var v *T          / var v T          (explicit type)
//	v := &T{...}      / v := T{...}      (composite literal)
//
// Deliberately excluded, and why: function parameters (no textual initializer
// in a hunk to resolve against), package-qualified types like pkg.T (a new
// import-resolution dependency, not a same-file text match), interfaces (no
// literal form to bind from), and any other assignment (`v = x`, `v := other`)
// — resolving those needs dataflow, not text. A name bound more than once
// anywhere in the file is treated as ambiguous and dropped entirely, the same
// discipline goReceiverTypes applies to receivers.
//
// KNOWN RESIDUAL, same class as recvmethod.go's: a plain reassignment
// (`v = &U{}`, no `:=`/`var`) after a valid typed binding is invisible to the
// rebinding scan below (which only recognises `:=` and `var` forms), so v is
// still judged against its ORIGINAL type. This mirrors a pre-existing gap in
// the receiver check (a receiver reassigned inside its own method body is
// equally invisible to it) rather than introducing a new one.
//
// The call-site gates (member-indexed, name-globally-unknown) and every
// helper below except the two binding regexes are shared verbatim with
// recvmethod.go — see that file's header for what each protects against.

// reGoVarDeclType matches `var v *T` / `var v T` with an explicit, same-
// package type. Like reGoMethodRecv, the type capture stops at the first
// non-word byte, stripping generic instantiation.
//
// A package-qualified type (`var v pkg.T`) would still satisfy this pattern —
// \b is a word/non-word transition, and "pkg" ends on exactly such a
// transition into ".T" — so the caller MUST check the byte immediately after
// the type capture and discard the match if it is '.'. RE2 has no lookahead
// to express that inside the pattern itself.
var reGoVarDeclType = regexp.MustCompile(`(?:^|[^\w.])var\s+([A-Za-z_]\w*)\s+\*?\s*([A-Za-z_]\w*)\b`)

// reGoCompositeLitBind matches `v := &T{` / `v := T{`, including a generic
// instantiation (`v := &Set[int]{`) — the optional bracket group between the
// type name and `{` is what a plain `T\s*\{` requirement would miss. Anchored
// so v is the ONLY name on the left: a tuple form (`v, err := f()`) or a
// non-literal RHS (`v := f()`, `v := map[string]T{}`, `v := []T{}`) cannot
// satisfy "identifier (plus at most one bracket group) immediately followed
// by {", so those are not merely unrecognised — the pattern itself cannot
// match them. A package-qualified literal (`v := &pkg.T{}`) is excluded the
// same way: the candidate capture "pkg" is never immediately followed by '['
// or '{' (a '.' sits in between), so no position in "pkg.T{" completes the
// match.
var reGoCompositeLitBind = regexp.MustCompile(`(?:^|[^\w.])([A-Za-z_]\w*)\s*:=\s*&?\s*([A-Za-z_]\w*)(?:\[[^][]*\])?\s*\{`)

// matchingBraceClose returns the index of the '}' matching the '{' at open,
// scanning only the rest of scan (this line) with a running depth counter so
// a nested brace pair inside is not mistaken for the close. Returns -1 if it
// does not close before the line ends (a multi-line composite literal) —
// callers must treat that as "unknown", not as "does not close".
func matchingBraceClose(scan string, open int) int {
	depth := 0
	for i := open; i < len(scan); i++ {
		switch scan[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// reGoFuncLine matches any line introducing a function signature — a
// top-level func, a method (with receiver), or an inline closure.
var reGoFuncLine = regexp.MustCompile(`(?:^|[^\w.])func\b`)

var reGoIdent = regexp.MustCompile(`[A-Za-z_]\w*`)

// goFuncSignatureIdents returns every identifier appearing on any function
// signature — from its `func` line through the line that opens its body —
// receiver name, parameter names AND types, return names and types, the
// function's own name.
//
// This exists to protect against the one failure mode a file-wide,
// scope-blind text scan cannot otherwise see: a name reused as a PARAMETER in
// one function and as a var/composite-literal binding in another. Parameters
// are invisible to goVarTypes' var/:= rebinding gate — they are never `var`
// or `:=` — so a file-wide name collision with a parameter reads as safe when
// it is not. This is not hypothetical: golang.org/x/text's
// unicode/cldr/resolve.go declares `parent reflect.Value` as a parameter in
// three functions and `var parent *LDML` as a genuinely typed local in a
// fourth; without this reservation, every `parent.IsNil()`/
// `parent.FieldByIndex()` call inside the first three — where parent is a
// reflect.Value, not an LDML — read as an LDML method-set violation. Caught
// by the compiler-oracle differential's external-corpus sweep, not by any
// same-repo test.
//
// Deliberately over-inclusive: reserving every identifier on the signature
// (not just the parameter names) trades some recall for never having to
// parse Go's grouped-parameter syntax correctly (`func f(a, b string, c
// *T)` — a, b, c are names, string and T are types, and getting that
// parse wrong risks reintroducing exactly this bug). The cost is one-sided —
// fewer bindings, never a wrong one.
//
// The signature is judged to end at the first '{' seen once the
// parameter-list (and any parenthesized return-list) parens have balanced
// back to zero — gated on PAREN depth, not on line content. An earlier draft
// ended the signature at the first '{' ANYWHERE on the line, with no paren
// tracking at all: a bare, unparenthesized parameter or return type carrying
// its own self-contained brace pair (`map[string]interface{}`, an inline
// `struct{...}`) sits INSIDE the still-open parameter-list parens, and ending
// the scan there silently failed to reserve every parameter on a later line
// of a multi-line signature — reopening the exact collision this function
// exists to prevent. Caught by adversarial review, reproduced with a
// `map[string]interface{}` parameter ahead of a same-named `parent *Other`
// parameter in a multi-line signature. Gating on paren depth alone is
// sufficient: every identifier on a line is captured in one pass BEFORE this
// loop asks where the signature ends, so a same-line self-contained brace
// that happens to sit at paren-depth zero (a bare, unparenthesized return
// type) costs nothing — nothing else remains on that same line to miss,
// since Go's body brace, once reached, is what starts the body.
func goFuncSignatureIdents(ctx []AddedLine) map[string]struct{} {
	out := make(map[string]struct{})
	inSig := false
	parenDepth := 0
	open := ""
	for _, l := range ctx {
		scan, newOpen := stripLiteralsStateful(LangGo, l.Text, open)
		open = newOpen
		if !inSig {
			if !reGoFuncLine.MatchString(scan) {
				continue
			}
			inSig = true
			parenDepth = 0
		}
		for _, m := range reGoIdent.FindAllString(scan, -1) {
			if m != "func" {
				out[m] = struct{}{}
			}
		}
		for i := 0; i < len(scan) && inSig; i++ {
			switch scan[i] {
			case '(':
				parenDepth++
			case ')':
				if parenDepth > 0 {
					parenDepth--
				}
			case '{':
				if parenDepth == 0 {
					inSig = false
				}
			}
		}
	}
	return out
}

// goVarTypes maps a local variable name to its type, for names bound via
// exactly one DISTINCT var/:= statement in ctx, via one of the two recognised
// forms above. A name touched by any second, textually different var/:=
// statement anywhere in ctx is ambiguous and absent from the result, so every
// consumer abstains on it.
//
// "Distinct" is measured by statement TEXT, not by occurrence count. wholeFile
// and addedLines legitimately overlap for a Write of a file whose content is
// unchanged (or a whole-file Write generally, where the pre-edit and proposed
// text are the same by construction) — ctx then contains the SAME binding
// line twice. Counting occurrences would misread that as two independent
// bindings and drop every valid one; comparing text collapses the duplicate
// while still catching a REAL rebinding (necessarily different text, even to
// the same type) — the same conservative "never rebound" discipline
// goReceiverTypes applies, just tolerant of this one duplication source.
func goVarTypes(ctx []AddedLine) map[string]string {
	types := make(map[string]string)
	bindingTexts := make(map[string]map[string]struct{})
	mark := func(name, text string) {
		set, ok := bindingTexts[name]
		if !ok {
			set = make(map[string]struct{})
			bindingTexts[name] = set
		}
		set[text] = struct{}{}
	}

	open := ""
	for _, l := range ctx {
		scan, newOpen := stripLiteralsStateful(LangGo, l.Text, open)
		open = newOpen

		for _, name := range goShortDeclNames(scan) {
			mark(name, scan)
		}
		for _, m := range reGoVarBinding.FindAllStringSubmatch(scan, -1) {
			mark(m[1], scan)
		}

		if idx := reGoCompositeLitBind.FindStringSubmatchIndex(scan); idx != nil {
			nameStart, nameEnd := idx[2], idx[3]
			typStart, typEnd := idx[4], idx[5]
			// idx[1] is one past the whole match, which ends in the literal
			// '{' the pattern requires — so idx[1]-1 is that brace's index.
			openBrace := idx[1] - 1
			close := matchingBraceClose(scan, openBrace)
			// close < 0 — the literal's closing brace is not on this line (a
			// multi-line composite literal). matchingBraceClose's own
			// contract is that this means "unknown", not "does not close" —
			// an earlier draft treated it as "not chained" and proceeded to
			// bind, silently contradicting that contract. Found by
			// /code-review: `v := &T{\n  Field: 1,\n}.Clone()` bound v to T
			// even though the real type is Clone()'s return type, because
			// close==-1 skipped the ".Clone()" check entirely instead of
			// abstaining on the unknown case.
			chained := close < 0
			if !chained {
				rest := strings.TrimLeft(scan[close+1:], " \t")
				// v := T{}.Clone() — the composite literal is not the RHS,
				// it is the RECEIVER of a chained selector/call. v's real
				// type is whatever the chain resolves to, not T; abstain
				// rather than bind the wrong one.
				chained = strings.HasPrefix(rest, ".")
			}
			if !chained {
				name, typ := scan[nameStart:nameEnd], scan[typStart:typEnd]
				if prev, seen := types[name]; !seen || prev == typ {
					types[name] = typ
				}
			}
		}
		if idx := reGoVarDeclType.FindStringSubmatchIndex(scan); idx != nil {
			nameStart, nameEnd := idx[2], idx[3]
			typStart, typEnd := idx[4], idx[5]
			if typEnd < len(scan) && (scan[typEnd] == '.' || scan[typEnd] >= utf8.RuneSelf) {
				// '.' — package-qualified type (var v pkg.T), out of scope.
				// >= RuneSelf — a UTF-8 continuation/lead byte immediately
				// after the ASCII-only \w* capture: \b is satisfied at this
				// boundary (RE2's \w is ASCII-only), so a unicode type name
				// (var v *Readerテスト) truncates to its ASCII prefix instead
				// of failing to match. Found by adversarial review — abstain
				// rather than risk binding to a coincidentally-real but
				// wrong, truncated type name.
				continue
			}
			name, typ := scan[nameStart:nameEnd], scan[typStart:typEnd]
			if prev, seen := types[name]; !seen || prev == typ {
				types[name] = typ
			}
		}
	}
	if len(types) == 0 {
		return nil
	}
	// Computed only once there is at least one candidate binding to filter —
	// this is a second full pass over ctx, and most files have none.
	reserved := goFuncSignatureIdents(ctx)
	for name := range types {
		if _, isReserved := reserved[name]; len(bindingTexts[name]) > 1 || isReserved {
			delete(types, name)
		}
	}
	if len(types) == 0 {
		return nil
	}
	return types
}

// GoVarTypeMethodViolations reports calls in addedLines of the form
// `v.Method(...)` where v is a local variable bound exactly once, in ctx, to
// a repo type T via an explicit `var`/composite-literal form, and T has no
// indexed method Method. wholeFile is the current file (pre-edit in hook
// mode); known is the flat repo symbol set. Go only; returns nil when nothing
// in the file binds a usable local.
//
// Shares every call-site gate with GoReceiverMethodViolations — see that
// file's header for what each protects against. The two checks are kept
// separate (not merged into one type map) so each has its own env gate and
// decision-log reason; see cmd/runecho-guard/vartype.go for why.
func GoVarTypeMethodViolations(wholeFile, addedLines []AddedLine, known map[string]struct{}) []Violation {
	ctx := make([]AddedLine, 0, len(wholeFile)+len(addedLines))
	ctx = append(ctx, wholeFile...)
	ctx = append(ctx, addedLines...)

	varTypes := goVarTypes(ctx)
	if len(varTypes) == 0 {
		return nil
	}
	typesWithMembers := goTypesWithMembers(known)

	var violations []Violation
	seen := make(map[string]struct{})
	open := ""
	prevNo := 0
	for i, l := range addedLines {
		if i == 0 || l.LineNo != prevNo+1 {
			open = ""
		}
		prevNo = l.LineNo
		if open == "" && isCommentLine(LangGo, l.Text) {
			continue
		}
		scan, newOpen := stripLiteralsStateful(LangGo, l.Text, open)
		open = newOpen
		for _, idx := range reGoQualifiedCall.FindAllStringSubmatchIndex(scan, -1) {
			qStart, qEnd := idx[2], idx[3]
			symStart, symEnd := idx[4], idx[5]
			q := scan[qStart:qEnd]
			sym := scan[symStart:symEnd]
			// Left-guard exactly as the receiver and qualified checks do: a
			// preceding '.' or word byte means this is a deeper selector
			// (`a.v.Foo`), where `v` is a field rather than the local itself.
			if qStart > 0 {
				if prev := scan[qStart-1]; prev == '.' || isWordByte(prev) {
					continue
				}
			}
			typ, ok := varTypes[q]
			if !ok {
				continue // not a single unambiguous typed local binding
			}
			if _, ok := typesWithMembers[typ]; !ok {
				continue // T has no indexed member set to argue from
			}
			if _, ok := known[typ+"."+sym]; ok {
				continue // the method exists on this type
			}
			if _, ok := known[sym]; ok {
				continue // the name exists somewhere — could be promoted
			}
			if goNameUsedAsAnyMethod(known, sym) {
				continue // the name is a method of some other type
			}
			key := typ + "." + sym
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			suggestions, _ := Suggest(sym, methodNamesOf(known, typ))
			violations = append(violations, Violation{
				Line:        l.LineNo,
				Symbol:      key,
				Lang:        LangGo,
				Suggestions: suggestions,
			})
		}
	}
	return violations
}
