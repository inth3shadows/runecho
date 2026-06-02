package claims

import (
	"bufio"
	"regexp"
	"strings"
)

var (
	backtickRe = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*)`")
	declRe     = regexp.MustCompile(`\b(?:func|type|var|const)\s+([A-Z][A-Za-z0-9_]*)`)
)

// ExtractSymbolRefs returns a map of symbol → context snippet from text.
// Targets: backtick-quoted identifiers, and func/type/var/const declaration patterns.
// Conservative: only flags CamelCase names (mixed upper+lower) to avoid
// ALL_CAPS env vars, snake_case functions, and Python dunders.
func ExtractSymbolRefs(text string) map[string]string {
	refs := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(text))

	for scanner.Scan() {
		line := scanner.Text()
		for _, m := range backtickRe.FindAllStringSubmatch(line, -1) {
			sym := m[1]
			if IsCodeSymbol(sym) {
				if _, seen := refs[sym]; !seen {
					refs[sym] = truncate(line, 80)
				}
			}
		}
		for _, m := range declRe.FindAllStringSubmatch(line, -1) {
			sym := m[1]
			if IsCodeSymbol(sym) {
				if _, seen := refs[sym]; !seen {
					refs[sym] = truncate(line, 80)
				}
			}
		}
	}
	return refs
}

// IsCodeSymbol returns true if the name looks like a CamelCase code identifier.
// Requires both upper and lower letters to avoid ALL_CAPS constants, snake_case
// helpers, and Python dunders (__init__).
func IsCodeSymbol(name string) bool {
	if len(name) <= 2 {
		return false
	}
	hasUpper, hasLower := false, false
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		}
	}
	return hasUpper && hasLower
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
