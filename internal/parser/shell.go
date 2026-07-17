package parser

import (
	"regexp"
	"sort"
	"strings"
)

// ShellParser implements structural parsing for POSIX / bash shell scripts
// (.sh, .bash). Shell has a deliberately THIN symbol model: this parser extracts
// top-level function DEFINITIONS only — the two forms `name() { … }` and
// `function name { … }`. What it intentionally does NOT do, and why:
//
//   - Imports: `source file` / `. file` pulls in a whole file, binding no *named*
//     symbols, so there is nothing to record as an import name. Imports stay [].
//   - Calls / refs: a shell "call" is a bare command (`grep`, `git`, `awk`),
//     indistinguishable from thousands of external binaries. The guard's
//     hallucination check therefore deliberately stays OUT of shell —
//     guard.LangFor returns Unknown for .sh/.bash, so ExtractRefs never runs on
//     shell and this parser feeds the IR/oracle (map/locate/structure/diff) only.
//   - Body hashes: robustly delimiting a shell function body (heredocs, ${…},
//     $(…), {a,b} brace expansion, quoted braces) is error-prone, and a wrong
//     hash yields a wrong diff. SymbolHashes is left nil, so modified-symbol
//     diffing degrades to add/remove for shell functions — the documented
//     regex-parser degradation in FileStructure. Body hashing is a future
//     enhancement, not part of this MVP.
//
// Known limitations (documented, not bugs; tracked in issue #155): a heredoc
// opened with a quoted or variable delimiter (`<<"$x"`) is not detected; a
// `<<WORD` appearing inside a string literal could be misread as a heredoc opener
// (masking shell strings properly is the future-enhancement counterpart to body
// hashing); and only the FIRST heredoc on a line is tracked, so a stacked
// `cmd <<A <<B` skips A's body but not B's. None occur in ordinary end-of-line
// heredocs.
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
	reShellFuncParen = regexp.MustCompile(`^\s*(` + shellNameClass + `)\s*\(\)`)
	// `function name` / `function name()`.
	reShellFuncKw = regexp.MustCompile(`^\s*function\s+(` + shellNameClass + `)\b`)
	// Heredoc opener: `<<WORD`, `<<-WORD`, `<< 'WORD'`, `<< "WORD"`. The leading
	// `(^|[^<])` ensures the operator is exactly `<<` and not `<<<` (a herestring,
	// which has no body). Capture groups: 1 = the leading `^`/non-`<` char, 2 = the
	// `-` (tab-strip) flag, 3 = the terminator word. The body is skipped so
	// function-def-looking lines inside it are never extracted.
	reHeredocOpen = regexp.MustCompile(`(^|[^<])<<(-?)\s*['"]?(` + shellNameClass + `)`)
)

// Parse extracts top-level shell function definitions. Best-effort and never
// errors: shell has no parse tree here, only a line scan, so malformed input just
// yields whatever function defs were matched (honoring the Parser interface's
// partial-structure contract).
func (p *ShellParser) Parse(source string) (FileStructure, error) {
	// Normalize CRLF→LF so line numbers are line-ending-independent (parity with
	// the Go/Python parsers).
	source = strings.ReplaceAll(source, "\r\n", "\n")

	functions := []string{}
	lines := make(map[string]int)
	record := func(name string, lineNo int) {
		functions = append(functions, name)
		key := "function:" + name
		if _, ok := lines[key]; !ok { // anchor at the FIRST definition
			lines[key] = lineNo
		}
	}

	heredocTerm := ""    // active heredoc terminator word ("" = not inside a heredoc)
	heredocDash := false // <<- form: leading tabs are stripped before matching the terminator

	for i, line := range strings.Split(source, "\n") {
		lineNo := i + 1

		// Inside a heredoc body: skip every line until the terminator so
		// function-def-looking content in the body is never extracted.
		if heredocTerm != "" {
			term := line
			if heredocDash {
				term = strings.TrimLeft(term, "\t") // <<- strips leading TABS from the terminator
			}
			// Bash recognizes the delimiter only on a line that is EXACTLY the word
			// (no leading/trailing blanks; <<- strips leading tabs only). Matching
			// exactly — not a trimmed compare — avoids ending the heredoc early on a
			// body line like `EOF ` that bash treats as body, which would then leak
			// the rest of the body as code.
			if term == heredocTerm {
				heredocTerm = ""
			}
			continue
		}

		// Whole-line comment.
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}

		if m := reShellFuncParen.FindStringSubmatch(line); m != nil {
			record(m[1], lineNo)
		} else if m := reShellFuncKw.FindStringSubmatch(line); m != nil {
			record(m[1], lineNo)
		}

		// Check whether this line OPENS a heredoc last, so a `foo() { cat <<EOF`
		// line is still recorded as a function before its body is skipped. The
		// Contains guard skips the regex on the common no-heredoc line. Only the
		// FIRST heredoc on a line is tracked (stacked `<<A <<B` is a documented limit).
		if strings.Contains(line, "<<") {
			if hm := reHeredocOpen.FindStringSubmatch(line); hm != nil {
				heredocDash = hm[2] == "-"
				heredocTerm = hm[3]
			}
		}
	}

	sort.Strings(functions)
	if len(lines) == 0 {
		lines = nil
	}
	return FileStructure{
		Imports:      []string{},
		Functions:    deduplicate(functions),
		Classes:      []string{},
		Exports:      []string{},
		SymbolHashes: nil,
		SymbolLines:  lines,
	}, nil
}
