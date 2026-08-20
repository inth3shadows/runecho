package guard

import (
	"maps"
	"regexp"
	"slices"
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
	// Go 1.21 builtins. Their absence was invisible while unexported Go
	// references were skipped wholesale — the compiler-oracle differential
	// surfaced `min` as a proven false positive the moment that skip was lifted.
	"min", "max", "clear",
	// basic type names used in conversion position
	"string", "int", "int8", "int16", "int32", "int64",
	"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
	"float32", "float64", "complex64", "complex128",
	"bool", "byte", "rune", "error", "any",
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

// jsTestGlobals are the ambient globals JS/TS test runners (Vitest, Jest, Mocha)
// inject into a spec file's scope — never imported, never defined, so a bare call
// to one reads as a hallucination to the additive check. Folded into the known
// set ONLY for test files (see IsJSTestFile / FoldTestGlobals), so a genuinely
// hallucinated it()/test()/expect() in PRODUCT code is still caught — FN (a silent
// miss) is the worst class, so this stays scoped rather than joining jsBuiltins.
// `vi`/`jest` are omitted: they are used qualified (`vi.fn()`, `jest.mock()`) and
// so already exempt via the qualified-`.` skip; a bare `vi(`/`jest(` never occurs.
var jsTestGlobals = setOf(
	"describe", "it", "test", "expect",
	"beforeEach", "afterEach", "beforeAll", "afterAll",
	"suite", "context", "specify",
	"xit", "xdescribe", "fit", "fdescribe",
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
	reGoDef = regexp.MustCompile(`^\s*func\s+(?:\([^)]*\)\s+)?([A-Za-z_]\w*)\s*[(\[]`)
	rePyDef = regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)\s*\(`)
	// The optional `<...>` mirrors reJSCallIdent: a generic function decl
	// `function transform<T>(x) {` must be captured as a definition, else the
	// call-side regex (which now bridges the type-arg list) reads the function's
	// own name as an unresolved call — a self-referential false positive on
	// ordinary generic TS. Kept in sync with reJSCallIdent's type-arg body.
	reJSFuncDef = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)\s*(?:<[\w$,.\[\]<>\s]+>)?\s*\(`)
	reJSVarDef  = regexp.MustCompile(`^\s*(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*=>|[A-Za-z_$][\w$]*\s*=>)`)
	// reJSVarDefCont mirrors reJSVarDef but for a SECOND (or later) function/arrow
	// declarator on the same `const`/`let`/`var` statement (`const a = () => {},
	// b = () => {}`) — valid JS, each declarator needing its own initializer. Not
	// anchored at `^` (it matches mid-line, after the comma), so defNamesInContext applies
	// it with FindAllStringSubmatch to pick up every trailing declarator; reJSVarDef
	// still owns the first one, which sits before any comma.
	reJSVarDefCont = regexp.MustCompile(`,\s*([A-Za-z_$][\w$]*)\s*=\s*(?:async\s+)?(?:function\b|\([^)]*\)\s*=>|[A-Za-z_$][\w$]*\s*=>)`)
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
//
// Order is deterministic: names appear in line order, and alphabetically within
// a line that defines more than one (a chained assignment, a tuple target list).
// The Python branch collects into a map, whose iteration order Go deliberately
// randomizes, so this needs the explicit sort below — without it the function
// returned a different order across runs on any multi-name line. Every in-tree
// caller folds the result into a set and so could not see it, but ExtractDefs is
// exported: a golden-output test or any order-sensitive consumer would have
// flaked about half the time (#296).
func ExtractDefs(lang Lang, lines []AddedLine) []string {
	return extractDefsSeeded(lang, lines, nil, nil)
}

// extractDefsSeeded is ExtractDefs with optional openSeed/braceDepthSeed — Pass
// 1's counterpart to extractRefs' own seeding (code-review finding on PR #290,
// #289's follow-up). ExtractDefs used to match rePyConstDef directly with no
// brace-context awareness at all: a multi-line dict key was added to the known
// set as a DEFINITION regardless of whether it sat inside an open dict literal.
// That made #289's actual fix (appendConstRefs/defNamesInContext correctly
// reading the key as a reference) moot in Run's full pipeline — Pass 2 checks
// `known[ref.Name]` first, and Pass 1 had already put the key there. The Python
// branch now delegates to defNamesInContext (the same brace-context-aware logic
// appendConstRefs/extractRefs use) instead of re-matching rePyConstDef on its
// own; this is a strict superset of the old set (it also picks up chained-
// assignment targets ExtractDefs never captured), which only ever narrows Pass
// 2's violation surface, never widens it.
//
// openSeed matters here too, not just braceDepthSeed: without it, a hunk that
// begins inside a pre-existing docstring has its own local pyOpen tracker start
// OUTSIDE any string (the unseeded default), so the docstring's closing `"""`
// reads as OPENING a new one — desyncing this function's brace count from
// extractRefs' correctly-masked one for the exact same hunk (round-3 review
// finding: Pass 1 and Pass 2 disagreeing on whether a name sits inside an open
// dict literal). Callers must pass the SAME openSeed they give extractRefs.
func extractDefsSeeded(lang Lang, lines []AddedLine, openSeed func(lineNo int) string, braceDepthSeed func(lineNo int) int) []string {
	var defs []string
	pyBraceDepth := 0
	pyOpen := ""
	prevNo := 0
	for i, l := range lines {
		if lang == LangPython && (i == 0 || l.LineNo != prevNo+1) {
			if openSeed != nil {
				pyOpen = openSeed(l.LineNo)
			} else {
				pyOpen = ""
			}
			if braceDepthSeed != nil {
				pyBraceDepth = braceDepthSeed(l.LineNo)
			} else {
				pyBraceDepth = 0
			}
		}
		prevNo = l.LineNo
		switch lang {
		case LangGo:
			if m := reGoDef.FindStringSubmatch(l.Text); m != nil {
				defs = append(defs, m[1])
			}
		case LangPython:
			// Brace counting uses the literal-stripped scan (a `{` inside a string
			// on this same line must not count) with f-string interpolation braces
			// additionally neutralized (#291). Masking happens BEFORE the name
			// extraction below because the context it produces is what decides
			// ref-vs-definition; pyBraceDepth advances only afterwards, so the ctx
			// carries the depth at the START of the line.
			var braceScan string
			_, braceScan, pyOpen = stripLiteralsBraces(LangPython, l.Text, pyOpen)
			ctx := pyLineCtx{scan: braceScan, base: pyBraceDepth}
			// Sorted, not raw map order — see ExtractDefs' contract (#296). Sorting
			// per line rather than over the whole result keeps line order intact,
			// which is the half of the ordering that carries meaning.
			defs = append(defs, slices.Sorted(maps.Keys(defNamesInContext(lang, l.Text, ctx)))...)
			pyBraceDepth = ctx.depthAtEnd()
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
	// reJSImportKeyword matches the start of any `import` statement, single- or
	// multi-line. Used only to detect a multi-line named-import block —
	// reJSImport itself already handles the single-line case where `from` sits
	// on the same line.
	reJSImportKeyword = regexp.MustCompile(`^\s*import\s`)
	// reJSImportSideEffect matches a side-effect-only import (`import
	// './styles.css';`) — starts with `import` and carries no `from` clause, but
	// binds no names and is never a multi-line opener, unlike a named-import
	// clause that merely hasn't reached its `from` yet.
	reJSImportSideEffect = regexp.MustCompile(`^\s*import\s*['"]`)
	// reJSFromClause matches a standalone `from` token — the line that closes a
	// multi-line named-import block always carries one (`} from 'm';`). Word-
	// bounded so an identifier like `fromDate` inside the block never matches.
	// Callers must match this against masked (stripLiterals) text — see
	// ExtractImports' LangJS branch — so a comment or string containing the word
	// "from" cannot be mistaken for the closing clause.
	reJSFromClause = regexp.MustCompile(`\bfrom\b`)
	// reJSExportFrom matches an `export ... from ...` re-export at any spacing —
	// spaced (`export { a } from 'm'`), minified (`export{a}from"m"`), or a
	// partially-spaced mix of the two (`export{a} from"m"`). isImportLine
	// already checks the `export` prefix; this only needs to confirm a genuine
	// `from` keyword (word-bounded, so it can't match inside an identifier like
	// `fromCache`) appears somewhere after it.
	reJSExportFrom = regexp.MustCompile(`^export\b.*\bfrom\b`)
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
	inPyParen := false  // inside a multi-line `from M import ( … )`
	inJSImport := false // inside a multi-line `import { … } from 'm'` (#304)
	var jsImportBuf strings.Builder
	prevNo := 0
	for i, l := range lines {
		// A diff hunk's added lines may be non-contiguous; multi-line import state
		// cannot carry across a gap (mirrors the `open` reset in ExtractRefs).
		// Without this, an import block split across two hunks would leave
		// inPyParen/inJSImport set and misclassify the continuation hunk's first
		// lines.
		if i > 0 && l.LineNo != prevNo+1 {
			inPyParen = false
			inJSImport = false
			jsImportBuf.Reset()
		}
		prevNo = l.LineNo
		text := l.Text
		switch lang {
		case LangPython:
			// Strip a trailing inline `# ...` comment before parsing. A Python
			// import statement contains no string literals, so the first `#` on an
			// import line always begins a comment. Without this, strings.Trim(list,
			// "()") leaves the comment attached to the last name and every name after
			// the first is dropped (`from m import (A, B)  # c` binds only A), and a
			// `)` inside a comment can prematurely close a multi-line group (#144).
			text = stripPyLineComment(text)
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
			// A multi-line named-import block (`import {\n  a,\n} from 'm'`) splits
			// the clause reJSImport needs whole across several lines — the dominant
			// style in any real TS codebase. Accumulate the comment/string-MASKED
			// text (not raw text — a `// pulled from utils` comment line, or a
			// module-path string containing "from", must never be mistaken for the
			// closing clause; code review on #319) until the closing `from` clause
			// appears, then parse the joined line exactly as the single-line case
			// (#304).
			masked := stripLiterals(LangJS, text)
			if inJSImport {
				jsImportBuf.WriteByte(' ')
				jsImportBuf.WriteString(masked)
				if reJSFromClause.MatchString(masked) {
					if m := reJSImport.FindStringSubmatch(jsImportBuf.String()); m != nil {
						names = append(names, parseJSImportClause(m[1])...)
					}
					inJSImport = false
					jsImportBuf.Reset()
				}
				continue
			}
			if m := reJSImport.FindStringSubmatch(text); m != nil {
				names = append(names, parseJSImportClause(m[1])...)
			} else if m := reJSRequire.FindStringSubmatch(text); m != nil {
				names = append(names, parseJSBindingTarget(m[1])...)
			} else if reJSImportKeyword.MatchString(masked) &&
				!reJSImportSideEffect.MatchString(masked) &&
				!reJSFromClause.MatchString(masked) {
				// `import` opened but this is neither a side-effect-only statement
				// nor already complete on this line — the clause continues on
				// following lines regardless of whether this line's braces happen to
				// balance (`import { a, b }\n  from 'm';` balances on line 1 yet
				// still needs the next line — code review on #319, Bug 2).
				inJSImport = true
				jsImportBuf.Reset()
				jsImportBuf.WriteString(masked)
			}
		}
	}
	return names
}

// stripPyLineComment truncates a Python import line at its inline `# ...` comment.
// Import statements have no string literals, so the first `#` always begins a
// comment — no string-literal masking is needed here (issue #144).
func stripPyLineComment(s string) string {
	if i := strings.IndexByte(s, '#'); i >= 0 {
		return s[:i]
	}
	return s
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
// name, a `{a, b: c}` object destructuring, or a `[a, b]` array destructuring —
// at any nesting depth. Delegates to jsBindingTargets, the bracket-depth-aware
// parser JSDeclaredNames already uses for `const`/`let`/`var` declarators: a
// require() binding target has the exact same LHS-pattern shape, so reusing it
// (rather than the previous strings.Trim(s, "{}") cutset trim, which stripped
// only the outermost brace pair and corrupted nested patterns, and never
// recognized `[` at all) fixes both the nesting and array cases at once.
func parseJSBindingTarget(s string) []string {
	return jsBindingTargets(strings.TrimSpace(s))
}

// JSDeclaredNames returns the identifiers bound by `const`/`let`/`var`
// declarations on the given lines — the LHS binding targets ONLY. It exists
// because a huge share of JS/TS additive false positives are bare calls to a
// name bound by a form ExtractDefs/ExtractImports miss: a useState setter
// (`const [x, setX] = useState()`), an object destructure (`const {a, b} = o`),
// or a callable assigned from a computed member (`const fn = handlers[k]`).
//
// Precision matters here in a way it does NOT for LocallyBoundNames (which is
// over-inclusive and used only to SUPPRESS dropped-import warnings): folding a
// name into the additive known set means a genuine hallucination of that name is
// no longer caught. So this binds only true declarator targets — it takes the
// value side of an object rename (`b: c` → c, never the key), the leading
// identifier of a bare/annotated declarator (`x: Foo` → x, never the type), and
// recurses into nested patterns. Function/arrow parameters are deliberately
// EXCLUDED: a parameter's TS type annotation (`ctx: RouteContext`) would leak the
// type name as a bound symbol and mask a real undefined-type reference.
func JSDeclaredNames(lines []AddedLine) []string {
	var out []string
	scanStripped(LangJS, lines, func(s string, _ AddedLine) {
		mm := reJSDeclList.FindStringSubmatch(s)
		if mm == nil {
			return
		}
		// splitJSParamList, not splitTopLevelCommas: the latter is not
		// angle-aware, so a generic in the INITIALISER split mid-type and its
		// tail bound the type name.
		//
		// KNOWN RESIDUAL GAP: a generic ARROW declarator is still open, because
		// its `<` follows a space rather than an identifier and so opens no
		// angle depth — `const f = <T, Bogus>(x: T, y: Bogus) => x` binds
		// `Bogus`. Requiring an identifier before `<` is what keeps an ordinary
		// `a = b < c` from being read as a generic, and relaxing it here would
		// trade this leak for that false positive. Recorded rather than
		// hidden — `const m = new Map<string, Bogus>();`
		// yielded `[m Bogus]`, and `const [a, setA] = useState<Map<string,
		// Bogus>>(…)` yielded `[a setA Bogus]`. The nested split inside
		// jsBindingTargets was already fixed for this; the frame above it was
		// not, so the leak survived at the top level.
		for _, decl := range splitJSParamList(mm[1]) {
			lhs := jsAssignLHS(decl)
			if lhs == "" {
				lhs = decl // declarator with no initializer (`let a, b;`)
			}
			out = append(out, jsBindingTargets(lhs)...)
		}
	})
	return out
}

// jsBindingTargets returns the identifiers a single binding pattern introduces:
// a bare name (annotation/default dropped), an array pattern (`[a, setA]` → a,
// setA), or an object pattern (`{a, b: c}` → a, c).
//
// Splits with splitJSParamList, not splitTopLevelCommas: the latter does not
// track `<>`, so a generic in a nested pattern's DEFAULT value split mid-type
// and bound the type name — `function f({ reg = new Map<string, Bogus>() })`
// yielded `[reg Bogus]`. appendJSParams was made angle-aware at the top level
// for exactly this reason; the leak simply survived one level down in the
// recursion. reIdentAll.FindString takes
// the leftmost identifier of each element, which also absorbs a default value
// (`a = 1` → a) since the binding name leads. Nested patterns recurse.
func jsBindingTargets(p string) []string {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	switch p[0] {
	case '[':
		inner, _ := jsPatternInner(p)
		var out []string
		for _, el := range splitJSParamList(inner) {
			out = appendJSBindElem(out, el, false)
		}
		return out
	case '{':
		inner, _ := jsPatternInner(p)
		var out []string
		for _, el := range splitJSParamList(inner) {
			out = appendJSBindElem(out, el, true)
		}
		return out
	default:
		if id := reIdentAll.FindString(p); id != "" {
			return []string{id}
		}
		return nil
	}
}

// appendJSBindElem adds the binding identifier(s) of one destructuring element.
// For an object element (obj=true) with a top-level rename colon (`b: c`), the
// binding is the value side (`c`) — the key is never bound. A nested pattern
// recurses; otherwise the leading identifier is taken (defaults/annotations
// trail it and are ignored).
func appendJSBindElem(out []string, el string, obj bool) []string {
	el = strings.TrimSpace(el)
	if el == "" {
		return out
	}
	if obj {
		if i := jsTopLevelColon(el); i >= 0 {
			el = strings.TrimSpace(el[i+1:])
		}
	}
	if strings.HasPrefix(el, "[") || strings.HasPrefix(el, "{") {
		return append(out, jsBindingTargets(el)...)
	}
	if id := reIdentAll.FindString(el); id != "" {
		out = append(out, id)
	}
	return out
}

// jsPatternInner returns the content between the leading bracket of p and its
// matching close (excluding both), so a trailing type annotation on the whole
// pattern (`{a}: Props`) is discarded, not mistaken for a binding. On an
// unbalanced pattern (one that spanned a diff-hunk gap) it returns the remainder
// after the opening bracket — best effort, never a panic.
func jsPatternInner(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	var close byte
	switch p[0] {
	case '[':
		close = ']'
	case '{':
		close = '}'
	default:
		return "", false
	}
	depth := 0
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
			if depth == 0 && p[i] == close {
				return p[1:i], true
			}
		}
	}
	return p[1:], true
}

// jsTopLevelColon returns the index of the first ':' in s at bracket depth 0, or
// -1. Used to split an object-destructure rename (`b: c`) at its key/value colon
// without being fooled by a colon inside a nested pattern or computed key.
func jsTopLevelColon(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
		case ':':
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// PyDeclaredNames returns the names bound by plain assignment statements on the
// given lines — the LHS target(s) only. It is the Python sibling of
// JSDeclaredNames: the additive check would otherwise flag a bare call to a name
// bound from a computed member (`handler = HANDLERS[key]; handler(payload)`) or
// any local assignment as a hallucination, because ExtractDefs sees only
// `def`/`class`.
//
// Precision is preserved the same way JSDeclaredNames preserves it — bind only
// real targets, never a type name or a keyword argument, so a genuine
// hallucination of that name is still caught (a false negative is the worst
// class). Concretely it:
//   - takes only the target list before a TOP-LEVEL plain `=` (skips ==, !=,
//     <=, >=, walrus :=, and augmented ops like += *= //=);
//   - skips a line that begins inside an unclosed bracket carried from a prior
//     line, so a kwarg on a wrapped call (`  timeout=30,`) is not read as a
//     binding; bracket depth is carried across lines and reset on a diff-hunk gap;
//   - drops attribute/subscript targets (`a.b`, `a[i]` rebind no new name),
//     annotation types (`x: int` → x, never int), and a leading `*` (starred
//     target). Tuple targets (`a, b = f()`) bind each name.
//
// Known limitation (accepted, FP-suppression side): a chained assignment
// (`a = b = c = 0`) binds only the first target — under-capture, never a false
// alarm.
//
// bracketDepthSeed supplies the ()/[]/{}-nesting depth in effect at the START
// of the given line — PyBracketDepthBefore's caller-facing form (#294). Nil
// (or a gap with no seed) starts at depth 0, the previous unseeded behavior.
// Without it, a hunk beginning inside a pre-existing multi-line call (opener
// unchanged context above the hunk) misreads a kwarg like `timeout=30,` as a
// genuine top-level assignment and folds `timeout` into the declared-names
// set, silently suppressing a later genuinely unresolved `timeout(...)` call.
func PyDeclaredNames(lines []AddedLine, bracketDepthSeed func(lineNo int) int) []string {
	var out []string
	depth := 0
	prevNo := 0
	first := true
	scanStrippedBraces(LangPython, lines, func(s, braceScan string, l AddedLine) {
		// A diff hunk's added lines may be non-contiguous; bracket continuity
		// can't be assumed across a gap (mirrors the open-string reset
		// elsewhere) — reseed from the real file state at THIS line rather than
		// resetting to 0, mirroring extractDefsSeeded's own gap handling.
		if first || l.LineNo != prevNo+1 {
			if bracketDepthSeed != nil {
				depth = bracketDepthSeed(l.LineNo)
			} else {
				depth = 0
			}
		}
		first = false
		prevNo = l.LineNo
		// Only a line that starts OUTSIDE any bracket is a statement-level
		// assignment; otherwise a `=` here is a kwarg/default inside a call.
		if depth == 0 {
			if lhs := pyAssignLHS(s); lhs != "" {
				out = append(out, pyBindTargets(lhs)...)
			}
		}
		// Depth advances on the f-string-neutralized braceScan, not the plain
		// code scan: an f-string interpolation's own brackets
		// (f"{compute(a, b)}") are string syntax, not real call/list/dict
		// nesting, and counting them desyncs this tracker from
		// PyBracketDepthBefore's seed the same way #291 desynced pyBraceDepth
		// before that fix.
		depth += pyBracketDelta(braceScan)
		if depth < 0 {
			depth = 0
		}
	})
	return out
}

// pyAssignLHS returns the target-list text before the first TOP-LEVEL plain `=`
// in s, or "" if s carries no such operator (not an assignment, or the `=` is
// part of ==/!=/<=/>=/:= or an augmented op like += //=).
func pyAssignLHS(s string) string {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case '=':
			if depth != 0 {
				continue
			}
			if i+1 < len(s) && s[i+1] == '=' {
				i++ // `==`
				continue
			}
			if i > 0 {
				switch s[i-1] {
				case '=', '!', '<', '>', ':', '+', '-', '*', '/', '%', '&', '|', '^', '@', '~':
					continue // tail of ==/!=/<=/>=/:= or an augmented assignment
				}
			}
			return s[:i]
		}
	}
	return ""
}

// pyBindTargets extracts the plain identifiers a target list binds, skipping
// attribute/subscript targets (`a.b`, `a[i]`), annotation types (`x: T` → x),
// and a leading `*`. Reuses splitTopLevelCommas so a tuple target with call
// commas in a nested annotation doesn't split wrongly.
func pyBindTargets(lhs string) []string {
	var out []string
	for _, t := range splitTopLevelCommas(lhs) {
		t = strings.TrimSpace(t)
		t = strings.TrimSpace(strings.TrimPrefix(t, "*"))
		// A parenthesized/bracketed tuple target (`(a, b) = f()`, `[a, b] = xs`,
		// or nested `a, (b, c) = …`) is a target list itself — recurse into it so
		// each inner name binds, rather than dropping the whole element below.
		if len(t) >= 2 && ((t[0] == '(' && t[len(t)-1] == ')') || (t[0] == '[' && t[len(t)-1] == ']')) {
			out = append(out, pyBindTargets(t[1:len(t)-1])...)
			continue
		}
		if i := jsTopLevelColon(t); i >= 0 { // `x: int` → x (colon logic is language-neutral)
			t = strings.TrimSpace(t[:i])
		}
		if strings.ContainsAny(t, ".[]()") {
			continue // attribute/subscript/call target binds no new name
		}
		if reIdent.MatchString(t) {
			out = append(out, t)
		}
	}
	return out
}

// rePyDefParamOpen matches the start of a `def`/`async def` signature up to and
// including its opening paren, capturing everything after it on the same line as
// the first slice of the parameter list. A def signature may span several lines,
// so this only opens the capture; pyParamScan threads the rest.
var rePyDefParamOpen = regexp.MustCompile(`^\s*(?:async\s+)?def\s+[A-Za-z_]\w*\s*\((.*)$`)

// PyParamNames returns the parameter names bound by every `def`/`async def` and
// `lambda` signature across lines. A parameter is a genuinely bound local: a bare
// call to one (`transform(line)` where `transform` is a `Callable`-typed param,
// or `fetch()` where `fetch` is a lambda arg) is not a hallucination, but neither
// ExtractDefs (sees only `def`/`class`) nor PyDeclaredNames (assignment targets
// only) folds it — so it was the last surviving Python false-positive class in the
// live decision log (parameters used as callables).
//
// Precision matches PyDeclaredNames and JSDeclaredNames exactly: bind the
// parameter NAME, never its type annotation or default value. `def f(cb: Handler)`
// binds `cb`, never `Handler` — folding the type would mask a genuine hallucinated
// `Handler()` elsewhere, the false-negative direction the whole guard avoids. A
// leading `*`/`**` (varargs/kwargs) is stripped; `/` and `*` positional/keyword
// markers bind nothing.
//
// Multi-line `def` signatures are threaded by paren depth so a parameter on its
// own continuation line (`    watchdog_starter: Callable[[], T] = _start,`) is
// captured. `lambda` is single-line: its params run from `lambda` to the first
// top-level `:` (lambda params take no annotations, so the colon is unambiguous).
//
// paramSigDepthSeed supplies the def-signature nesting depth in effect at
// the START of the given line — PyParamSigDepthBefore's caller-facing form
// (#294). Nil (or a gap with no seed) starts at depth 0, the previous
// unseeded behavior: a hunk whose FIRST line sits mid-signature (opener in
// unchanged context above the hunk) is then recognized as a continuation
// instead of being scanned as if no signature were open. This still cannot
// recover a signature whose CLOSE also sits outside the hunk — there is no
// added text to bind in that case, an inherent limit of a hunk-only
// scanner, not something seeding alone can fix.
//
// This is PyParamNames' OWN seed, not LocallyBoundNames'/
// PyDefSigDepthBefore's: the depth below tracks ALL of ()/[]/{} (via
// pyBracketDelta, matching the continuation branch's own advance), not
// parens alone — see PyParamSigDepthBefore's doc for why the two must not
// share a seed.
//
// Only a seed of EXACTLY 1 is trusted to resume accumulation. sig
// accumulates raw text and hands it to pyParseParamList, which tracks its
// OWN depth from 0 assuming that 0 IS the signature's own paren level — true
// when the hunk begins directly inside the signature (seed 1) and its own
// +=pyBracketDelta advance stays consistent with that from then on, but NOT
// true when the hunk begins already nested one level deeper (seed 2+, e.g.
// mid-way through a multi-line list/dict default value): pyParseParamList
// would then read the nested literal's own closing bracket as the
// signature's, truncating (and potentially misparsing) the accumulated
// text. A seed of 2+ is deliberately treated as unseedable (falls back to
// depth 0, the pre-#294 miss — safe, never a wrong parse) rather than risked.
func PyParamNames(lines []AddedLine, paramSigDepthSeed func(lineNo int) int) []string {
	var out []string
	// sigDepth > 0 means we are inside a def signature's parens spanning lines;
	// sig accumulates the stripped parameter-list text across them.
	sigDepth := 0
	var sig strings.Builder
	prevNo := 0
	first := true
	scanStrippedBraces(LangPython, lines, func(s, braceScan string, l AddedLine) {
		// A diff hunk's added lines may be non-contiguous; a signature cannot be
		// assumed to continue across a gap. Reseed from the real file state at
		// THIS line (mirrors PyDeclaredNames' own reseed) rather than always
		// resetting to 0 — a seed of exactly 1 means a signature genuinely IS
		// open here at a depth pyParseParamList can safely resume from, so the
		// branch below treats this line as its continuation. A deeper seed
		// (2+) is not safely resumable (see the doc above) and falls back to 0.
		if first || l.LineNo != prevNo+1 {
			sigDepth = 0
			if paramSigDepthSeed != nil {
				if seed := paramSigDepthSeed(l.LineNo); seed == 1 {
					sigDepth = 1
				}
			}
			sig.Reset()
		}
		first = false
		prevNo = l.LineNo

		// Continuation of a multi-line def signature already in progress
		// (either genuinely opened earlier in this scan, or seeded above).
		if sigDepth > 0 {
			sig.WriteByte(' ')
			sig.WriteString(s)
			// Advances on the f-string-neutralized braceScan, not the plain
			// code scan (#294): a default value's f-string interpolation
			// brackets (`x=f"{g(a,b)}"`) are string syntax, not signature
			// nesting, and counting them would close the signature early or
			// late depending on the interpolation's own bracket balance.
			sigDepth += pyBracketDelta(braceScan)
			if sigDepth <= 0 {
				out = append(out, pyParseParamList(sig.String())...)
				sigDepth = 0
				sig.Reset()
			}
		} else if loc := rePyDefParamOpen.FindStringSubmatchIndex(s); loc != nil {
			// A def signature opened on this line. Depth starts at 1 for the
			// paren the regex consumed, plus the net brackets in the captured
			// tail. loc[2]:loc[3] is capture group 1's byte span in s; braceScan
			// is byte-offset aligned with s (same length, same source buffer —
			// see scanStrippedBraces), so the identical span slices the
			// f-string-safe text for the depth count.
			rest := s[loc[2]:loc[3]]
			restBrace := braceScan[loc[2]:loc[3]]
			sigDepth = 1 + pyBracketDelta(restBrace)
			sig.Reset()
			sig.WriteString(rest)
			if sigDepth <= 0 {
				out = append(out, pyParseParamList(sig.String())...)
				sigDepth = 0
				sig.Reset()
			}
		}

		// Lambdas are single-line and may appear anywhere on the line, including
		// inside a call argument — scan the whole line independently of the def
		// state above (a def default value can itself be a lambda).
		out = append(out, pyLambdaParams(s)...)
	})
	return out
}

// PyParamSigDepthBefore returns the def-signature nesting depth PyParamNames
// itself tracks — in effect at the START of fileLines[idx], 0 if no
// signature is open there (#294).
//
// NOT PyDefSigDepthBefore: that function (dropped_import.go) tracks parens
// alone via pyConsumeParens, matching LocallyBoundNames' own pre-existing
// rule. PyParamNames' live continuation branch tracks ALL of ()/[]/{} via
// pyBracketDelta(braceScan) instead — a multi-line default value's own `[`
// or `{` must stay "open" from the signature's point of view too:
// `b=[\n 1, 2,\n],` only truly closes the signature at its OUTER `)`, not at
// the list's `]`. Seeding PyParamNames from PyDefSigDepthBefore's parens-only
// count desyncs the two the moment a default value spans a bracket the
// parens-only rule doesn't count — a nested list/dict/set literal reads as
// closing the signature early, silently dropping every parameter after it.
// This function mirrors PyParamNames' own rule exactly instead.
func PyParamSigDepthBefore(fileLines []AddedLine, idx int) int {
	if idx <= 0 || len(fileLines) == 0 {
		return 0
	}
	if idx > len(fileLines) {
		idx = len(fileLines)
	}
	open := ""
	depth := 0
	for _, l := range fileLines[:idx] {
		var scan, braceScan string
		scan, braceScan, open = stripLiteralsBraces(LangPython, l.Text, open)
		if depth > 0 {
			depth += pyBracketDelta(braceScan)
			if depth <= 0 {
				depth = 0
			}
		} else if loc := rePyDefParamOpen.FindStringSubmatchIndex(scan); loc != nil {
			rest := braceScan[loc[2]:loc[3]]
			depth = 1 + pyBracketDelta(rest)
			if depth <= 0 {
				depth = 0
			}
		}
	}
	return depth
}

// pyParseParamList extracts the bound parameter names from the text of a def
// parameter list — everything inside the signature parens, possibly with a
// trailing `):` and return annotation that pyParseParamList ignores. It splits on
// top-level commas and takes each segment's leading identifier, stripping a
// `*`/`**` prefix and stopping at the first `:` (annotation) or `=` (default).
func pyParseParamList(list string) []string {
	// Drop everything from the top-level closing paren onward (return annotation,
	// trailing `:`), so a `-> SomeType:` tail never leaks a type name.
	depth := 0
	for i := 0; i < len(list); i++ {
		switch list[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				list = list[:i]
				i = len(list)
				continue
			}
			depth--
		}
	}
	var out []string
	for _, seg := range splitTopLevelCommas(list) {
		if n := pyParamName(seg); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// pyParamName returns the identifier a single parameter segment binds, or "" for
// a bare positional/keyword marker (`*`, `/`) or an unparseable segment.
func pyParamName(seg string) string {
	seg = strings.TrimSpace(seg)
	seg = strings.TrimPrefix(seg, "**")
	seg = strings.TrimPrefix(seg, "*")
	seg = strings.TrimSpace(seg)
	// Cut at the annotation colon or default `=`, whichever comes first, so only
	// the name survives.
	if i := strings.IndexAny(seg, ":="); i >= 0 {
		seg = strings.TrimSpace(seg[:i])
	}
	if reIdent.MatchString(seg) {
		return seg
	}
	return ""
}

// rePyLambda matches a `lambda` and captures its parameter list up to the first
// colon on the same stripped line. Lambda params take no annotations, so the
// first top-level colon is unambiguously the body separator.
var rePyLambda = regexp.MustCompile(`\blambda\b([^:]*):`)

// pyLambdaParams extracts parameter names from every lambda on one stripped line.
//
// The param list is split with splitTopLevelCommas, NOT a naive strings.Split:
// a lambda default can be a multi-argument call (`lambda cfg=build(a, b): …`),
// whose interior commas would otherwise split the param list and bind the call's
// arguments (`b`) as if they were lambda parameters — folding a name a later
// hallucinated `b()` would then be masked against (a false negative). The def
// path already uses splitTopLevelCommas for the same reason; this brings lambda
// to parity.
func pyLambdaParams(s string) []string {
	var out []string
	for _, m := range rePyLambda.FindAllStringSubmatch(s, -1) {
		for _, seg := range splitTopLevelCommas(m[1]) {
			if n := pyParamName(seg); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}

// pyBracketDelta returns the net bracket nesting change across s ((/[/{ minus
// )/]/}), on already literal-stripped text so brackets in strings don't count.
func pyBracketDelta(s string) int {
	d := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			d++
		case ')', ']', '}':
			d--
		}
	}
	return d
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
// a false negative. Go uses `[...]`; the TS `<...>` twin is handled separately
// by reJSCallIdent.
//
// An index-then-call `Container[i](x)` also matches (the callee is really
// `Container[i]`, not `Container`), but the realistic FP surface is nearly empty:
// an unexported container is dropped by the exported-name filter; an exported
// package-level one resolves as a known Export; an exported-cased local (rare —
// Go locals are lowercase) is bound by LocallyBoundNames when its assignment is
// in the hunk. Any residual flag is FP-over-FN-consistent. The bracket body
// allows ONE level of nesting (`\[(?:[^\[\]]|\[[^\[\]]*\])*\]`), so a type arg
// like `Foo[map[K]V](x)` or `Foo[[]byte](x)` matches; a two-level nested arg
// (`Foo[map[[]K]V](x)`) is still missed — a much narrower slice of the same FN.
// The alternation is RE2 (linear, no backtracking), so nesting adds no ReDoS risk.
var reGoCallIdent = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*(?:\[(?:[^\[\]]|\[[^\[\]]*\])*\])?\s*\(`)

// reJSCallIdent is reCallIdent with an optional TS generic type-argument list
// (`Foo<T>(x)`, `Transform<K, V>(x)`) between the name and `(`, so a hallucinated
// generic call `DoesNotExist<T>(5)` is checked instead of silently missed (FN).
//
// `<...>` is genuinely ambiguous with comparison/shift, so this is deliberately
// tight to keep the FP surface near-zero rather than maximize recall:
//   - The `<` must be flush against the name (no space). Generic calls are written
//     `Foo<T>(` unspaced; comparisons are conventionally spaced (`a < b`), so a
//     real `a < b > (c)` never matches. Only an unspaced `a<b>(c)` comparison —
//     convention-violating and vanishingly rare — would, an acceptable FP.
//   - The type-arg body is restricted to type-expression bytes
//     (`[\w$,.\[\]<>\s]`): no operators, quotes, or parens. A compound boolean
//     like `a<b && c>(d)` fails on `&`, and string-literal / union / intersection
//     type args (`Foo<'a'>`, `Foo<A|B>`) simply aren't matched — a residual FN,
//     the safe direction. Go/Python keep reCallIdent (`<` is only ever an operator
//     there).
var reJSCallIdent = regexp.MustCompile(`([A-Za-z_$][\w$]*)(?:<[\w$,.\[\]<>\s]+>)?\s*\(`)

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

// rePyTupleAssignTargets matches a Python tuple/multiple-assignment LHS list
// (`MAX, OTHER = 5, 10`) and captures the whole comma-separated target span in
// group 1, so EVERY name in it — not just the first — can be recognized as a
// definition target rather than a reference. Requires at least one comma, so a
// single target (`MAX = 5`) still falls through to appendConstRefs' plain `:`/
// `=` check below, which also covers the annotation-only form (`MAX: int`) this
// pattern does not. The trailing `(?:[^=]|$)` excludes `==` the same way
// splitTopLevelAssigns does for chained assignment.
var rePyTupleAssignTargets = regexp.MustCompile(`^\s*((?:[A-Za-z_]\w*\s*,\s*)+[A-Za-z_]\w*)\s*=(?:[^=]|$)`)

// appendConstRefs adds SCREAMING_SNAKE constant references found in the (already
// literal-stripped) scan to refs. Skips qualified attrs (`x.MAX`), definition
// targets (`MAX: int = 5` / `MAX = 5` / `MAX, OTHER = 5, 10` / chained
// `MAX = OTHER = 5`), and builtins.
//
// defs is the caller's already-computed defNamesInContext result for this same
// line — appendConstRefs previously never consulted it, so a name defNamesInContext
// correctly recognizes as a chained-assignment target (`OTHER` in `MAX =
// OTHER = 5`) was still added here as a plain reference: its own fix
// never reached the code path that actually decides ref-vs-definition for
// SCREAMING_SNAKE names. Checking defs first covers that case; the two checks
// below remain for shapes defNamesInContext does not itself capture (dict-key
// disambiguation, tuple-assignment target spans).
//
// A `:` or `=` is only read as introducing a definition when the name is at
// statement-start position and outside any open `{}` literal — `{MAX: 5}` has
// MAX inside a dict, so it is a KEY (a genuine reference), not a type
// annotation, even though the text after it looks identical to one. ctx answers
// both at the name's own offset, which covers the inline case above, the
// multi-line one where the `{` sits on an earlier line (#289), and the mixed one
// where a dict closes and a new statement begins on the same physical line
// (#292). Tuple-assignment targets are identified separately
// (rePyTupleAssignTargets) since a name anywhere in `MAX, OTHER = 5, 10`'s
// target list — not only the first — is being defined, not used.
func appendConstRefs(refs []Ref, seen map[string]struct{}, scan string, lineNo int, builtins map[string]struct{}, defs map[string]struct{}, ctx pyLineCtx) []Ref {
	tupleTargets := pyTupleTargetSpans(ctx, scan)
	// reUpperSnakeRef yields matches in increasing offset order, so one forward
	// scanner answers every position without re-walking the line per match.
	defPos := newPyDefPosScanner(ctx)
	for _, idx := range reUpperSnakeRef.FindAllStringSubmatchIndex(scan, -1) {
		s, e := idx[2], idx[3]
		name := scan[s:e]
		if s > 0 && scan[s-1] == '.' {
			continue // qualified attribute access
		}
		if _, isDef := defs[name]; isDef {
			continue // recognized elsewhere on this line as a definition target
		}
		if inAnySpan(tupleTargets, s, e) {
			continue // part of a tuple-assignment LHS target list — a definition
		}
		rest := strings.TrimLeft(scan[e:], " \t")
		if defPos.at(s) {
			if strings.HasPrefix(rest, ":") || (strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, "==")) {
				continue // `NAME: type = value` / `NAME = value` — a definition, not a use
			}
		}
		if _, ok := builtins[name]; ok {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		refs = append(refs, Ref{Name: name, LineNo: lineNo})
	}
	return refs
}

// inAnySpan reports whether [s,e) lies wholly inside one of the spans.
func inAnySpan(spans [][2]int, s, e int) bool {
	for _, sp := range spans {
		if s >= sp[0] && e <= sp[1] {
			return true
		}
	}
	return false
}

// appendTypeRefs adds PascalCase type-annotation references (`param: TypeName`)
// found in the scan to refs. Skips qualified types, single-char generic params
// (T/K/V), TS primitive/utility types, and JS builtins.
func appendTypeRefs(refs []Ref, seen map[string]struct{}, scan string, lineNo int) []Ref {
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
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
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
	return extractRefs(lang, lines, nil, nil)
}

// extractRefs is ExtractRefs with optional openSeed/braceDepthSeed. openSeed(lineNo)
// returns the unterminated multi-line string delimiter in effect at the START of
// new-file line lineNo, letting a diff hunk that begins inside a pre-existing
// string or docstring be scanned in the right (masked) state — the hunk's added
// lines alone can't reveal it, since the opening delimiter sits in unchanged
// context above the hunk (issue #145). braceDepthSeed(lineNo) is the analogous
// seed for pyBraceDepth: the {}-nesting depth in effect at the START of that line,
// letting a hunk that begins inside an already-open (unchanged) multi-line dict
// literal be read correctly instead of assuming depth 0 — the dominant real-world
// shape of #289 (adding one key to an EXISTING dict, so the opening `{` is
// unchanged context, not part of the diff). A nil seed preserves the full-file /
// unseeded behavior for either: every contiguous run starts outside any string /
// at depth 0.
func extractRefs(lang Lang, lines []AddedLine, openSeed func(lineNo int) string, braceDepthSeed func(lineNo int) int) []Ref {
	if lang == LangUnknown {
		return nil
	}
	builtins := builtinsFor(lang)

	var refs []Ref
	// seen dedups refs by name (first occurrence wins). Both consumers already
	// collapse by name downstream — the generator into a name set, validate.go via
	// its per-(path,name) seen map keyed on the first line — so this changes no
	// output, but it bounds the returned slice to the distinct-identifier count.
	// Without it a pathological input (a file of millions of `a()` lines) would
	// hold millions of Ref structs before the caller dedups.
	seen := make(map[string]struct{})
	// open tracks an unterminated multi-line string delimiter carried across
	// lines (Python triple-quote `"""`/`'''`, JS/Go backtick). It resets on a
	// non-consecutive line, since a diff hunk's added lines may not be contiguous
	// and string continuity can't be assumed across a gap.
	open := ""
	prevNo := 0
	// inIface reports whether we are inside a Go `interface { ... }` body. Its lines
	// are method *signatures* (Name(params) returns) and embedded interface names —
	// declarations, never calls — but reCallIdent reads `Name(` as a call and
	// defNamesInContext only recognizes `func`-prefixed defs, so without this an added
	// interface flags every method as an unresolved call. Interface bodies hold no
	// nested braces in practice (methods have no body; type sets use `|`/`~`), so a
	// boolean entered on a genuine body opener and cleared on the first `}` is both
	// sufficient and safe. Reset on a diff-hunk gap alongside `open`, since brace
	// continuity can't be assumed across a gap.
	inIface := false
	// pyBraceDepth counts unclosed `{`/`}` nesting carried across lines, for
	// Python only. appendConstRefs' statement-start check can't see past its own
	// line, so a dict key on its own line inside a still-open multi-line dict
	// literal (`result = {\n    MAX_VALUE: 5,\n}`) looks identical to a genuine
	// top-level annotation — this is a running counter, not a boolean like
	// inIface, because dict literals nest arbitrarily (#289). Reset (seeded, like
	// open) on a diff-hunk gap, since brace continuity can't be assumed across one.
	pyBraceDepth := 0
	for i, l := range lines {
		text := l.Text
		if i == 0 || l.LineNo != prevNo+1 {
			// Start of a contiguous run (the first line, or after a hunk gap):
			// reset carried state. Seed the string state from the file context
			// above the run when a seed is available, so a run that opens inside a
			// pre-existing docstring is masked instead of scanned as code (#145);
			// without a seed the run starts outside any string, as before. Same
			// idea for pyBraceDepth via braceDepthSeed (#289): without it, a run
			// that begins inside an already-open dict literal from unchanged
			// context above starts wrongly at depth 0.
			if openSeed != nil {
				open = openSeed(l.LineNo)
			} else {
				open = ""
			}
			if braceDepthSeed != nil {
				pyBraceDepth = braceDepthSeed(l.LineNo)
			} else {
				pyBraceDepth = 0
			}
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
		scan, braceScan, newOpen := stripLiteralsBraces(lang, text, open)
		open = newOpen
		// pyCtx carries the depth at the START of this line, so every consumer
		// resolves brace state at the offset of the name it is judging rather than
		// from one line-start snapshot (#292). braceScan, not scan: an f-string
		// interpolation's braces are string syntax and must not feed the
		// cross-line dict counter (#291).
		pyCtx := pyLineCtx{scan: braceScan, base: pyBraceDepth}
		if lang == LangPython {
			pyBraceDepth = pyCtx.depthAtEnd()
		}
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
		defs := defNamesInContext(lang, text, pyCtx)
		callRe := reCallIdent
		switch lang {
		case LangGo:
			callRe = reGoCallIdent
		case LangJS:
			callRe = reJSCallIdent
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
			// Unexported Go references used to be skipped outright, because the IR
			// indexed only exported symbols and there was nothing to validate them
			// against. The Go parser now records unexported top-level declarations
			// under the "unexported" symbol kind, so that premise no longer holds
			// and the check covers them — closing the largest measured blind spot
			// in the guard's Go reach (0/29 caught, per the compiler-oracle
			// differential). Locals and parameters, which the IR will never hold,
			// are folded in by FoldInFileDefs via GoDeclaredNames.
			// Skip the definition's own name (self-reference on a def line).
			if _, isDef := defs[name]; isDef {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
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
				refs = appendConstRefs(refs, seen, scan, l.LineNo, builtins, defs, pyCtx)
			case LangJS:
				refs = appendTypeRefs(refs, seen, scan, l.LineNo)
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

// IsJSTestFile reports whether path is a JS/TS test/spec file by the conventional
// filename markers (`*.test.*`, `*.spec.*`) or a `__tests__/` directory. Used to
// scope jsTestGlobals to files where the runner actually injects them, so product
// code keeps full precision. Non-JS paths are always false.
func IsJSTestFile(path string) bool {
	if LangFor(path) != LangJS {
		return false
	}
	p := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	if strings.Contains(p, "/__tests__/") || strings.HasPrefix(p, "__tests__/") {
		return true
	}
	base := p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		base = p[i+1:]
	}
	return strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
}

// FoldTestGlobals adds the ambient test-runner globals to known when path is a
// JS/TS test file; no-op otherwise. Centralizing the test-file gate here keeps the
// pre-commit and hook known-set builders in sync through one call site each.
func FoldTestGlobals(known map[string]struct{}, path string) {
	if !IsJSTestFile(path) {
		return
	}
	for name := range jsTestGlobals {
		known[name] = struct{}{}
	}
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

// scanStrippedBraces is scanStripped's counterpart for a caller that also needs
// the f-string-neutralized braceScan (#291) — PyDeclaredNames and PyParamNames'
// own bracket-depth tracking, which used to run pyBracketDelta over the plain
// code scan and so counted an f-string interpolation's `()/[]/{}` as real
// nesting (#294). scan and braceScan are byte-offset aligned (same length,
// same underlying buffer — see stripLiteralsBraces/finishStrip), so a caller
// that locates a match in scan (FindStringSubmatchIndex) can slice braceScan
// with the identical offsets. Same literal-stripping and gap-reset behavior as
// scanStripped, computed in one pass via stripLiteralsBraces instead of
// stripLiteralsStateful's discard of the second scan.
func scanStrippedBraces(lang Lang, lines []AddedLine, fn func(scan, braceScan string, l AddedLine)) {
	open := ""
	prevNo := 0
	for i, l := range lines {
		if i > 0 && l.LineNo != prevNo+1 {
			open = ""
		}
		prevNo = l.LineNo
		var scan, braceScan string
		scan, braceScan, open = stripLiteralsBraces(lang, l.Text, open)
		fn(scan, braceScan, l)
	}
}

// OpenStateBefore returns the unterminated multi-line string delimiter in effect
// at the START of fileLines[idx] — the seed a scanner needs to read a block
// beginning at that line in the right (masked) state. fileLines must be a whole
// file's contiguous lines; idx is clamped into range, so an out-of-range index
// yields the state at the nearest end rather than a panic.
//
// This is the hook path's counterpart to openSeedFor (which reads the file and
// indexes by real new-file line number). The hook has the file's lines already
// in hand and needs the state at a MATCHED position rather than at a diff line
// number, so it threads the same masking here instead. See FileDiff.SeedByLine.
func OpenStateBefore(lang Lang, fileLines []AddedLine, idx int) string {
	if idx <= 0 || len(fileLines) == 0 {
		return ""
	}
	if idx > len(fileLines) {
		idx = len(fileLines)
	}
	open := ""
	for _, l := range fileLines[:idx] {
		_, open = stripLiteralsStateful(lang, l.Text, open)
	}
	return open
}

// PyBraceDepthBefore returns the Python {}-brace nesting depth in effect at the
// START of fileLines[idx] — OpenStateBefore's counterpart for pyBraceDepth (#289).
// Without it, the hook path's per-block seeding only covered the open-string leak
// (#145): a block that edits a dict literal without touching its opening `{` line
// (the opener sits in unchanged context, so it is never among the hook's added
// lines) started scanning at depth 0 regardless of the file's real state there.
// Mirrors ExtractRefs' own tracking: `{` minus `}` counted on each line's
// literal-stripped scan, threading the multi-line string state so braces inside a
// string/docstring never count. Python-only by construction (like pyBraceDepth
// itself) — always scanned as LangPython since callers only invoke this for .py
// files. fileLines must be a whole file's contiguous lines; idx is clamped into
// range.
func PyBraceDepthBefore(fileLines []AddedLine, idx int) int {
	if idx <= 0 || len(fileLines) == 0 {
		return 0
	}
	if idx > len(fileLines) {
		idx = len(fileLines)
	}
	open := ""
	depth := 0
	for _, l := range fileLines[:idx] {
		var braceScan string
		_, braceScan, open = stripLiteralsBraces(LangPython, l.Text, open)
		// Same accounting as extractRefs' own per-line advance, including the
		// clamp and the f-string neutralization (#291) — a seed computed any other
		// way would hand the run a depth its own tracking would never produce.
		depth = pyLineCtx{scan: braceScan, base: depth}.depthAtEnd()
	}
	return depth
}

// PyBracketDepthBefore returns the Python ()/[]/{}-bracket nesting depth in
// effect at the START of fileLines[idx] — PyBraceDepthBefore's counterpart for
// PyDeclaredNames/PyParamNames' own tracking (#294). Without it, a hunk that
// adds a kwarg-style line (`    timeout=30,`) inside a pre-existing multi-line
// call/list/dict whose opener sits in unchanged context above the hunk starts
// scanning at depth 0, misreading the line as a genuine top-level assignment or
// a fresh def signature rather than a continuation deep inside one.
//
// Uses the same f-string-neutralized braceScan PyBraceDepthBefore uses (#291):
// an f-string interpolation's own brackets are string syntax, not real
// nesting, and would otherwise desync this seed from what PyDeclaredNames' own
// per-line advance (once threaded through the same braceScan) produces.
// Python-only by construction; fileLines must be a whole file's contiguous
// lines; idx is clamped into range.
func PyBracketDepthBefore(fileLines []AddedLine, idx int) int {
	if idx <= 0 || len(fileLines) == 0 {
		return 0
	}
	if idx > len(fileLines) {
		idx = len(fileLines)
	}
	open := ""
	depth := 0
	for _, l := range fileLines[:idx] {
		var braceScan string
		_, braceScan, open = stripLiteralsBraces(LangPython, l.Text, open)
		depth += pyBracketDelta(braceScan)
		if depth < 0 {
			depth = 0
		}
	}
	return depth
}

// pyLineCtx is one Python line's brace/statement context: the literal-stripped,
// f-string-neutralized scan (braceScan from stripLiteralsBraces — same length as
// the raw line, so offsets are interchangeable) plus base, the {}-nesting depth
// in effect at the START of the line.
//
// It replaced a plain `inOpenBrace bool` snapshot taken at line start (#289's
// first cut). That snapshot was applied uniformly to every name on the line, so
// a name sitting after the line's own closing `}` — `}; MAX_TIMEOUT = 30`, where
// the dict ends and a fresh statement begins on one physical line — was read
// with the depth from before the `}` and misfiled as a dict key (#292). Every
// question is answered at the position of the specific name instead.
type pyLineCtx struct {
	scan string
	base int
}

// pyBraceStateAt reports the {}-nesting depth in effect at byte offset pos of
// scan, and whether pos begins a fresh statement — nothing but whitespace since
// the line start or since the last top-level `;`.
//
// Depth clamps at 0 progressively rather than only at end of line: a stray close
// (whose opener sits in unchanged context this run never saw) must not push the
// count negative, or a genuine dict opened later on the SAME line reads as depth
// 0 and reopens the #289 false negative the tracking exists to close.
func pyBraceStateAt(scan string, base, pos int) (depth int, stmtStart bool) {
	depth, stmtStart = base, true
	if pos > len(scan) {
		pos = len(scan)
	}
	for i := 0; i < pos; i++ {
		depth, stmtStart = pyBraceStep(scan[i], depth, stmtStart)
	}
	return depth, stmtStart
}

// pyBraceStep applies ONE byte of a masked line to the running (depth,
// stmtStart) pair. It is the single definition of how Python brace/statement
// state moves, and both walkers over a line — pyBraceStateAt and stmtSpans — step
// through it rather than keeping a copy.
//
// That is not stylistic. Hand-keeping two copies of one accounting rule is
// exactly what produced this PR's own seed-provider bug: three copies of the
// brace arithmetic existed, one kept the pre-fix behaviour, and the divergence
// was invisible until a review found it. A second walker with its own inlined
// clamp would have reopened the same class one function later.
func pyBraceStep(c byte, depth int, stmtStart bool) (int, bool) {
	switch c {
	case '{':
		return depth + 1, false
	case '}':
		// Clamp at 0: a stray close, whose opener sits in unchanged context this
		// run never saw, must not push the count negative — a genuine dict opened
		// later on the SAME line would then read as depth 0 and reopen the #289
		// false negative this tracking exists to close.
		if depth > 0 {
			depth--
		}
		return depth, false
	case ';':
		// A top-level `;` ends the statement, so what follows starts a fresh one
		// exactly as the line start does. Inside a literal it is punctuation (and
		// inside a string it is already blanked).
		return depth, depth == 0
	case ' ', '\t':
		return depth, stmtStart // whitespace does not end statement-start position
	}
	return depth, false
}

// pyDefPosScanner answers isDefinitionPos for a sequence of NON-DECREASING
// offsets in ONE forward pass. isDefinitionPos re-walks from offset 0 every
// call, which is fine for the handful of statement starts pyConstDefNames asks
// about but quadratic over a line's SCREAMING_SNAKE matches: it replaced an
// effectively O(1) prefix test, and a single module-level constant tuple
// (`ALLOWED = (CODE_0, CODE_1, ...)`) is exactly the shape that has thousands of
// them on one line. Measured before this scanner: 74ms for a 21KB line and 751ms
// at the 64KiB capLine ceiling, against a PreToolUse budget of ~12ms.
type pyDefPosScanner struct {
	ctx       pyLineCtx
	i         int
	depth     int
	stmtStart bool
}

func newPyDefPosScanner(ctx pyLineCtx) pyDefPosScanner {
	return pyDefPosScanner{ctx: ctx, depth: ctx.base, stmtStart: true}
}

// at reports isDefinitionPos(pos). pos must be >= the previous call's.
func (s *pyDefPosScanner) at(pos int) bool {
	if pos > len(s.ctx.scan) {
		pos = len(s.ctx.scan)
	}
	for s.i < pos {
		s.depth, s.stmtStart = pyBraceStep(s.ctx.scan[s.i], s.depth, s.stmtStart)
		s.i++
	}
	return s.stmtStart && s.depth == 0
}

// isDefinitionPos reports whether a SCREAMING_SNAKE name starting at byte offset
// pos sits where `NAME = value` / `NAME: T = value` means a genuine definition
// rather than a dict key. Both conditions must hold: the name opens a statement,
// and no `{` literal is open around it.
func (c pyLineCtx) isDefinitionPos(pos int) bool {
	depth, stmtStart := pyBraceStateAt(c.scan, c.base, pos)
	return stmtStart && depth == 0
}

// depthAtEnd returns the {}-depth this line carries to the next one.
func (c pyLineCtx) depthAtEnd() int {
	depth, _ := pyBraceStateAt(c.scan, c.base, len(c.scan))
	return depth
}

// stmtSpans returns the byte span of every statement on this line that begins
// OUTSIDE any open `{}` literal: one starting at 0 when the line itself starts at
// depth 0, plus one after each top-level `;`. A line that opens inside a
// multi-line dict has no statement span at all, which is what keeps its keys from
// being read as definitions.
//
// Each span is bounded at the NEXT statement rather than at end-of-line. That
// matters for any consumer that scans a whole segment instead of anchoring at its
// start: slicing to end-of-line made the per-statement split work only for the
// LAST statement on a line, so `MAX_A = OTHER_B = 5; z = 1` still lost OTHER_B
// and reported it as an unresolved use (code review, round 2).
func (c pyLineCtx) stmtSpans() [][2]int {
	var spans [][2]int
	start := -1
	if c.base == 0 {
		start = 0
	}
	depth, stmtStart := c.base, true
	for i := 0; i < len(c.scan); i++ {
		depth, stmtStart = pyBraceStep(c.scan[i], depth, stmtStart)
		// stmtStart is true after a `;` exactly when that `;` separates top-level
		// statements — the shared step's own notion, not a second copy of it.
		if c.scan[i] != ';' || !stmtStart {
			continue
		}
		if start >= 0 {
			spans = append(spans, [2]int{start, i})
		}
		if start = i + 1; start >= len(c.scan) {
			start = -1
		}
	}
	if start >= 0 {
		spans = append(spans, [2]int{start, len(c.scan)})
	}
	return spans
}

// pyConstDefNames returns every SCREAMING_SNAKE name this line DEFINES via a
// `NAME: T = value` / `NAME = value` statement. rePyConstDef is `^`-anchored, so
// on its own it can only ever see the first statement on a line; running it once
// per top-level `;`-separated statement is what lets `}; MAX_TIMEOUT = 30` be
// recognized as the definition it is (#292). Each candidate is still position-
// checked, so a dict key never qualifies.
func pyConstDefNames(ctx pyLineCtx) []string {
	var names []string
	for _, sp := range ctx.stmtSpans() {
		seg := ctx.scan[sp[0]:sp[1]]
		m := rePyConstDef.FindStringSubmatchIndex(seg)
		if m == nil {
			continue
		}
		// rePyConstDef's `\s*[:=]` matches the FIRST `=` of a `==`, so a
		// comparison reads as a definition and the name is folded into the known
		// set unchecked — a hallucinated constant used only in a comparison would
		// never be flagged. appendConstRefs carries the same guard; running the
		// regex at every top-level `;` offset is what made it matter here too
		// (code review on this PR).
		if strings.HasPrefix(strings.TrimLeft(seg[m[3]:], " \t"), "==") {
			continue
		}
		if pos := sp[0] + m[2]; ctx.isDefinitionPos(pos) {
			names = append(names, seg[m[2]:m[3]])
		}
	}
	return names
}

// pyTupleTargetSpans returns the byte span of every top-level statement's
// tuple-assignment target list (`MAX, OTHER = 5, 10`), so a name anywhere inside
// one is read as a definition rather than a use. Per-statement for the same
// reason pyConstDefNames is: rePyTupleAssignTargets is `^`-anchored and so could
// only ever see a line's first statement, leaving `}; MAX_A, MAX_B = 1, 2` — the
// multi-target arm of #292's own shape — flagging both names (code review on this
// PR).
func pyTupleTargetSpans(ctx pyLineCtx, scan string) [][2]int {
	var spans [][2]int
	for _, sp := range ctx.stmtSpans() {
		if m := rePyTupleAssignTargets.FindStringSubmatchIndex(scan[sp[0]:sp[1]]); m != nil {
			spans = append(spans, [2]int{sp[0] + m[2], sp[0] + m[3]})
		}
	}
	return spans
}

// pyChainedTargetNames returns every chained-assignment target on the line
// (`MAX_A = OTHER_B = 5` → both), once per top-level statement. Read off the
// masked scan rather than the raw text so an `=` inside a string literal cannot
// be mistaken for an assignment operator. This is the consumer that makes
// stmtSpans' right-hand bound load-bearing: pyChainedAssignTargets splits its
// WHOLE input, so an unbounded slice would swallow every following statement.
func pyChainedTargetNames(ctx pyLineCtx) []string {
	var names []string
	for _, sp := range ctx.stmtSpans() {
		names = append(names, pyChainedAssignTargets(ctx.scan[sp[0]:sp[1]])...)
	}
	return names
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
	scan, _, newOpen := stripLiteralsBraces(lang, text, open)
	return scan, newOpen
}

// stripLiteralsBraces is stripLiteralsStateful plus a second scan — braceScan —
// in which every Python f-string interpolation region is blanked outright.
//
// The f-string branches below deliberately leave an interpolation intact so a
// call inside `f"{Build(x)}"` is still seen. That is right for the CODE scan and
// wrong for the STATEMENT-STRUCTURE one: an interpolation's `{`/`}` are string
// syntax, not dict/set nesting, so when the matching close does not survive on
// the same line — a call spanning lines inside an interpolation, whose
// continuation the propagated open-quote state blanks wholesale — the depth
// leaks upward permanently and every later constant definition in that run is
// misread as a dict key (#291). The braces are not the only hazard: a `;` inside
// a format spec or nested replacement field would likewise read as a statement
// separator, so the whole region goes rather than a chosen subset of its
// punctuation (code review, round 2). Reference extraction still reads scan, so
// nothing inside an interpolation stops being checked.
//
// braceScan is the same string as scan whenever a line has no interpolation,
// which is every non-f-string line, so the common path costs no extra
// allocation.
func stripLiteralsBraces(lang Lang, text, open string) (string, string, string) {
	b := []byte(text)
	n := len(b)
	out := make([]byte, n)
	copy(out, b)
	i := 0
	// Spans of the f-string interpolation regions left intact for the code scan —
	// blanked in braceScan only (see finishStrip).
	var fstrSpans [][2]int

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
			return finishStrip(out, fstrSpans, open)
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
				return finishStrip(out, fstrSpans, "*/") // opens multi-line comment state
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
					// The whole region is f-string syntax, never the statement
					// structure the brace/`;` walkers read — record it for braceScan
					// (#291).
					spanStart := i
					depth := 1
					i++
					for i < n && depth > 0 && !hasAt(b, i, delim) {
						if b[i] == '\'' || b[i] == '"' {
							// A string literal nested in the interpolation is DATA, not
							// code — see maskNestedLiteral.
							i = maskNestedLiteral(b, out, i, n)
							continue
						}
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
					fstrSpans = append(fstrSpans, [2]int{spanStart, i})
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
				return finishStrip(out, fstrSpans, delim)
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
				return finishStrip(out, fstrSpans, "`")
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
						// literal could appear — track brace depth to be safe). The
						// whole region is f-string syntax, never the statement structure
						// the brace/`;` walkers read — record it for braceScan (#291).
						// This is the branch whose unclosed `{` leaks depth when the
						// interpolation runs past end of line.
						spanStart := i
						depth := 1
						i++ // past the '{'
						for i < n && depth > 0 && b[i] != quote {
							if b[i] == '\'' || b[i] == '"' {
								// A string literal nested in the interpolation is DATA,
								// not code — see maskNestedLiteral. Only a quote DIFFERENT
								// from the outer one reaches here; a same-quote nested
								// literal (valid only in Python 3.12+) already ends this
								// loop, which is the pre-existing limitation documented
								// below.
								i = maskNestedLiteral(b, out, i, n)
								continue
							}
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
						fstrSpans = append(fstrSpans, [2]int{spanStart, i})
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
							return finishStrip(out, fstrSpans, string(quote))
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
	return finishStrip(out, fstrSpans, open)
}

// finishStrip builds stripLiteralsBraces' two scans from the one masked buffer:
// scan as-is, and braceScan with the recorded f-string interpolation regions
// blanked. out is mutated only after scan has been materialized (string(out)
// copies), and when there is nothing to blank both results share one allocation.
func finishStrip(out []byte, fstrSpans [][2]int, open string) (string, string, string) {
	scan := string(out)
	if len(fstrSpans) == 0 {
		return scan, scan, open
	}
	for _, sp := range fstrSpans {
		for i := sp[0]; i < sp[1] && i < len(out); i++ {
			out[i] = ' '
		}
	}
	return scan, string(out), open
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
	// The prefix must be EXACTLY a valid f-string combination, not merely
	// CONTAIN an 'f' — the prior contains-check misclassified `bf`/`fb`/`ff`
	// as f-strings, none of which are (bare/case-doubled `f` isn't a distinct
	// prefix, and `b`+`f` are mutually exclusive prefix letters in Python).
	switch strings.ToLower(string(b[start:i])) {
	case "f", "rf", "fr":
		return true
	}
	return false
}

// maskNestedLiteral blanks a plain string literal nested inside an f-string
// interpolation and returns the index just past its closing quote (or n if the
// literal is unterminated on this line). i must index the literal's opening quote.
//
// The bytes of an interpolation are deliberately left intact so a genuine call
// inside `f"{Build(y)}"` is seen. A quoted literal WITHIN that interpolation is
// data, though, and leaving it intact makes its contents read as code:
// `f"{'acc(curr)':>10}"` reported `acc` as a bare call, and a format spec built
// this way is common enough in real code to matter (#256).
//
// A nested F-STRING is the exception and is left INTACT: its own interpolation is
// code, so blanking it drops any call inside — `f"outer {f'{Build(x)}'}"` would
// stop reporting a hallucinated `Build`, trading a false positive for a false
// negative, which is the worse direction for a truth oracle. Nested f-strings with
// differing quotes are valid on every supported Python, not only 3.12+. The cost is
// that the #256 false positive can still occur one level deeper, inside a nested
// f-string — accepted, because it is exactly the pre-fix behavior and no worse.
//
// Blanking is length-preserving, so match indices stay honest, and only non-code
// bytes are removed: a call that FOLLOWS the literal in the same interpolation
// (`f"{fmt('x') + compute(y)}"`) is still scanned.
func maskNestedLiteral(b, out []byte, i, n int) int {
	if isFStringPrefix(b, i) {
		// Skip the WHOLE nested f-string, leaving every byte intact so its
		// interpolation is still scanned. Resuming one byte in instead would leave the
		// nested string's CLOSING quote to be read as the opening of a fresh literal,
		// which then blanks forward past the outer interpolation's closing brace.
		q := b[i]
		i++
		for i < n {
			if b[i] == '\\' && i+1 < n {
				i += 2
				continue
			}
			if b[i] == q {
				return i + 1
			}
			i++
		}
		return n
	}
	quote := b[i]
	out[i] = ' '
	i++
	for i < n {
		if b[i] == '\\' && i+1 < n {
			out[i] = ' '
			out[i+1] = ' '
			i += 2
			continue
		}
		out[i] = ' '
		i++
		if b[i-1] == quote {
			return i
		}
	}
	return n
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

// defNamesInContext returns the identifier(s) a line DEFINES (the
// func/def/class/const name), so a reference to a definition's own name can be
// skipped as a self-match while genuine calls that share the line are still
// validated. Empty for a non-definition line. Each def regex captures the
// declared name in group 1.
//
// ctx supplies Python's cross-line brace state (#289): rePyConstDef's
// statement-start anchor (`^\s*NAME\s*[:=]`) has no way to see that the `{`
// opening a multi-line dict literal sits on an earlier line, so a dict key on
// its own line (`MAX_VALUE: 5,` inside `result = {\n    MAX_VALUE: 5,\n}`) reads
// identically to a genuine top-level constant definition. The name is
// position-checked against ctx instead, so a dict key falls through as a
// reference while a real definition — including one that follows a top-level `;`
// on the same line as the dict's close (#292) — is still captured. Python
// callers MUST supply a ctx built from this same line; other languages ignore
// it. Every caller already masks the line for its own scanning, so ctx is a
// hand-off of work already done rather than a second pass.
//
// captureAll exists alongside capture because a single statement can define more
// than one name — `const a = () => {}, b = () => {}` (JS, one declarator per
// function/arrow value) binds two names on one line. capture's underlying
// regexes are `^`-anchored (true string-start only, never re-matching after an
// internal comma), so switching capture itself to FindAll would not find a
// second name — reJSVarDefCont is deliberately unanchored so FindAll can walk
// the rest of the line for each later declarator. Python's chained-assignment
// equivalent (`MAX_A = OTHER_B = 5`) is NOT handled this way: an unanchored
// regex with a trailing `=` guard has the same problem one level up — FindAll's
// non-overlapping matches consume that trailing `=`, so it can supply at most
// one extra name and silently drops a third+ (confirmed: a 3-way chain lost
// its last target). pyChainedAssignTargets splits on every top-level bare `=`
// instead, which has no such limit; pyChainedTargetNames runs it once per
// top-level statement.
func defNamesInContext(lang Lang, text string, ctx pyLineCtx) map[string]struct{} {
	names := make(map[string]struct{})
	capture := func(re *regexp.Regexp) {
		if m := re.FindStringSubmatch(text); m != nil {
			names[m[1]] = struct{}{}
		}
	}
	captureAll := func(re *regexp.Regexp) {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			names[m[1]] = struct{}{}
		}
	}
	switch lang {
	case LangGo:
		capture(reGoDef)
	case LangPython:
		capture(rePyDef)
		capture(rePyClassDef)
		for _, n := range pyConstDefNames(ctx) {
			names[n] = struct{}{}
		}
		for _, n := range pyChainedTargetNames(ctx) {
			names[n] = struct{}{}
		}
		// Tuple-assignment targets are definitions too. Suppressing them only at
		// the reference site left them out of the known set, so a LATER use
		// (`}; MAX_A, MAX_B = 1, 2` then `use(MAX_A, MAX_B)`) still flagged both —
		// the false positive was relocated by a line, not removed (code review,
		// round 3).
		for _, sp := range pyTupleTargetSpans(ctx, ctx.scan) {
			for _, n := range reUpperSnakeRef.FindAllString(ctx.scan[sp[0]:sp[1]], -1) {
				names[n] = struct{}{}
			}
		}
	case LangJS:
		capture(reJSFuncDef)
		capture(reJSVarDef)
		captureAll(reJSVarDefCont)
		capture(reJSTypeDef)
	}
	return names
}

// pyChainedAssignTargets returns every target name in a Python chained
// assignment (`MAX_A = OTHER_B = THIRD_C = 5` → all three), or nil if text
// isn't one. Splits on every top-level bare `=` (splitTopLevelAssigns already
// excludes `==`/`!=`/`<=`/`>=`) rather than matching name-by-name, so it has no
// limit on chain length — see defNamesInContext' comment for why the regex-FindAll
// approach this replaced could not go past one extra name. Requires every
// segment but the last to be, once trimmed, a single SCREAMING_SNAKE
// identifier and nothing else; anything looser (`a.b = c = 5`, a genuine
// single assignment with only one `=`) returns nil rather than guess.
func pyChainedAssignTargets(text string) []string {
	segs := splitTopLevelAssigns(text)
	if len(segs) < 3 { // fewer than 2 targets + 1 value is not a chain
		return nil
	}
	names := make([]string, 0, len(segs)-1)
	for _, seg := range segs[:len(segs)-1] {
		seg = strings.TrimSpace(seg)
		if reUpperSnakeRef.FindString(seg) != seg {
			return nil // not a clean NAME segment — bail rather than guess
		}
		names = append(names, seg)
	}
	return names
}

// splitTopLevelAssigns splits s on every bare `=` (excluding `==`, and
// excluding `!=`/`<=`/`>=` by checking the preceding byte) that sits outside
// any (), [], or {} nesting. Mirrors splitTopLevelCommas' bracket-depth
// approach for a different operator.
func splitTopLevelAssigns(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case '=':
			if depth != 0 {
				continue
			}
			if i+1 < len(s) && s[i+1] == '=' {
				i++ // skip the whole `==`
				continue
			}
			if i > 0 {
				switch s[i-1] {
				case '!', '<', '>':
					continue // !=, <=, >=
				}
			}
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
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
		// `import{` (no space) covers minified/bundled syntax
		// (`import{a}from"m"`); the spaced form is still checked separately
		// since `import(` (dynamic import, a call expression) must not match.
		if strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "import{") {
			return true
		}
		// `export ... from '…'` is a re-export (binding); but `export function|const|
		// class|interface …` is a definition whose param/annotation types we DO want
		// to check, so only treat export-with-from as an import line. reJSExportFrom
		// tolerates any (or no) whitespace around `from` — a literal `" from "`/
		// `"}from"` substring check missed a partially-spaced minified/reformatted
		// mix like `export{a} from"m"`.
		if reJSExportFrom.MatchString(t) {
			return true
		}
		return reJSRequire.MatchString(text)
	}
	return false
}

// jsTypeAliasOpen matches the head of a TS type alias (`type H = …`,
// `export type H<T> = …`). Everything a type alias binds is a TYPE, so a
// parameter list inside one — `type Handler = (evt: MouseEvent) => void` — binds
// NOTHING. Without this, that arrow reads exactly like a value arrow (its `(` is
// preceded by `=`, the same as `const f = (evt) => …`) and JSParamNames would
// bind `evt`, masking a genuine undefined `evt` reference elsewhere in the file.
var jsTypeAliasOpen = regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?type\s+[A-Za-z_$][\w$]*\s*[=<]`)

// jsInterfaceOpen matches the head of a TS interface body. Same reasoning as
// jsTypeAliasOpen: an interface body is a pure type position.
var jsInterfaceOpen = regexp.MustCompile(`^\s*(?:export\s+)?(?:declare\s+)?interface\s+[A-Za-z_$][\w$]*`)

// jsFunctionOpen matches a `function` keyword whose parameter list opens on the
// same line — `function f(`, `async function <T>(`, `export default function(`.
// The `function` keyword is the one opener that is unambiguously a VALUE
// position, so it needs no lookahead past the closing paren to be trusted.
var jsFunctionOpen = regexp.MustCompile(`\bfunction\b[^(]*\(`)

// JSParamNames returns the identifiers bound by JS/TS function and arrow
// PARAMETER lists, across lines. It is the JS sibling of PyParamNames and
// GoDeclaredNames, both of which have folded parameters into the resolvable set
// since they shipped; the JS arm folded only JSDeclaredNames (const/let/var
// declarators), so a parameter — most visibly a destructured callback prop —
// resolved nowhere and a bare call to it was reported as a hallucination:
//
//	function Picker({          // <- `onChange` bound here, multi-line
//	  value,
//	  onChange,
//	}: PickerProps) {
//	  return <button onClick={() => onChange(value)} />;   // <- flagged (#302)
//	}
//
// JSDeclaredNames' doc says parameters are "deliberately EXCLUDED" because a TS
// annotation (`ctx: RouteContext`) "would leak the type name". That reason no
// longer holds: jsBindingTargets takes the LEADING identifier of an annotated
// element and jsPatternInner discards a whole-pattern annotation, so
// `{value, onChange}: PickerProps` yields exactly [value onChange] and never
// `PickerProps`. Type leakage is handled by the shared helpers; what is NOT
// handled by them — and is this function's real hazard — is a parameter list
// sitting in a TYPE position, where nothing is bound at all. Those are excluded
// structurally below, not by annotation-stripping.
//
// Precision follows JSDeclaredNames, not LocallyBoundNames: folding a name into
// the additive known set means a genuine hallucination of that name stops being
// caught, so every ambiguous construct binds nothing rather than guessing.
func JSParamNames(lines []AddedLine) []string {
	var out []string
	// sigDepth > 0 means a parameter list's parens are open across lines; sig
	// accumulates the stripped text, exactly as PyParamNames does for a def.
	sigDepth := 0
	var sig strings.Builder
	sigIsFunction := false // opener carried the `function` keyword
	sigLines := 0          // lines the open parameter list has spanned
	typeDepth := 0         // >0 while inside an interface body or type alias
	inTypeStmt := false
	typeOpenedBrackets := false // the type head opened a bracket nesting
	prevNo := 0
	first := true
	scanStripped(LangJS, lines, func(s string, l AddedLine) {
		// A diff hunk's added lines may be non-contiguous, so no cross-line
		// state may be assumed to survive a gap. Unlike PyParamNames there is
		// no seed callback here: this extractor's callers pass whole,
		// contiguous files (see foldinfile.go / validate.go), and inventing a
		// resumable-depth rule for a state nobody currently reaches would be
		// untestable surface. Reset and move on.
		if first || l.LineNo != prevNo+1 {
			sigDepth, typeDepth, inTypeStmt, sigLines = 0, 0, false, 0
			typeOpenedBrackets = false
			sig.Reset()
		}
		first = false
		prevNo = l.LineNo

		// Capped HERE, once, before anything derives from it. The cap used to
		// live inside jsParenMatches, which truncated the match table while
		// jsParamListOpen went on scanning the full line — so `match[open]`
		// indexed past the table and PANICKED on any line over 64 KiB.
		// parseDiffOutput (unlike TextToAddedLines) applies no capLine and its
		// scanner accepts up to 4 MB, and pre-commit mode is not wrapped in
		// deferOnPanic, so that aborted `git commit` with a stack trace.
		// One cap, one string, every derived index in range.
		s = capLine(s)

		// Where this line's opener scan begins. Nonzero only when a multi-line
		// signature closed partway through it, so the remainder is still scanned.
		lineFrom := 0

		// --- type positions bind nothing -----------------------------------
		if typeDepth > 0 || inTypeStmt {
			// A new top-level statement ends any type statement outright, so a
			// head with no `;` cannot swallow the rest of the file. Checked
			// FIRST and fallen through, not merely cleared: this line is real
			// code and must still be scanned for its own parameter list. An
			// earlier revision cleared the flag and returned anyway, which lost
			// the very first declaration after every unterminated type head.
			if jsTopLevelStatementStart(s) {
				typeDepth, inTypeStmt, typeOpenedBrackets = 0, false, false
			} else {
				// A head that OPENED a bracket or generic nesting ends when that
				// nesting closes — its `}`/`>` needs no `;`. Requiring one
				// latched the extractor for the rest of the enclosing block
				// whenever the type body was indented (inside a namespace, or a
				// local type in a function body), reopening #302 for that region.
				//
				// A head that opened NO nesting has only its `;`, and under
				// `semi: false` inside a block there is none — an indented
				// multi-line union (`type H =` / `| A` / `| B`) latched the same
				// way. Ending it at the first line that does not READ as a type
				// continuation bounds that: a continuation starts with an
				// operator or an opener, never with a keyword or a bare
				// identifier-led statement. That line is then scanned normally.
				// A line that OPENS a nesting is still part of the type
				// statement even when it does not read as a continuation — a
				// wrapped `extends B,` / `C {` puts the interface body's brace
				// on an identifier-led line, and ending the statement there
				// scanned the body as ordinary code.
				if typeDepth <= 0 && !typeOpenedBrackets &&
					!jsTypeContinuationStart(s) && jsTypeDelta(s) <= 0 {
					typeDepth, inTypeStmt = 0, false
				} else {
					typeDepth += jsTypeDelta(s)
					// No typeOpenedBrackets update here. A nesting opened on a
					// LATER line than the head does not need one: when it
					// closes, the next line that does not read as a
					// continuation ends the statement via the branch above.
					// Setting the flag was written first and then deleted for
					// being unkillable — no fixture could distinguish it,
					// including one built specifically to try (an indented
					// wrapped interface inside a namespace, where the
					// column-zero escape cannot mask the difference).
					if typeDepth <= 0 {
						typeDepth = 0
						if typeOpenedBrackets {
							typeOpenedBrackets = false
							inTypeStmt = false
						}
						// A type STATEMENT ends at its `;`, not merely when
						// brackets balance — that is what keeps a conditional
						// type or a wrapped generic in type position across
						// lines.
						//
						// No fall-through into the opener scan here: the line
						// carrying the `;` is the type statement's own last
						// line — for `type H =` / `(e: MouseEvent) => void;` it
						// IS the function type — so scanning it would bind `e`
						// from a pure type position. Review round 5 read this as
						// the early-exit defect fixed on the
						// jsTopLevelStatementStart path; it is not, and the case
						// that prompted it (a COMPLETE one-line conditional type
						// latching at all) is fixed in jsHeadIsUnfinished.
						if strings.Contains(s, ";") {
							inTypeStmt = false
						}
					}
					return
				}
			}
		}
		// Substring-gated: these two regexes are anchored but still cost real
		// backtracking on a deeply-indented line, and running them unguarded on
		// every line of a large file was 89% of this extractor's total runtime
		// (CPU profile, 400 KB TS corpus). The keyword test is exact — both
		// patterns require the literal word — so gating changes no result.
		// An ambient declaration or a bodyless overload signature declares a
		// TYPE, not a value: nothing in its parameter list is bound at runtime,
		// so binding those names would mask a hallucinated call to one. Both end
		// in `;` with no body brace, which is what distinguishes them from a
		// real declaration.
		if jsAmbientOrOverload(s) {
			return
		}
		if (strings.Contains(s, "interface") && jsInterfaceOpen.MatchString(s)) ||
			(strings.Contains(s, "type") && jsTypeAliasOpen.MatchString(s)) {
			typeDepth = jsTypeDelta(s)
			// An alias head that neither opens a bracket nor terminates —
			// `type H =` with the function type on the NEXT line — is still
			// inside a type position. Keying only on the bracket delta let that
			// continuation line be read as an ordinary value arrow and bind its
			// parameter, which is a false negative for every reference to that
			// name in the file.
			// The test is whether the head is UNFINISHED, not whether it lacks
			// a semicolon. Those differ under `semi: false` (prettier), where
			// `type Props = { a: string }` is a complete statement with no `;`
			// — and treating it as unfinished consumed the following line,
			// which for a whole class of TS codebases blanked the code after
			// every type alias and brought #302's false positive back.
			if typeDepth > 0 || jsHeadIsUnfinished(s) {
				inTypeStmt = true
				typeOpenedBrackets = typeDepth > 0
			}
			return
		}

		// --- continuation of a parameter list already open ------------------
		if sigDepth > 0 {
			// A real parameter list does not run for dozens of lines, but an
			// unbalanced `(` can latch this state forever — and one source of
			// those is not hypothetical: stripLiteralsStateful does NOT strip
			// JS regex literals, so `const OPEN = /\(/;` contributes a naked
			// open paren. Left unbounded, a single such line at the top of a
			// file silenced EVERY binding below it, reopening #302's false
			// positive for the whole file. Bounding the run contains that to
			// the lines it actually spans. (Stripping regex literals properly
			// belongs in the shared scanner, where it would affect every
			// check — filed rather than done here.)
			// A line that starts a new top-level statement cannot be the
			// continuation of a parameter list. This is what actually contains
			// the unstripped-regex case: `const OPEN = /\(/;` latches sigDepth
			// on line 1, and without this the `export function Picker({…})`
			// below it is swallowed rather than parsed — silencing every
			// binding in the file and reopening #302 for it. The line bound
			// below is the backstop for a latch inside a nested block, where
			// no column-zero statement follows.
			if jsTopLevelStatementStart(s) {
				sigDepth, sigLines = 0, 0
				sig.Reset()
				// Fall through to the opener scan: this line may itself open a
				// real parameter list, which is exactly the repro above.
			} else {
				if sigLines++; sigLines > maxJSSigLines {
					// Rescan what was collected before discarding it. The close
					// path already does this for a group that bound nothing;
					// the bail did not, so a `export const X = (` wrapping a
					// JSX body longer than the cap lost every arrow inside it —
					// `early` in a 60-line body was flagged as a hallucination
					// while the same code at 5 lines was clean. It also bounded
					// the unstripped-regex-literal containment to 40 lines
					// whenever the latch sits inside an indented block, where
					// jsTopLevelStatementStart cannot fire and this bail is the
					// only backstop.
					out = append(out, jsParamsInText(sig.String())...)
					sigDepth, sigLines = 0, 0
					sig.Reset()
					// Fall through rather than return: this line is what breaks
					// a runaway latch, and it may carry its own parameter list.
					// Its two siblings — the jsTopLevelStatementStart arm above
					// and the twin in dropped_import.go — both fall through.
					goto openerScan
				}
				sig.WriteByte(' ')
				sig.WriteString(s)
				idx, closed := jsConsumeParens(s, &sigDepth)
				if !closed {
					return
				}
				// Test BEFORE materialising the accumulated text: sig.String()
				// copies the whole buffer, and for a multi-line call that copy
				// is thrown away immediately.
				full := sig.String()
				// Trim back to the closing paren so a trailing `=> { body }`
				// is not parsed as a parameter.
				inner := full[:len(full)-(len(s)-idx)]
				before := len(out)
				if sigIsFunction || jsArrowFollows(s[idx+1:]) {
					out = appendJSParams(out, inner, sigIsFunction, s[idx+1:])
				}
				if len(out) == before {
					// The group bound nothing, so it was a parenthesised
					// EXPRESSION, not a parameter list — `export const Panel = (`
					// followed by multi-line JSX is the common one. Its interior
					// was swallowed by the latch, and every arrow inside it would
					// be lost. The single-line scan handles this by resuming
					// inside an unbound group; do the same here, over the text
					// the accumulator collected.
					out = append(out, jsParamsInText(inner)...)
				}
				sigDepth, sigLines = 0, 0
				sig.Reset()
				// Do NOT return: the rest of the closing line is ordinary code
				// and may hold its own parameter lists. Returning here was the
				// same early exit the single-line loop below was changed to
				// avoid, and it cost both the body of `function f(\n a,\n) {
				// const g = (bb) => bb(); }` and the inner half of a multi-line
				// curried arrow, `(\n store,\n) => (next) => …`.
				lineFrom = idx + 1
			}
		}

	openerScan:
		// The unparenthesized single-arg arrow (`cb => cb()`, `items.forEach(it
		// => it())`) has no `(` at all, so the opener scan below can never see
		// it. LocallyBoundNames — the sibling this change also edits — has
		// covered the shape since it shipped, via reJSArrowParamsBare; only
		// this extractor did not, leaving #302's false positive open for the
		// concise form, which is at least as common in TS/React as the
		// destructured-prop form. Precision is the same rule that regex
		// documents: the identifier must be directly followed by `=>`, so the
		// parenthesized form (whose `=>` follows a `)`) cannot match here.
		out = append(out, jsBareArrowParams(s[lineFrom:])...)

		// --- a parameter list may open on this line -------------------------
		// Scan openers left to right rather than committing to the first. A
		// parameter list is often not the first `(` on its line — `function
		// mk() { return (a) => a; }` and `await (async (tok) => tok)` both put
		// a call or an empty list ahead of the real one — and stopping at the
		// first cost those bindings entirely. Retry is only possible for a list
		// that CLOSES on this line; once one runs past the line end the scanner
		// is committed to it.
		//
		// The `function` opener is located ONCE, before the loop. Re-deriving it
		// per iteration meant a `strings.Contains(s[from:], "function")` scan of
		// the whole remaining line on every pass, which is O(n) work inside an
		// O(k) loop — quadratic in line length. On a single generated line of
		// `f0((x0)=>x0),…` that was 4x per doubling and 156 ms at the 64 KiB
		// capLine ceiling, against a ~12 ms budget; uncapped (ParseUnifiedDiff
		// does not cap) it reached seconds.
		fnOpen := -1
		if strings.Contains(s, "function") {
			if loc := jsFunctionOpen.FindStringIndex(s); loc != nil {
				fnOpen = jsFunctionParenAfter(s, loc[0])
			}
		}
		// Matching close for every `(` on the line, computed in ONE pass. The
		// loop below resumes INSIDE a group that bound nothing (an arrow can be
		// nested in a call or a parenthesised expression), and re-deriving each
		// group's close with jsConsumeParens then re-walked the same region per
		// nesting level — O(n^2). A chain of bare nested parens hit 835 ms at
		// 40 KB and 1.88 s at 60 KB, both under capLine and so reachable, on a
		// path the pre-commit mode runs with no deadline. The table makes the
		// close a lookup. TestJSParamNamesLinearInLineLength missed it because
		// its input is the shape where every group BINDS, and a bound group is
		// skipped whole.
		match := jsParenMatches(s)
		// Hoisted for the same reason fnOpen is: jsLastGenericClose is an O(n)
		// scan, and computing it inside jsParamListOpen put that scan inside the
		// opener loop — quadratic in line length all over again, which the two
		// linearity tests caught immediately.
		lastGT := jsLastGenericClose(s)
		from := lineFrom
		for {
			open, isFn, mayLatch := jsParamListOpen(s, from, fnOpen, lastGT)
			if open < 0 {
				return
			}
			sigIsFunction = isFn
			sigDepth = 1
			rest := s[open+1:]
			idx, closed := -1, false
			if m := match[open]; m >= 0 {
				idx, closed = m-open-1, true
			}
			if !closed {
				if !mayLatch {
					// An expression keyword's `(` that runs past the line end
					// is a parenthesised expression, not a parameter list —
					// overwhelmingly a multi-line JSX `return (`. Keep scanning
					// this line instead of swallowing the ones below it.
					sigDepth = 0
					if from = open + 1; from >= len(s) {
						return
					}
					continue
				}
				// Compute the REAL depth rather than assuming 1. A signature
				// whose opening line contains an unclosed nested call —
				// `const handler = (a = foo(` / `1` / `)) => a;` — is two deep
				// at the line end, and starting the continuation at 1 made the
				// first `)` on a later line read as the list's close, so
				// jsArrowFollows saw `") => a;"` and rejected it. Both siblings
				// already do this: PyParamNames via pyBracketDelta, and this
				// PR's own LocallyBoundNames twin via jsConsumeParens.
				sigDepth = 1
				jsConsumeParens(rest, &sigDepth)
				sigLines = 1
				sig.Reset()
				sig.WriteString(rest)
				return
			}
			before := len(out)
			out = appendJSParams(out, rest[:idx], sigIsFunction, rest[idx+1:])
			sigDepth, sigLines = 0, 0
			sig.Reset()
			// Keep scanning even after a successful bind. Stopping at the first
			// list that bound anything lost the inner half of a single-line
			// curried arrow — `const mw = (store) => (next) => next(store);`
			// bound only `store` and then flagged `next`. That is one
			// definition, not the "two definitions sharing a line" the early
			// exit was justified by, and it is the standard redux-middleware /
			// HOC shape. Every later candidate still passes jsArrowFollows, so
			// continuing cannot bind a call's arguments.
			//
			// Where to resume depends on what this group turned out to be. A
			// group that BOUND is a parameter list, and its interior holds
			// nothing further of interest, so resume after its close. A group
			// that bound nothing was a call or a parenthesised expression, and
			// an arrow can be nested INSIDE it — `await (async (tok) => tok)`
			// and `return (foo.map((x) => x))` both hide the real list there —
			// so resume just past its opening paren instead. Skipping the whole
			// group in that case is what lost them.
			if len(out) > before {
				from = open + 1 + idx + 1
			} else {
				from = open + 1
			}
			if from >= len(s) {
				return
			}
		}
	})
	return out
}

// jsParamListOpen finds the first parameter list opener on a line, returning the
// index of its `(` and whether the opener carried the `function` keyword.
// Returns -1 when the line opens no parameter list that may be trusted.
//
// Two openers are recognised, and the asymmetry between them is deliberate:
//
//   - `function` — unambiguously a value position, trusted on sight.
//   - a bare `(` — only MAYBE an arrow's parameter list. It is trusted only if
//     the closing paren is followed by `=>` (decided in appendJSParams, which
//     is the first point the closer is known), and only if the `(` is not
//     preceded by `:`. That `:` test drops two constructs at once: a type
//     annotation (`onChange: (v: X) => void`) and an object-literal arrow
//     property (`{ onChange: (v) => … }`). The second IS a real value binding
//     and is knowingly given up — separating the two needs to know whether the
//     enclosing braces are a type or an object literal, which is parsing, and
//     the cost of guessing wrong is a false negative.
//
// A method shorthand (`handleClick(e) { … }`) is deliberately NOT recognised:
// it is indistinguishable from a call (`handleClick(e);`) without knowing
// whether a `{` that follows opens a body or a block. Documented gap.
//
// Only the FIRST eligible opener on a line is returned. Two function
// definitions sharing one line is not a shape worth the state to handle.
// The third result reports whether the opener may LATCH across lines.
// `function`, and a `(` in a plain value position, may. A `(` led by an
// EXPRESSION keyword (`return (`, `yield (`) may not: those are followed by
// multi-line JSX far more often than by a wrapped arrow signature, and latching
// on one swallowed the whole JSX body — including every arrow inside it, which
// is exactly the binding this extractor exists to find.
// fnOpen is the index of the `function` form's parameter `(` on this line, or
// -1 — computed once by the caller, since re-deriving it per call made the
// opener loop quadratic in line length.
func jsParamListOpen(s string, from, fnOpen, lastGT int) (int, bool, bool) {
	// fnOpen is NOT short-circuited ahead of the scan. Returning it immediately
	// skipped any arrow parameter list EARLIER on the same line —
	// `const g = (aaa) => 0; function f(bbb) {}` bound only `bbb` — and left
	// the second of two `function` expressions unreachable, since only the first
	// one's index is precomputed. It is matched in position below instead.
	// Generic depth, so a parameter list inside a type-parameter clause is not
	// mistaken for the real one. jsFunctionParenAfter does this for the
	// `function` form; the arrow form needs it too, for a NESTED generic —
	// `const f = <T extends Array<(zzz: number) => void>>(x: T) => x` bound
	// `zzz`, a name from a pure type position that binds nothing at runtime,
	// masking a hallucinated `zzz(...)` elsewhere in the file.
	angle := 0
	for i := from; i < len(s); i++ {
		switch s[i] {
		case '{', ';', ')':
			// A generic parameter clause never spans a block, a statement, or a
			// closing paren. Without this an unspaced comparison or shift —
			// `if (i<n) {`, `while (i<n) list.forEach((cb) => …)` — opened a
			// depth that never closed, and the `angle > 0` guard below then hid
			// every later `(` on the line, silently disabling the fix there.
			//
			// `)` is safe to reset on even though a generic constraint may
			// CONTAIN parens (`<T extends Array<(z: number) => void>>`): that
			// inner `(` is already skipped while the depth is open, and by the
			// time its `)` resets the depth the clause has nothing left to hide.
			angle = 0
			continue
		case '<':
			// This function tracks a type PARAMETER clause (`f<T extends …>(`),
			// not a type argument list — so require the conventional uppercase
			// type-parameter name after `<`. An unspaced COMPARISON is
			// lowercase on the right (`count<max`, `i<n`, `m<n`), and treating
			// one as a generic opened a depth that hid every later `(` on the
			// line, losing a real parameter and flagging a bare call to it.
			// A `(` also opens one: a function type may sit in a type
			// argument position (`Array<(z: number) => void>`), and rejecting
			// that let its parameter name bind.
			//
			// splitJSParamList deliberately uses a different rule: it tracks
			// type ARGUMENTS (`Map<string, Handler>`), which are lowercase.
			if i < lastGT && i > 0 && isJSIdentByte(s[i-1]) && i+1 < len(s) &&
				((s[i+1] >= 'A' && s[i+1] <= 'Z') || s[i+1] == '(') {
				angle++
			}
			continue
		case '>':
			if angle > 0 && (i == 0 || s[i-1] != '=') {
				angle--
			}
			continue
		}
		if s[i] != '(' || angle > 0 {
			continue
		}
		if i == fnOpen {
			return i, true, true
		}
		j := i - 1
		for j >= 0 && (s[j] == ' ' || s[j] == '\t') {
			j--
		}
		// Preceded by `:` at any distance of whitespace → type annotation or
		// object-literal property. Skip THIS paren and keep scanning: abandoning
		// the whole line here would let one object-literal arrow property
		// suppress a legitimate parameter list later on the same line.
		if j >= 0 && s[j] == ':' {
			continue
		}
		// Preceded directly by an identifier character → usually a CALL
		// (`foo(`) or a method shorthand (`m(a) {`), neither of which is a
		// parameter list. Skipping those before the line-accumulator runs is a
		// worthwhile saving (measured on the 400 KB TS corpus: 4.42 → 4.78 ms
		// and 961 → 1205 KB per run without it).
		//
		// But the preceding word is NOT always a callee. A keyword can sit
		// directly before a genuine arrow's parameter list — `async (url) => …`
		// above all, which is ubiquitous in the TS/React code this extractor
		// exists for. Rejecting on the raw byte dropped every one of them, so
		// this is a CORRECTNESS rule with a performance motive, not the pure
		// optimisation an earlier revision of this comment claimed.
		// TestJSParamNamesKeywordLedArrows pins it.
		if j < 0 || !isJSIdentByte(s[j]) {
			// A `(` directly after a fat arrow or a `?` opens the arrow's BODY
			// or a ternary branch, never a parameter list — so it must not
			// latch across lines. `=> (\n  list.map((row) => row())\n)` is the
			// same wrapped-JSX hazard `return (` is exempted for, and latching
			// swallowed every arrow inside it. The same-line close path is left
			// intact, which is what binds a curried arrow.
			if jsPrecededByArrowOrTernary(s, j) {
				return i, false, false
			}
			return i, false, true
		}
		if kw, mayLatch, word := jsKeywordEndsAt(s, j); kw {
			return i, word == "function", mayLatch
		}
	}
	return -1, false, false
}

// isJSIdentByte reports whether b may appear in a JS/TS identifier.
func isJSIdentByte(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// appendJSParams parses one complete parameter list and appends its bindings.
// after is the text following the closing paren, used to confirm that a
// non-`function` opener really was an arrow: `(a, b) => …` binds, `foo(a, b);`
// and `method(a) { … }` do not. An optional TS return annotation may sit between
// the `)` and the `=>` (`(a): Foo => a`), so the test is that `=>` appears
// before any `{` or `;`, not that it appears immediately.
func appendJSParams(out []string, inner string, isFunction bool, after string) []string {
	if !isFunction && !jsArrowFollows(after) {
		return out
	}
	// An ambient declaration or a bodyless overload signature binds nothing at
	// runtime. jsAmbientOrOverload catches the single-line form before the line
	// is ever scanned, but a WRAPPED one (`declare function f(\n x: T\n): R;`)
	// latches across lines and never re-consults it — precisely the shape the
	// multi-line accumulator exists to read. Decided here instead, where the
	// whole signature has closed: no body brace and a trailing `;` after the
	// `)` means there is no function, only its type.
	if isFunction && jsBodylessAfter(after) {
		return out
	}
	// splitJSParamList, not splitTopLevelCommas: the latter tracks ()/[]/{} but
	// NOT <>, so a generic type argument containing a comma is split mid-type
	// (`m: Map<string, Handler>` → `m: Map<string` + `Handler>`) and the
	// fragment binds `Handler`, folding a TYPE NAME into the resolvable set and
	// masking a hallucinated `Handler(...)`.
	//
	// An earlier revision skipped fragments by accumulating angle depth ACROSS
	// elements, measured only after a top-level `:`. That heuristic was wrong in
	// both directions and shipped one bug of each kind: a generic in a DEFAULT
	// value never opened the guard (`reg = new Map<string, TotallyMadeUp>()`
	// bound the type name), and a `<` COMPARISON in an annotated default opened
	// it spuriously (`a: number = x < y, cb` swallowed `cb`, a fresh false
	// positive of exactly the class this file exists to close). Splitting
	// correctly in the first place removes the need to guess afterwards.
	for _, el := range splitJSParamList(inner) {
		out = append(out, jsBindingTargets(el)...)
	}
	return out
}

// splitJSParamList splits a parameter list on its top-level commas, tracking
// generic `<…>` in addition to the ()/[]/{} that splitTopLevelCommas handles.
//
// `<` and `>` are ambiguous in JS — they are also comparison operators — so a
// `<` opens a generic only in the position a generic can occupy: directly after
// an identifier byte (`Map<`, `Record<`, `Array<`), with no space between. A
// comparison is conventionally spaced (`x < y`), and an unspaced `x<y` inside a
// parameter default is rare enough to trade against the type-name leak. A `>`
// closes only when a generic is actually open, so a stray `n > 0` cannot drive
// the depth negative — the failure mode of the accumulator this replaces.
func splitJSParamList(s string) []string {
	var out []string
	// A `<` with no `>` after it anywhere is a comparison or a shift, never a
	// generic. Without this an unspaced `f(a = i<n, cb)` latched the depth and
	// stopped splitting, losing `cb`.
	lastGT := jsLastGenericClose(s)
	depth, angle, start := 0, 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			// Guarded, as the splitTopLevelCommas this replaced is: a stray
			// unmatched closer drives depth negative, after which NO top-level
			// comma splits again and every later declarator is lost. Regex
			// literals are not stripped, so `const re = /[)]/, handler = …`
			// reaches here with an unbalanced `)` and dropped `handler` — a
			// fresh false positive of the class this file exists to close.
			if depth > 0 {
				depth--
			}
		case '<':
			// `=>`'s `>` never reaches here, and `<=` is a comparison.
			// The discriminator is the byte BEFORE `<`, not after it: a
			// generic follows its constructor directly (`Map<`), while a
			// comparison is spaced on the left (`x < y`). Also requiring a
			// non-space AFTER `<` rejected the legal `Map< string, Handler >`,
			// which then split mid-type and bound `Handler` — the type-name
			// leak this tracking exists to prevent.
			if i < lastGT && i > 0 && isJSIdentByte(s[i-1]) && jsGenericArgsClose(s, i) {
				angle++
			}
		case '>':
			if angle > 0 && (i == 0 || s[i-1] != '=') {
				angle--
			}
		case ',':
			if depth == 0 && angle == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// jsTopLevelStatementStart reports whether s begins, at column zero, a new
// top-level statement — the boundary at which an unclosed parameter list must
// be abandoned rather than extended across it.
func jsTopLevelStatementStart(s string) bool {
	if s == "" || s[0] == ' ' || s[0] == '\t' {
		return false
	}
	if s[0] == '}' {
		// A column-zero `}` usually closes a block — but it also closes a
		// destructured parameter pattern, and `}: PickerProps) {` is the
		// CLOSING line of the very signature this extractor exists to read.
		// Treating that as a statement boundary abandoned the parameter list
		// one line before it completed, which silenced #302's headline case.
		// A pattern close is followed by `:`, `)`, `,` or `=`; a block close by
		// nothing or `;`.
		i := 1
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '}') {
			i++
		}
		if i < len(s) {
			switch s[i] {
			case ':', ')', ',', '=':
				return false
			}
		}
		return true
	}
	for _, kw := range []string{"export ", "function ", "class ", "const ", "let ", "var ", "import ", "interface ", "type ", "async function"} {
		if strings.HasPrefix(s, kw) {
			return true
		}
	}
	return false
}

// jsArrowFollows reports whether `=>` appears in s before any `{` or `;` —
// i.e. whether the parameter list just closed belongs to an arrow function
// rather than to a call or a method body.
// The `=>` must belong to THIS paren, so only two things may follow the close:
// the arrow itself, or a return-type annotation leading to it. Scanning ahead
// for any `=>` before `{`/`;` was too loose — it read a parenthesised
// EXPRESSION followed by a chained call as a parameter list, so a wrapped
// `(items).map((i) => i)` bound `items`, and `register(Dropped).then(v => v)`
// bound `Dropped`. Both fold a non-parameter into the known set, the
// false-negative direction; the second additionally suppresses a genuine
// dropped-import warning.
func jsArrowFollows(s string) bool {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i >= len(s) {
		return false
	}
	if s[i] == '=' {
		return i+1 < len(s) && s[i+1] == '>'
	}
	// A return-type annotation abuts its `)` — `(a): Foo => a`. A ternary's
	// `:` arm is spaced — `cond ? (a) : b => b` — and accepting that bound `a`,
	// a parenthesised value expression rather than a parameter. Requiring the
	// colon to be the very next byte separates them.
	if i != 0 || s[0] != ':' {
		return false
	}
	for i++; i < len(s); i++ {
		switch s[i] {
		case '(', ')', '{', '}', ';', '[':
			return false // not a plain return-type annotation
		case '=':
			if i+1 < len(s) && s[i+1] == '>' {
				return true
			}
		}
	}
	return false
}

// jsConsumeParens advances *depth over s's parens and returns the index of the
// paren that balanced it (and true) when the list closes on this line.
func jsConsumeParens(s string, depth *int) (int, bool) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			*depth++
		case ')':
			*depth--
			if *depth <= 0 {
				return i, true
			}
		}
	}
	return -1, false
}

// jsOpenParamListLoose is jsParamListOpen's over-inclusive twin, for
// LocallyBoundNames. It drops the `:`-preceded exclusion (a type annotation's
// parameter names cost nothing in a set that only suppresses warnings) and does
// no type-block tracking. Precision belongs to JSParamNames; this one's job is
// reach. Returns the index of the `(` and whether a `function` keyword led it.
func jsOpenParamListLoose(s string) (int, bool) {
	if strings.Contains(s, "function") {
		if loc := jsFunctionOpen.FindStringIndex(s); loc != nil {
			return loc[1] - 1, true
		}
	}
	if i := strings.IndexByte(s, '('); i >= 0 {
		return i, false
	}
	return -1, false
}

// maxJSSigLines bounds how many lines one parameter list may span before
// JSParamNames abandons it. Chosen well above any real signature (the widest in
// the 400 KB TS corpus is single digits) and well below "the rest of the file",
// which is what an unbalanced `(` from an unstripped regex literal would
// otherwise consume. See the continuation branch for why that case is real.
const maxJSSigLines = 40

// jsKeywordsBeforeParams are the words that may sit directly before a genuine
// parameter list's `(`. Without this exemption the identifier-byte rejection in
// jsParamListOpen reads `async (url) => …` as a call to something named `async`
// and binds nothing — and `async` arrows are the dominant form in the TS/React
// code this extractor exists to serve.
// The value is whether that keyword's `(` may latch across lines. Only `async`
// and `await` genuinely lead a parameter list that can wrap (`async (\n url\n)
// => …`, which TestJSParamNamesKeywordLedArrows covers). The rest lead
// EXPRESSIONS — see jsParamListOpen's third result.
var jsKeywordsBeforeParams = map[string]bool{
	"async": true, "await": true,
	"return": false, "yield": false, "typeof": false, "void": false,
	"delete": false, "in": false, "of": false, "case": false,
	"do": false, "else": false, "new": false,
	// `export default (props) => …` is a real arrow, and a `function`
	// EXPRESSION may appear anywhere an argument may (`foo(function (a) {},
	// function (b) {})`), where the regex finds only the first.
	"default": true, "function": true,
}

// jsKeywordEndsAt reports whether the identifier ending at s[j] (inclusive) is
// one of jsKeywordsBeforeParams, and whether it may latch across lines.
// The third result is the keyword itself, so the caller can tell a `function`
// EXPRESSION (`foo(function (a) {…})`) from an arrow-leading keyword: its body
// opens with `{`, which jsArrowFollows rightly rejects, so it must be marked as
// a function opener rather than left to that test.
func jsKeywordEndsAt(s string, j int) (bool, bool, string) {
	if j < 0 {
		return false, false, ""
	}
	end := j + 1
	i := j
	for i >= 0 && isJSIdentByte(s[i]) {
		i--
	}
	word := s[i+1 : end]
	latch, ok := jsKeywordsBeforeParams[word]
	return ok, latch, word
}

// jsHeadIsUnfinished reports whether a stripped line ends mid-statement — its
// last meaningful byte is an operator or opener that requires a continuation.
// Used to decide whether a type-alias head carries into the next line, where
// "has no semicolon" is the wrong test: under `semi: false` a complete
// statement has none either.
func jsHeadIsUnfinished(s string) bool {
	t := strings.TrimRight(s, " \t\r")
	if t == "" {
		return false
	}
	switch t[len(t)-1] {
	case '=', '|', '&', ',', '(', '<', '+', '-', '?', ':', '.':
		return true
	}
	// A head that opens no body brace and does not terminate is unfinished even
	// when it ends on an identifier — prettier wraps a long extends clause, so
	// `export interface Props` / `  extends Base {` and `type Result = Success`
	// / `  | (...)` both leave one. Without this the body was scanned as
	// ordinary code and bound its type-only parameter names.
	if !strings.ContainsAny(t, "{;") {
		return true
	}
	// A conditional type's head ends on an IDENTIFIER (`type A = B extends C`)
	// with the `?`/`:` arms on following lines, so the trailing-operator test
	// alone lets it out of type position after one line. But a conditional type
	// written wholly on one line (`type R = A extends B ? C : D`) is FINISHED,
	// and treating it as open latched the extractor over the code after it.
	// The `?` arm is what distinguishes the two.
	return strings.Contains(t, "extends") && !strings.Contains(t, "?")
}

// jsPrecededByArrowOrTernary reports whether the byte at s[j] (the last
// non-space byte before a `(`) ends a fat arrow or is a ternary `?`.
func jsPrecededByArrowOrTernary(s string, j int) bool {
	if j < 0 {
		return false
	}
	if s[j] == '?' {
		return true
	}
	return s[j] == '>' && j > 0 && s[j-1] == '='
}

// jsFunctionParenAfter returns the index of the parameter list's `(` for the
// `function` keyword starting at kw, skipping a generic parameter clause whose
// constraint may itself contain parentheses. Returns -1 if none is on the line.
func jsFunctionParenAfter(s string, kw int) int {
	angle := 0
	for i := kw; i < len(s); i++ {
		switch s[i] {
		case '<':
			if i+1 < len(s) && s[i+1] != '=' && s[i+1] != ' ' {
				angle++
			}
		case '>':
			if angle > 0 && (i == 0 || s[i-1] != '=') {
				angle--
			}
		case '(':
			if angle == 0 {
				return i
			}
		}
	}
	return -1
}

// jsParenMatches returns, for each byte of s, the index of the `)` matching a
// `(` at that position — or -1 for every other position and for a `(` that does
// not close on this line. One left-to-right pass, so the opener loop can look a
// group's extent up instead of re-walking it per nesting level.
func jsParenMatches(s string) []int {
	m := make([]int, len(s))
	for i := range m {
		m[i] = -1
	}
	var stack []int
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			stack = append(stack, i)
		case ')':
			if n := len(stack); n > 0 {
				m[stack[n-1]] = i
				stack = stack[:n-1]
			}
		}
	}
	return m
}

// jsTypeDelta is jsBracketDelta plus generic `<…>`, which type syntax nests in
// and ()[]{}-counting cannot see. Used only inside a type position, where a `<`
// after an identifier is always a generic rather than a comparison.
func jsTypeDelta(s string) int {
	d := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			d++
		case ')', ']', '}':
			d--
		case '<':
			// A `<` ENDING the line still opens a generic — `type Reg = Record<`
			// is the wrapped-generic head. Requiring a following byte (to
			// exclude `<=`) silently excluded it, so the head opened no nesting
			// and its continuation lines were read as ordinary code.
			if i > 0 && isJSIdentByte(s[i-1]) && (i+1 >= len(s) || s[i+1] != '=') {
				d++
			}
		case '>':
			if i > 0 && s[i-1] != '=' {
				d--
			}
		}
	}
	return d
}

// jsTypeContinuationStart reports whether a line reads as the continuation of a
// type statement rather than as new code: it begins with an operator or an
// opener (`| A`, `? X`, `: never`, `(e: E) => void`, `string[]`) rather than
// with a keyword or an identifier-led statement (`const cb = …`).
func jsTypeContinuationStart(s string) bool {
	t := strings.Trim(s, " \t\r")
	if t == "" {
		return true // a blank line does not end a type statement
	}
	switch t[0] {
	case '|', '&', '?', ':', ',', '(', '[', '{', '<', '>', ')', ']', '}':
		return true
	}
	return strings.HasPrefix(t, "extends") || strings.HasPrefix(t, "infer") ||
		strings.HasPrefix(t, "keyof") || strings.HasPrefix(t, "readonly")
}

// jsAmbientOrOverload reports whether a line is a `declare function …;` or a
// bodyless TS overload signature (`export function fmt(c: number): string;`).
// Both are pure type positions — the parameter names bind nothing at runtime —
// and both are distinguished from a real declaration by ending at a `;` with no
// body brace on the line.
func jsAmbientOrOverload(s string) bool {
	if !strings.Contains(s, "function") {
		return false
	}
	t := strings.TrimRight(s, " \t\r")
	if !strings.HasSuffix(t, ";") || strings.ContainsAny(t, "{}") {
		return false
	}
	// `const f = function (a) { … };` is a value; it carries a body brace and is
	// already excluded above. What remains is a declaration head with no body.
	return jsFunctionOpen.MatchString(t)
}

// jsBodylessAfter reports whether the text following a function's `)` shows a
// declaration with no body — an optional return-type annotation and then a `;`,
// never a `{`. That is an ambient declaration or a TS overload signature, whose
// parameter names bind nothing at runtime.
func jsBodylessAfter(after string) bool {
	t := strings.TrimRight(after, " \t\r")
	return strings.HasSuffix(t, ";") && !strings.ContainsAny(t, "{}")
}

// jsParamsInText runs the single-line opener scan over an arbitrary span of
// stripped code, for recovering parameter lists nested inside a multi-line
// group that turned out not to be one. It deliberately does NOT track type
// positions or latch across anything — the text it is given is already one
// group's interior.
func jsParamsInText(s string) []string {
	// ALLOCATION BOUND ONLY — no fixture can kill a mutation of this line, and
	// it is not the panic fix. The panic came from jsParenMatches capping while
	// its caller did not, so table and string disagreed; that is fixed by there
	// being exactly one cap, in JSParamNames. This input is an accumulated
	// multi-line signature (up to maxJSSigLines lines) and so can still be large
	// on its own, and jsParenMatches allocates 8 bytes per input byte. Kept for
	// that, claimed as unkillable rather than annotated as a gap.
	s = capLine(s)
	var out []string
	out = append(out, jsBareArrowParams(s)...)
	match := jsParenMatches(s)
	lastGT := jsLastGenericClose(s)
	fnOpen := -1
	if strings.Contains(s, "function") {
		if loc := jsFunctionOpen.FindStringIndex(s); loc != nil {
			fnOpen = jsFunctionParenAfter(s, loc[0])
		}
	}
	for from := 0; from < len(s); {
		open, isFn, _ := jsParamListOpen(s, from, fnOpen, lastGT)
		if open < 0 {
			break
		}
		m := match[open]
		if m < 0 {
			break
		}
		before := len(out)
		out = appendJSParams(out, s[open+1:m], isFn, s[m+1:])
		if len(out) > before {
			from = m + 1
		} else {
			from = open + 1
		}
	}
	return out
}

// jsLastGenericClose returns the index of the last `>` that could close a
// generic — i.e. one not forming the arrow token `=>`. A plain LastIndexByte
// was not enough: on `compute(a.n<max).map((y) => y)` the only `>` is the
// arrow's, which `case '>'` correctly refuses to treat as a close, so the
// unspaced `<` opened a depth that could never close and hid every later `(`.
func jsLastGenericClose(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '>' && (i == 0 || s[i-1] != '=') {
			return i
		}
	}
	return -1
}

// jsBareArrowParams returns the parameters of unparenthesized single-argument
// arrows on a line (`cb => cb()`, `items.forEach(it => it())`). The opener scan
// cannot see these — there is no `(` at all — and LocallyBoundNames has covered
// the shape since it shipped, so only this extractor left #302's false positive
// open for the concise form.
//
// Hand-rolled rather than reJSArrowParamsBare: that regex is unanchored, so on
// a long line it backtracks from every identifier — 31 ms on the 1500-unit
// linearity corpus, against ~800 us for the rest of the extractor. This is one
// backward walk per `=>`.
//
// Precision comes from what precedes the identifier. A `)` means the
// parenthesized form, already handled by the opener scan. A `:` means a RETURN
// TYPE — `(a): Foo => a` would otherwise bind `Foo`, folding a type name into
// the resolvable set, which is the leak this file exists to avoid. A `.` means
// a member expression, not a binding.
func jsBareArrowParams(s string) []string {
	var out []string
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '=' || s[i+1] != '>' {
			continue
		}
		j := i - 1
		for j >= 0 && (s[j] == ' ' || s[j] == '\t') {
			j--
		}
		if j < 0 || !isJSIdentByte(s[j]) {
			continue
		}
		end := j + 1
		for j >= 0 && isJSIdentByte(s[j]) {
			j--
		}
		k := j
		for k >= 0 && (s[k] == ' ' || s[k] == '\t') {
			k--
		}
		if k >= 0 && (s[k] == ':' || s[k] == '.') {
			continue
		}
		out = append(out, s[j+1:end])
	}
	return out
}

// jsAssignLHS is assignLHS with bracket-depth awareness, for JS declarators.
// assignLHS splits at the FIRST `=` wherever it sits, so a destructuring
// pattern carrying a default truncated mid-pattern:
// `const { onSave = noop, onCancel } = props;` gave the LHS `"{ onSave "` and
// bound only `onSave`, leaving a bare `onCancel(…)` to be reported as a
// hallucination — a live false positive of the very class this file addresses,
// and destructuring-with-default is as ordinary a React props shape as the
// destructured callback prop.
//
// Only the `=` at depth 0 separates a declarator's target from its initialiser.
// Kept separate from assignLHS rather than changing it, because that helper is
// shared with the Python paths.
func jsAssignLHS(decl string) string {
	depth := 0
	for i := 0; i < len(decl); i++ {
		switch decl[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case '=':
			if depth != 0 {
				continue
			}
			// `==`, `===`, `=>` and `<=`/`>=`/`!=` are not assignment.
			if i+1 < len(decl) && (decl[i+1] == '=' || decl[i+1] == '>') {
				continue
			}
			if i > 0 && strings.IndexByte("=!<>+-*/%&|^", decl[i-1]) >= 0 {
				continue
			}
			return decl[:i]
		}
	}
	return ""
}

// jsGenericArgsClose reports whether the `<` at s[i] opens a type ARGUMENT list
// that actually closes — i.e. a `>` is reached over type-ish text only.
//
// The bare "is there a later `>` somewhere" test was too weak: in a declarator
// list an unspaced comparison latched the depth and suppressed all further
// top-level comma splitting, so `let i = 0, ok = i<n, more = x>y;` dropped
// `more` entirely and a bare call to it was then flagged — a false positive of
// the class this file exists to close. An assignment `=` inside the candidate
// span is the tell: a type argument list has no assignment in it, while
// `i<n, more = x>` does. `=>` is not an assignment and is allowed, since a
// function type may legitimately appear among type arguments.
func jsGenericArgsClose(s string, i int) bool {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '>':
			if s[j-1] != '=' {
				return true
			}
		case '=':
			if j+1 >= len(s) || s[j+1] != '>' {
				return false // an assignment: this was a comparison, not a generic
			}
			j++ // skip the arrow
		case '(', ')', '{', '}', ';':
			return false
		}
	}
	return false
}
