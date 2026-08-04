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
	// AST parse errors; the primary path reads the AST directly.
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

	var (
		astFunctions []string
		astHashes    map[string]string
		astLines     map[string]int
		hasError     = true
	)
	if lang := bashLanguage(); lang != nil {
		astFunctions, astHashes, astLines, hasError = shellSymbolsFromAST(source, lang)
	}

	var functions []string
	var hashes map[string]string
	var lines map[string]int

	switch {
	case !hasError:
		// Clean parse: the AST is authoritative, no fallback consultation. The
		// fallback's line-anchored regex is quote/heredoc-blind, so consulting
		// it here would risk pulling in a def-shaped line the AST correctly
		// recognized as non-code — e.g. a line that only LOOKS like a top-level
		// definition because it sits inside a multi-line double-quoted string
		// (see MaskingKeepsBodySpanHonest).
		functions, hashes, lines = astFunctions, astHashes, astLines

	case len(astFunctions) == 0:
		// No AST result to cross-check against (grammar unavailable, parse
		// panicked/over-nested/failed outright, or a genuinely function-free
		// file that also errored) — use the heredoc-aware fallback directly.
		// No body hashes on this path: nothing to derive them from.
		fNames, fLines := shellFallbackFunctions(src)
		functions = fNames
		lines = make(map[string]int, len(fLines))
		for name, line := range fLines {
			lines["function:"+name] = line
		}

	default:
		// Partial/corrupted parse with at least one AST-found function: the
		// vendored tree-sitter-bash grammar has a confirmed gap on stacked
		// heredocs on one line (`<<A <<B`) where error recovery fabricates a
		// real-looking function_definition node out of what is actually
		// heredoc body text — verified by direct AST inspection. Trust only
		// AST-found names the independent heredoc-aware fallback ALSO names
		// (intersection, not union): a name the fallback does not corroborate
		// is exactly that fabrication signature, and a name only the fallback
		// finds is deliberately NOT added back in — on this same partial-parse
		// path the fallback's quote-blind regex is exactly what could wrongly
		// match a def-shaped line inside a string the AST correctly excluded.
		// This does forgo the "union" coverage-never-regresses posture the
		// JS/TS parser uses, because here (unlike there) the failure mode is
		// fabrication, not just omission — see shell_treesitter_acceptance_test.go
		// and PR #285 for the three rounds of masker patches this replaces.
		fNames, _ := shellFallbackFunctions(src)
		fallbackSet := make(map[string]bool, len(fNames))
		for _, n := range fNames {
			fallbackSet[n] = true
		}
		hashes = make(map[string]string)
		lines = make(map[string]int)
		for _, name := range astFunctions {
			if !fallbackSet[name] {
				continue
			}
			functions = append(functions, name)
			key := "function:" + name
			if h, ok := astHashes[key]; ok {
				hashes[key] = h
			}
			if l, ok := astLines[key]; ok {
				lines[key] = l
			}
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
// (both `name() {…}` and `function name {…}` forms), flat and unqualified. hasError
// reports whether the parse panicked, over-nested, failed outright, or partially
// recovered (Node.HasError) — any of which tells the caller to supplement with
// the regex fallback.
func shellSymbolsFromAST(source string, lang *ts.Language) (functions []string, hashes map[string]string, lines map[string]int, hasError bool) {
	// The pure-Go tree-sitter runtime can panic on adversarial or malformed
	// input; a panic here would otherwise propagate through parseFile→Generate
	// and crash the indexer/MCP server. Recover and degrade to no AST symbols
	// (the same fail-safe path as a nil grammar) so one bad file can't take down
	// the process. Named returns are reset so a panic mid-walk can't leak a
	// partial, inconsistent symbol set. hasError=true tells the caller to
	// supplement with the regex fallback, matching the JS/TS parser's posture.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "runecho: shell parse panicked (%v); AST symbols for this file disabled\n", r)
			functions, hashes, lines, hasError = nil, nil, nil, true
		}
	}()
	src := []byte(source)
	// Reject pathologically-nested input before the super-linear tree-sitter
	// parse can hang the process; degrade to the regex fallback (see maxParseNestDepth).
	if exceedsNestDepth(src) {
		fmt.Fprintf(os.Stderr, "runecho: shell source exceeds max nesting depth (%d); AST symbols for this file disabled\n", maxParseNestDepth)
		return nil, nil, nil, true
	}
	tree, err := ts.NewParser(lang).Parse(src)
	if err != nil || tree == nil || tree.RootNode() == nil {
		return nil, nil, nil, true
	}
	hasError = tree.RootNode().HasError()

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
				if name := shellFieldText(c, "name", lang, src); name != "" {
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

	return functions, hashes, lines, hasError
}

// shellFieldText returns the text of n's named field, or "" if absent.
func shellFieldText(n *ts.Node, field string, lang *ts.Language, src []byte) string {
	if f := n.ChildByFieldName(field, lang); f != nil {
		return f.Text(src)
	}
	return ""
}

// shellFallbackFunctions matches reShellFuncParen/reShellFuncKw against
// heredoc-body-masked source — the degraded path used only when the AST pass
// could not cleanly parse the file. lines is keyed by plain function name (the
// caller adds the "function:" prefix), anchored at each name's first match.
func shellFallbackFunctions(src []byte) (names []string, lines map[string]int) {
	masked := shellFallbackMaskHeredocs(src)
	starts := lineStartsOf(src)
	lines = make(map[string]int)
	record := func(nameStart, nameEnd int) {
		name := string(src[nameStart:nameEnd])
		names = append(names, name)
		if _, ok := lines[name]; !ok {
			lines[name] = lineForOffset(starts, nameStart)
		}
	}
	for _, m := range reShellFuncParen.FindAllSubmatchIndex(masked, -1) {
		record(m[2], m[3])
	}
	for _, m := range reShellFuncKw.FindAllSubmatchIndex(masked, -1) {
		record(m[2], m[3])
	}
	return names, lines
}

// shellFallbackMaskHeredocs returns a length-preserving copy of src with heredoc
// body/terminator lines blanked to spaces, used only by shellFallbackFunctions so
// a def-shaped line inside a heredoc body isn't mistaken for a real definition on
// the degraded fallback path. Deliberately narrow: it tracks ONLY heredoc
// openers and their terminator lines — no quote/paren/subshell frame stack — so
// it cannot reintroduce the `((` ambiguity that caused three failed review
// rounds on the deleted maskShell (see PR #285). A `<<` matched inside a string
// is an accepted trade-off on this rarely-triggered path (see Parse's hasError
// branch); a heredoc's own delimiter handling (quoted delimiter, `<<-`
// tab-stripping, exact terminator match, stacked heredocs) is not ambiguous the
// way `((` is, so replicating just that part carries none of that risk.
func shellFallbackMaskHeredocs(src []byte) []byte {
	n := len(src)
	out := make([]byte, n)
	copy(out, src)

	type hd struct {
		term string
		dash bool
	}
	var heredocs []hd

	i := 0
	for i < n {
		c := src[i]
		switch {
		case c == '<' && i+1 < n && src[i+1] == '<' &&
			(i+2 >= n || src[i+2] != '<') && (i == 0 || src[i-1] != '<'):
			// Heredoc opener `<<`/`<<-` (not `<<<` herestring — guarded on both
			// sides so neither the 1st nor the 2nd `<` of `<<<` is read as an
			// opener).
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
		case c == '\n':
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
		default:
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
