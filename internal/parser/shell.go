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
	blanking := 0 // count of blanking frames (s/d/b/c/p) currently on the stack
	push := func(k byte) {
		stack = append(stack, k)
		switch k {
		case frameSingle, frameDouble, frameBacktick, frameCmdSub, frameParam:
			blanking++
		}
	}
	pop := func() {
		k := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch k {
		case frameSingle, frameDouble, frameBacktick, frameCmdSub, frameParam:
			blanking--
		}
	}
	top := func() byte {
		if len(stack) == 0 {
			return 0
		}
		return stack[len(stack)-1]
	}

	type hd struct {
		term string
		dash bool
	}
	var heredocs []hd
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
				push(frameBacktick)
				i++
			case c == '$' && i+1 < n && src[i+1] == '(':
				mask1(i)
				mask1(i + 1)
				push(frameCmdSub)
				i += 2
			case c == '$' && i+1 < n && src[i+1] == '{':
				mask1(i)
				mask1(i + 1)
				push(frameParam)
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
				push(frameParam)
				i++
			case c == '$' && i+1 < n && src[i+1] == '{':
				mask1(i)
				mask1(i + 1)
				push(frameParam)
				i += 2
			case c == '$' && i+1 < n && src[i+1] == '(':
				mask1(i)
				mask1(i + 1)
				push(frameCmdSub)
				i += 2
			case c == '\'':
				mask1(i)
				push(frameSingle)
				i++
			case c == '"':
				mask1(i)
				push(frameDouble)
				i++
			case c == '`':
				mask1(i)
				push(frameBacktick)
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
			push(frameSingle)
			i++
			atWordStart = false

		case c == '"':
			if wasBlank {
				mask1(i)
			}
			push(frameDouble)
			i++
			atWordStart = false

		case c == '`':
			if wasBlank {
				mask1(i)
			}
			push(frameBacktick)
			i++
			atWordStart = false

		case c == '$' && i+1 < n && src[i+1] == '(':
			if wasBlank {
				mask1(i)
			}
			mask1(i + 1) // blank the '(' delimiter; content follows blanked
			push(frameCmdSub)
			i += 2
			atWordStart = false
		case c == '$' && i+1 < n && src[i+1] == '{':
			if wasBlank {
				mask1(i)
			}
			mask1(i + 1)
			push(frameParam)
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
			push(frameSubshell)
			i++
			atWordStart = true
		case c == '{':
			if wasBlank {
				mask1(i)
			}
			push(frameGroup)
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

		case c == '<' && blanking == 0 && i+1 < n && src[i+1] == '<' &&
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
				heredocs = append(heredocs, hd{term: string(src[ws:j]), dash: dash})
			}
			if quoted && j < n && (src[j] == '\'' || src[j] == '"') {
				j++
			}
			i = j // keep the `<<…delim` chars (code); resume after the delimiter spec
			atWordStart = false

		case c == '\n':
			atWordStart = true
			if blanking == 0 && len(heredocs) > 0 {
				i++ // move past the opener line's newline; bodies start here
				for len(heredocs) > 0 && i < n {
					lineEnd := i
					for lineEnd < n && src[lineEnd] != '\n' {
						lineEnd++
					}
					cmp := src[i:lineEnd]
					if heredocs[0].dash {
						cmp = trimLeftTabs(cmp)
					}
					for k := i; k < lineEnd; k++ {
						out[k] = ' ' // blank the whole heredoc body/terminator line
					}
					matched := string(cmp) == heredocs[0].term
					i = lineEnd
					if i < n {
						i++ // consume the line's newline (kept as '\n')
					}
					if matched {
						heredocs = heredocs[1:]
					}
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

// trimLeftTabs returns b with leading TAB bytes removed (the `<<-` heredoc rule
// strips leading tabs — only tabs, not spaces — from the terminator line).
func trimLeftTabs(b []byte) []byte {
	k := 0
	for k < len(b) && b[k] == '\t' {
		k++
	}
	return b[k:]
}
