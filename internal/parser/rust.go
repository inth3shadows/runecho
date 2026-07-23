package parser

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// RustParser implements structural parsing for .rs files using the vendored
// pure-Go tree-sitter Rust grammar — the same engine the Python and JS/TS
// parsers use, so it emits per-symbol start lines and body hashes and gets
// modified-symbol diffing for free.
//
// A real grammar rather than a regex/masker pass (the shell parser's approach)
// is the right call here specifically because Rust's lexical surface is hostile
// to masking: `'a` is a lifetime in `fn f<'a>(x: &'a str)` but a char literal in
// `let c = 'a';`, and the two are indistinguishable without parsing. A masker
// that guessed wrong would blank real code and silently drop symbols. Block
// comments also nest in Rust (`/* /* */ */`), which a naive scanner terminates
// early. The grammar resolves both by construction.
//
// Symbol routing:
//   - `fn` items → Functions. Inside an `impl`, qualified by the implementing
//     type ("Reader.fetch"), matching the Go parser's receiver qualification, so
//     identical method names on different types never collide. Inside an
//     `impl Trait for Type`, still qualified by the *type* — that is where the
//     callable actually lives.
//   - Trait method signatures → Functions, qualified by the trait
//     ("Fetch.get"), parity with the Go parser's interface-method handling.
//   - `struct` / `enum` / `trait` / `union` / `type` items → Classes. Rust has no
//     single "class" concept; these are its named type-defining items, which is
//     what Classes means in the IR's cross-language vocabulary.
//   - `const` / `static` items → Exports (located, not hashed — no body), and
//     unlike every other item kind, REGARDLESS of `pub`. Exports is the only
//     bucket FileStructure has for value symbols — there is no Variables list —
//     so for Rust it doubles as "value symbols", and gating it on `pub` would
//     drop private consts from the IR entirely rather than merely from the
//     public surface. That is the exact guard false-negative the
//     extract-everything rule below exists to prevent, so the cross-language
//     meaning of Exports is what bends here, not the visibility rule.
//   - `macro_rules!` definitions → Functions. A macro invocation is written and
//     read as a call, so a reference to one resolves where a caller expects.
//
// Items are qualified by enclosing `mod` ("inner.nested"), so two modules in one
// file may define the same name without collapsing.
//
// Visibility: unlike the Go parser (which extracts only capitalized names), this
// extracts ALL items and additionally lists `pub` ones in Exports (except
// const/static — see above). Go's
// convention makes unexported names unreferenceable outside the package, so
// skipping them loses nothing; Rust's does not — a same-crate reference to a
// private `fn` is ordinary and must still resolve. Dropping non-`pub` items
// would hand the guard a false negative on the common case.
type RustParser struct{}

// NewRustParser creates a new Rust parser.
func NewRustParser() *RustParser { return &RustParser{} }

// SupportsExtension returns true for .rs files.
func (p *RustParser) SupportsExtension(ext string) bool {
	return ext == ".rs"
}

// rustLang lazily loads and caches the tree-sitter Rust grammar. Loaded once;
// the resulting *Language is safe for concurrent reads. A fresh Parser is
// created per Parse call because ts.Parser is not concurrency-safe.
var (
	rustLangOnce sync.Once
	rustLang     *ts.Language
)

func rustLanguage() *ts.Language {
	rustLangOnce.Do(func() {
		// Recover so a grammar-decode panic doesn't propagate out of the first
		// Parse call. sync.Once marks itself done even on panic, so an unrecovered
		// panic would leave rustLang nil forever anyway; recovering degrades to the
		// nil-language path (no symbols), which is fail-safe.
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "runecho: Rust grammar failed to load (%v); Rust symbols disabled\n", r)
			}
		}()
		rustLang = grammars.RustLanguage()
	})
	return rustLang
}

// Parse extracts structure from Rust source via tree-sitter.
// Best-effort on parse errors: tree-sitter recovers to a partial tree for most
// mid-edit buffers, and we walk whatever it produced — honoring the Parser
// interface's partial-structure contract without a separate degraded path.
func (p *RustParser) Parse(source string) (FileStructure, error) {
	// Normalize line endings: a CRLF checkout must index identically to an LF
	// one, and per-symbol body hashes must not depend on line-ending style.
	source = strings.ReplaceAll(source, "\r\n", "\n")

	imports, functions, classes, exports, hashes, lines := rustSymbolsFromAST(source)

	sort.Strings(imports)
	sort.Strings(functions)
	sort.Strings(classes)
	sort.Strings(exports)

	return FileStructure{
		Imports:      deduplicate(imports),
		Functions:    deduplicate(functions),
		Classes:      deduplicate(classes),
		Exports:      deduplicate(exports),
		SymbolHashes: hashes,
		SymbolLines:  lines,
	}, nil
}

// rustSymbolsFromAST does the actual walk. Split out so the panic recovery has a
// single place to reset every named return — a panic mid-walk must not leak a
// partial, inconsistent symbol set into the IR.
func rustSymbolsFromAST(source string) (imports, functions, classes, exports []string, hashes map[string]string, lines map[string]int) {
	// Initialize non-nil so a file with no symbols yields [] rather than null,
	// matching the contract the other parsers honor.
	imports, functions, classes, exports = []string{}, []string{}, []string{}, []string{}

	// The pure-Go tree-sitter runtime can panic on adversarial or malformed
	// input; a panic here would otherwise propagate through parseFile→Generate
	// and crash the indexer/MCP server. Degrade to no symbols instead, so one bad
	// file can't take down the process.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "runecho: Rust parse panicked (%v); symbols for this file disabled\n", r)
			imports, functions, classes, exports = []string{}, []string{}, []string{}, []string{}
			hashes, lines = nil, nil
		}
	}()

	lang := rustLanguage()
	if lang == nil {
		// Grammar unavailable (e.g. a grammar_subset build that omitted Rust).
		return imports, functions, classes, exports, nil, nil
	}
	src := []byte(source)
	// Reject pathologically-nested input before the super-linear tree-sitter
	// parse can hang the process (see maxParseNestDepth).
	if exceedsNestDepth(src) {
		fmt.Fprintf(os.Stderr, "runecho: Rust source exceeds max nesting depth (%d); symbols for this file disabled\n", maxParseNestDepth)
		return imports, functions, classes, exports, nil, nil
	}
	tree, err := ts.NewParser(lang).Parse(src)
	if err != nil || tree == nil || tree.RootNode() == nil {
		return imports, functions, classes, exports, nil, nil
	}

	hashes = make(map[string]string)
	lines = make(map[string]int)

	// recordHash combines on collision so a change in ANY variant of a collapsed
	// name flips the hash. Rust reaches this via `#[cfg(...)]`-gated duplicate
	// definitions, which are legal in one file and share a name. Last-write-wins
	// would silently hide edits to every variant but the last in AST order.
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

	var walk func(n *ts.Node, prefix string, depth int)
	walk = func(n *ts.Node, prefix string, depth int) {
		// Bound recursion so a deeply-nested AST can't overflow the goroutine
		// stack — a runtime throw the recover() above cannot catch.
		if depth > maxParseNestDepth {
			return
		}
		for i := 0; i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			span := src[c.StartByte():c.EndByte()]
			line := int(c.StartPoint().Row) + 1
			name := rustFieldText(c, "name", lang, src)
			isPub := rustIsPub(c, lang)

			switch c.Type(lang) {
			case "use_declaration":
				if path := rustUsePath(c, lang, src); path != "" {
					imports = append(imports, path)
				}

			case "function_item", "function_signature_item":
				if name == "" {
					continue
				}
				full := qualify(prefix, name)
				functions = append(functions, full)
				recordHash("function:"+full, span)
				recordLine("function:"+full, line)
				if isPub {
					exports = append(exports, full)
				}

			case "struct_item", "enum_item", "union_item", "type_item":
				if name == "" {
					continue
				}
				full := qualify(prefix, name)
				classes = append(classes, full)
				recordHash("class:"+full, span)
				recordLine("class:"+full, line)
				if isPub {
					exports = append(exports, full)
				}

			case "trait_item":
				if name == "" {
					continue
				}
				full := qualify(prefix, name)
				classes = append(classes, full)
				recordHash("class:"+full, span)
				recordLine("class:"+full, line)
				if isPub {
					exports = append(exports, full)
				}
				// Descend so the trait's method signatures become referenceable as
				// Trait.method (parity with the Go parser's interface methods).
				walk(c, full, depth+1)

			case "impl_item":
				// Qualify by the implementing TYPE, not the trait: `impl Fetch for
				// Reader { fn get }` puts a callable at Reader.get, and that is what
				// a caller writes. An impl with no resolvable type name falls back
				// to the enclosing prefix rather than inventing one.
				//
				// When the target does not reduce to a plain identifier (`&str`,
				// a tuple, a slice), fall back to its NORMALIZED TYPE TEXT as the
				// prefix — never to the enclosing prefix. Falling back to the
				// enclosing prefix emits the method under its bare name at file
				// scope, where it is indistinguishable from a real top-level fn:
				// `impl MyTrait for &str { fn helper }` alongside `fn helper()`
				// collapsed into one symbol, and because recordHash COMBINES on
				// collision, editing the impl method flipped the top-level
				// function's hash — reporting an unedited symbol as modified.
				implPrefix := rustTypeName(rustFieldText(c, "type", lang, src))
				if implPrefix == "" {
					implPrefix = rustTypeText(rustFieldText(c, "type", lang, src))
				}
				walk(c, qualify(prefix, implPrefix), depth+1)

			case "mod_item":
				// Qualify a module's contents so two mods in one file may define the
				// same name without collapsing into one symbol.
				modPrefix := prefix
				if name != "" {
					modPrefix = qualify(prefix, name)
				}
				walk(c, modPrefix, depth+1)

			case "const_item", "static_item":
				if name == "" {
					continue
				}
				full := qualify(prefix, name)
				exports = append(exports, full)
				recordLine("export:"+full, line)

			case "macro_definition":
				// A macro invocation is written and read as a call, so route it to
				// Functions where a reference to it will resolve.
				if name == "" {
					continue
				}
				full := qualify(prefix, name)
				functions = append(functions, full)
				recordHash("function:"+full, span)
				recordLine("function:"+full, line)
				if isPub {
					exports = append(exports, full)
				}

			default:
				// Recurse through wrappers (attribute items, cfg-gated blocks,
				// declaration lists) so conditionally-defined symbols are still seen.
				walk(c, prefix, depth+1)
			}
		}
	}
	walk(tree.RootNode(), "", 0)

	// Nil out empty maps so the IR omits them for files with no spanned symbols
	// (parity with the other AST parsers).
	if len(hashes) == 0 {
		hashes = nil
	}
	if len(lines) == 0 {
		lines = nil
	}
	return imports, functions, classes, exports, hashes, lines
}

// rustIsPub reports whether an item carries a visibility modifier (`pub`,
// `pub(crate)`, `pub(super)`, …). Any of them is treated as exported: the IR's
// Exports list is about "visible beyond this item's own scope", and
// `pub(crate)` is exactly that for an in-repo caller.
func rustIsPub(n *ts.Node, lang *ts.Language) bool {
	for i := 0; i < n.NamedChildCount(); i++ {
		if n.NamedChild(i).Type(lang) == "visibility_modifier" {
			return true
		}
	}
	return false
}

// rustUsePath returns a `use` declaration's path as written, minus the `use`
// keyword and trailing semicolon — e.g. "std::collections::HashMap" or
// "foo::{bar, baz as qux}". Interior whitespace is collapsed so a multi-line
// brace list yields one stable, comparable string rather than an import whose
// text changes with formatting.
func rustUsePath(n *ts.Node, lang *ts.Language, src []byte) string {
	for i := 0; i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		// Skip the visibility modifier on a `pub use` re-export; the path is the
		// next named child.
		if c.Type(lang) == "visibility_modifier" {
			continue
		}
		return strings.Join(strings.Fields(c.Text(src)), " ")
	}
	return ""
}

// rustTypeName reduces an impl target type expression to its base name:
// "Reader" → "Reader", "Set<T>" → "Set", "crate::mod::Reader" → "Reader". The
// bare name is what a caller writes at the call site, so that is what the
// qualified symbol must key on. Returns "" for a type it cannot reduce (e.g. a
// tuple or reference type); the caller then falls back to rustTypeText.
func rustTypeName(text string) string {
	if i := strings.IndexAny(text, "<"); i >= 0 {
		text = text[:i]
	}
	text = strings.TrimSpace(text)
	if i := strings.LastIndex(text, "::"); i >= 0 {
		text = text[i+2:]
	}
	// Reject anything that isn't a plain identifier — references (&T), tuples,
	// slices, and trait objects have no single name to key on.
	if text == "" {
		return ""
	}
	for i, r := range text {
		isAlpha := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if i == 0 && !isAlpha {
			return ""
		}
		if !isAlpha && !(r >= '0' && r <= '9') {
			return ""
		}
	}
	return text
}

// rustTypeText is the fallback qualifier for an impl target with no plain-
// identifier name: the type as written, with interior whitespace collapsed so
// formatting does not change the symbol ("&str", "(A, B)", "[u8; 4]"). It is
// deliberately NOT a Rust identifier — that is the point. The prefix cannot
// collide with a real top-level function, so the method stays visible in the
// index without corrupting an unrelated symbol's hash. Returns "impl" for an
// empty target so the prefix is never blank.
func rustTypeText(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if t == "" {
		return "impl"
	}
	return t
}

// rustFieldText returns the text of n's named field, or "" if absent.
func rustFieldText(n *ts.Node, field string, lang *ts.Language, src []byte) string {
	if f := n.ChildByFieldName(field, lang); f != nil {
		return f.Text(src)
	}
	return ""
}
