package claims

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// Allows dotted segments so qualified/method refs like `Reader.fetch` are
	// captured, symmetric with the locate oracle's last-dotted-segment matching
	// (mcp/tools_oracle.go symbolMatches). Without this, qualified refs in
	// backticks were silently dropped from validate-claims and truth-trail.
	backtickRe = regexp.MustCompile("`([\\p{L}_][\\p{L}\\p{N}_]*(?:\\.[\\p{L}_][\\p{L}\\p{N}_]*)*)`")
	// group 1 is a comma-separated name list so a multi-name decl like
	// `var MaxSize, MinSize int` yields every name, not just the first; split on
	// commas downstream. func/type take a single name (the `(` or body ends the
	// list), so they are unaffected.
	declRe = regexp.MustCompile(`\b(?:func|type|var|const)\s+(\p{Lu}[\p{L}\p{N}_]*(?:\s*,\s*\p{Lu}[\p{L}\p{N}_]*)*)`)
	// Non-Go declaration keywords (Python/JS/TS), giving them decl-pattern parity
	// with Go. Unlike Go, these languages don't signal export by case, so the name
	// may start lowercase (camelCase `processData`); IsCodeSymbol still filters
	// pure snake_case/lowercase noise. `export`/`async`/`default` modifiers precede
	// the keyword, so the leading \b matches regardless. Single name only (class/
	// function/def take one; multi-name `let a, b` is rare and left to backticks).
	// `const` is JS/TS-only here (Go's is covered by declRe, which requires an
	// uppercase name); a lowercase `const MAX_SIZE` is still filtered by
	// IsCodeSymbol's mixed-case requirement, so this adds no noise.
	langDeclRe = regexp.MustCompile(`\b(?:class|function|let|const|def)\s+(\p{L}[\p{L}\p{N}_]*)`)
	// Method declarations `func (r *Reader) Fetch()` — declRe can't see these
	// because the receiver breaks the `func <name>` shape, so they extracted
	// nothing. group 1 is the receiver type (pointer and generic args stripped),
	// group 2 the method name; emitted as `Type.Name` to match how the parser
	// and locate oracle store methods (mcp/tools_oracle.go symbolMatches).
	methodRe = regexp.MustCompile(`\bfunc\s+\(\s*(?:[\p{L}_][\p{L}\p{N}_]*\s+)?\*?(\p{L}[\p{L}\p{N}_]*)(?:\[[^\]]*\])?\s*\)\s+(\p{Lu}[\p{L}\p{N}_]*)`)
	// Opens a gofmt-style parenthesized decl block (`var (`, `const (`, `type (`).
	// Member lines inside carry no keyword, so declRe can't see them.
	blockOpenRe = regexp.MustCompile(`^\s*(?:var|const|type)\s*\(\s*$`)
	// A member line inside such a block: leading exported name(s) followed by a
	// type, `=`, or end of line. The trailing `(?:\s|=|$)` excludes composite
	// literal field keys (`Host: "x"`), whose name is followed by a colon.
	blockMemberRe = regexp.MustCompile(`^\s*(\p{Lu}[\p{L}\p{N}_]*(?:\s*,\s*\p{Lu}[\p{L}\p{N}_]*)*)(?:\s|=|$)`)
)

// ExtractSymbolRefs returns a map of symbol → context snippet from text.
// Targets: backtick-quoted identifiers, Go func/type/var/const declarations
// (including every name of a multi-name decl and members of a parenthesized
// `var (`/`const (`/`type (` block), Go method declarations
// `func (r *Reader) Fetch()` (emitted qualified as Reader.Fetch), and
// Python/JS/TS class/function/let/const/def declarations.
// Conservative: only flags CamelCase names (mixed upper+lower) to avoid
// ALL_CAPS env vars, snake_case functions, and Python dunders.
//
// Lines are split directly rather than via bufio.Scanner: transcripts routinely
// contain single lines longer than the Scanner's 64KB default cap, and a Scanner
// would drop them silently — the exact failure mode this tool exists to catch.
func ExtractSymbolRefs(text string) map[string]string {
	refs := make(map[string]string)

	// add records sym (when it looks like a code symbol) with its line snippet,
	// first occurrence winning. Shared by every extraction pattern below.
	add := func(line, sym string) {
		if IsCodeSymbol(sym) {
			if _, seen := refs[sym]; !seen {
				refs[sym] = truncate(line, 80)
			}
		}
	}
	// addList splits a comma-separated name list and adds each name.
	addList := func(line, list string) {
		for _, name := range strings.Split(list, ",") {
			add(line, strings.TrimSpace(name))
		}
	}

	// inBlock tracks membership in a parenthesized var/const/type block; depth
	// counts parens so a member whose value spans lines (e.g. a multi-line
	// regexp.MustCompile(...)) does not close the block at its inner `)`.
	// braceDepth tracks `{`/`}` so a struct/interface nested inside a `type (...)`
	// group — whose FIELD lines also match blockMemberRe — is not mistaken for
	// block-level declarations (fields are captured only at brace depth 0).
	inBlock, depth, braceDepth := false, 0, 0
	for _, line := range strings.Split(text, "\n") {
		for _, m := range backtickRe.FindAllStringSubmatch(line, -1) {
			add(line, m[1])
		}
		for _, m := range methodRe.FindAllStringSubmatch(line, -1) {
			add(line, m[1]+"."+m[2]) // Receiver.Method, e.g. Reader.Fetch
		}
		for _, m := range declRe.FindAllStringSubmatch(line, -1) {
			addList(line, m[1])
		}
		for _, m := range langDeclRe.FindAllStringSubmatch(line, -1) {
			add(line, m[1])
		}

		if !inBlock {
			if blockOpenRe.MatchString(line) {
				inBlock, depth, braceDepth = true, 1, 0
			}
			// KNOWN limitation: the continue means a member declared on the SAME
			// line as the block opener (e.g. `var (MaxSize int`) is not captured —
			// only members on subsequent lines are. Go's gofmt always puts the first
			// member on its own line, so this is degenerate in practice.
			continue
		}
		// Only capture members at the block's top level. Inside a nested struct or
		// interface (brace depth > 0) the matching lines are FIELDS/method sigs, not
		// declarations. The name of the struct itself sits at brace depth 0 (before
		// its opening `{` on the same line), so it is still captured.
		if braceDepth == 0 {
			if m := blockMemberRe.FindStringSubmatch(line); m != nil {
				addList(line, m[1])
			}
		}
		braceDepth += strings.Count(line, "{") - strings.Count(line, "}")
		if braceDepth < 0 {
			braceDepth = 0
		}
		depth += strings.Count(line, "(") - strings.Count(line, ")")
		if depth <= 0 {
			inBlock = false
		}
	}
	return refs
}

// IsCodeSymbol returns true if the name looks like a CamelCase code identifier.
// Requires both upper and lower letters to avoid ALL_CAPS constants, snake_case
// helpers, and Python dunders (__init__). Unicode letters count: Go, JS, and
// Python all permit them in identifiers.
func IsCodeSymbol(name string) bool {
	if utf8.RuneCountInString(name) <= 2 {
		return false
	}
	hasUpper, hasLower := false, false
	for _, r := range name {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		}
	}
	return hasUpper && hasLower
}

// truncate cuts s to at most n bytes on a rune boundary, so snippets are
// always valid UTF-8 (they are stored and JSON-marshaled downstream).
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}
