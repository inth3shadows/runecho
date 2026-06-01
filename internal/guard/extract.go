package guard

import (
	"regexp"
	"strings"
)

// Lang identifies the source language for a file.
type Lang string

const (
	LangGo     Lang = "go"
	LangJS     Lang = "js"  // covers .js, .ts, .jsx, .tsx, .gs (GAS)
	LangPython Lang = "py"
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
	"console", "require", "import", "typeof", "await", "new", "return",
	"if", "for", "while", "switch", "function", "throw",
	"Object", "Array", "String", "Number", "Boolean", "JSON", "Math",
	"Promise", "Symbol", "Map", "Set", "WeakMap", "WeakSet",
	"parseInt", "parseFloat", "isNaN", "isFinite", "encodeURIComponent",
	"decodeURIComponent", "setTimeout", "setInterval", "clearTimeout",
	"clearInterval", "fetch", "Error", "TypeError", "RangeError",
	"undefined", "null", "true", "false",
)

var pyBuiltins = setOf(
	"print", "len", "range", "str", "int", "float", "bool",
	"list", "dict", "set", "tuple", "type", "isinstance", "issubclass",
	"super", "enumerate", "zip", "map", "filter", "open",
	"repr", "getattr", "setattr", "hasattr", "format",
	"sorted", "reversed", "sum", "min", "max", "abs",
	"any", "all", "next", "iter", "id", "hash", "dir",
	"vars", "callable", "input", "exit", "quit",
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
	for _, l := range lines {
		text := l.Text
		// Skip comment lines — they generate many false positives.
		if isCommentLine(lang, text) {
			continue
		}
		matches := reCallIdent.FindAllStringSubmatchIndex(text, -1)
		for _, idx := range matches {
			fullStart := idx[0]
			nameStart, nameEnd := idx[2], idx[3]
			name := text[nameStart:nameEnd]

			// Skip if preceded by '.' (qualified call)
			if fullStart > 0 && text[fullStart-1] == '.' {
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
