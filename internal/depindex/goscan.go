package depindex

import (
	"strings"
)

// Fast extraction of a Go package's exported top-level names.
//
// The obvious implementation — go/parser — is correct and far too slow here: it
// parses every function BODY, and a single stdlib package costs 20–80ms
// (net/http measured at 79ms), against a guard budget of ~12ms for the whole
// edit. Nothing in an export set needs a body.
//
// So this scans lines instead, relying on the one thing that is true of
// essentially all published Go: gofmt puts every top-level declaration at column
// zero and indents everything else. A declaration at column zero is a top-level
// declaration; an indented line never is. That makes a linear scan sufficient,
// with no AST and no bodies.
//
// The risk of a scanner over an AST runs one way and it is the dangerous way: a
// MISSED export becomes a false positive, because the guard would then flag a
// real symbol as absent. So this is not trusted on argument — it is
// differentially tested against go/parser over every package in the local module
// cache and GOROOT (TestGoScannerMatchesAST), and any divergence fails.
// Non-gofmt'd source is the theoretical hole; that test is how we know the size
// of it in practice.

// GoPackageExports returns the exported top-level names declared across the
// given Go source files (the contents of one package directory), and reports
// whether the scan is trustworthy.
//
// ok=false means some file could not be scanned confidently — the caller must
// degrade to Partial rather than report a set that may be missing names.
func GoPackageExports(sources []string) (map[string]struct{}, bool) {
	out := map[string]struct{}{}
	for _, src := range sources {
		if !scanGoFileExports(src, out) {
			return nil, false
		}
	}
	return out, true
}

// scanGoFileExports adds one file's exported top-level names to out.
//
// Returns false when the file ends inside an unterminated raw string or block
// comment, which means the literal-masking state machine lost track and later
// "column zero" lines may have been literal text rather than code.
func scanGoFileExports(src string, out map[string]struct{}) bool {
	src = strings.ReplaceAll(src, "\r\n", "\n")

	// blockKind is non-empty while inside a parenthesized declaration group
	// (`var (`, `const (`, `type (`), whose entries are indented and so would
	// otherwise be skipped by the column-zero rule.
	blockKind := ""
	// groupDepth is the net bracket nesting INSIDE a declaration group. Only
	// lines at depth 0 declare a name: a `type ( X struct { … } )` group has its
	// struct FIELDS on following lines, and without depth tracking every one of
	// them was read as a type declaration. That was the sole source of divergence
	// from go/parser across 1500 real packages (go/ast, x/sys/unix, debug/macho).
	groupDepth := 0
	inRaw, inComment := false, false

	for _, line := range strings.Split(src, "\n") {
		code, raw, comment := maskGoLine(line, inRaw, inComment)
		startedClean := !inRaw && !inComment
		inRaw, inComment = raw, comment

		if blockKind != "" {
			if startedClean && groupDepth == 0 && strings.HasPrefix(strings.TrimSpace(code), ")") {
				blockKind = ""
				groupDepth = 0
				continue
			}
			if startedClean && groupDepth == 0 {
				// `type` groups declare type names, `var`/`const` groups declare
				// value names.
				addGoGroupEntry(code, blockKind, out)
			}
			// Bracket depth is updated even for a line that BEGAN inside a raw
			// string or block comment. Such a line still ends in real code — the
			// closing "`}, 0, cfg}," of a multi-line sample — and skipping it left
			// those closers uncounted, so groupDepth never returned to zero and
			// every later entry in the group was silently dropped. That was a
			// MISSED export, i.e. the false-positive direction, and it is exactly
			// what the differential test caught in gosec's testutils.
			groupDepth += goBracketDelta(code)
			if groupDepth < 0 {
				groupDepth = 0
			}
			continue
		}

		if !startedClean {
			// Outside a declaration group, a line that began inside a literal or
			// comment cannot open a top-level declaration: its column zero is
			// literal text, not code.
			continue
		}

		// Only column-zero lines can open a top-level declaration.
		if code == "" || code[0] == ' ' || code[0] == '\t' {
			continue
		}
		switch {
		case strings.HasPrefix(code, "func "):
			addGoFuncName(code, out)
		case strings.HasPrefix(code, "type "):
			if rest := strings.TrimSpace(code[len("type "):]); strings.HasPrefix(rest, "(") {
				blockKind = "type"
				groupDepth = goBracketDelta(rest[1:])
				continue
			}
			addGoFirstName(code[len("type "):], out)
		case strings.HasPrefix(code, "var "), strings.HasPrefix(code, "const "):
			kw := "var "
			if strings.HasPrefix(code, "const ") {
				kw = "const "
			}
			if rest := strings.TrimSpace(code[len(kw):]); strings.HasPrefix(rest, "(") {
				blockKind = "value"
				groupDepth = goBracketDelta(rest[1:])
				continue
			}
			addGoValueNames(code[len(kw):], out)
		}
	}
	return !inRaw && !inComment
}

// addGoFuncName records an exported top-level function name. A method
// (`func (r T) Name(`) is skipped: it is reachable through a value, not through
// the package qualifier this index serves.
func addGoFuncName(code string, out map[string]struct{}) {
	rest := strings.TrimSpace(code[len("func "):])
	if strings.HasPrefix(rest, "(") {
		return // method
	}
	addGoFirstName(rest, out)
}

// addGoFirstName records the leading identifier of rest if it is exported.
func addGoFirstName(rest string, out map[string]struct{}) {
	if name := leadingGoIdent(strings.TrimSpace(rest)); isGoExported(name) {
		out[name] = struct{}{}
	}
}

// addGoValueNames records every name on the left of a var/const declaration:
// `Foo, Bar = 1, 2` declares both.
func addGoValueNames(rest string, out map[string]struct{}) {
	lhs := rest
	// Cut at `=` (assignment) or at the type, whichever comes first; names are
	// only ever before them.
	if i := strings.IndexByte(lhs, '='); i >= 0 {
		lhs = lhs[:i]
	}
	for _, part := range strings.Split(lhs, ",") {
		addGoFirstName(part, out)
	}
}

// addGoGroupEntry records the names declared by one line inside a parenthesized
// declaration group. In a `type (` group the line is `Name spec`; in a
// `var`/`const` group it is `Name[, Name] [type] [= value]`, and a bare line with
// no value continues the previous const's iota expression while still declaring
// its own names.
func addGoGroupEntry(code, kind string, out map[string]struct{}) {
	code = strings.TrimSpace(code)
	if code == "" {
		return
	}
	if kind == "type" {
		addGoFirstName(code, out)
		return
	}
	addGoValueNames(code, out)
}

// goBracketDelta is the net bracket nesting a line adds, over already-masked
// code (so brackets inside strings and comments are gone).
func goBracketDelta(code string) int {
	delta := 0
	for i := 0; i < len(code); i++ {
		switch code[i] {
		case '{', '[', '(':
			delta++
		case '}', ']', ')':
			delta--
		}
	}
	return delta
}

// leadingGoIdent returns the identifier at the start of s ("" if none).
func leadingGoIdent(s string) string {
	i := 0
	for i < len(s) && isGoIdentByte(s[i], i == 0) {
		i++
	}
	return s[:i]
}

func isGoIdentByte(b byte, first bool) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b == '_':
		return true
	case b >= '0' && b <= '9':
		return !first
	}
	return false
}

// isGoExported reports whether name is a non-empty identifier starting with an
// ASCII uppercase letter. Go's real rule is unicode.IsUpper, but a qualified
// reference the guard would validate is written in the source as ASCII in
// practice, and restricting to ASCII here can only shrink the export set —
// which is the unsafe direction. So the differential test against go/parser is
// what confirms this never actually diverges.
func isGoExported(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}

// maskGoLine blanks the contents of string literals, rune literals, and comments
// on one line, so that a keyword or brace inside them is never read as code. It
// threads raw-string and block-comment state across lines.
//
// Returned code has literal bodies replaced by spaces, preserving column
// positions so the column-zero rule stays valid.
func maskGoLine(line string, inRaw, inComment bool) (code string, raw, comment bool) {
	b := []byte(line)
	out := make([]byte, len(b))
	copy(out, b)

	i := 0
	for i < len(b) {
		switch {
		case inComment:
			if i+1 < len(b) && b[i] == '*' && b[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i += 2
				inComment = false
				continue
			}
			out[i] = ' '
			i++
		case inRaw:
			if b[i] == '`' {
				out[i] = ' '
				i++
				inRaw = false
				continue
			}
			out[i] = ' '
			i++
		case i+1 < len(b) && b[i] == '/' && b[i+1] == '/':
			for ; i < len(b); i++ {
				out[i] = ' '
			}
		case i+1 < len(b) && b[i] == '/' && b[i+1] == '*':
			out[i], out[i+1] = ' ', ' '
			i += 2
			inComment = true
		case b[i] == '`':
			out[i] = ' '
			i++
			inRaw = true
		case b[i] == '"' || b[i] == '\'':
			quote := b[i]
			out[i] = ' '
			i++
			for i < len(b) {
				if b[i] == '\\' {
					out[i] = ' '
					i++
					if i < len(b) {
						out[i] = ' '
						i++
					}
					continue
				}
				if b[i] == quote {
					out[i] = ' '
					i++
					break
				}
				out[i] = ' '
				i++
			}
		default:
			i++
		}
	}
	return string(out), inRaw, inComment
}
