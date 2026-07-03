package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// PythonParser parses .py files. Imports and __all__ exports are extracted with
// deterministic regex (cheap and sufficient — line-oriented constructs). Functions
// and classes use a real tree-sitter AST via a pure-Go (CGO-free) runtime, so
// `async def`, methods, nested defs, and private/dunder helpers are all captured
// as first-class symbols. The previous regex pass matched only plain top-level
// `def` and dropped everything else into the refs bucket.
type PythonParser struct{}

func NewPythonParser() *PythonParser { return &PythonParser{} }

var (
	// import foo or import foo.bar
	pyImportRegex = regexp.MustCompile(`^import\s+([\w.]+)`)

	// from foo import bar (captures the module path)
	pyFromImportRegex = regexp.MustCompile(`^from\s+([\w.]+)\s+import\s+`)

	// __all__ = ["foo", "bar"] or ('foo', 'bar'); also __all__ += [...]
	pyAllRegex = regexp.MustCompile(`__all__\s*\+?=\s*[\[\(]([^\]\)]+)[\]\)]`)

	// individual quoted names inside __all__
	pyAllItemRegex = regexp.MustCompile(`["'](\w+)["']`)

	// An __all__ *assignment* (statement-leading, any indentation) — including an
	// empty `__all__ = []` that pyAllRegex (which requires list content) misses
	// and an annotated `__all__: list[str] = [...]`. Presence makes __all__
	// authoritative and suppresses the no-underscore fallback below. Requiring the
	// `=` avoids a docstring that merely *mentions* __all__ falsely suppressing the
	// fallback; allowing leading indentation keeps presence-detection consistent
	// with pyAllRegex's unanchored name extraction, so a conditionally-declared
	// `__all__` (e.g. inside `if TYPE_CHECKING:`) is not extracted-then-discarded.
	pyAllPresentRegex = regexp.MustCompile(`(?m)^\s*__all__\s*(?::[^=\n]*)?\+?=`)

	// module-level UPPER_CASE constant assignment, used only for the __all__-absent
	// fallback. Anchored at column 0 (no indent = module scope); captures NAME in
	// `NAME = ...` and annotated `NAME: T = ...`. The trailing [^=] rejects `==`
	// comparisons. Like the import/export regexes this is line-oriented and will
	// also match an assignment-shaped line inside a triple-quoted string — the same
	// cheap-and-deterministic tradeoff the rest of this parser accepts.
	pyConstRegex = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*)\s*(?::[^=\n]*)?=[^=]`)
	// pyTupleConstRegex matches a module-level tuple/parallel assignment of
	// uppercase constants (`MIN, MAX = 0, 100`). pyConstRegex — anchored to a
	// single name immediately before `=` — cannot see these, so both names would
	// otherwise be silently dropped from the no-__all__ fallback export list.
	pyTupleConstRegex = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*(?:\s*,\s*[A-Z][A-Z0-9_]*)+)\s*=[^=]`)
)

// pythonLang lazily loads and caches the tree-sitter Python grammar. The grammar
// is loaded once; the resulting *Language is safe for concurrent reads. A fresh
// Parser is created per Parse call because ts.Parser is not concurrency-safe.
var (
	pyLangOnce sync.Once
	pyLang     *ts.Language
)

func pythonLanguage() *ts.Language {
	pyLangOnce.Do(func() {
		// Recover so a grammar-decode panic doesn't propagate out of the first
		// Parse call. sync.Once marks itself done even on panic, so an unrecovered
		// panic here would also leave pyLang nil forever; recovering degrades to
		// the nil-language path (no AST symbols) instead, which is fail-safe.
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "runecho: Python grammar failed to load (%v); Python symbols disabled\n", r)
			}
		}()
		pyLang = grammars.PythonLanguage()
	})
	return pyLang
}

func (p *PythonParser) SupportsExtension(ext string) bool {
	return ext == ".py"
}

func (p *PythonParser) Parse(source string) (FileStructure, error) {
	// Normalize line endings: CRLF checkouts must parse identically to LF, and
	// per-symbol body hashes must not depend on line-ending style.
	source = strings.ReplaceAll(source, "\r\n", "\n")

	imports, exports, hasAll := pyImportsAndExports(source)
	functions, classes, hashes, lines := pySymbolsFromAST(source)

	// When __all__ is absent, a module's public surface is conventionally its
	// non-underscore top-level names (the rule `from m import *`, PEP 8, and
	// autodoc use). Fall back to that so no-__all__ modules stop under-reporting
	// exports relative to the Go/JS parsers. An explicit __all__ — even an empty
	// one — is a deliberate declaration and stays authoritative.
	if !hasAll {
		exports = pyFallbackExports(source, functions, classes)
	}

	sort.Strings(imports)
	sort.Strings(functions)
	sort.Strings(classes)
	sort.Strings(exports)

	// Dedupe after sorting (parity with the Go/JS parsers): a top-level name can
	// legitimately repeat across conditional def/class blocks.
	return FileStructure{
		Imports:      deduplicate(imports),
		Functions:    deduplicate(functions),
		Classes:      deduplicate(classes),
		Exports:      deduplicate(exports),
		SymbolHashes: hashes,
		SymbolLines:  lines,
	}, nil
}

// pyImportsAndExports extracts module imports (line regex) and __all__ exports
// (whole-source regex) — the line-oriented parts the AST pass does not own.
// hasAll reports whether the module declares __all__ at all, so the caller can
// distinguish "no declared export surface" (fall back to the no-underscore
// convention) from "deliberately exports nothing" (`__all__ = []`).
func pyImportsAndExports(source string) (imports, exports []string, hasAll bool) {
	importSet := make(map[string]bool)
	for _, line := range strings.Split(source, "\n") {
		if m := pyImportRegex.FindStringSubmatch(line); m != nil {
			// Dedupe on the full module path so distinct dotted imports
			// (import os.path AND import os) are both recorded.
			if !importSet[m[1]] {
				imports = append(imports, m[1])
				importSet[m[1]] = true
			}
			continue
		}
		if m := pyFromImportRegex.FindStringSubmatch(line); m != nil {
			if !importSet[m[1]] {
				imports = append(imports, m[1])
				importSet[m[1]] = true
			}
		}
	}

	// FindAll, not FindString: a module may both assign and extend __all__.
	for _, m := range pyAllRegex.FindAllStringSubmatch(source, -1) {
		for _, item := range pyAllItemRegex.FindAllStringSubmatch(m[1], -1) {
			exports = append(exports, item[1])
		}
	}
	return imports, exports, pyAllPresentRegex.MatchString(source)
}

// pyFallbackExports derives a module's public API when __all__ is absent, using
// the best-practice no-underscore convention: top-level (unqualified) functions
// and classes whose names don't start with "_", plus module-level UPPER_CASE
// constants. functions/classes come from the AST pass (already qualified, so
// nested defs and methods carry a "." and are excluded); constants are matched
// from source since the AST walk only records def/class nodes.
func pyFallbackExports(source string, functions, classes []string) []string {
	var exports []string
	for _, name := range functions {
		if isPyPublicTopLevel(name) {
			exports = append(exports, name)
		}
	}
	for _, name := range classes {
		if isPyPublicTopLevel(name) {
			exports = append(exports, name)
		}
	}
	for _, m := range pyConstRegex.FindAllStringSubmatch(source, -1) {
		exports = append(exports, m[1])
	}
	for _, m := range pyTupleConstRegex.FindAllStringSubmatch(source, -1) {
		for _, name := range strings.Split(m[1], ",") {
			exports = append(exports, strings.TrimSpace(name))
		}
	}
	return exports
}

// isPyPublicTopLevel reports whether a qualified AST name is a module-level public
// symbol: unqualified (no enclosing scope) and not underscore-prefixed.
func isPyPublicTopLevel(qualified string) bool {
	return !strings.Contains(qualified, ".") && !strings.HasPrefix(qualified, "_")
}

// pySymbolsFromAST walks the Python AST and returns every function and class
// definition. Methods and nested defs/classes are qualified by their enclosing
// scope (e.g. "Reader.fetch", "outer.inner") so identical leaf names in different
// scopes never collide. hashes carries a per-function body hash keyed
// "function:<qualified name>" for modified-symbol diffing; classes are not hashed
// (their changes surface through their members, avoiding double-reporting). lines
// carries each symbol's 1-based start line, keyed "kind:<qualified name>", for the
// repo map.
func pySymbolsFromAST(source string) (functions, classes []string, hashes map[string]string, lines map[string]int) {
	// The pure-Go tree-sitter runtime can panic on adversarial or malformed
	// input; a panic here would otherwise propagate through parseFile→Generate
	// and crash the indexer/MCP server. Recover and degrade to no AST symbols
	// (the same fail-safe path as a nil grammar) so one bad file can't take down
	// the process. Named returns are reset so a panic mid-walk can't leak a
	// partial, inconsistent symbol set.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "runecho: Python parse panicked (%v); AST symbols for this file disabled\n", r)
			functions, classes, hashes, lines = nil, nil, nil, nil
		}
	}()
	lang := pythonLanguage()
	if lang == nil {
		// Grammar unavailable (e.g. a grammar_subset build that omitted Python).
		// Degrade to no AST symbols rather than panicking; imports/exports still
		// come from the regex pass.
		return nil, nil, nil, nil
	}
	src := []byte(source)
	// Reject pathologically-nested input before the super-linear tree-sitter
	// parse can hang the process; degrade to no AST symbols (see maxParseNestDepth).
	if exceedsNestDepth(src) {
		fmt.Fprintf(os.Stderr, "runecho: Python source exceeds max nesting depth (%d); AST symbols for this file disabled\n", maxParseNestDepth)
		return nil, nil, nil, nil
	}
	tree, err := ts.NewParser(lang).Parse(src)
	if err != nil || tree == nil || tree.RootNode() == nil {
		return nil, nil, nil, nil
	}

	hashes = make(map[string]string)
	lines = make(map[string]int)

	// recordHash stores a function's body hash. If the qualified name already has
	// one (e.g. an @property getter/setter/deleter, or conditional def branches —
	// all of which share a name and collapse to one symbol), the hashes are
	// combined so a change in ANY variant flips the result. Last-write-wins would
	// silently hide edits to every variant but the last one in AST order.
	recordHash := func(key string, span []byte) {
		h := hashBytesHex(span)
		if existing, ok := hashes[key]; ok {
			h = hashBytesHex([]byte(existing + h))
		}
		hashes[key] = h
	}
	// recordLine anchors a symbol at its FIRST definition; later same-name
	// variants don't move the anchor.
	recordLine := func(key string, line int) {
		if _, ok := lines[key]; !ok {
			lines[key] = line
		}
	}

	// record handles a function/class definition node, attributing it to spanNode
	// for the hashed body and start line. spanNode differs from defNode only for a
	// decorated definition, where it is the decorator-inclusive wrapper so that a
	// decorator change (e.g. an edited @app.route path) is detected.
	var walk func(n *ts.Node, prefix string, depth int)
	record := func(defNode, spanNode *ts.Node, prefix string, depth int) {
		name := pyFieldText(defNode, "name", lang, src)
		if name == "" {
			return
		}
		full := qualify(prefix, name)
		switch defNode.Type(lang) {
		case "function_definition":
			functions = append(functions, full)
			recordHash("function:"+full, src[spanNode.StartByte():spanNode.EndByte()])
			recordLine("function:"+full, int(spanNode.StartPoint().Row)+1)
		case "class_definition":
			classes = append(classes, full)
			recordHash("class:"+full, src[spanNode.StartByte():spanNode.EndByte()])
			recordLine("class:"+full, int(spanNode.StartPoint().Row)+1)
		default:
			return
		}
		if body := defNode.ChildByFieldName("body", lang); body != nil {
			walk(body, full, depth+1)
		}
	}

	walk = func(n *ts.Node, prefix string, depth int) {
		// Bound recursion so a deeply-nested AST (Python nests via indentation, not
		// brackets, so the byte-level guard above doesn't catch it) can't overflow
		// the goroutine stack — a runtime throw the recover() above cannot catch.
		if depth > maxParseNestDepth {
			return
		}
		for i := 0; i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			switch c.Type(lang) {
			case "function_definition", "class_definition":
				record(c, c, prefix, depth)
			case "decorated_definition":
				// Hash/line span the whole decorated block; name comes from the
				// inner definition.
				if def := c.ChildByFieldName("definition", lang); def != nil {
					record(def, c, prefix, depth)
				} else {
					walk(c, prefix, depth+1)
				}
			default:
				// Recurse through wrappers (if/try/with blocks etc.) so
				// conditionally-defined symbols are still seen.
				walk(c, prefix, depth+1)
			}
		}
	}
	walk(tree.RootNode(), "", 0)

	if len(hashes) == 0 {
		hashes = nil
	}
	if len(lines) == 0 {
		lines = nil
	}
	return functions, classes, hashes, lines
}

func qualify(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func pyFieldText(n *ts.Node, field string, lang *ts.Language, src []byte) string {
	if f := n.ChildByFieldName(field, lang); f != nil {
		return f.Text(src)
	}
	return ""
}

// hashBytesHex returns the lowercase-hex SHA256 of b — same algorithm the IR
// uses for file hashes, so symbol-body hashes are comparable in kind.
func hashBytesHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
