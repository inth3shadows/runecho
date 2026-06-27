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
	// Method declarations `func (r *Reader) Fetch()` — declRe can't see these
	// because the receiver breaks the `func <name>` shape, so they extracted
	// nothing. group 1 is the receiver type (pointer and generic args stripped),
	// group 2 the method name; emitted as `Type.Name` to match how the parser
	// and locate oracle store methods (mcp/tools_oracle.go symbolMatches).
	methodRe = regexp.MustCompile(`\bfunc\s+\(\s*(?:[\p{L}_][\p{L}\p{N}_]*\s+)?\*?(\p{L}[\p{L}\p{N}_]*)(?:\[[^\]]*\])?\s*\)\s+(\p{Lu}[\p{L}\p{N}_]*)`)
)

// ExtractSymbolRefs returns a map of symbol → context snippet from text.
// Targets: backtick-quoted identifiers, func/type/var/const declarations
// (including every name of a multi-name decl), and method declarations
// `func (r *Reader) Fetch()` (emitted qualified as Reader.Fetch).
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

	for _, line := range strings.Split(text, "\n") {
		for _, m := range backtickRe.FindAllStringSubmatch(line, -1) {
			add(line, m[1])
		}
		for _, m := range methodRe.FindAllStringSubmatch(line, -1) {
			add(line, m[1]+"."+m[2]) // Receiver.Method, e.g. Reader.Fetch
		}
		for _, m := range declRe.FindAllStringSubmatch(line, -1) {
			for _, name := range strings.Split(m[1], ",") {
				add(line, strings.TrimSpace(name))
			}
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
