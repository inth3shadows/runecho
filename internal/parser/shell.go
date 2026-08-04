package parser

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// ShellParser implements structural parsing for POSIX / bash shell scripts
// (.sh, .bash) using the vendored pure-Go tree-sitter Bash grammar. It extracts
// top-level function definitions — the two forms `name() { … }` and
// `function name { … }` — with per-function start lines AND body hashes (so an
// edit to a function body surfaces as "modified" in diff/verify, parity with the
// AST parsers).
//
// A real grammar rather than a regex/masker pass, by the same test the Rust and
// Ruby parsers applied (#281, #282): `((x << y))` bit-shift vs. a `<<EOF`
// heredoc opener, and whether a heredoc opened inside `$(…)` is still a heredoc,
// are exactly the kind of question bash resolves by attempting to parse — which
// a length-preserving byte masker structurally cannot do. Three review rounds of
// patching the former masker's frame-stack heuristic (`maskShell`, deleted here)
// each fixed one such case and introduced another; see
// internal/parser/shell_treesitter_acceptance_test.go and the closed PR #285.
//
// Deliberately parser-only: the guard's hallucination check stays OUT of shell —
// guard.LangFor returns Unknown for .sh/.bash, so ExtractRefs never runs on shell —
// because a shell "call" is a bare command (grep/git/awk) indistinguishable from
// thousands of external binaries. This parser feeds the IR/oracle only. Shell also
// has no import model (`source` binds no named symbols) and no classes, so Imports,
// Classes, and Exports are always empty.
//
// Functions are extracted flat (no scope qualification): the prior masker-based
// implementation never distinguished a nested definition from a top-level one, and
// no test or consumer relies on qualified shell names, so the AST walk preserves
// that flat contract rather than introducing one.
type ShellParser struct{}

// NewShellParser creates a new shell parser.
func NewShellParser() *ShellParser { return &ShellParser{} }

// SupportsExtension returns true for .sh and .bash files.
func (p *ShellParser) SupportsExtension(ext string) bool {
	return ext == ".sh" || ext == ".bash"
}

// shellNameClass is the function-name charset. Conventionally an identifier, but
// bash permits `-`, `.`, and `:` (common in git hooks and namespaced helpers), so
// they are allowed. The `-` is last in the class so it is a literal, not a range.
const shellNameClass = `[A-Za-z_][A-Za-z0-9_.:-]*`

var (
	// `name()` / `name ()` — empty parens after a name is a function DEFINITION
	// only (a call never writes `()`), so this is unambiguous and low false-positive.
	// Used only by the regex-fallback path (see shellFallbackFunctions) when the
	// AST is unavailable entirely; the primary path reads the AST directly.
	reShellFuncParen = regexp.MustCompile(`(?m)^[ \t]*(` + shellNameClass + `)[ \t]*\(\)`)
	// `function name` / `function name()`.
	reShellFuncKw = regexp.MustCompile(`(?m)^[ \t]*function[ \t]+(` + shellNameClass + `)`)
)

// bashLang lazily loads and caches the tree-sitter Bash grammar. The grammar is
// loaded once; the resulting *Language is safe for concurrent reads. A fresh
// Parser is created per Parse call because ts.Parser is not concurrency-safe.
var (
	bashLangOnce sync.Once
	bashLang     *ts.Language
)

func bashLanguage() *ts.Language {
	bashLangOnce.Do(func() {
		// Recover so a grammar-decode panic doesn't propagate out of the first
		// Parse call; sync.Once marks itself done even on panic, so recovering
		// degrades to the nil-language path (regex-only fallback) rather than
		// leaving a panic to escape forever.
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "runecho: Bash grammar failed to load (%v); shell symbols degraded to regex fallback\n", r)
			}
		}()
		bashLang = grammars.BashLanguage()
	})
	return bashLang
}

// Parse extracts top-level shell function definitions with body hashes and start
// lines. Best-effort and never errors: an unparseable or over-nested file
// degrades to the regex fallback rather than returning nothing.
func (p *ShellParser) Parse(source string) (FileStructure, error) {
	// Normalize line endings so hashes and start lines are style-independent
	// (parity with the Go/Python parsers).
	source = strings.ReplaceAll(source, "\r\n", "\n")
	src := []byte(source)
	// Comments/quotes/backticks/param-expansions/heredoc bodies blanked to
	// spaces — shared by the AST walk (to recognize and skip a fabricated
	// function_definition whose start position lands inside one of these
	// regions) and by the total-fallback path below. See shellFallbackMask.
	masked := shellFallbackMask(src)

	var (
		astFunctions []string
		astHashes    map[string]string
		astLines     map[string]int
	)
	if lang := bashLanguage(); lang != nil {
		astFunctions, astHashes, astLines = shellSymbolsFromAST(source, lang, masked)
	}

	var functions []string
	var hashes map[string]string
	var lines map[string]int

	if len(astFunctions) > 0 {
		// Trust the AST for whatever it found. shellSymbolsFromAST already
		// excluded any function_definition node whose start position falls
		// inside a masked (comment/quote/heredoc-body) region — the signature
		// of the vendored grammar's confirmed stacked-heredoc gap, where error
		// recovery can fabricate a function_definition out of what is
		// actually heredoc body text (see shell_treesitter_acceptance_test.go
		// and PR #285). That per-node filter is more precise than a whole-tree
		// HasError() gate (which both over- and under-trusts: it flags a
		// merely-quirky-but-correct span like `${z:-{a,b}}` as untrustworthy,
		// while a fabricated sibling node can show HasError()==false itself),
		// so no further cross-checking against the regex fallback is needed
		// here — including no need to intersect against it, which would have
		// silently dropped a genuine function only because the fallback's
		// line-anchored regex can't see a definition after a `;` on the same
		// line as another.
		functions, hashes, lines = astFunctions, astHashes, astLines
	} else {
		// No AST result at all (grammar unavailable, parse panicked or
		// over-nested, or every function_definition node the AST did see was
		// heredoc-body noise) — fall back fully to the masked-regex scan,
		// including hashes via a simple brace/paren depth count on the same
		// masked buffer (safe because strings/comments/heredoc bodies are
		// already blanked, so no stray delimiter from string content can
		// desync the count).
		fNames, fHashes, fLines := shellFallbackFunctions(masked, src)
		functions = fNames
		hashes = fHashes
		lines = make(map[string]int, len(fLines))
		for name, line := range fLines {
			lines["function:"+name] = line
		}
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

// shellSymbolsFromAST walks the Bash AST and returns every function definition
// (both `name() {…}` and `function name {…}` forms), flat and unqualified. masked
// is the shared comment/quote/heredoc-body mask (see shellFallbackMask): a
// function_definition node whose name starts on a masked byte is skipped rather
// than recorded — that position can only be reached by content the grammar
// itself would never place a real definition on (heredoc body text mis-parsed as
// code being the confirmed case), so trusting the node there would trust a
// fabrication.
func shellSymbolsFromAST(source string, lang *ts.Language, masked []byte) (functions []string, hashes map[string]string, lines map[string]int) {
	// The pure-Go tree-sitter runtime can panic on adversarial or malformed
	// input; a panic here would otherwise propagate through parseFile→Generate
	// and crash the indexer/MCP server. Recover and degrade to no AST symbols
	// (the same fail-safe path as a nil grammar, which the caller then covers
	// with the regex fallback) so one bad file can't take down the process.
	// Named returns are reset so a panic mid-walk can't leak a partial,
	// inconsistent symbol set.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "runecho: shell parse panicked (%v); AST symbols for this file disabled\n", r)
			functions, hashes, lines = nil, nil, nil
		}
	}()
	src := []byte(source)
	// Reject pathologically-nested input before the super-linear tree-sitter
	// parse can hang the process; degrade to the regex fallback (see maxParseNestDepth).
	if exceedsNestDepth(src) {
		fmt.Fprintf(os.Stderr, "runecho: shell source exceeds max nesting depth (%d); AST symbols for this file disabled\n", maxParseNestDepth)
		return nil, nil, nil
	}
	tree, err := ts.NewParser(lang).Parse(src)
	if err != nil || tree == nil || tree.RootNode() == nil {
		return nil, nil, nil
	}
	// Only consult the mask when the parse actually errored. shellFallbackMask
	// does not — and, to avoid the `((` ambiguity that sank three review rounds
	// on maskShell, deliberately cannot — track paren nesting, so its heredoc
	// detector cannot tell `((x << y))` arithmetic from a real heredoc opener
	// either; unconditionally trusting it here would misidentify legitimate
	// arithmetic as a heredoc body and wrongly drop every real function after
	// it (this is exactly #281, reappearing one layer up). Gating on HasError()
	// keeps the mask out of the loop entirely on a clean parse, where the AST
	// is already known-correct and there is nothing to distrust it against.
	if !tree.RootNode().HasError() {
		masked = nil
	}

	hashes = make(map[string]string)
	lines = make(map[string]int)

	// recordHash combines on collision so a change in ANY variant of a redefined
	// function flips the hash (parity with the Go/Python/JS parsers' recordHash).
	recordHash := func(key string, span []byte) {
		h := hashBytesHex(span)
		if existing, ok := hashes[key]; ok {
			h = hashBytesHex([]byte(existing + h))
		}
		hashes[key] = h
	}
	// recordLine anchors a symbol at its FIRST definition; later same-name
	// redefinitions don't move the anchor.
	recordLine := func(key string, line int) {
		if _, ok := lines[key]; !ok {
			lines[key] = line
		}
	}

	var walk func(n *ts.Node, depth int)
	walk = func(n *ts.Node, depth int) {
		// Bound recursion so a deeply-nested AST can't overflow the goroutine
		// stack — a runtime throw the recover() above cannot catch.
		if depth > maxParseNestDepth {
			return
		}
		for i := 0; i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c.Type(lang) == "function_definition" {
				start := int(c.StartByte())
				if start < len(masked) && masked[start] == ' ' {
					// This node's own start position falls inside a masked
					// region (heredoc body, comment, string, ...) — a real
					// function_definition can never legitimately start there,
					// so this is the fabrication signature and the node is
					// dropped rather than recorded.
				} else if name := shellFieldText(c, "name", lang, src); name != "" {
					functions = append(functions, name)
					key := "function:" + name
					recordHash(key, src[c.StartByte():c.EndByte()])
					recordLine(key, int(c.StartPoint().Row)+1)
				}
			}
			walk(c, depth+1)
		}
	}
	walk(tree.RootNode(), 0)

	return functions, hashes, lines
}

// shellFieldText returns the text of n's named field, or "" if absent.
func shellFieldText(n *ts.Node, field string, lang *ts.Language, src []byte) string {
	if f := n.ChildByFieldName(field, lang); f != nil {
		return f.Text(src)
	}
	return ""
}

// shellFallbackFunctions matches reShellFuncParen/reShellFuncKw against masked
// (comment/quote/heredoc-body-blanked) source — the degraded path used only when
// the AST is entirely unavailable. Hashes are computed via shellFallbackBodyEnd
// on the same masked buffer; lines is keyed by plain function name (the caller
// adds the "function:" prefix), anchored at each name's first match.
func shellFallbackFunctions(masked, src []byte) (names []string, hashes map[string]string, lines map[string]int) {
	starts := lineStartsOf(src)
	hashes = make(map[string]string)
	lines = make(map[string]int)
	record := func(nameStart, nameEnd, matchEnd int, kwForm bool) {
		name := string(src[nameStart:nameEnd])
		names = append(names, name)
		if _, ok := lines[name]; !ok {
			lines[name] = lineForOffset(starts, nameStart)
		}
		if end, ok := shellFallbackBodyEnd(masked, matchEnd, kwForm); ok {
			h := hashBytesHex(src[nameStart : end+1])
			if existing, ok := hashes[name]; ok {
				h = hashBytesHex([]byte(existing + h))
			}
			hashes[name] = h
		}
	}
	for _, m := range reShellFuncParen.FindAllSubmatchIndex(masked, -1) {
		record(m[2], m[3], m[1], false)
	}
	for _, m := range reShellFuncKw.FindAllSubmatchIndex(masked, -1) {
		record(m[2], m[3], m[1], true)
	}
	return names, hashes, lines
}

// shellFallbackBodyEnd finds the end offset of a function body on the masked
// source, starting the search at `from` (just past the matched `name()` for the
// paren form, or just past `name` for the keyword form). It skips whitespace to
// the body opener — `{` (command group) or `(` (subshell) — then brace/paren-
// matches to the closing delimiter via simple depth counting, which is safe here
// because masked has already blanked every comment/quote/backtick/param-expansion
// and heredoc body, so any `{}()`  remaining is genuine code-level structure (no
// frame-kind ambiguity to resolve — see shellFallbackMask). For the keyword form
// it first skips an optional empty `()`. Returns (closeOffset, true) or (0,
// false) when no body opener is found (fail-open: the definition is still
// recorded with a line, just no hash).
func shellFallbackBodyEnd(masked []byte, from int, kwForm bool) (int, bool) {
	n := len(masked)
	i := from
	skipWS := func() {
		for i < n && (masked[i] == ' ' || masked[i] == '\t' || masked[i] == '\n') {
			i++
		}
	}
	skipWS()
	if kwForm && i < n && masked[i] == '(' {
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

// shellFallbackMask returns a length-preserving copy of src with comments,
// single/double-quoted string content, backtick content, ${…} parameter
// expansion content, and heredoc bodies blanked to spaces (newlines preserved).
// Shared by shellSymbolsFromAST (to recognize a fabricated function_definition —
// see Parse) and shellFallbackFunctions (the total-fallback path).
//
// Deliberately narrower than the deleted maskShell: it tracks ONLY these
// unambiguous, single-character-terminated contexts (plus heredoc openers gated
// on not currently being inside one of them). It does NOT track `$(…)` command
// substitution or bare `(`/`{` as structural frames — that `((` code-level
// tracking, and the "is this `<<` a heredoc opener" question it fed, is exactly
// what caused three failed review rounds on maskShell (see PR #285). Omitting it
// here means a `$(…)` or bare `(…)`/`{…}` is walked as plain, unmasked bytes:
// def-shaped text on its OWN line inside one is not protected the way it would
// be inside a string, but that combination is not part of any known regression
// (the demonstrated one — a def-shaped line inside a multi-line double-quoted
// string — needs only quote tracking, which this does have) and adding paren
// nesting back in would reintroduce the actual ambiguity, not remove a gap.
func shellFallbackMask(src []byte) []byte {
	n := len(src)
	out := make([]byte, n)
	copy(out, src)

	mask1 := func(i int) {
		if src[i] != '\n' {
			out[i] = ' '
		}
	}

	const (
		frameSingle byte = 's'
		frameDouble byte = 'd'
		frameBack   byte = 'b'
		frameParam  byte = 'p'
	)
	var stack []byte
	push := func(k byte) { stack = append(stack, k) }
	pop := func() { stack = stack[:len(stack)-1] }
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
	atWordStart := true

	i := 0
	for i < n {
		c := src[i]
		t := top()

		if t == frameSingle {
			mask1(i)
			if c == '\'' {
				pop()
			}
			i++
			continue
		}

		if c == '\\' && t != frameSingle {
			if i+1 < n && src[i+1] == '\n' {
				mask1(i)
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

		if t == frameDouble {
			switch {
			case c == '"':
				mask1(i)
				pop()
				i++
			case c == '`':
				mask1(i)
				push(frameBack)
				i++
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
		if t == frameBack {
			mask1(i)
			if c == '`' {
				pop()
			}
			i++
			atWordStart = false
			continue
		}
		if t == frameParam {
			switch {
			case c == '}':
				mask1(i)
				pop()
				i++
			case c == '{':
				// A bare `{` inside ${…} (e.g. `${x:-{a,b}}`) nests a brace
				// level, so the FIRST inner `}` closes it rather than popping
				// the param early.
				mask1(i)
				push(frameParam)
				i++
			case c == '$' && i+1 < n && src[i+1] == '{':
				mask1(i)
				mask1(i + 1)
				push(frameParam)
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
				push(frameBack)
				i++
			default:
				mask1(i)
				i++
			}
			atWordStart = false
			continue
		}

		// Top level: no `(`/`{` structural tracking (see doc comment) — only
		// quote/comment/param-expansion openers and heredoc openers, gated on
		// not currently inside one of the former.
		switch {
		case c == '\'':
			push(frameSingle)
			i++
			atWordStart = false
		case c == '"':
			push(frameDouble)
			i++
			atWordStart = false
		case c == '`':
			push(frameBack)
			i++
			atWordStart = false
		case c == '$' && i+1 < n && src[i+1] == '{':
			mask1(i)
			mask1(i + 1)
			push(frameParam)
			i += 2
			atWordStart = false

		case c == '#' && atWordStart:
			for i < n && src[i] != '\n' {
				mask1(i)
				i++
			}

		case c == '<' && i+1 < n && src[i+1] == '<' &&
			(i+2 >= n || src[i+2] != '<') && (i == 0 || src[i-1] != '<'):
			// Heredoc opener `<<`/`<<-` (not `<<<` herestring).
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
			i = j
			atWordStart = false

		case c == '\n':
			atWordStart = true
			if len(heredocs) > 0 {
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

// trimLeftTabs returns b with leading TAB bytes removed (the `<<-` heredoc rule
// strips leading tabs — only tabs, not spaces — from the terminator line).
func trimLeftTabs(b []byte) []byte {
	k := 0
	for k < len(b) && b[k] == '\t' {
		k++
	}
	return b[k:]
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
