package guard

import (
	"regexp"
	"strings"
)

// Lang identifies the source language for a file.
type Lang string

const (
	LangGo      Lang = "go"
	LangJS      Lang = "js" // covers .js, .ts, .jsx, .tsx, .gs (GAS)
	LangPython  Lang = "py"
	LangUnknown Lang = ""
)

// LangFor returns the Lang for a file path based on extension.
func LangFor(path string) Lang {
	switch {
	case strings.HasSuffix(path, ".go"):
		return LangGo
	case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".ts"),
		strings.HasSuffix(path, ".jsx"), strings.HasSuffix(path, ".tsx"),
		strings.HasSuffix(path, ".gs"):
		return LangJS
	case strings.HasSuffix(path, ".py"):
		return LangPython
	default:
		return LangUnknown
	}
}

// --- builtin / keyword exclusion sets ---

var goBuiltins = setOf(
	// builtins
	"len", "cap", "make", "append", "copy", "new", "delete",
	"panic", "recover", "close", "complex", "real", "imag",
	"print", "println",
	// basic type names used in conversion position
	"string", "int", "int8", "int16", "int32", "int64",
	"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
	"float32", "float64", "bool", "byte", "rune", "error", "any",
	// all Go keywords (can appear in call-like positions or before '(')
	"break", "case", "chan", "const", "continue", "default",
	"defer", "else", "fallthrough", "for", "func", "go", "goto",
	"if", "import", "interface", "map", "package", "range",
	"return", "select", "struct", "switch", "type", "var",
)

var jsBuiltins = setOf(
	// keywords that can appear in a call-like position (`x in (…)`, `case (…)`).
	// Go gets this for free via its full keyword list; JS/Py must enumerate too,
	// or every `return (`, `in (`, `of (` becomes a false positive.
	"return", "if", "else", "for", "while", "do", "switch", "case", "default",
	"break", "continue", "function", "throw", "try", "catch", "finally",
	"new", "typeof", "instanceof", "in", "of", "void", "delete", "await",
	"async", "yield", "var", "let", "const", "class", "extends", "super", "import",
	"export", "from", "as", "with", "debugger", "this",
	// globals / standard library callables (bare, unqualified)
	"console", "require", "Object", "Array", "String", "Number", "Boolean",
	"JSON", "Math", "Promise", "Symbol", "Map", "Set", "WeakMap", "WeakSet",
	"Date", "RegExp", "URL", "URLSearchParams", "Buffer", "Proxy", "Reflect",
	"BigInt", "structuredClone", "queueMicrotask", "globalThis",
	// common browser/runtime globals seen as bare calls/constructors
	"Notification", "EventSource", "WebSocket", "FormData", "Headers",
	"Request", "Response", "AbortController", "TextEncoder", "TextDecoder",
	"Blob", "File", "FileReader", "Image", "Audio", "Worker", "Event",
	"CustomEvent", "DOMParser", "XMLHttpRequest", "IntersectionObserver",
	"MutationObserver", "ResizeObserver",
	"parseInt", "parseFloat", "isNaN", "isFinite", "encodeURIComponent",
	"decodeURIComponent", "setTimeout", "setInterval", "clearTimeout",
	"clearInterval", "fetch",
	"Error", "TypeError", "RangeError", "SyntaxError", "ReferenceError",
	"EvalError", "URIError", "AggregateError",
	"undefined", "null", "true", "false",
)

var pyBuiltins = setOf(
	// keywords that can appear immediately before '(' (`return (x)`, `for i in (…)`,
	// `raise X`, `a or (b)`). Without these, ~half of all Python edits false-positive.
	"return", "raise", "yield", "assert", "del", "pass", "break", "continue",
	"global", "nonlocal", "lambda", "with", "as", "from", "import", "in", "is",
	"and", "or", "not", "if", "elif", "else", "for", "while", "try", "except",
	"finally", "def", "class", "async", "await", "None", "True", "False",
	// builtin functions
	"print", "len", "range", "str", "int", "float", "bool",
	"list", "dict", "set", "tuple", "type", "isinstance", "issubclass",
	"super", "enumerate", "zip", "map", "filter", "open",
	"repr", "getattr", "setattr", "hasattr", "delattr", "format",
	"sorted", "reversed", "sum", "min", "max", "abs",
	"any", "all", "next", "iter", "id", "hash", "dir",
	"vars", "callable", "input", "exit", "quit",
	"round", "divmod", "pow", "bytes", "bytearray", "frozenset", "complex",
	"slice", "object", "property", "staticmethod", "classmethod", "memoryview",
	"ord", "chr", "hex", "oct", "bin", "ascii", "globals", "locals",
	"eval", "exec", "compile", "breakpoint",
	// exception hierarchy (constantly raised: `raise ValueError(...)`)
	"Exception", "BaseException", "ValueError", "TypeError", "KeyError",
	"IndexError", "AttributeError", "RuntimeError", "OSError", "IOError",
	"FileNotFoundError", "FileExistsError", "PermissionError", "IsADirectoryError",
	"NotADirectoryError", "NotImplementedError", "StopIteration",
	"StopAsyncIteration", "GeneratorExit", "KeyboardInterrupt", "SystemExit",
	"ArithmeticError", "ZeroDivisionError", "OverflowError", "FloatingPointError",
	"LookupError", "NameError", "UnboundLocalError", "ImportError",
	"ModuleNotFoundError", "AssertionError", "TimeoutError", "ConnectionError",
	"ConnectionResetError", "BrokenPipeError", "RecursionError", "MemoryError",
	"BufferError", "EOFError", "TabError", "IndentationError", "SyntaxError",
	"UnicodeError", "UnicodeDecodeError", "UnicodeEncodeError", "Warning",
	"DeprecationWarning", "UserWarning", "RuntimeWarning",
)

func setOf(ss ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		m[s] = struct{}{}
	}
	return m
}

// --- definition extraction ---

var (
	reGoDef     = regexp.MustCompile(`^\s*func\s+(?:\([^)]*\)\s+)?([A-Za-z_]\w*)\s*[(\[]`)
	rePyDef     = regexp.MustCompile(`^\s*def\s+([A-Za-z_]\w*)\s*\(`)
	reJSFuncDef = regexp.MustCompile(`^\s*function\s+([A-Za-z_$][\w$]*)\s*\(`)
	reJSVarDef  = regexp.MustCompile(`^\s*(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*=>|[A-Za-z_$][\w$]*\s*=>)`)
)

// ExtractDefs extracts top-level function/method names being *defined* on the
// given lines (irrespective of language). Used in pass 1 to include same-commit
// definitions in the known set.
func ExtractDefs(lang Lang, lines []AddedLine) []string {
	var defs []string
	for _, l := range lines {
		switch lang {
		case LangGo:
			if m := reGoDef.FindStringSubmatch(l.Text); m != nil {
				defs = append(defs, m[1])
			}
		case LangPython:
			if m := rePyDef.FindStringSubmatch(l.Text); m != nil {
				defs = append(defs, m[1])
			}
		case LangJS:
			if m := reJSFuncDef.FindStringSubmatch(l.Text); m != nil {
				defs = append(defs, m[1])
			} else if m := reJSVarDef.FindStringSubmatch(l.Text); m != nil {
				defs = append(defs, m[1])
			}
		}
	}
	return defs
}

// --- import extraction ---

var (
	rePyFrom    = regexp.MustCompile(`^\s*from\s+[.\w]+\s+import\s+(.+?)\s*$`)
	rePyImport  = regexp.MustCompile(`^\s*import\s+(.+?)\s*$`)
	reJSImport  = regexp.MustCompile(`^\s*import\s+(.+?)\s+from\s+`)
	reJSRequire = regexp.MustCompile(`^\s*(?:const|let|var)\s+(.+?)\s*=\s*require\s*\(`)
	reIdent     = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
)

// ExtractImports returns the locally-bound names introduced by import statements
// on the given lines — `from pathlib import Path` binds `Path`, `import x as y`
// binds `y`, `import {a, b as B} from 'm'` binds `a` and `B`. These are real,
// callable symbols whose binding line usually sits outside an edit hunk, so
// folding them into the known set (via the in-file context) stops bare calls to
// imported helpers from reading as hallucinations. Go is intentionally excluded:
// imported packages are used qualified (pkg.Foo) and skipped already.
func ExtractImports(lang Lang, lines []AddedLine) []string {
	var names []string
	inPyParen := false // inside a multi-line `from M import ( … )`
	for _, l := range lines {
		text := l.Text
		switch lang {
		case LangPython:
			if inPyParen {
				seg := text
				if idx := strings.IndexByte(seg, ')'); idx >= 0 {
					seg = seg[:idx]
					inPyParen = false
				}
				names = append(names, parsePyNames(seg)...)
				continue
			}
			if m := rePyFrom.FindStringSubmatch(text); m != nil {
				list := strings.TrimSpace(m[1])
				if strings.HasPrefix(list, "(") && !strings.Contains(list, ")") {
					inPyParen = true // names continue on following lines
				}
				names = append(names, parsePyNames(strings.Trim(list, "()"))...)
			} else if m := rePyImport.FindStringSubmatch(text); m != nil {
				names = append(names, parsePyPlainImport(m[1])...)
			}
		case LangJS:
			if m := reJSImport.FindStringSubmatch(text); m != nil {
				names = append(names, parseJSImportClause(m[1])...)
			} else if m := reJSRequire.FindStringSubmatch(text); m != nil {
				names = append(names, parseJSBindingTarget(m[1])...)
			}
		}
	}
	return names
}

// parsePyNames parses a comma-separated import name segment (`a, b as B`),
// taking the alias when `as` is present and keeping only valid identifiers.
// Parentheses are stripped by the caller (single- and multi-line forms differ).
func parsePyNames(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if idx := strings.Index(item, " as "); idx >= 0 {
			item = strings.TrimSpace(item[idx+4:])
		}
		if item != "*" && reIdent.MatchString(item) {
			out = append(out, item)
		}
	}
	return out
}

// parsePyPlainImport parses `import x, y.z as a` → binds `x`, `a` (top-level
// module name, or alias when `as` is present).
func parsePyPlainImport(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if idx := strings.Index(item, " as "); idx >= 0 {
			item = strings.TrimSpace(item[idx+4:])
		} else if d := strings.IndexByte(item, '.'); d >= 0 {
			item = item[:d]
		}
		if reIdent.MatchString(item) {
			out = append(out, item)
		}
	}
	return out
}

// parseJSImportClause parses the clause of `import <clause> from 'm'`: a default
// name, `* as ns`, and/or a `{a, b as B}` named group.
func parseJSImportClause(s string) []string {
	var out []string
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.IndexByte(s, '}'); j > i {
			for _, item := range strings.Split(s[i+1:j], ",") {
				item = strings.TrimSpace(item)
				if idx := strings.Index(item, " as "); idx >= 0 {
					item = strings.TrimSpace(item[idx+4:])
				}
				if reIdent.MatchString(item) {
					out = append(out, item)
				}
			}
		}
		s = s[:i]
	}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if strings.HasPrefix(item, "*") {
			if idx := strings.Index(item, " as "); idx >= 0 {
				item = strings.TrimSpace(item[idx+4:])
			} else {
				continue
			}
		}
		if reIdent.MatchString(item) {
			out = append(out, item)
		}
	}
	return out
}

// parseJSBindingTarget parses the LHS of `const <target> = require('m')`: a bare
// name or a `{a, b: c}` destructuring (binds `a`, `c`).
func parseJSBindingTarget(s string) []string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") {
		var out []string
		for _, item := range strings.Split(strings.Trim(s, "{}"), ",") {
			item = strings.TrimSpace(item)
			if idx := strings.IndexByte(item, ':'); idx >= 0 {
				item = strings.TrimSpace(item[idx+1:])
			}
			if reIdent.MatchString(item) {
				out = append(out, item)
			}
		}
		return out
	}
	if reIdent.MatchString(s) {
		return []string{s}
	}
	return nil
}

// --- reference extraction ---

// callPattern matches an identifier immediately followed by '(' that is NOT
// preceded by '.' (which would make it a method/package call on an external value).
// The negative lookbehind is emulated by checking the character before the match.
var reCallIdent = regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\s*\(`)

// Ref is a function-call reference extracted from a line.
type Ref struct {
	Name   string
	LineNo int
}

// ExtractRefs extracts bare function call targets from the added lines for the
// given language. Qualified calls (pkg.Foo / obj.Method) are skipped.
func ExtractRefs(lang Lang, lines []AddedLine) []Ref {
	if lang == LangUnknown {
		return nil
	}
	builtins := builtinsFor(lang)

	var refs []Ref
	// open tracks an unterminated multi-line string delimiter carried across
	// lines (Python triple-quote `"""`/`'''`, JS/Go backtick). It resets on a
	// non-consecutive line, since a diff hunk's added lines may not be contiguous
	// and string continuity can't be assumed across a gap.
	open := ""
	prevNo := 0
	for i, l := range lines {
		text := l.Text
		if i > 0 && l.LineNo != prevNo+1 {
			open = ""
		}
		prevNo = l.LineNo
		// Skip whole-line comments (only meaningful when not mid-string).
		if open == "" && isCommentLine(lang, text) {
			continue
		}
		// Blank out string-literal and trailing-comment content so identifiers
		// inside them (e.g. `COUNT(` in a SQL string, or prose in a docstring) are
		// not mistaken for calls. Length is preserved, so match indices and LineNo
		// stay correct. open threads multi-line string state across lines.
		scan, newOpen := stripLiteralsStateful(lang, text, open)
		open = newOpen
		matches := reCallIdent.FindAllStringSubmatchIndex(scan, -1)
		for _, idx := range matches {
			fullStart := idx[0]
			nameStart, nameEnd := idx[2], idx[3]
			name := scan[nameStart:nameEnd]

			// Skip if preceded by '.' (qualified call)
			if fullStart > 0 && scan[fullStart-1] == '.' {
				continue
			}
			// Skip builtins / keywords
			if _, ok := builtins[name]; ok {
				continue
			}
			// For Go, skip unexported (lowercase) refs — the IR only indexes exported
			// symbols, so there is nothing to validate unexported calls against.
			if lang == LangGo && (name[0] < 'A' || name[0] > 'Z') {
				continue
			}
			// Skip definition lines (func/def/function keyword on same line before ident)
			if isDefLine(lang, text) {
				continue
			}
			refs = append(refs, Ref{Name: name, LineNo: l.LineNo})
		}
	}
	return refs
}

func builtinsFor(lang Lang) map[string]struct{} {
	switch lang {
	case LangGo:
		return goBuiltins
	case LangJS:
		return jsBuiltins
	case LangPython:
		return pyBuiltins
	}
	return nil
}

func isCommentLine(lang Lang, text string) bool {
	trimmed := strings.TrimSpace(text)
	switch lang {
	case LangGo, LangJS:
		return strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "*/")
	case LangPython:
		return strings.HasPrefix(trimmed, "#")
	}
	return false
}

// stripLiterals blanks string-literal and trailing-comment content on a single
// line (no multi-line state). Thin wrapper over stripLiteralsStateful for callers
// and tests that work one line at a time.
func stripLiterals(lang Lang, text string) string {
	s, _ := stripLiteralsStateful(lang, text, "")
	return s
}

// stripLiteralsStateful blanks string-literal and trailing-comment content on one
// line, replacing interior characters with spaces so length (and therefore match
// indices and LineNo) is preserved. This stops identifiers inside strings/comments
// — SQL keywords like `COUNT(`/`VALUES (`, or prose inside a docstring — from
// being read as calls.
//
// open is the multi-line string delimiter currently in effect at the start of the
// line ("" if none): Python `"""`/`”'`, or a JS/Go backtick. It returns the
// delimiter still open at end of line, which the caller threads to the next line.
// An unterminated single-line `"`/`'` string blanks to end-of-line (those do not
// span lines); a triple-quote or backtick with no close opens multi-line state.
func stripLiteralsStateful(lang Lang, text, open string) (string, string) {
	b := []byte(text)
	n := len(b)
	out := make([]byte, n)
	copy(out, b)
	i := 0

	// Continuation of a multi-line string from a previous line: blank until the
	// closing delimiter, or the whole line if it does not close here.
	if open != "" {
		for i < n {
			if hasAt(b, i, open) {
				i += len(open)
				open = ""
				break
			}
			out[i] = ' '
			i++
		}
		if open != "" {
			return string(out), open
		}
	}

	for i < n {
		c := b[i]
		// Trailing inline comment outside a string → blank to end of line.
		if isInlineCommentAt(lang, b, i) {
			for ; i < n; i++ {
				out[i] = ' '
			}
			break
		}
		// Python triple-quoted string (docstring / multi-line SQL).
		if lang == LangPython && (hasAt(b, i, `"""`) || hasAt(b, i, `'''`)) {
			delim := string(b[i : i+3])
			i += 3
			for i < n {
				if hasAt(b, i, delim) {
					i += 3
					delim = ""
					break
				}
				out[i] = ' '
				i++
			}
			if delim != "" {
				return string(out), delim // opens multi-line state
			}
			continue
		}
		// JS template literal / Go raw string (both span lines).
		if (lang == LangJS || lang == LangGo) && c == '`' {
			i++
			for i < n && b[i] != '`' {
				out[i] = ' '
				i++
			}
			if i < n {
				i++ // closing backtick on same line
			} else {
				return string(out), "`"
			}
			continue
		}
		// Single-line "..." / '...' string.
		if c == '"' || c == '\'' {
			quote := c
			i++
			for i < n && b[i] != quote {
				if b[i] == '\\' && i+1 < n {
					out[i] = ' '
					i++
					out[i] = ' '
					i++
					continue
				}
				out[i] = ' '
				i++
			}
			if i < n && b[i] == quote {
				i++
			}
			continue
		}
		i++
	}
	return string(out), open
}

// hasAt reports whether b contains s starting at index i.
func hasAt(b []byte, i int, s string) bool {
	if i+len(s) > len(b) {
		return false
	}
	return string(b[i:i+len(s)]) == s
}

// isInlineCommentAt reports whether a line comment begins at position i (outside
// any string — the caller only invokes it in unquoted context).
func isInlineCommentAt(lang Lang, b []byte, i int) bool {
	switch lang {
	case LangPython:
		return b[i] == '#'
	case LangGo, LangJS:
		return b[i] == '/' && i+1 < len(b) && b[i+1] == '/'
	}
	return false
}

func isDefLine(lang Lang, text string) bool {
	switch lang {
	case LangGo:
		return reGoDef.MatchString(text)
	case LangPython:
		return rePyDef.MatchString(text)
	case LangJS:
		return reJSFuncDef.MatchString(text) || reJSVarDef.MatchString(text)
	}
	return false
}
