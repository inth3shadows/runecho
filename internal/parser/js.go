package parser

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// JSParser parses .js, .mjs, .cjs, .ts, .jsx, .tsx, .gs files. Functions, classes,
// imports, and exports all use a real tree-sitter AST via a pure-Go
// (CGO-free) runtime when the matching grammar is embedded in the build:
// functions/classes carry per-symbol start lines and function body hashes
// (matching the Python parser's FileStructure contract), while imports/
// exports are resolved structurally off the grammar's own node/field shapes
// (import_statement, export_statement, export_specifier, variable_declarator,
// …) rather than pattern-matching source text, so alias-vs-local-name
// confusion and TS `type`-only forms are no longer regex edge cases. CommonJS
// require(...) calls have no dedicated grammar node and stay regex-matched
// regardless of AST availability (see extractCJSRequires). When the grammar
// is absent (a build without the grammar_subset_* tags), or the reduced
// grammar's error recovery leaves a partial tree, this degrades to (or
// supplements with) the former line-oriented regex extraction — so symbol
// coverage never regresses.
//
// Altitude: like the Go parser, it captures top-level functions/classes plus
// class methods (qualified as Class.method); it does not descend into function
// bodies, so nested closures/callbacks are intentionally omitted.
//
// Best-effort: the shipped grammars are reduced "subset" grammars covering the
// common surface (declarations, classes/interfaces/enums, methods, arrow and
// function consts). Some advanced TypeScript syntax — notably a return-typed
// arrow const, `const f = (x: T): R => ...` — does not parse cleanly and yields
// fewer symbols. This matches the prior regex parser's gap (no regression); it
// is documented rather than silently dropped.
type JSParser struct{}

var (
	// Import patterns (ESM and CommonJS)
	// Matches: import ... from "path"
	importESMRegex = regexp.MustCompile(`import\s+(?:[\w\s{},*]*\s+from\s+)?['"]([^'"]+)['"]`)
	// Matches: require("path")
	importCJSRegex = regexp.MustCompile(`require\s*\(\s*['"]([^'"]+)['"]\s*\)`)

	// Function declarations
	// Matches: function name(...) or async function name(...)
	funcDeclRegex = regexp.MustCompile(`(?:^|\s)(?:async\s+)?function\s+(\w+)\s*\(`)
	// Matches: const/let/var name = function(...) or name = async function(...)
	funcExprRegex = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?function\s*\(`)
	// Matches: const/let/var name = (...) => or name = async (...) =>
	// The optional `(?:\s*:\s*[^=]+)?` tolerates a TS return-type annotation
	// between the parameter list and `=>` (`(x: T): R => ...`) — `[^=]` can
	// never consume into the arrow's own `=`, so it can't overrun into `=>`.
	arrowFuncRegex = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s+)?(?:\([^)]*\)|[\w]+)\s*(?:\s*:\s*[^=]+)?\s*=>`)

	// Class declarations
	// Matches: class Name or export class Name or export default class Name
	classDeclRegex = regexp.MustCompile(`(?:^|\s)(?:export\s+(?:default\s+)?)?class\s+(\w+)`)

	// Export patterns
	// Matches: export { name1, name2 } and the TS type-only re-export
	// export type { name1, name2 } — the `type` keyword sits before the brace
	// (idiomatic under isolatedModules / verbatimModuleSyntax). The optional
	// group leaves the value form untouched.
	exportNamedRegex = regexp.MustCompile(`export\s+(?:type\s+)?\{([^}]+)\}`)
	// Matches: export function/class/type/interface/enum name. const/let/var
	// are handled separately by exportMultiDeclRegex below, because a single
	// declaration can bind more than one name (`export const A = 1, B = 2;`).
	// type/interface/enum land in Exports the same way `export class` does — the
	// AST records them in Classes, this regex records the exported name (so a TS
	// `export interface Shape {}` is enumerable as both a class and an export).
	exportDeclRegex = regexp.MustCompile(`export\s+(?:function|class|async\s+function|type|interface|enum)\s+(\w+)`)
	// Matches: export const/let/var NAME[: Type][ = value][, NAME2 ...];
	// Captures the whole declarator list up to the statement terminator so
	// multi-name declarations can be split on TOP-LEVEL commas only (see
	// splitTopLevelDeclNames) — a naive split on every comma would shatter an
	// initializer like `f(1, 2)` into a phantom declarator name.
	exportMultiDeclRegex = regexp.MustCompile(`export\s+(?:const|let|var)\s+([^;\n]+)`)
	// Matches the declarator name at the start of one comma-separated segment
	// of a declarator list, stopping at the first non-identifier character
	// (the `:` of a type annotation or the `=` of an initializer).
	declNameRegex = regexp.MustCompile(`^\w+`)
	// Matches the binding list of a destructured object export:
	// export const { foo, bar } = x. The trailing `=` distinguishes a
	// destructuring assignment from an object type; the single-\w+ decl regexes
	// above don't capture these bindings, and splitTopLevelDeclNames yields
	// nothing for a `{`/`[`-leading segment, so the two paths never double-count.
	exportObjDestructureRegex = regexp.MustCompile(`export\s+(?:const|let|var)\s*\{([^}]*)\}\s*=`)
	// Matches the binding list of a destructured array export:
	// export const [ a, b ] = y.
	exportArrDestructureRegex = regexp.MustCompile(`export\s+(?:const|let|var)\s*\[([^\]]*)\]\s*=`)
	// Matches: export * as ns from './m' — the namespace re-export binds `ns`.
	exportStarAsRegex = regexp.MustCompile(`export\s+\*\s+as\s+(\w+)`)
	// Matches: export * from './m' — the bare form, with no `as` clause. Requires
	// "from" to follow "*" with only whitespace between, so it never matches the
	// namespace form above (which has "as ns" in between).
	exportStarBareRegex = regexp.MustCompile(`export\s+\*\s+from\s+['"]([^'"]+)['"]`)
	// Matches: export default function Foo / export default [abstract] class Foo /
	// export default ident. Three capture groups — first non-empty wins; keywords
	// (function/class/async) in group 3 are discarded so anonymous defaults don't
	// pollute Exports. The `abstract` modifier is consumed before `class` so the
	// class NAME (not "abstract") is captured for `export default abstract class`.
	exportDefaultRegex = regexp.MustCompile(`export\s+default\s+(?:(?:async\s+)?function\s+(\w+)|(?:abstract\s+)?class\s+(\w+)|(\w+))`)
)

// NewJSParser creates a new JavaScript/TypeScript parser.
func NewJSParser() *JSParser {
	return &JSParser{}
}

// SupportsExtension returns true for .js, .mjs, .cjs, .ts, .jsx, .tsx, .gs
// files. .mjs/.cjs are plain JS syntax (ESM/CJS module-system markers, not a
// grammar difference) — they fall through ParseExt's default case to the same
// JS grammar as .js.
func (p *JSParser) SupportsExtension(ext string) bool {
	switch ext {
	case ".js", ".mjs", ".cjs", ".ts", ".jsx", ".tsx", ".gs":
		return true
	default:
		return false
	}
}

// Parse satisfies the Parser interface. Without the file extension it cannot
// pick the most specific grammar, so it assumes JavaScript (the JS grammar also
// parses JSX). The generator calls ParseExt instead (via the ExtAwareParser
// interface), which selects typescript/tsx for .ts/.tsx.
func (p *JSParser) Parse(source string) (FileStructure, error) {
	return p.parse(source, "")
}

// ParseExt is the extension-aware entry point (see ExtAwareParser). ext selects
// the tree-sitter grammar: .ts → typescript, .tsx → tsx, everything else → js.
func (p *JSParser) ParseExt(source, ext string) (FileStructure, error) {
	return p.parse(source, ext)
}

func (p *JSParser) parse(source, ext string) (FileStructure, error) {
	// Normalize line endings so spans/hashes are independent of CRLF vs LF
	// (parity with the Python parser).
	source = strings.ReplaceAll(source, "\r\n", "\n")

	// Run the regex fallbacks on comment-stripped source so commented-out
	// statements are ignored; the AST paths below parse the raw `source`
	// directly (tree-sitter handles comments itself).
	noComments := removeComments(source)

	// Functions, classes, imports, exports: AST when the grammar is
	// available, regex otherwise.
	var (
		functions, classes, imports, exports, wildcardReexports []string
		hashes                                                  map[string]string
		lines                                                   map[string]int
		fallbackRan                                             bool
	)
	if lang := jsLanguageFor(ext); lang != nil {
		var hasError bool
		functions, classes, hashes, lines, hasError = jsSymbolsFromAST(source, lang)
		fallbackRan = hasError
		if hasError {
			// The reduced grammar failed to cleanly parse at least part of this
			// file (e.g. a typed arrow parameter/return type) — error recovery
			// can corrupt or drop sibling top-level declarations from the tree
			// entirely, not just the offending statement. Supplement (union,
			// not replace) with the regex fallback so a real top-level symbol
			// is never silently missing from the known set — the accepted
			// trade-off is picking up an occasional nested closure the AST
			// path deliberately excludes, which only widens the known set
			// rather than narrowing it.
			functions = append(functions, extractFunctions(noComments)...)
			classes = append(classes, extractClasses(noComments)...)
		}

		var ieHasError bool
		imports, exports, wildcardReexports, ieHasError = jsImportsExportsFromAST(source, lang)
		if ieHasError {
			// Same posture as the functions/classes fallback above: supplement,
			// don't replace, so a partially-recovered tree never loses a real
			// import/export that plain regex matching would still have found.
			imports = append(imports, extractImports(noComments)...)
			exports = append(exports, extractExports(noComments)...)
			wildcardReexports = append(wildcardReexports, extractWildcardReexports(noComments)...)
		}
		// require(...) calls have no dedicated grammar node (they're an
		// ordinary call_expression that can appear anywhere, including inside
		// function bodies the declaration-level AST walk doesn't descend
		// into) — always union in the regex-matched requires.
		imports = append(imports, extractCJSRequires(noComments)...)
	} else {
		// No grammar embedded in this build — degrade to the former
		// line-oriented regex extraction entirely.
		functions = extractFunctions(noComments)
		classes = extractClasses(noComments)
		imports = extractImports(noComments)
		exports = extractExports(noComments)
		wildcardReexports = extractWildcardReexports(noComments)
		fallbackRan = true
	}

	// Give regex-fallback symbols a start line so locate can resolve them (the
	// AST path already recorded lines for everything it saw; a typed arrow const
	// it could not parse arrives here name-only). Only fills gaps — an
	// AST-recorded line always wins — and only for names actually in the symbol
	// set, so a stray fallback match never plants an orphan line entry.
	if fallbackRan {
		fLines, cLines := fallbackSymbolLines(source)
		if lines == nil {
			lines = make(map[string]int, len(fLines)+len(cLines))
		}
		mergeFallbackLines(lines, "function:", functions, fLines)
		mergeFallbackLines(lines, "class:", classes, cLines)
	}

	sort.Strings(imports)
	sort.Strings(functions)
	sort.Strings(classes)
	sort.Strings(exports)
	sort.Strings(wildcardReexports)

	return FileStructure{
		Imports:           deduplicate(imports),
		Functions:         deduplicate(functions),
		Classes:           deduplicate(classes),
		Exports:           deduplicate(exports),
		WildcardReexports: deduplicate(wildcardReexports),
		SymbolHashes:      hashes,
		SymbolLines:       lines,
	}, nil
}

// Grammar caches: each grammar is loaded once and the *Language is safe for
// concurrent reads (a fresh ts.Parser is created per Parse since it is not
// concurrency-safe). The accessors return nil when the corresponding
// grammar_subset_* blob is not embedded in the build.
var (
	jsLangOnce, tsLangOnce, tsxLangOnce sync.Once
	jsLang, tsLang, tsxLang             *ts.Language
)

// jsLanguageFor returns the tree-sitter grammar for the given file extension,
// or nil if that grammar is not embedded in this build.
func jsLanguageFor(ext string) *ts.Language {
	switch ext {
	case ".ts":
		return cachedLang(&tsLangOnce, &tsLang, "typescript", grammars.TypescriptLanguage)
	case ".tsx":
		return cachedLang(&tsxLangOnce, &tsxLang, "tsx", grammars.TsxLanguage)
	default: // .js, .mjs, .cjs, .jsx, .gs, "" — the JS grammar also parses JSX.
		return cachedLang(&jsLangOnce, &jsLang, "javascript", grammars.JavascriptLanguage)
	}
}

// cachedLang lazily loads a grammar under once, recovering from a decode panic
// so a bad blob degrades to nil (no AST symbols) rather than crashing the first
// Parse call — mirrors the Python parser's grammar-load guard.
func cachedLang(once *sync.Once, dst **ts.Language, name string, load func() *ts.Language) *ts.Language {
	once.Do(func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "runecho: %s grammar failed to load (%v); %s symbols disabled\n", name, r, name)
			}
		}()
		*dst = load()
	})
	return *dst
}

// jsSymbolsFromAST walks the JS/TS AST and returns every function and class
// definition. Methods/nested defs are qualified by their enclosing scope (e.g.
// "Widget.doThing"), matching the Python and Go parsers, so identical leaf names
// in different scopes never collide. Functions/methods carry a body hash keyed
// "function:<qualified name>" for modified-symbol diffing; classes, interfaces,
// enums, and type aliases are located (start line) but not hashed (their changes
// surface through their members).
func jsSymbolsFromAST(source string, lang *ts.Language) (functions, classes []string, hashes map[string]string, lines map[string]int, hasError bool) {
	// The pure-Go tree-sitter runtime can panic on adversarial or malformed
	// input; a panic here would otherwise propagate through parseFile→Generate
	// and crash the indexer/MCP server. Recover and degrade to no AST symbols
	// (the same fail-safe path as a nil grammar) so one bad file can't take down
	// the process. Named returns are reset so a panic mid-walk can't leak a
	// partial, inconsistent symbol set.
	// hasError=true tells the caller to supplement with the regex fallback. Every
	// give-up path below sets it, because "we produced no AST symbols" is exactly
	// when the coverage-never-regresses fallback must run — returning false here
	// would leave a file that panicked, over-nested, or failed to parse with zero
	// symbols AND no fallback (the silent-miss bug the fallback exists to prevent).
	// The fallback is a linear regex scan, so it is safe even on the nested input
	// the nest guard rejects, and it only ever unions symbols in.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "runecho: JS/TS parse panicked (%v); AST symbols for this file disabled\n", r)
			functions, classes, hashes, lines, hasError = nil, nil, nil, nil, true
		}
	}()
	src := []byte(source)
	// Reject pathologically-nested input before the super-linear tree-sitter
	// parse can hang the process; degrade to no AST symbols (see maxParseNestDepth).
	if exceedsNestDepth(src) {
		fmt.Fprintf(os.Stderr, "runecho: JS/TS source exceeds max nesting depth (%d); AST symbols for this file disabled\n", maxParseNestDepth)
		return nil, nil, nil, nil, true
	}
	tree, err := ts.NewParser(lang).Parse(src)
	if err != nil || tree == nil || tree.RootNode() == nil {
		return nil, nil, nil, nil, true
	}
	// The reduced grammar can't parse some declarator shapes — notably a typed
	// arrow parameter or return type (`const f = (x: T): R => ...`) — and error
	// recovery can cascade far enough to corrupt or drop sibling top-level
	// declarations entirely (confirmed by direct AST inspection: a single
	// broken statement can erase a later, otherwise clean function from the
	// tree). HasError() flags this so the caller can supplement with the
	// regex fallback over the whole file rather than trusting a partial walk.
	hasError = tree.RootNode().HasError()

	hashes = make(map[string]string)
	lines = make(map[string]int)

	recordHash := func(key string, span []byte) {
		h := hashBytesHex(span)
		if existing, ok := hashes[key]; ok {
			h = hashBytesHex([]byte(existing + h))
		}
		hashes[key] = h
	}
	recordLine := func(key string, line int) {
		if _, ok := lines[key]; !ok {
			lines[key] = line
		}
	}
	recordFunc := func(full string, span *ts.Node) {
		functions = append(functions, full)
		recordHash("function:"+full, src[span.StartByte():span.EndByte()])
		recordLine("function:"+full, int(span.StartPoint().Row)+1)
	}
	recordClass := func(full string, node *ts.Node) {
		classes = append(classes, full)
		recordHash("class:"+full, src[node.StartByte():node.EndByte()])
		recordLine("class:"+full, int(node.StartPoint().Row)+1)
	}
	var walk func(n *ts.Node, prefix string, depth int)
	walk = func(n *ts.Node, prefix string, depth int) {
		// Bound recursion so a deeply-nested AST can't overflow the goroutine
		// stack (a runtime throw the recover() above cannot catch). The nesting
		// guard above already caps bracket depth, but this is the direct stack
		// backstop.
		if depth > maxParseNestDepth {
			return
		}
		for i := 0; i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			switch c.Type(lang) {
			case "function_declaration", "generator_function_declaration",
				"method_definition", "method_signature", "abstract_method_signature":
				// A named function/method (incl. interface method_signature and
				// `abstract foo(): void` abstract_method_signature). We do NOT recurse
				// its body: like the Go parser (and unlike Python), JS/TS symbols are
				// top-level decls plus class methods — capturing nested closures/
				// callbacks would just add orientation noise. Bare function_expressions
				// (e.g. a named callback `setTimeout(function tick(){})`) are
				// intentionally NOT a case here; only those bound to a variable (below)
				// are captured.
				name := fieldText(c, "name", lang, src)
				if name == "" {
					continue
				}
				recordFunc(qualify(prefix, name), c)

			case "class_declaration", "abstract_class_declaration",
				"interface_declaration", "enum_declaration", "type_alias_declaration",
				"internal_module", "module":
				// Class-like containers: classes, interfaces, enums, type aliases, and
				// TS namespaces/modules (`namespace X {}` parses as internal_module,
				// `module X {}` as module). Recorded as a class, hashed over its full
				// span (a member change flips the hash), and descended with the
				// qualified prefix so members become X.member — without this,
				// namespace members would escape qualification and collide with
				// identically-named top-level symbols.
				name := fieldText(c, "name", lang, src)
				if name == "" {
					continue
				}
				full := qualify(prefix, name)
				recordClass(full, c)
				walk(c, full, depth+1) // descend into the body so methods become Class.method

			case "variable_declarator":
				// `const name = () => ...` / `= function(){}` / `= function*(){}`:
				// attribute the function to the bound variable name, spanning the
				// function value so a body change flips the hash. Body is not
				// recursed (top-level altitude — see the function case above).
				//
				// Only a plain identifier binds a value to a name. A destructuring
				// pattern (`const {a} = f()`, `const [x] = g()`) has an
				// object_pattern/array_pattern in the name field; its text (`{ a }`)
				// is not a symbol, so it must not be recorded — recurse instead.
				nameNode := c.ChildByFieldName("name", lang)
				if nameNode != nil && nameNode.Type(lang) == "identifier" {
					name := nodeText(nameNode, lang, src)
					if fn := childOfType(c, lang, "arrow_function", "function_expression", "generator_function"); fn != nil {
						recordFunc(qualify(prefix, name), fn)
						continue
					}
					// `const X = class {...}` / `= class Named {...}`: record the
					// class under the bound name and descend for its methods.
					if cls := childOfType(c, lang, "class", "class_expression"); cls != nil {
						full := qualify(prefix, name)
						recordClass(full, cls)
						walk(cls, full, depth+1)
						continue
					}
				}
				walk(c, prefix, depth+1)

			default:
				// Recurse through wrappers (export_statement, lexical_declaration,
				// class_body, statement_block, and ERROR-recovery nodes) so
				// declarations nested inside them are still found.
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
	return functions, classes, hashes, lines, hasError
}

// fieldText returns the text of n's named child in the given field, or ""
// if the field is absent. A string-literal field (an import/export source,
// or the rare string-form module export name in `export { "x" as y }`) is
// unquoted — callers never see the surrounding quote characters. Package
// level (not a jsSymbolsFromAST closure) so jsImportsExportsFromAST can
// share it without duplicating the field-lookup logic.
func fieldText(n *ts.Node, field string, lang *ts.Language, src []byte) string {
	return nodeText(n.ChildByFieldName(field, lang), lang, src)
}

// childOfType returns the first named child of n whose type is in types, or
// nil. Package level for the same reason as fieldText.
func childOfType(n *ts.Node, lang *ts.Language, types ...string) *ts.Node {
	for i := 0; i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		for _, want := range types {
			if c.Type(lang) == want {
				return c
			}
		}
	}
	return nil
}

// nodeText returns n's source text, unquoting it if n is a string-literal
// node (grammar type "string" — a single-quoted, double-quoted, or
// backtick-delimited span). Returns "" for a nil node so callers can chain
// it straight off ChildByFieldName/childOfType without a separate nil check.
func nodeText(n *ts.Node, lang *ts.Language, src []byte) string {
	if n == nil {
		return ""
	}
	txt := n.Text(src)
	if n.Type(lang) == "string" && len(txt) >= 2 {
		first, last := txt[0], txt[len(txt)-1]
		if (first == '\'' || first == '"' || first == '`') && first == last {
			return txt[1 : len(txt)-1]
		}
	}
	return txt
}

// jsImportsExportsFromAST walks the JS/TS AST and returns this file's import
// specifiers (module paths — FileStructure.Imports is a list of paths, not
// bound names), exported names, and bare wildcard re-export specifiers. It
// mirrors jsSymbolsFromAST's structure (same panic/nest-depth guards, same
// hasError contract) but walks import_statement/export_statement nodes
// directly instead of extracting functions/classes, resolving alias vs.
// local name and TS `type`-only forms off the grammar's own fields rather
// than regex. require(...) calls have no dedicated grammar node — they stay
// regex-matched via extractCJSRequires regardless of AST availability (see
// p.parse), since a declaration-level walk doesn't descend into arbitrary
// call expressions/function bodies where require() commonly appears.
func jsImportsExportsFromAST(source string, lang *ts.Language) (imports, exports, wildcardReexports []string, hasError bool) {
	// Same fail-safe posture as jsSymbolsFromAST: a panic degrades to no AST
	// imports/exports rather than crashing the indexer/MCP server.
	// Same hasError contract as jsSymbolsFromAST: every give-up path sets it so the
	// caller runs the regex fallback rather than silently dropping every import/
	// export in a file that panicked, over-nested, or failed to parse.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "runecho: JS/TS import/export parse panicked (%v); AST imports/exports for this file disabled\n", r)
			imports, exports, wildcardReexports, hasError = nil, nil, nil, true
		}
	}()
	src := []byte(source)
	if exceedsNestDepth(src) {
		fmt.Fprintf(os.Stderr, "runecho: JS/TS source exceeds max nesting depth (%d); AST imports/exports for this file disabled\n", maxParseNestDepth)
		return nil, nil, nil, true
	}
	tree, err := ts.NewParser(lang).Parse(src)
	if err != nil || tree == nil || tree.RootNode() == nil {
		return nil, nil, nil, true
	}
	// Same rationale as jsSymbolsFromAST: error-recovery on a partially
	// unparseable file can drop sibling statements from the tree, so the
	// caller supplements with the regex fallback rather than trusting a
	// partial walk.
	hasError = tree.RootNode().HasError()

	var walk func(n *ts.Node, depth int)
	walk = func(n *ts.Node, depth int) {
		if depth > maxParseNestDepth {
			return
		}
		for i := 0; i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			switch c.Type(lang) {
			case "import_statement":
				collectImportSource(c, lang, src, &imports)
			case "export_statement":
				collectExportStatement(c, lang, src, &exports, &wildcardReexports)
				// export_statement is also a container — e.g. `export
				// namespace NS { export const X = 1; }` nests another
				// export_statement inside its declaration's body — so keep
				// descending into it like any other wrapper node.
				walk(c, depth+1)
			default:
				// Recurse through every other wrapper (program, statement_block,
				// class_body, internal_module, ERROR-recovery nodes, …) so
				// import/export statements nested inside them are still found.
				walk(c, depth+1)
			}
		}
	}
	walk(tree.RootNode(), 0)

	return imports, exports, wildcardReexports, hasError
}

// collectImportSource extracts an import_statement's module specifier into
// *imports. Covers `import ... from '...'`, the bare side-effect form
// `import '...'`, and the TS `import x = require('...')` form (whose source
// lives on the nested import_require_clause — a visible grammar rule, so its
// own "source" field isn't promoted up to import_statement the way a hidden
// rule's fields are).
func collectImportSource(n *ts.Node, lang *ts.Language, src []byte, imports *[]string) {
	if source := fieldText(n, "source", lang, src); source != "" {
		*imports = append(*imports, source)
		return
	}
	if req := childOfType(n, lang, "import_require_clause"); req != nil {
		if source := fieldText(req, "source", lang, src); source != "" {
			*imports = append(*imports, source)
		}
	}
}

// collectExportStatement extracts one export_statement node's contribution
// to *exports/*wildcardReexports. An export_statement takes one of a handful
// of shapes distinguished by which fields/children are present:
//
//   - "declaration" field: `export function|class|interface|enum|type|const|
//     let|var ...` (with or without a leading `default`) — see
//     collectExportedDeclNames.
//   - "value" field: `export default <expression>` where the expression
//     isn't itself a declaration (an identifier, or an anonymous/named
//     function or class expression) — see collectExportDefaultValueName.
//   - a namespace_export child: `export * as ns [from '...']` binds ns.
//   - an export_clause child: `export {a, b as c} [from '...']` and the TS
//     `export type {...}` form — both resolved via export_specifier's
//     name/alias fields, covering local exports and named re-exports in one
//     path.
//   - neither of the above, but a "source" field directly on this node
//     (promoted up from the hidden _from_clause rule): the bare wildcard
//     re-export `export * from '...'` — its names aren't enumerable from
//     this file's text alone (see extractWildcardReexports).
//
// TS `export = expr` and `export as namespace X` match none of these and are
// intentionally left as a no-op (out of scope — not a named export/import).
func collectExportStatement(n *ts.Node, lang *ts.Language, src []byte, exports, wildcardReexports *[]string) {
	if decl := n.ChildByFieldName("declaration", lang); decl != nil {
		collectExportedDeclNames(decl, lang, src, exports)
		return
	}
	if val := n.ChildByFieldName("value", lang); val != nil {
		collectExportDefaultValueName(val, lang, src, exports)
		return
	}
	if ns := childOfType(n, lang, "namespace_export"); ns != nil {
		// namespace_export's bound name isn't itself a Field() in the
		// grammar (only the Seq around it is), so it's just the sole named
		// child rather than reachable via ChildByFieldName.
		if name := nodeText(childOfType(ns, lang, "identifier", "string"), lang, src); name != "" {
			*exports = append(*exports, name)
		}
		return
	}
	if clause := childOfType(n, lang, "export_clause"); clause != nil {
		for i := 0; i < clause.NamedChildCount(); i++ {
			spec := clause.NamedChild(i)
			if spec.Type(lang) != "export_specifier" {
				continue
			}
			// The alias is what consumers actually import; only fall back to
			// the local name when there's no `as` clause.
			name := fieldText(spec, "alias", lang, src)
			if name == "" {
				name = fieldText(spec, "name", lang, src)
			}
			if name != "" {
				*exports = append(*exports, name)
			}
		}
		return
	}
	if source := fieldText(n, "source", lang, src); source != "" {
		*wildcardReexports = append(*wildcardReexports, source)
	}
}

// collectExportedDeclNames extracts the bound name(s) of an exported
// declaration into *exports. Most declaration shapes (function, class,
// interface, enum, type alias, TS namespace/module, …) carry a single "name"
// field directly, matching jsSymbolsFromAST's class-like case. const/let/var
// declarations are the exception — a single statement can bind more than one
// name (`export const A = 1, B = 2`) or a destructuring pattern
// (`export const {a, b} = x`), so those recurse through each
// variable_declarator's real pattern structure via collectPatternNames
// instead of a single name field.
func collectExportedDeclNames(decl *ts.Node, lang *ts.Language, src []byte, exports *[]string) {
	switch decl.Type(lang) {
	case "lexical_declaration", "variable_declaration":
		for i := 0; i < decl.NamedChildCount(); i++ {
			d := decl.NamedChild(i)
			if d.Type(lang) != "variable_declarator" {
				continue
			}
			collectPatternNames(d.ChildByFieldName("name", lang), lang, src, exports)
		}
	default:
		// function_declaration, generator_function_declaration,
		// class_declaration, abstract_class_declaration,
		// interface_declaration, enum_declaration, type_alias_declaration,
		// function_signature, internal_module, module — all carry a plain
		// "name" field. Anything else (e.g. TS import_alias/
		// ambient_declaration, which don't) yields "" and is a safe no-op.
		if name := fieldText(decl, "name", lang, src); name != "" {
			*exports = append(*exports, name)
		}
	}
}

// collectExportDefaultValueName extracts a name from `export default
// <expression>` when the expression isn't itself a declaration (those go
// through collectExportedDeclNames instead — e.g. `export default class Foo
// {}` parses as a class_declaration in the "declaration" field, not here).
// An anonymous function/class expression, or any other expression form
// (call, member, arrow, literal, parenthesized, …), has no exportable name
// and is intentionally skipped — matching exportDefaultRegex's existing
// treatment of anonymous defaults.
func collectExportDefaultValueName(val *ts.Node, lang *ts.Language, src []byte, exports *[]string) {
	switch val.Type(lang) {
	case "identifier":
		*exports = append(*exports, val.Text(src))
	case "function_expression", "generator_function", "class", "class_expression":
		if name := fieldText(val, "name", lang, src); name != "" {
			*exports = append(*exports, name)
		}
	}
}

// collectPatternNames extracts the bound identifier(s) from a
// variable_declarator's "name" node into *exports — either a plain
// identifier, or an object/array destructuring pattern (nestable to
// arbitrary depth). Mirrors destructuredNames' semantics for the regex
// fallback (renamed binding keeps the bound name, a default value resolves
// to its left-hand target, a rest element binds its trailing identifier) but
// walks real pattern node boundaries instead of comma-splitting text, so an
// initializer's own internal commas/braces can never be mistaken for another
// binding.
func collectPatternNames(n *ts.Node, lang *ts.Language, src []byte, exports *[]string) {
	if n == nil {
		return
	}
	switch n.Type(lang) {
	case "identifier", "shorthand_property_identifier_pattern":
		*exports = append(*exports, n.Text(src))

	case "object_pattern":
		for i := 0; i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			switch c.Type(lang) {
			case "shorthand_property_identifier_pattern", "identifier":
				// `{ foo }` — no rename, no default.
				*exports = append(*exports, c.Text(src))
			case "pair_pattern":
				// `{ key: value }` — the bound name is the value side, not
				// the source key (matches destructuredNames' rename handling).
				collectPatternNames(c.ChildByFieldName("value", lang), lang, src, exports)
			case "object_assignment_pattern":
				// `{ foo = default }` — a shorthand binding with a default;
				// the bound name is the left side.
				collectPatternNames(c.ChildByFieldName("left", lang), lang, src, exports)
			case "rest_pattern":
				// `{ ...rest }` binds the trailing identifier.
				collectPatternNames(restPatternTarget(c), lang, src, exports)
			}
		}

	case "array_pattern":
		for i := 0; i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			switch c.Type(lang) {
			case "assignment_pattern":
				// `[ a = default ]` — the bound name is the left side.
				collectPatternNames(c.ChildByFieldName("left", lang), lang, src, exports)
			case "rest_pattern":
				// `[ ...rest ]` binds the trailing identifier.
				collectPatternNames(restPatternTarget(c), lang, src, exports)
			default:
				// A plain element (identifier) or a nested destructuring
				// pattern (`[[a, b], c]`) — recurse straight back through
				// the same switch.
				collectPatternNames(c, lang, src, exports)
			}
		}

	case "assignment_pattern":
		// Reached when a top-level variable_declarator name is itself an
		// assignment_pattern (not expected from real source, but handled for
		// robustness/symmetry with the nested cases above).
		collectPatternNames(n.ChildByFieldName("left", lang), lang, src, exports)
	}
}

// restPatternTarget returns a rest_pattern's bound identifier — its sole
// named child (`...rest` / `...others`), since rest_pattern's grammar
// production isn't Field()-wrapped.
func restPatternTarget(n *ts.Node) *ts.Node {
	if n.NamedChildCount() == 0 {
		return nil
	}
	return n.NamedChild(n.NamedChildCount() - 1)
}

// Parse extracts top-level structure from JavaScript/TypeScript source.
// removeComments strips single-line and multi-line comments.
// Multi-line /* … */ comments are removed via regex. Single-line // comments
// are stripped per-line with string-literal awareness so that URLs inside
// import strings (e.g. import 'http://example.com') are preserved correctly.
// blockCommentRegex matches a /* ... */ block comment (non-greedy, spans lines).
var blockCommentRegex = regexp.MustCompile(`/\*[\s\S]*?\*/`)

func removeComments(source string) string {
	source = blockCommentRegex.ReplaceAllString(source, "")

	lines := strings.Split(source, "\n")
	for i, line := range lines {
		lines[i] = stripLineComment(line)
	}
	return strings.Join(lines, "\n")
}

// maskCommentsLineFaithful blanks comment content but preserves every newline,
// so byte offsets into the result map to the same 1-based line numbers as the
// original source. removeComments deletes block comments outright, which shifts
// every line after a multi-line comment — unusable for computing a symbol's
// start line. Block comments are replaced by just their newlines (dropping the
// intra-line bytes is harmless: line numbers depend only on newline counts, and
// the regex fallback matches against this same masked string), and line
// comments are stripped per line.
func maskCommentsLineFaithful(source string) string {
	masked := blockCommentRegex.ReplaceAllStringFunc(source, func(m string) string {
		return strings.Repeat("\n", strings.Count(m, "\n"))
	})
	lines := strings.Split(masked, "\n")
	for i, line := range lines {
		lines[i] = stripLineComment(line)
	}
	return strings.Join(lines, "\n")
}

// fallbackSymbolLines computes 1-based start lines for the function and class
// names the regex fallback recovers, keyed by leaf name. It exists so locate
// can resolve symbols the AST path missed — notably a typed arrow const
// (`const f = (x: T): R => …`) the reduced grammar cannot parse, which the
// fallback otherwise recovers name-only (no line). Comments are masked
// line-faithfully so the reported line is the real source line. The start line
// is taken from the captured name's offset (not the whole match), which avoids
// the leading `(?:^|\s)` in funcDeclRegex counting the preceding newline.
func fallbackSymbolLines(source string) (funcLines, classLines map[string]int) {
	masked := maskCommentsLineFaithful(source)
	funcLines = map[string]int{}
	classLines = map[string]int{}
	record := func(m map[string]int, idx []int) {
		// idx[2]/idx[3] delimit capture group 1 (the symbol name); -1 when absent.
		if len(idx) < 4 || idx[2] < 0 {
			return
		}
		name := masked[idx[2]:idx[3]]
		if name == "" {
			return
		}
		if _, ok := m[name]; ok {
			return // first declaration wins, matching the AST's first-seen line
		}
		m[name] = 1 + strings.Count(masked[:idx[2]], "\n")
	}
	for _, re := range []*regexp.Regexp{funcDeclRegex, funcExprRegex, arrowFuncRegex} {
		for _, idx := range re.FindAllStringSubmatchIndex(masked, -1) {
			record(funcLines, idx)
		}
	}
	for _, idx := range classDeclRegex.FindAllStringSubmatchIndex(masked, -1) {
		record(classLines, idx)
	}
	return funcLines, classLines
}

// mergeFallbackLines records "<prefix><name>" → line into lines for each name
// in names that has a computed line in nameLines and no line yet. It never
// overrides an existing entry (AST lines win) and only touches names present in
// the accepted symbol set, so orphan line entries are impossible.
func mergeFallbackLines(lines map[string]int, prefix string, names []string, nameLines map[string]int) {
	for _, n := range names {
		ln, ok := nameLines[n]
		if !ok {
			continue
		}
		key := prefix + n
		if _, exists := lines[key]; !exists {
			lines[key] = ln
		}
	}
}

// stripLineComment removes a // comment from a line, skipping // that appears
// inside a string literal (single-quote, double-quote, or backtick).
// Handles backslash escapes inside string literals. Template literal nesting
// and exotic Unicode escapes are not tracked — this is still best-effort.
func stripLineComment(line string) string {
	inStr := false
	var strChar byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inStr {
			if c == '\\' {
				i++ // skip the escaped character
				continue
			}
			if c == strChar {
				inStr = false
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			inStr = true
			strChar = c
			continue
		}
		if c == '/' && i+1 < len(line) && line[i+1] == '/' {
			return line[:i]
		}
	}
	return line
}

// extractImports finds all import statements.
func extractImports(source string) []string {
	imports := []string{}

	// ESM imports
	matches := importESMRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			imports = append(imports, match[1])
		}
	}

	// CommonJS requires
	matches = importCJSRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			imports = append(imports, match[1])
		}
	}

	return imports
}

// extractCJSRequires finds require('path') call specifiers via regex. Unlike
// ESM imports, a require() call has no dedicated tree-sitter node — it's an
// ordinary call_expression that can appear anywhere in the file, including
// inside function bodies the declaration-level jsImportsExportsFromAST walk
// doesn't descend into — so this stays regex-driven and is unioned into
// Imports regardless of AST availability (see p.parse).
func extractCJSRequires(source string) []string {
	var imports []string
	matches := importCJSRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			imports = append(imports, match[1])
		}
	}
	return imports
}

// extractFunctions finds all top-level function declarations (regex fallback
// used only when no tree-sitter grammar is embedded).
func extractFunctions(source string) []string {
	functions := []string{}

	// Function declarations
	matches := funcDeclRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			functions = append(functions, match[1])
		}
	}

	// Function expressions
	matches = funcExprRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			functions = append(functions, match[1])
		}
	}

	// Arrow functions
	matches = arrowFuncRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			functions = append(functions, match[1])
		}
	}

	return functions
}

// extractClasses finds all class declarations (regex fallback).
func extractClasses(source string) []string {
	classes := []string{}

	matches := classDeclRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			classes = append(classes, match[1])
		}
	}

	return classes
}

// extractExports finds all exported symbol names.
func extractExports(source string) []string {
	exports := []string{}

	// Named exports: export { foo, bar }
	matches := exportNamedRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			names := strings.Split(match[1], ",")
			for _, name := range names {
				name = strings.TrimSpace(name)
				// Handle "as" syntax: `export { foo as Bar }` exports Bar, not the
				// local foo — record the name AFTER "as", which is what consumers
				// import. (Previously recorded the pre-alias local name.)
				if idx := strings.Index(name, " as "); idx >= 0 {
					name = strings.TrimSpace(name[idx+len(" as "):])
				}
				if name != "" {
					exports = append(exports, name)
				}
			}
		}
	}

	// Declaration exports: export function foo() / export class Foo / etc.
	matches = exportDeclRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			exports = append(exports, match[1])
		}
	}

	// Declaration exports: export const/let/var foo = ..., bar = ...;
	// May bind more than one name per statement — split on top-level commas.
	matches = exportMultiDeclRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			exports = append(exports, splitTopLevelDeclNames(match[1])...)
		}
	}

	// Destructured declaration exports: export const { foo, bar } = x and
	// export const [ a, b ] = y bind multiple names at once.
	for _, re := range []*regexp.Regexp{exportObjDestructureRegex, exportArrDestructureRegex} {
		matches = re.FindAllStringSubmatch(source, -1)
		for _, match := range matches {
			if len(match) > 1 {
				exports = append(exports, destructuredNames(match[1])...)
			}
		}
	}

	// Namespace re-export: export * as ns from './m' binds ns.
	matches = exportStarAsRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			exports = append(exports, match[1])
		}
	}

	// Default exports: export default [function|class] Foo
	matches = exportDefaultRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		name := match[1] // export default [async] function Foo
		if name == "" {
			name = match[2] // export default class Foo
		}
		if name == "" && match[3] != "function" && match[3] != "class" && match[3] != "async" {
			name = match[3] // export default identifier
		}
		if name != "" {
			exports = append(exports, name)
		}
	}

	return exports
}

// extractWildcardReexports finds bare `export * from './mod'` module
// specifiers — the names they re-export aren't enumerable from this file's
// text alone. See exportStarBareRegex.
func extractWildcardReexports(source string) []string {
	var specifiers []string
	matches := exportStarBareRegex.FindAllStringSubmatch(source, -1)
	for _, match := range matches {
		if len(match) > 1 {
			specifiers = append(specifiers, match[1])
		}
	}
	return specifiers
}

// splitTopLevelDeclNames splits a `const`/`let`/`var` declarator list (the
// text after the keyword, up to the statement terminator) into its bound
// names. Commas inside (), [], {}, or a string/template literal are NOT
// split boundaries — only a comma at depth 0 separates declarators — so an
// initializer's own commas (e.g. `f(1, 2)`) don't get mistaken for another
// declarator. Each segment's name is its leading identifier; a trailing
// `: Type` annotation or `= value` is discarded by declNameRegex. A segment
// that leads with `{` or `[` (a destructuring pattern) yields no name here —
// exportObjDestructureRegex / exportArrDestructureRegex own that case.
func splitTopLevelDeclNames(decl string) []string {
	var names []string
	depth := 0
	inStr := false
	var strChar byte
	start := 0
	flush := func(end int) {
		seg := strings.TrimSpace(decl[start:end])
		if name := declNameRegex.FindString(seg); name != "" {
			names = append(names, name)
		}
	}
	for i := 0; i < len(decl); i++ {
		c := decl[i]
		if inStr {
			if c == '\\' {
				i++ // skip the escaped character
				continue
			}
			if c == strChar {
				inStr = false
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inStr = true
			strChar = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				flush(i)
				start = i + 1
			}
		}
	}
	flush(len(decl))
	return names
}

// destructuredNames extracts the bound identifiers from the inner text of a
// destructuring pattern (the part between { } or [ ]). For each comma-separated
// element it strips a default value (`a = 1` → `a`) and, for a renamed object
// binding (`a: renamedA` → `renamedA`), keeps the bound name — the identifier
// that is actually referenceable, not the source key. A rest element
// (`...rest` / `[first, ...others]`) binds the trailing name, so the leading
// `...` is stripped and `rest`/`others` recorded rather than the literal token.
func destructuredNames(inner string) []string {
	var names []string
	for _, part := range strings.Split(inner, ",") {
		// Strip a default value first so a colon inside it (e.g. a ternary)
		// can't be mistaken for a rename separator.
		if idx := strings.Index(part, "="); idx >= 0 {
			part = part[:idx]
		}
		// Renamed object binding `key: bound` — the bound name is what's usable.
		if idx := strings.Index(part, ":"); idx >= 0 {
			part = part[idx+1:]
		}
		part = strings.TrimSpace(part)
		// Rest element: `...rest` binds `rest`. Strip the spread so the bound
		// name is recorded, not the literal `...rest` (which is no symbol).
		part = strings.TrimPrefix(part, "...")
		if part = strings.TrimSpace(part); part != "" {
			names = append(names, part)
		}
	}
	return names
}

// deduplicate removes duplicate entries from a sorted slice.
func deduplicate(sorted []string) []string {
	if len(sorted) == 0 {
		return sorted
	}

	result := []string{sorted[0]}
	for i := 1; i < len(sorted); i++ {
		if sorted[i] != sorted[i-1] {
			result = append(result, sorted[i])
		}
	}

	return result
}
