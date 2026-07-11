package guard

import (
	"regexp"
	"strings"
)

// Lang identifies the source language for a file.
type Lang string

const (
	LangGo      Lang = "go"
	LangJS      Lang = "js" // covers .js, .mjs, .cjs, .ts, .jsx, .tsx, .gs (GAS)
	LangPython  Lang = "py"
	LangUnknown Lang = ""
)

// LangFor returns the Lang for a file path based on extension.
func LangFor(path string) Lang {
	switch {
	case strings.HasSuffix(path, ".go"):
		return LangGo
	case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".mjs"),
		strings.HasSuffix(path, ".cjs"), strings.HasSuffix(path, ".ts"),
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
	"Intl", "Function", "WeakRef", "FinalizationRegistry",
	// binary data: ArrayBuffer/DataView + the typed-array constructors
	"ArrayBuffer", "SharedArrayBuffer", "DataView",
	"Int8Array", "Uint8Array", "Uint8ClampedArray", "Int16Array", "Uint16Array",
	"Int32Array", "Uint32Array", "Float32Array", "Float64Array",
	"BigInt64Array", "BigUint64Array",
	// common browser/runtime globals seen as bare calls/constructors
	"Notification", "EventSource", "WebSocket", "FormData", "Headers",
	"Request", "Response", "AbortController", "TextEncoder", "TextDecoder",
	"Blob", "File", "FileReader", "Image", "Audio", "Worker", "Event",
	"CustomEvent", "DOMParser", "XMLHttpRequest", "IntersectionObserver",
	"MutationObserver", "ResizeObserver",
	"parseInt", "parseFloat", "isNaN", "isFinite", "encodeURIComponent",
	"decodeURIComponent", "encodeURI", "decodeURI", "setTimeout", "setInterval",
	"clearTimeout", "clearInterval", "fetch", "btoa", "atob", "crypto",
	// browser dialog globals seen as bare calls
	"alert", "confirm", "prompt",
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
	rePyDef     = regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)\s*\(`)
	reJSFuncDef = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)\s*\(`)
	reJSVarDef  = regexp.MustCompile(`^\s*(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*=>|[A-Za-z_$][\w$]*\s*=>)`)
	// rePyClassDef / rePyConstDef capture Python class and SCREAMING_SNAKE module
	// constant definitions so that references to them elsewhere in the file are
	// not mistaken for hallucinations once const/class references are extracted.
	rePyClassDef = regexp.MustCompile(`^\s*class\s+([A-Za-z_]\w*)`)
	rePyConstDef = regexp.MustCompile(`^\s*([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+)\s*[:=]`)
	// reJSTypeDef captures TS type-level definitions (interface/type/enum/class) so
	// that a local type used in an annotation resolves instead of false-positiving.
	reJSTypeDef = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:declare\s+)?(?:abstract\s+)?(?:interface|type|enum|class)\s+([A-Za-z_$][\w$]*)`)
	// reGoInterfaceOpen matches a Go `interface {` token. It matches the anonymous
	// empty `interface{}` too (`\s*` allows zero spaces), so the caller must reject
	// an opener whose brace closes on the same line — an empty interface in a type
	// position or a `map[string]interface{}{...}` composite literal is not a
	// method-bearing body.
	reGoInterfaceOpen = regexp.MustCompile(`\binterface\s*\{`)
)

// ExtractDefs extracts the names being *defined* on the given lines (functions,
// methods, and — for Python/TS — classes, module constants, and type-level
// declarations). Used in pass 1 to include same-commit definitions in the known
// set, so a reference to something defined elsewhere in the edit/file does not
// read as a hallucination.
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
			} else if m := rePyClassDef.FindStringSubmatch(l.Text); m != nil {
				defs = append(defs, m[1])
			} else if m := rePyConstDef.FindStringSubmatch(l.Text); m != nil {
				defs = append(defs, m[1])
			}
		case LangJS:
			if m := reJSFuncDef.FindStringSubmatch(l.Text); m != nil {
				defs = append(defs, m[1])
			} else if m := reJSVarDef.FindStringSubmatch(l.Text); m != nil {
				defs = append(defs, m[1])
			} else if m := reJSTypeDef.FindStringSubmatch(l.Text); m != nil {
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
	prevNo := 0
	for i, l := range lines {
		// A diff hunk's added lines may be non-contiguous; the multi-line import
		// paren state cannot carry across a gap (mirrors the `open` reset in
		// ExtractRefs). Without this, an import block split across two hunks would
		// leave inPyParen set and misclassify the continuation hunk's first lines.
		if i > 0 && l.LineNo != prevNo+1 {
			inPyParen = false
		}
		prevNo = l.LineNo
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
//
// No leading `\b`: RE2's `\w` excludes `$`, so a `\b` anchor sits *between* `$`
// and a following letter — making `$http(` capture `http` (wrong symbol) and
// bare `$(` (jQuery) match nothing at all. Instead the left boundary is emulated
// in the scan loop by rejecting a match whose preceding byte is an identifier
// byte (see isWordByte), which correctly treats a leading `$` as part of the name.
var reCallIdent = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*\(`)

// reGoCallIdent is reCallIdent with an optional Go generic type-argument list
// (`Foo[int](x)`, `Transform[K, V](x)`) between the name and `(`. Without it a
// generic-instantiated call is silently missed (the name isn't immediately
// followed by `(`), so a hallucinated `DoesNotExist[int](5)` is never checked —
// a false negative. Go-only: `<...>`-style generic calls (TS) are left to
// reCallIdent to avoid the `a < b > (c)` comparison ambiguity.
//
// An index-then-call `Container[i](x)` also matches (the callee is really
// `Container[i]`, not `Container`), but the realistic FP surface is nearly empty:
// an unexported container is dropped by the exported-name filter; an exported
// package-level one resolves as a known Export; an exported-cased local (rare —
// Go locals are lowercase) is bound by LocallyBoundNames when its assignment is
// in the hunk. Any residual flag is FP-over-FN-consistent. The bracket body is
// non-nesting (`[^\[\]]*`), so a deeply-nested type arg (`Foo[map[K]V](x)`) is
// still missed — a narrower slice of the same FN.
var reGoCallIdent = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*(?:\[[^\[\]]*\])?\s*\(`)

// reUpperSnakeRef matches a SCREAMING_SNAKE_CASE identifier (requires at least
// one underscore-joined segment). These are module-constant references — a
// high-signal, low-false-positive class: a hallucinated constant (often a dropped
// import) reads as one, while ordinary locals almost never use this casing.
var reUpperSnakeRef = regexp.MustCompile(`\b([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+)\b`)

// reTSParamType matches a `paramName: PascalType` annotation — a lowercase-led
// binding annotated with a PascalCase type. Narrow on purpose: requiring the
// lowercase binding before the colon excludes object-literal keys like
// `Component: X` and most non-type colons.
var reTSParamType = regexp.MustCompile(`\b[a-z_$][\w$]*\??\s*:\s*([A-Z][A-Za-z0-9_$]*)`)

// tsTypeBuiltins are TS primitive and utility type names that must never be
// flagged in a type-annotation position. (jsBuiltins also covers Array, Promise,
// Map, Set, Date, etc., and is consulted too.)
var tsTypeBuiltins = setOf(
	"string", "number", "boolean", "any", "unknown", "never", "void", "object",
	"symbol", "bigint", "null", "undefined", "this", "true", "false",
	"Record", "Partial", "Required", "Readonly", "Pick", "Omit", "Exclude",
	"Extract", "NonNullable", "ReturnType", "Parameters", "InstanceType",
	"Awaited", "ReadonlyArray", "ThisType", "Uppercase", "Lowercase",
	"Capitalize", "Uncapitalize", "Iterable", "Iterator", "AsyncIterable",
)

// appendConstRefs adds SCREAMING_SNAKE constant references found in the (already
// literal-stripped) scan to refs. Skips qualified attrs (`x.MAX`), definition
// targets (`MAX =` / `MAX:`), and builtins.
func appendConstRefs(refs []Ref, scan string, lineNo int, builtins map[string]struct{}) []Ref {
	for _, idx := range reUpperSnakeRef.FindAllStringSubmatchIndex(scan, -1) {
		s, e := idx[2], idx[3]
		name := scan[s:e]
		if s > 0 && scan[s-1] == '.' {
			continue // qualified attribute access
		}
		rest := strings.TrimLeft(scan[e:], " \t")
		if strings.HasPrefix(rest, ":") || (strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, "==")) {
			continue // assignment / annotation target — a definition, not a use
		}
		if _, ok := builtins[name]; ok {
			continue
		}
		refs = append(refs, Ref{Name: name, LineNo: lineNo})
	}
	return refs
}

// appendTypeRefs adds PascalCase type-annotation references (`param: TypeName`)
// found in the scan to refs. Skips qualified types, single-char generic params
// (T/K/V), TS primitive/utility types, and JS builtins.
func appendTypeRefs(refs []Ref, scan string, lineNo int) []Ref {
	for _, idx := range reTSParamType.FindAllStringSubmatchIndex(scan, -1) {
		s, e := idx[2], idx[3]
		name := scan[s:e]
		if len(name) < 2 {
			continue // single-letter generic type parameter
		}
		if s > 0 && scan[s-1] == '.' {
			continue // qualified type (ns.Type)
		}
		if _, ok := tsTypeBuiltins[name]; ok {
			continue
		}
		if _, ok := jsBuiltins[name]; ok {
			continue
		}
		refs = append(refs, Ref{Name: name, LineNo: lineNo})
	}
	return refs
}

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
	// inIface reports whether we are inside a Go `interface { ... }` body. Its lines
	// are method *signatures* (Name(params) returns) and embedded interface names —
	// declarations, never calls — but reCallIdent reads `Name(` as a call and
	// defNames only recognizes `func`-prefixed defs, so without this an added
	// interface flags every method as an unresolved call. Interface bodies hold no
	// nested braces in practice (methods have no body; type sets use `|`/`~`), so a
	// boolean entered on a genuine body opener and cleared on the first `}` is both
	// sufficient and safe. Reset on a diff-hunk gap alongside `open`, since brace
	// continuity can't be assumed across a gap.
	inIface := false
	for i, l := range lines {
		text := l.Text
		if i > 0 && l.LineNo != prevNo+1 {
			open = ""
			inIface = false
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
		// Go interface bodies hold method signatures, not calls (see inIface). Braces
		// are counted on the stripped scan (literals blanked, so braces in strings
		// don't count).
		if lang == LangGo {
			if inIface {
				// A line containing `}` closes the body: exit and scan it as code, so
				// a real call sharing the close line (e.g. `}](m T) { Foo()` from a
				// multi-line generic type set) is still checked — FP-over-FN. Method
				// signatures effectively never share the closing line. An interior
				// signature line has no `}` and is suppressed.
				if strings.ContainsRune(scan, '}') {
					inIface = false
				} else {
					continue
				}
			} else if loc := reGoInterfaceOpen.FindStringIndex(scan); loc != nil {
				// Enter only a genuine method-bearing body: the brace must not close
				// on the same line. `interface{}` in a type position and the
				// `map[string]interface{}{...}` / `[]interface{}{...}` composite
				// literal both close immediately and must not enter. A fully inline
				// `interface { Method() }` also closes on its line and stays a known
				// FP (rare; favors this simple tracker over per-position matching).
				if after := scan[loc[1]:]; !strings.ContainsRune(after, '}') {
					inIface = true
				}
			}
		}

		// Names this line DEFINES (func/def/class/const). A reference to the
		// definition's own name is a self-match, not a call to validate — but any
		// OTHER call sharing the line (a one-line function body, a Python default-
		// arg factory) still is, so we skip per-name rather than the whole line.
		defs := defNames(lang, text)
		callRe := reCallIdent
		if lang == LangGo {
			callRe = reGoCallIdent
		}
		matches := callRe.FindAllStringSubmatchIndex(scan, -1)
		for _, idx := range matches {
			fullStart := idx[0]
			nameStart, nameEnd := idx[2], idx[3]
			name := scan[nameStart:nameEnd]

			// Skip if preceded by '.' (qualified call), or by an identifier byte
			// (the match is mid-identifier — this emulates the left `\b` the regex
			// no longer carries, while still allowing a leading `$` in the name).
			if fullStart > 0 {
				if prev := scan[fullStart-1]; prev == '.' || isWordByte(prev) {
					continue
				}
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
			// Skip the definition's own name (self-reference on a def line).
			if _, isDef := defs[name]; isDef {
				continue
			}
			refs = append(refs, Ref{Name: name, LineNo: l.LineNo})
		}

		// High-signal non-call references, kept narrow to protect precision. Import
		// lines are skipped (their identifiers are bindings, not uses); definition
		// lines are NOT skipped, because a function signature's parameter types are
		// genuine references worth checking. The per-extractor guards (assignment-
		// target for consts, the `param: Type` shape for types) keep a definition's
		// own name from self-flagging.
		if !isImportLine(lang, text) {
			switch lang {
			case LangPython:
				refs = appendConstRefs(refs, scan, l.LineNo, builtins)
			case LangJS:
				refs = appendTypeRefs(refs, scan, l.LineNo)
			}
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

// isCommentLine reports whether a line is a *whole-line* comment that should be
// skipped outright (used only when not mid-string/mid-block-comment).
//
// It deliberately only matches `//` (an unambiguous line comment). It does NOT
// match `* `/`*/` prefixes: those are only comment text when genuinely inside a
// /* ... */ region, which is now tracked statefully by stripLiteralsStateful
// (open == "*/"). Guessing "comment" from a `* ` prefix dropped real code —
// a wrapped `\t* Compute()` multiplication or a `*ptr`-style line — which for a
// truth-oracle is an FN (a silently missed hallucinated call), the worst class.
// Now such a line outside a block comment is scanned as code; the only cost is
// that a real block-comment continuation seen across a diff-hunk gap (where
// block state was reset, like the multi-line string reset above) reads as code
// and may yield a false positive. FP (noisy) is the safe direction over FN
// (silent miss). A `/*` that opens a block comment is handled by the stripper,
// not here, so genuine block comments are still blanked.
func isCommentLine(lang Lang, text string) bool {
	trimmed := strings.TrimSpace(text)
	switch lang {
	case LangGo, LangJS:
		return strings.HasPrefix(trimmed, "//")
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

// scanStripped calls fn for each line with its literal-stripped form (see
// stripLiteralsStateful), threading multi-line string state across lines and
// resetting it on a line-number gap — a non-contiguous AddedLine sequence, e.g.
// unrelated MultiEdit blocks joined by AddedLinesWithGap, whose open-string state
// must not leak across the boundary. This consolidates the identical
// state-threading loop the non-comment-aware scanners (firstUnqualifiedUseLines,
// LocallyBoundNames) would otherwise each copy; ExtractRefs/ExtractImports keep
// their own loops because they inspect state (comment lines / inPyParen) before
// stripping.
func scanStripped(lang Lang, lines []AddedLine, fn func(scan string, l AddedLine)) {
	open := ""
	prevNo := 0
	for i, l := range lines {
		if i > 0 && l.LineNo != prevNo+1 {
			open = ""
		}
		prevNo = l.LineNo
		var scan string
		scan, open = stripLiteralsStateful(lang, l.Text, open)
		fn(scan, l)
	}
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
		// Go/JS block comment /* ... */ — may span lines. Blank to the closing
		// */; if it doesn't close on this line, open multi-line state ("*/") so
		// the continuation lines (including `* ...`-prefixed ones) are blanked as
		// comment text rather than guessed at by line prefix. This is what makes
		// the `* `-prefix case state-driven instead of prefix-guessed: a `* Foo()`
		// line is only comment text when we're genuinely inside a /* */ region.
		if (lang == LangGo || lang == LangJS) && hasAt(b, i, "/*") {
			out[i] = ' '
			out[i+1] = ' '
			i += 2
			closed := false
			for i < n {
				if hasAt(b, i, "*/") {
					out[i] = ' '
					out[i+1] = ' '
					i += 2
					closed = true
					break
				}
				out[i] = ' '
				i++
			}
			if !closed {
				return string(out), "*/" // opens multi-line comment state
			}
			continue
		}
		// Python triple-quoted string (docstring / multi-line SQL).
		if lang == LangPython && (hasAt(b, i, `"""`) || hasAt(b, i, `'''`)) {
			delim := string(b[i : i+3])
			// A triple-quoted f-string (f"""…{Call()}…""") interpolates just like a
			// single-line one — blanking the whole interior would lose the call (an
			// FN). When this opens an f-string, scan {…} regions instead of blanking
			// them (same rule as the single-quote f-string branch below).
			fstr := isFStringPrefix(b, i)
			i += 3
			for i < n {
				if hasAt(b, i, delim) {
					i += 3
					delim = ""
					break
				}
				if fstr && i+1 < n && (hasAt(b, i, "{{") || hasAt(b, i, "}}")) {
					// Escaped literal brace — not an interpolation; blank both.
					out[i] = ' '
					out[i+1] = ' '
					i += 2
					continue
				}
				if fstr && b[i] == '{' {
					// Interpolation: leave bytes intact so reCallIdent sees the call.
					// Track brace depth (dict literals can appear); stop at the delim.
					depth := 1
					i++
					for i < n && depth > 0 && !hasAt(b, i, delim) {
						if b[i] == '{' {
							depth++
						} else if b[i] == '}' {
							depth--
							if depth == 0 {
								i++
								break
							}
						}
						i++
					}
					continue
				}
				out[i] = ' '
				i++
			}
			if delim != "" {
				// Multi-line triple-quote opens multi-line state. KNOWN limitation:
				// the multi-line `open` token carries only the delimiter, not the
				// f-string flag, so interpolations on continuation lines of a
				// multi-line triple f-string are NOT scanned (rare; single-line
				// triple f-strings are handled above).
				return string(out), delim
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
			// Python f-strings interpolate: f"{Build(y)}" contains a *real* call
			// inside the {...} region. Blanking the whole interior (as for an
			// ordinary string) silently loses that call — an FN, the worst class
			// for a truth-oracle. So for an f-string we blank the literal text but
			// SCAN (leave intact) the {...} interpolation regions, honoring the
			// {{ / }} escapes that denote literal braces (not interpolations).
			// Length is still preserved either way, keeping match indices honest.
			if lang == LangPython && isFStringPrefix(b, i) {
				quote := b[i]
				i++
				for i < n && b[i] != quote {
					switch {
					case b[i] == '\\' && i+1 < n:
						out[i] = ' '
						out[i+1] = ' '
						i += 2
					case hasAt(b, i, "{{") || hasAt(b, i, "}}"):
						// Escaped literal brace — not an interpolation; blank both.
						out[i] = ' '
						out[i+1] = ' '
						i += 2
					case b[i] == '{':
						// Interpolation region: leave bytes intact so the call inside
						// is seen by reCallIdent. Runs to the matching '}' (no nesting
						// of '{' inside an interpolation in valid f-strings, but a dict
						// literal could appear — track brace depth to be safe).
						depth := 1
						i++ // past the '{'
						for i < n && depth > 0 && b[i] != quote {
							if b[i] == '{' {
								depth++
							} else if b[i] == '}' {
								depth--
								if depth == 0 {
									i++ // past the closing '}'
									break
								}
							}
							i++
						}
						if depth > 0 && i >= n {
							// The interpolation's own '(' / '[' runs past end of line
							// unterminated (valid Python 3.12+: a call spanning lines
							// inside f"{...}"). Falling through to the final `return
							// string(out), open` would return the ORIGINAL open (""),
							// so the continuation line's closing quote is misread as
							// opening a fresh string instead of closing this one —
							// mirror the triple-quote branch and propagate the quote
							// itself as the open delimiter. Same KNOWN limitation as
							// the triple-quote case: a call nested inside the
							// continuation is not scanned, only the delimiter search.
							// FURTHER KNOWN LIMITATION: because `open` carries only the
							// quote byte (not the interpolation depth), on the
							// continuation line a *same-quote* character inside the
							// still-open interpolation is read as the string's close,
							// so trailing code after it on that line can be dropped
							// from scanning. This is net-positive over the prior
							// baseline (which returned "" here and misread the whole
							// continuation), and rare; a full fix would need open-state
							// to carry an interpolation-depth counter, not just the
							// quote byte — deferred.
							return string(out), string(quote)
						}
					default:
						out[i] = ' '
						i++
					}
				}
				if i < n && b[i] == quote {
					i++
				}
				continue
			}
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

// isFStringPrefix reports whether the quote at index i opens a Python f-string,
// by inspecting the (up to two) string-prefix letters immediately before it.
// Python allows f, rf, fr, and case variants (and br/rb, which are NOT f-strings).
// The prefix must be a token boundary on its left so we don't treat the `f` in an
// identifier like `conf"x"` (not valid Python, but be defensive) as a prefix.
func isFStringPrefix(b []byte, i int) bool {
	// Collect the run of letters directly preceding the quote (max 2 for valid
	// Python prefixes).
	start := i
	for start > 0 && start > i-2 && isPrefixLetter(b[start-1]) {
		start--
	}
	if start == i {
		return false
	}
	// Left of the prefix must not be an identifier char (else it's a bare name,
	// not a string prefix).
	if start > 0 {
		p := b[start-1]
		if p == '_' || (p >= 'A' && p <= 'Z') || (p >= 'a' && p <= 'z') || (p >= '0' && p <= '9') {
			return false
		}
	}
	for j := start; j < i; j++ {
		if b[j] == 'f' || b[j] == 'F' {
			return true
		}
	}
	return false
}

func isPrefixLetter(c byte) bool {
	switch c {
	case 'f', 'F', 'r', 'R', 'b', 'B':
		return true
	}
	return false
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

// defNames returns the identifier(s) a line DEFINES (the func/def/class/const
// name), so a reference to a definition's own name can be skipped as a self-match
// while genuine calls that share the line are still validated. Empty for a
// non-definition line. Each def regex captures the declared name in group 1.
func defNames(lang Lang, text string) map[string]struct{} {
	names := make(map[string]struct{})
	capture := func(re *regexp.Regexp) {
		if m := re.FindStringSubmatch(text); m != nil {
			names[m[1]] = struct{}{}
		}
	}
	switch lang {
	case LangGo:
		capture(reGoDef)
	case LangPython:
		capture(rePyDef)
		capture(rePyClassDef)
		capture(rePyConstDef)
	case LangJS:
		capture(reJSFuncDef)
		capture(reJSVarDef)
		capture(reJSTypeDef)
	}
	return names
}

// isImportLine reports whether a line is an import/require statement. References
// extracted from non-call positions skip these, since the names there are
// bindings, not uses.
func isImportLine(lang Lang, text string) bool {
	switch lang {
	case LangPython:
		return rePyFrom.MatchString(text) || rePyImport.MatchString(text)
	case LangJS:
		t := strings.TrimSpace(text)
		if strings.HasPrefix(t, "import ") {
			return true
		}
		// `export ... from '…'` is a re-export (binding); but `export function|const|
		// class|interface …` is a definition whose param/annotation types we DO want
		// to check, so only treat export-with-from as an import line.
		if strings.HasPrefix(t, "export ") && strings.Contains(t, " from ") {
			return true
		}
		return reJSRequire.MatchString(text)
	}
	return false
}
