package parser

import (
	"regexp"
	"sort"
	"strings"
)

// ShellParser implements structural parsing for POSIX / bash shell scripts
// (.sh, .bash). It extracts top-level function definitions — the two forms
// `name() { … }` and `function name { … }` — with per-function start lines AND
// body hashes (so an edit to a function body surfaces as "modified" in diff/verify,
// parity with the AST parsers).
//
// The engine is a length-preserving shell-aware masker (maskShell) — the shell
// counterpart of the guard's stripLiteralsStateful. It blanks the content of
// single/double-quoted strings, $(…) and `…` command substitutions, ${…} parameter
// expansions, comments, and heredoc bodies (respecting quoted/`<<-` and stacked
// delimiters and backslash escapes), so def-detection and brace/paren matching see
// only real code-level structure. A `}` or a `foo()`-shaped line inside a string or
// heredoc can therefore neither close a body early nor be mistaken for a definition.
//
// Deliberately parser-only: the guard's hallucination check stays OUT of shell —
// guard.LangFor returns Unknown for .sh/.bash, so ExtractRefs never runs on shell —
// because a shell "call" is a bare command (grep/git/awk) indistinguishable from
// thousands of external binaries. This parser feeds the IR/oracle only. Shell also
// has no import model (`source` binds no named symbols) and no classes, so Imports,
// Classes, and Exports are always empty.
//
// Known limitations (issue #155): a heredoc whose delimiter is a variable (`<<$x`)
// is not detected; extremely exotic quoting/nesting a real shell lexer would resolve
// may mask imperfectly. A regex+state line-aware scan is intentional — a full
// tree-sitter-bash grammar is heavier than the symbol model warrants.
//
// Two more, from the heredoc-eligibility fix (#281/#282). A heredoc inside a
// backtick substitution is legal shell but is not recognised: `…` content is masked
// wholesale without tracking inner structure, so there is nothing to hand a
// recognised heredoc to. And an UNTERMINATED heredoc — one whose terminator never
// appears before EOF — is abandoned rather than blanked, so the body of a truncated
// script is scanned as code and can yield a phantom definition from its tail. That
// is a deliberate trade (see the newline arm of maskShell): blanking forward
// unconditionally is what let a single misread `<<` swallow an entire well-formed
// file, and malformed input losing its tail is the smaller of the two.
type ShellParser struct{}

// NewShellParser creates a new shell parser.
func NewShellParser() *ShellParser { return &ShellParser{} }

// SupportsExtension returns true for .sh and .bash files.
func (p *ShellParser) SupportsExtension(ext string) bool {
	return ext == ".sh" || ext == ".bash"
}

// maskShell context-stack frame kinds. s/d/b/c/p are "blanking" frames (their
// content is masked); g/r are structural (kept, unless nested in a blanking frame).
const (
	frameSingle   byte = 's' // '…' single-quoted string
	frameDouble   byte = 'd' // "…" double-quoted string
	frameBacktick byte = 'b' // `…` command substitution
	frameCmdSub   byte = 'c' // $(…) command substitution
	frameParam    byte = 'p' // ${…} parameter expansion
	frameGroup    byte = 'g' // { … } command group / brace expansion
	frameSubshell byte = 'r' // ( … ) subshell / parens
)

// shellNameClass is the function-name charset. Conventionally an identifier, but
// bash permits `-`, `.`, and `:` (common in git hooks and namespaced helpers), so
// they are allowed. The `-` is last in the class so it is a literal, not a range.
const shellNameClass = `[A-Za-z_][A-Za-z0-9_.:-]*`

var (
	// `name()` / `name ()` — empty parens after a name is a function DEFINITION
	// only (a call never writes `()`), so this is unambiguous and low false-positive.
	// Run against the MASKED source, so a `foo()` inside a string/heredoc (blanked)
	// never matches. `[ \t]*` not `\s*` keeps `^` anchored to a single line.
	reShellFuncParen = regexp.MustCompile(`(?m)^[ \t]*(` + shellNameClass + `)[ \t]*\(\)`)
	// `function name` / `function name()`.
	reShellFuncKw = regexp.MustCompile(`(?m)^[ \t]*function[ \t]+(` + shellNameClass + `)`)
)

// Parse extracts top-level shell function definitions with body hashes and start
// lines. Best-effort and never errors: it is a masked line scan, so malformed input
// just yields whatever function defs were matched (honoring the Parser interface's
// partial-structure contract).
func (p *ShellParser) Parse(source string) (FileStructure, error) {
	// Normalize CRLF→LF so body hashes and line numbers are line-ending-independent
	// (parity with the Go/Python parsers).
	source = strings.ReplaceAll(source, "\r\n", "\n")
	src := []byte(source)
	masked := maskShell(src)
	starts := lineStartsOf(src)

	functions := []string{}
	hashes := make(map[string]string)
	lines := make(map[string]int)

	// recordHash combines on collision so a change in ANY variant of a redefined
	// function flips the hash (parity with the Go/Python parsers' recordHash).
	recordHash := func(key string, span []byte) {
		h := hashBytesHex(span)
		if existing, ok := hashes[key]; ok {
			h = hashBytesHex([]byte(existing + h))
		}
		hashes[key] = h
	}

	// handle records one matched definition: name, start line, and — when a body
	// span can be delimited on the masked text — the hash of its raw source span
	// (name through the matching close brace/paren).
	handle := func(nameStart, nameEnd, matchEnd int, kwForm bool) {
		name := string(src[nameStart:nameEnd])
		functions = append(functions, name)
		key := "function:" + name
		if _, ok := lines[key]; !ok { // anchor at the first definition
			lines[key] = lineForOffset(starts, nameStart)
		}
		if end, ok := shellBodyEnd(masked, matchEnd, kwForm); ok {
			recordHash(key, src[nameStart:end+1])
		}
	}

	// FindAllSubmatchIndex returns absolute byte offsets into masked (== offsets
	// into src; masking is length-preserving), in source order.
	for _, m := range reShellFuncParen.FindAllSubmatchIndex(masked, -1) {
		handle(m[2], m[3], m[1], false)
	}
	for _, m := range reShellFuncKw.FindAllSubmatchIndex(masked, -1) {
		handle(m[2], m[3], m[1], true)
	}

	sort.Strings(functions)
	if len(hashes) == 0 {
		hashes = nil
	}
	if len(lines) == 0 {
		lines = nil
	}
	return FileStructure{
		Imports:      []string{},
		Functions:    deduplicate(functions),
		Classes:      []string{},
		Exports:      []string{},
		SymbolHashes: hashes,
		SymbolLines:  lines,
	}, nil
}

// shellBodyEnd finds the end offset of a function body on the masked source,
// starting the search at `from` (just past the matched `name()` for the paren form,
// or just past `name` for the keyword form). It skips whitespace/newlines to the
// body opener — `{` (command group) or `(` (subshell) — then brace/paren-matches to
// the closing delimiter. For the keyword form it first skips an optional empty `()`.
// Because the input is masked (strings/subs/params/heredocs/comments blanked), the
// only braces/parens seen are real code-level delimiters, which balance. Returns
// (closeOffset, true) or (0, false) when no body opener is found (fail-open: the
// definition is still recorded with a line, just no hash).
func shellBodyEnd(masked []byte, from int, kwForm bool) (int, bool) {
	n := len(masked)
	i := from
	skipWS := func() {
		for i < n && (masked[i] == ' ' || masked[i] == '\t' || masked[i] == '\n') {
			i++
		}
	}
	skipWS()
	if kwForm && i < n && masked[i] == '(' {
		// Optional empty parens on the keyword form: `function foo() { … }`.
		j := i + 1
		for j < n && (masked[j] == ' ' || masked[j] == '\t' || masked[j] == '\n') {
			j++
		}
		if j < n && masked[j] == ')' {
			i = j + 1
			skipWS()
		}
	}
	if i >= n {
		return 0, false
	}
	var open, close byte
	switch masked[i] {
	case '{':
		open, close = '{', '}'
	case '(':
		open, close = '(', ')'
	default:
		return 0, false
	}
	depth := 0
	for ; i < n; i++ {
		switch masked[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// lineStartsOf returns the byte offset of the start of each line (offset 0, then
// each byte after a '\n'), for O(log n) offset→line lookup.
func lineStartsOf(src []byte) []int {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineForOffset returns the 1-based line number containing byte offset off.
func lineForOffset(starts []int, off int) int {
	lo, hi := 0, len(starts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if starts[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}

// isShellNameByte reports whether b is valid in a shell function name / heredoc
// delimiter (first=true for the leading byte, which may not be a digit or punct).
func isShellNameByte(b byte, first bool) bool {
	if b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
		return true
	}
	if first {
		return false
	}
	return (b >= '0' && b <= '9') || b == '.' || b == ':' || b == '-'
}

// maskShell returns a length-preserving copy of src with the CONTENT of shell
// constructs that must not affect brace/paren matching or function-def detection
// blanked to spaces (newlines preserved so line numbers and (?m)^ anchoring stay
// correct): comments, single/double-quoted strings, $(…)/`…` command substitutions,
// ${…} parameter expansions, and heredoc bodies. Real code-level `{ } ( )` and
// identifiers are kept.
//
// It is a single byte-scan with a small context stack so nested constructs close on
// the right delimiter — e.g. the inner `"` in `"a $(echo "b") c"` does not end the
// outer string, and a `}` inside `${x:-"}"}` does not close a function body. Frame
// kinds: s single-quote, d double-quote, b backtick, c $(…) command sub, p ${…}
// param exp, g `{` group/brace-expansion, r `(` subshell/parens. s/d/b/c/p are the
// "blanking" frames; g/r are structural (kept) but are blanked when nested inside a
// blanking frame.
func maskShell(src []byte) []byte {
	n := len(src)
	out := make([]byte, n)
	copy(out, src)

	mask1 := func(i int) {
		if src[i] != '\n' {
			out[i] = ' '
		}
	}

	var stack []byte
	// stackAt[k] is the byte offset frame k was pushed at. Only heredoc eligibility
	// reads it (see inArithNest); masking never does.
	var stackAt []int
	blanking := 0 // count of blanking frames (s/d/b/c/p) currently on the stack
	// quoting counts only the frames whose BYTES are literal. It differs from
	// blanking by exactly frameCmdSub, and that difference is the whole of #282:
	// `$(…)` content is masked, but it is still CODE, so a heredoc inside it is
	// legal shell and must be recognised. blanking answers "are these bytes
	// literal" (right for masking); quoting is half of "can a heredoc start here".
	//
	// frameBacktick stays counted here even though a heredoc inside `…` is also
	// legal: backtick content is masked wholesale without tracking inner structure
	// (see the frameBacktick arm below), so there is nothing to hand a recognised
	// heredoc to. Known limitation, not an oversight.
	quoting := 0
	push := func(k byte, at int) {
		stack = append(stack, k)
		stackAt = append(stackAt, at)
		switch k {
		case frameSingle, frameDouble, frameBacktick, frameCmdSub, frameParam:
			blanking++
		}
		switch k {
		case frameSingle, frameDouble, frameBacktick, frameParam:
			quoting++
		}
	}
	pop := func() {
		k := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		stackAt = stackAt[:len(stackAt)-1]
		switch k {
		case frameSingle, frameDouble, frameBacktick, frameCmdSub, frameParam:
			blanking--
		}
		switch k {
		case frameSingle, frameDouble, frameBacktick, frameParam:
			quoting--
		}
	}
	top := func() byte {
		if len(stack) == 0 {
			return 0
		}
		return stack[len(stack)-1]
	}

	// inArithNest reports whether an arithmetic paren pair is open somewhere in the
	// run of frameSubshell frames at the top of the stack:
	//
	//   `((`  → frameSubshell, frameSubshell at offsets n, n+1
	//   `$((` → frameCmdSub,   frameSubshell at offsets n, n+2  (the `$(` is 2 bytes)
	//
	// It walks DOWN that run rather than checking only the top pair. A parenthesised
	// subexpression inside arithmetic pushes its own frame — `x=$(( (1 << bit) ))`
	// leaves the stack as cmdSub@n, subshell@n+2, subshell@m — so the arithmetic
	// opener is no longer adjacent at the top, and a top-pair-only check re-opens
	// #281 on exactly the spelling the arithmetic rule exists to cover.
	//
	// A dedicated frameArith kind was rejected: it would have to be popped by `))`,
	// so misreading `((echo a) | cat)` as arithmetic desyncs the stack on the first
	// single `)` — and a desynced masker swallowing the rest of the file IS the #281
	// failure being fixed. This stays a read-only predicate over frames pushed
	// exactly as before, so a wrong answer can never desync anything.
	//
	// The offset-adjacency comparison is NOT pinned by any test, and this note is
	// here instead of a fixture invented to cover it. Mutating both arms to return
	// true unconditionally leaves the suite green, because arithShift only consults
	// this inside a blanking frame, where a wrong answer emits nothing either way.
	// It is kept because it is what makes the predicate mean "an arithmetic opener"
	// rather than "some nested parens" — but it is redundancy, not load-bearing, and
	// a fixture claiming to pin it would be the vacuous-coverage antipattern this
	// package's tests exist to avoid.
	inArithNest := func() bool {
		for k := len(stack) - 1; k >= 1; k-- {
			if stack[k] != frameSubshell {
				return false
			}
			switch stack[k-1] {
			case frameSubshell:
				if stackAt[k] == stackAt[k-1]+1 {
					return true
				}
			case frameCmdSub:
				return stackAt[k] == stackAt[k-1]+2
			}
		}
		return false
	}

	// arithShift reports whether the `<<` at pos is the left-shift OPERATOR rather
	// than a heredoc redirect (#281) — and it deliberately answers true ONLY where
	// being wrong is free.
	//
	// An earlier version also suppressed inside a bare `((…))`, confirmed by seeing
	// a `))` close on the operator's line. Review found that backwards twice over.
	// frameSubshell is STRUCTURAL: its contents are kept as code, so suppressing a
	// heredoc there does not merely miss it — the body is scanned and `fake() {` in
	// it becomes a definition that does not exist. And the confirmation could not
	// tell `((cat <<EOF) | (tr a-z A-Z))` (a subshell pipeline whose own closer is
	// `))`) from arithmetic, so it caused exactly that.
	//
	// So suppression is now limited to `blanking > 0` — inside `$((…))`, where the
	// frame masks its contents wholesale and a missed heredoc emits nothing. The
	// bare `((x << y))` case is handled downstream instead, by refusing to blank for
	// a heredoc whose terminator does not exist. That is a fact about the file
	// rather than a guess about the syntax, which is why it can carry the case a
	// heuristic kept getting wrong.
	arithShift := func() bool { return inArithNest() && blanking > 0 }

	// codeLevel reports whether these bytes are code rather than literal text — the
	// half of heredoc eligibility that `blanking` used to answer wrongly, by counting
	// frameCmdSub and so suppressing a real heredoc inside `$(…)` (#282).
	//
	// It walks the stack from the top and answers on the INNERMOST frame, not on a
	// count. A count cannot express `x="$(cat <<EOF …)"` — the far more common
	// spelling than a bare `$(…)` — where a double quote is open but the command
	// substitution inside it re-enters code, and a heredoc there is legal. Counting
	// any quoting frame anywhere on the stack left that half of #282 unclosed.
	//
	// This walk is NOT pinned by any test, and the note is here rather than a
	// fixture invented for it: reverting to the count leaves the suite green,
	// because the terminator lookahead independently prevents the damage the
	// suppression used to cause. It is kept because a heredoc inside `"$(…)"` is
	// legal shell and recognising it is simply correct — but it is correctness
	// without an observable consequence in the symbol output, and saying so beats
	// asserting coverage that is not there.
	codeLevel := func() bool {
		for k := len(stack) - 1; k >= 0; k-- {
			switch stack[k] {
			case frameSingle, frameDouble, frameBacktick, frameParam:
				return false
			case frameCmdSub, frameSubshell, frameGroup:
				return true
			}
		}
		return true
	}

	var heredocs []shellHeredoc
	atWordStart := true // for '#': a comment starts only at a command/word boundary

	i := 0
	for i < n {
		c := src[i]
		t := top()

		// Single quotes are fully literal: nothing but the closing ' is special.
		if t == frameSingle {
			mask1(i)
			if c == '\'' {
				pop()
			}
			i++
			continue
		}

		// Backslash escape (every context except single-quote, handled above). Blank
		// the escaped char so an escaped brace/quote/paren is never counted or read.
		if c == '\\' {
			if i+1 < n && src[i+1] == '\n' {
				mask1(i) // line continuation: blank the backslash, keep the newline
				i++
				atWordStart = false
				continue
			}
			mask1(i)
			if i+1 < n {
				mask1(i + 1)
			}
			i += 2
			atWordStart = false
			continue
		}

		// String-like contexts (double, backtick, param expansion): braces and parens
		// are literal text here, so only each context's OWN specials are honored.
		// Treating `{`/`(` as structural inside a string would corrupt the stack (an
		// unbalanced `{` in `"fake() {"` must not open a group). $(…)/${…}/backtick
		// inside double quotes still nest into code, so those are handled.
		if t == frameDouble { // double-quoted string
			switch {
			case c == '"':
				mask1(i)
				pop()
				i++
			case c == '`':
				mask1(i)
				push(frameBacktick, i)
				i++
			case c == '$' && i+1 < n && src[i+1] == '(':
				mask1(i)
				mask1(i + 1)
				push(frameCmdSub, i)
				i += 2
			case c == '$' && i+1 < n && src[i+1] == '{':
				mask1(i)
				mask1(i + 1)
				push(frameParam, i)
				i += 2
			default:
				mask1(i)
				i++
			}
			atWordStart = false
			continue
		}
		if t == frameBacktick { // `…` command substitution: only the closing backtick matters
			mask1(i)
			if c == '`' {
				pop()
			}
			i++
			atWordStart = false
			continue
		}
		if t == frameParam { // ${…} parameter expansion
			switch {
			case c == '}':
				mask1(i)
				pop()
				i++
			case c == '{':
				// A bare `{` inside ${…} (a literal or brace-expansion-shaped default,
				// e.g. `${x:-{a,b}}`) nests a brace level, so the FIRST inner `}` closes
				// it rather than popping the param early and leaking a stray code `}`.
				mask1(i)
				push(frameParam, i)
				i++
			case c == '$' && i+1 < n && src[i+1] == '{':
				mask1(i)
				mask1(i + 1)
				push(frameParam, i)
				i += 2
			case c == '$' && i+1 < n && src[i+1] == '(':
				mask1(i)
				mask1(i + 1)
				push(frameCmdSub, i)
				i += 2
			case c == '\'':
				mask1(i)
				push(frameSingle, i)
				i++
			case c == '"':
				mask1(i)
				push(frameDouble, i)
				i++
			case c == '`':
				mask1(i)
				push(frameBacktick, i)
				i++
			default:
				mask1(i)
				i++
			}
			atWordStart = false
			continue
		}

		// Code-like contexts: top level (0), $(…) command sub (c), subshell (r), and
		// command group (g). Full structural handling; blanked when inside a sub.
		wasBlank := blanking > 0

		switch {
		case c == '\'':
			if wasBlank {
				mask1(i)
			}
			push(frameSingle, i)
			i++
			atWordStart = false

		case c == '"':
			if wasBlank {
				mask1(i)
			}
			push(frameDouble, i)
			i++
			atWordStart = false

		case c == '`':
			if wasBlank {
				mask1(i)
			}
			push(frameBacktick, i)
			i++
			atWordStart = false

		case c == '$' && i+1 < n && src[i+1] == '(':
			if wasBlank {
				mask1(i)
			}
			mask1(i + 1) // blank the '(' delimiter; content follows blanked
			push(frameCmdSub, i)
			i += 2
			atWordStart = false
		case c == '$' && i+1 < n && src[i+1] == '{':
			if wasBlank {
				mask1(i)
			}
			mask1(i + 1)
			push(frameParam, i)
			i += 2
			atWordStart = false

		case c == ')' && (t == frameCmdSub || t == frameSubshell):
			if wasBlank {
				mask1(i)
			}
			pop()
			i++
			atWordStart = true
		case c == '}' && (t == frameGroup || t == frameParam):
			if wasBlank {
				mask1(i)
			}
			pop()
			i++
			atWordStart = true

		case c == '(':
			if wasBlank {
				mask1(i)
			}
			push(frameSubshell, i)
			i++
			atWordStart = true
		case c == '{':
			if wasBlank {
				mask1(i)
			}
			push(frameGroup, i)
			i++
			atWordStart = true

		case c == '#' && atWordStart && (t == 0 || t == frameGroup || t == frameSubshell || t == frameCmdSub):
			// Comment to end of line (not inside a string; # mid-word or after $ is
			// not a comment — atWordStart guards that).
			for i < n && src[i] != '\n' {
				mask1(i)
				i++
			}
			// leave the newline for the next iteration to handle heredocs

		case c == '<' && codeLevel() && !arithShift() && i+1 < n && src[i+1] == '<' &&
			(i+2 >= n || src[i+2] != '<') && (i == 0 || src[i-1] != '<'):
			// Heredoc opener `<<`/`<<-` (not `<<<` herestring — guarded on both sides
			// so neither the 1st nor the 2nd `<` of `<<<` is read as an opener) at
			// code level.
			j := i + 2
			dash := false
			if j < n && src[j] == '-' {
				dash = true
				j++
			}
			for j < n && (src[j] == ' ' || src[j] == '\t') {
				j++
			}
			quoted := j < n && (src[j] == '\'' || src[j] == '"')
			if quoted {
				j++
			}
			ws := j
			for j < n && isShellNameByte(src[j], j == ws) {
				j++
			}
			if j > ws {
				heredocs = append(heredocs, shellHeredoc{term: string(src[ws:j]), dash: dash})
			}
			if quoted && j < n && (src[j] == '\'' || src[j] == '"') {
				j++
			}
			if wasBlank {
				// Every other code-level branch masks inside a blanking frame; this one
				// did not, and #282 made it reachable there. Without this `x=$(cat <<'END'`
				// leaves `<<'END'` unmasked inside a frame whose contents are supposed to
				// be blanked, including an unbalanced quote byte.
				for k := i; k < j; k++ {
					mask1(k)
				}
			}
			i = j // resume after the delimiter spec
			atWordStart = false

		case c == '\n':
			atWordStart = true
			if codeLevel() && len(heredocs) > 0 {
				i++ // move past the opener line's newline; bodies start here
				for len(heredocs) > 0 && i < n {
					// Confirm the terminator EXISTS before blanking a single byte.
					//
					// This is the safety net that lets arithShift stop guessing. The
					// old loop blanked forward unconditionally, so any `<<` misread as
					// a redirect blanked every remaining line to EOF — which is what
					// made #281 swallow whole files, and what made widening
					// eligibility into `$(…)` dangerous (`x=$(let y=1<<n)` took `n` as
					// a delimiter and dropped the rest of the file).
					//
					// A heredoc whose terminator never appears is a misparse, not a
					// heredoc, so the honest response is to abandon it and mask
					// nothing. The cost is a genuinely truncated script — an
					// unterminated heredoc at EOF — whose body is then scanned as
					// code. That is malformed shell and bounded to one file's tail;
					// blanking to EOF on every misread is neither.
					end, ok := heredocTermEnd(src, i, heredocs[0])
					if !ok {
						heredocs = nil
						break
					}
					for k := i; k < end; k++ {
						if src[k] != '\n' { // keep newlines: masking is line-preserving
							out[k] = ' '
						}
					}
					i = end
					if i < n {
						i++ // consume the terminator line's newline (kept as '\n')
					}
					heredocs = heredocs[1:]
				}
			} else {
				i++
			}

		default:
			if wasBlank {
				mask1(i)
			}
			switch c {
			case ' ', '\t', ';', '|', '&':
				atWordStart = true
			default:
				atWordStart = false
			}
			i++
		}
	}
	return out
}

// shellHeredoc is one pending heredoc: the terminator line to look for, and
// whether `<<-` tab-stripping applies to it.
type shellHeredoc struct {
	term string
	dash bool
}

// heredocTermEnd scans forward from `from` (the first byte of the body) for the
// line that exactly matches h's terminator, and returns the offset of that line's
// end. ok is false when the terminator never appears before EOF — which the caller
// treats as "this was not a heredoc", rather than blanking the rest of the file.
func heredocTermEnd(src []byte, from int, h shellHeredoc) (int, bool) {
	n := len(src)
	for i := from; i < n; {
		lineEnd := i
		for lineEnd < n && src[lineEnd] != '\n' {
			lineEnd++
		}
		cmp := src[i:lineEnd]
		if h.dash {
			cmp = trimLeftTabs(cmp)
		}
		if string(cmp) == h.term {
			return lineEnd, true
		}
		i = lineEnd
		if i < n {
			i++
		}
	}
	return 0, false
}

// trimLeftTabs returns b with leading TAB bytes removed (the `<<-` heredoc rule
// strips leading tabs — only tabs, not spaces — from the terminator line).
func trimLeftTabs(b []byte) []byte {
	k := 0
	for k < len(b) && b[k] == '\t' {
		k++
	}
	return b[k:]
}
