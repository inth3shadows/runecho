package guard

import (
	"regexp"
	"strings"
)

// Go local-binding extraction.
//
// WHY THIS IS REQUIRED, not an optimisation. Until unexported Go symbols were
// indexed, the guard skipped every lowercase Go reference outright, so nothing
// local could false-positive. Checking bare `foo()` changes that: in Go a bare
// call also resolves to a local variable, a parameter, a named return, a range
// variable or a type-switch binding, none of which are package-level
// declarations and none of which the IR will ever hold. Without this extractor,
// turning the check on would flag every `handler()` where `handler := …` — the
// exact false-positive class that took several releases to kill in JS and
// Python.
//
// PRECISE, NOT OVER-INCLUSIVE. The JS lesson (PR #156) applies unchanged: the
// fix there was JSDeclaredNames, a precise declared-names extractor, rather than
// the over-inclusive LocallyBoundNames. Over-inclusion looks safer because it
// suppresses more false positives, but it does so by masking real undefined
// references — converting a visible false positive into a silent false negative,
// which is strictly worse. So these patterns match binding FORMS, and a name is
// only collected where the syntax genuinely declares it.
//
// WHOLE-FILE, NOT HUNK-SCOPED. Callers pass the whole file: the binding for a
// name used inside an edited hunk almost always sits outside that hunk (a
// parameter on the enclosing function's signature line, a `:=` earlier in the
// body). A hunk-scoped extractor would see the use and miss the binding, which
// is the same defect from the other direction.

var (
	// reGoVarConst matches a single-line `var`/`const`/`type` declaration. The
	// keyword form carries the same multi-name left-hand side as a short
	// declaration.
	//
	// `type` belongs here even though top-level types are indexed: a type
	// declared INSIDE a function is not, and a local type is callable in
	// conversion position (`referralCode(5)`). The compiler-oracle differential
	// found exactly that as a proven false positive in x/text.
	reGoVarConst = regexp.MustCompile(`^\s*(?:var|const|type)\s+([A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*)`)

	// reGoFunc matches the `func` keyword as a word. The parenthesised groups
	// that follow it are collected by goFuncGroups with a balanced scan rather
	// than a regex: a func-typed parameter nests parens
	// (`openSeed func(lineNo int) string`), and a `[^)]*` capture stops at the
	// FIRST close paren, silently truncating the parameter list. That truncation
	// dropped every parameter after the first func-typed one, which the
	// compiler-oracle differential caught as a proven false positive
	// (`braceDepthSeed`) within minutes of this extractor going live.
	reGoFunc = regexp.MustCompile(`\bfunc\b`)

	// reGoParamName matches one `name Type` pair inside a parameter list. The
	// type is not captured: folding a type name into the known set would mask a
	// genuinely undefined type, which is the over-inclusion trap above. A group
	// may open with '(' as well as a comma, so the parameters of an inline func
	// literal bind too.
	reGoParamName = regexp.MustCompile(`(?:^|[,(])\s*([A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*)\s+\.{0,3}\s*` + goTypeStart)

	// reGoIdentList splits a captured left-hand side into individual names.
	reGoIdentList = regexp.MustCompile(`[A-Za-z_]\w*`)
)

// GoDeclaredNames returns the names bound by declarations, parameters and
// range/type-switch clauses across fileLines. Go only. Blank identifiers are
// skipped; the result is deduplicated and unordered.
//
// Every name here is one a bare `foo()` could legitimately resolve to without
// any package-level declaration existing, which is why FoldInFileDefs folds them
// into the known set before the additive check runs.
func GoDeclaredNames(fileLines []AddedLine) []string {
	return goDeclaredNamesScan(fileLines, nil)
}

// goDeclaredNamesSeeded is GoDeclaredNames for a diff hunk with validate.go's
// openSeed, so a declaration that is only text inside a raw string opened above
// the hunk is not bound (#436). Intersected with the unseeded read so a seed
// can only remove names, never add them (see knownDefs).
func goDeclaredNamesSeeded(lines []AddedLine, openSeed func(lineNo int) string) []string {
	if openSeed == nil {
		return goDeclaredNamesScan(lines, nil)
	}
	return intersectNames(goDeclaredNamesScan(lines, openSeed), goDeclaredNamesScan(lines, nil))
}

// goDeclaredNamesScan is the GoDeclaredNames pass. With a nil openSeed the
// literal state carries across line gaps (the historical whole-file scan); with
// one, it is taken from the seed at the start of every contiguous run. A wrapped
// signature is dropped at a gap either way.
func goDeclaredNamesScan(fileLines []AddedLine, openSeed func(lineNo int) string) []string {
	seen := make(map[string]struct{})
	open := ""
	inVarBlock := false
	blockDepth := 0
	// A wrapped `func` signature, joined line by line until its groups close
	// (#473); sigLines bounds how far a group that never closes is carried.
	sig, sigLines := "", 0
	prevNo := 0
	for i, l := range fileLines {
		if openSeed != nil && (i == 0 || l.LineNo != prevNo+1) {
			open = openSeed(l.LineNo)
		}
		if i > 0 && l.LineNo != prevNo+1 {
			sig = "" // never join text across a hunk gap into one parameter list
		}
		prevNo = l.LineNo
		// Literal stripping allocates a fresh byte slice per line, and it is the
		// single largest cost in this pass. A line with no quote character cannot
		// open, close or contain a literal, so when no multi-line literal is
		// already open its text, less a trailing `//` comment, is its own scan. Most lines qualify, which is what
		// takes this pass from 6.75ms to well under 1ms on a 2,200-line file.
		scan := l.Text
		// `/*` is checked alongside the quote characters because a block-comment
		// opener needs none of them. Without it the stripper never runs on such a
		// line, the multi-line comment state is never entered, and declarations
		// inside commented-out code get folded into the known set — over-inclusion,
		// which masks real hallucinations and is the failure this file's header
		// calls strictly worse than a false positive.
		if open != "" || strings.ContainsAny(l.Text, "\"'`") || strings.Contains(l.Text, "/*") {
			scan, open = stripLiteralsStateful(LangGo, l.Text, open)
		} else {
			// The stripper did not run, so a trailing comment survives in scan; cut
			// it once for every extractor below (#500). Stripped lines have theirs
			// blanked already.
			scan = withoutLineComment(scan)
		}
		if isCommentLine(LangGo, l.Text) {
			continue
		}
		// A carried line is scanned as part of the joined signature; scanning it
		// alone as well would bind names the joined scan knows are inside a type
		// list (a constraint line `F func(x int) bool,`).
		carried := sig != ""
		if carried {
			sigLines++
			joined := sig + " " + scan
			groups, rest := goFuncGroups(joined)
			bindFuncGroups(seen, groups)
			if rest != joined {
				sigLines = 0 // a new signature opened on the line that closed the last
			}
			sig = rest
			if sigLines >= maxWrappedSigLines {
				sig = ""
			}
		}
		// Cheap pre-filters before any regex. This pass runs on every Go hook
		// invocation inside a ~12ms end-to-end budget, and the unfiltered version
		// measured 6.75ms on a 2,200-line file — over half the budget for one
		// extractor. Most lines contain neither `:=` nor a declaration keyword nor
		// `func`, so a substring test skips the regex engine entirely for them.
		if strings.Contains(scan, ":=") {
			for _, name := range goShortDeclNames(scan) {
				if name != "_" {
					seen[name] = struct{}{}
				}
			}
		}
		if hasGoDeclKeyword(scan) {
			if m := reGoVarConst.FindStringSubmatch(scan); m != nil {
				addList(seen, m[1])
			}
		}
		// A parenthesised `var (` / `const (` / `type (` block declares names on lines that
		// carry no keyword of their own, so the block has to be tracked. Entry
		// requires the paren to stay open past the line; a single-line
		// `var (x = 1)` is already covered by reGoVarConst.
		if !inVarBlock {
			if hasGoDeclKeyword(scan) && reGoVarBlockOpen.MatchString(scan) {
				inVarBlock, blockDepth = true, 1
			}
		} else {
			// Depth, not "the first line starting with ')'". A wrapped call inside
			// the block — `a = compute(\n  1,\n)` — closes on an indented paren
			// without ending the block, and treating that as the end dropped every
			// name declared after it. This is the same nested-paren bug the parser's
			// AST rewrite fixed for the exported half (internal/parser/go.go), and
			// it came straight back in a regex extractor.
			before := blockDepth
			blockDepth += strings.Count(scan, "(") - strings.Count(scan, ")")
			if blockDepth <= 0 {
				inVarBlock = false
			} else if before == 1 {
				// Only lines at the block's own depth declare names; deeper lines are
				// arguments to a wrapped call.
				if m := reGoBlockDeclName.FindStringSubmatch(scan); m != nil {
					addList(seen, m[1])
				}
			}
		}
		// Every balanced group after a `func` keyword: the receiver, the parameter
		// list, and the named-return list are all `name Type` lists that bind, and
		// treating them uniformly avoids having to know which is which on a line
		// that may hold a method, a literal, or both.
		if !carried && strings.Contains(scan, "func") {
			groups, rest := goFuncGroups(scan)
			bindFuncGroups(seen, groups)
			if rest != "" && sig == "" {
				sig, sigLines = rest, 0
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// maxWrappedSigLines caps how many lines a still-open signature group is joined
// across. gofmt output closes one within a few dozen; a group that has not is
// something else, and carrying it further only risks binding body text.
const maxWrappedSigLines = 64

// goTypeStart is how a Go type may begin after a declared name, shared by the
// parameter and var-block patterns so the two cannot drift (#500). `<-` must be
// followed by `chan`: `x <-1` is the comparison `x < -1`, and a bare `<` never
// starts a type. A pointer's `*` must be glued to its base type: `x * y` is a
// product, and gofmt never writes a binary `*` as `x *y` (#506). The base may
// be any letter (`*Ärger`), a slice or array, or a parenthesised type.
const goTypeStart = `(?:<-\s*chan\b|\*+(?:[\pL_\[(]|<-\s*chan\b)|[A-Za-z_\[\]\.])`

var (
	reGoVarBlockOpen = regexp.MustCompile(`^\s*(?:var|const|type)\s*\(\s*$`)
	// `\b` after the names: without it a name the type pattern rejects
	// backtracked into a shorter name plus a "type" (`in <-chan int` bound `i`).
	// A type needs whitespace before it: gofmt always writes `name Type`, and with
	// none a continuation line gofmt glues (`x*(y+1)`, `x.Sel |`, `x[:],`) read
	// as a declaration of x (#506). The one glued form gofmt does write is a
	// generic type entry, `List[T any] struct{`, admitted only in type-parameter
	// shape: names, whitespace, then a constraint (`any`, `~int`, `*T`, `[]T`).
	// `=` may follow directly.
	reGoBlockDeclName = regexp.MustCompile(`^\s*([A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*)\b(?:\s+` + goTypeStart + `|\s*=|\[\s*[A-Za-z_]\w*(?:\s*,\s*[A-Za-z_]\w*)*\s+[A-Za-z_\[*(~])`)
)

// goShortDeclNames returns the identifiers bound by every `:=` on the line,
// found by walking LEFT from each operator rather than matching a line-anchored
// pattern.
//
// The anchored version handled `x :=`, `for k, v :=` and `switch t :=` and
// nothing else, so three ordinary Go forms bound nothing and false-positived:
//
//	if fn, ok := m[k]; ok { fn() }   // registry dispatch, the canonical idiom
//	case fn := <-ch:                 // channel receive in a select
//	func() { g := mk(); g() }()      // bind and call on one line
//
// Walking left from `:=` covers every position an operator can appear in, which
// is the property an enumeration of prefixes cannot have.
func goShortDeclNames(scan string) []string {
	var out []string
	for i := 0; i+1 < len(scan); i++ {
		if scan[i] != ':' || scan[i+1] != '=' {
			continue
		}
		// Walk left across `name` and `, name` pairs until something that cannot
		// be part of an identifier list.
		j := i
		for j > 0 {
			k := j
			for k > 0 && (scan[k-1] == ' ' || scan[k-1] == '\t') {
				k--
			}
			end := k
			for k > 0 && isWordByte(scan[k-1]) {
				k--
			}
			if k == end {
				break // no identifier here
			}
			out = append(out, scan[k:end])
			for k > 0 && (scan[k-1] == ' ' || scan[k-1] == '\t') {
				k--
			}
			if k == 0 || scan[k-1] != ',' {
				break
			}
			j = k - 1
		}
	}
	return out
}

// hasGoDeclKeyword reports whether a line opens with var/const/type after
// indentation — the only place reGoVarConst and reGoVarBlockOpen can match.
// Hand-rolled rather than another regex: it is the pre-filter, so it has to be
// cheaper than the thing it is filtering.
func hasGoDeclKeyword(scan string) bool {
	i := 0
	for i < len(scan) && (scan[i] == ' ' || scan[i] == '\t') {
		i++
	}
	rest := scan[i:]
	return strings.HasPrefix(rest, "var") || strings.HasPrefix(rest, "const") || strings.HasPrefix(rest, "type")
}

// bindFuncGroups records the parameter names in each group goFuncGroups returned.
func bindFuncGroups(seen map[string]struct{}, groups []string) {
	for _, group := range groups {
		for _, p := range reGoParamName.FindAllStringSubmatch(group, -1) {
			addList(seen, p[1])
		}
	}
}

// withoutLineComment cuts a trailing `//` comment from scan. The stripper only
// runs on lines with a quote or `/*` (see the pre-filter in goDeclaredNamesScan),
// so an unmasked comment survives, and its words or parens would otherwise reach
// the `:=`, var-block and signature extractors. Any `//` still in scan is a
// comment: string content has already been masked whenever a quote is present.
func withoutLineComment(scan string) string {
	if i := strings.Index(scan, "//"); i >= 0 {
		return scan[:i]
	}
	return scan
}

// addList records every identifier in a captured left-hand side, skipping the
// blank identifier.
func addList(seen map[string]struct{}, lhs string) {
	for _, name := range reGoIdentList.FindAllString(lhs, -1) {
		if name != "_" {
			seen[name] = struct{}{}
		}
	}
}

// goFuncGroups returns the text inside each top-level parenthesised group that
// follows a `func` keyword on scan, stopping at the body brace; a bracket group
// (type parameters, `[]T`) is stepped over, not returned. Nesting is
// tracked, so a group whose content contains its own parens is returned whole
// rather than cut at the first close paren.
//
// scan must already have string and comment content masked. A group still open
// at the end of scan (a signature gofmt wrapped across lines) is not guessed at:
// the groups that closed are returned, and open is scan from that group's `func`
// keyword on, for the caller to join to the following lines and scan again (#473).
func goFuncGroups(scan string) (out []string, open string) {
	// End of the last bracket group stepped over: a `func` inside one (a
	// func-typed constraint, `[F func(x int) bool]`) is part of a type and its
	// parameter names are not bindings.
	stepped := 0
	for _, loc := range reGoFunc.FindAllStringIndex(scan, -1) {
		if loc[0] < stepped {
			continue
		}
		i := loc[1]
		for i < len(scan) {
			switch scan[i] {
			case '(':
				depth, start := 1, i+1
				j := i + 1
				for ; j < len(scan) && depth > 0; j++ {
					switch scan[j] {
					case '(':
						depth++
					case ')':
						depth--
					}
				}
				if depth != 0 {
					return out, scan[loc[0]:]
				}
				out = append(out, scan[start:j-1])
				i = j
				continue
			case '[':
				// A type-parameter list (or a `[]T` / `map[K]V` type). Its names are
				// types, so it is skipped, not collected; stepping over it whole keeps
				// a constraint's brace (`interface{ ~int }`) from reading as the body.
				// Left open at end of line it is a wrapped signature like an open
				// paren group (#500).
				depth := 1
				j := i + 1
				for ; j < len(scan) && depth > 0; j++ {
					switch scan[j] {
					case '[':
						depth++
					case ']':
						depth--
					}
				}
				if depth != 0 {
					return out, scan[loc[0]:]
				}
				stepped = max(stepped, j)
				i = j
				continue
			case '{':
				i = len(scan) // body starts; no more signature groups
				continue
			}
			i++
		}
	}
	return out, ""
}
