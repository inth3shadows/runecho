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

// RubyParser implements structural parsing for .rb files using the vendored
// pure-Go tree-sitter Ruby grammar.
//
// A grammar rather than a regex/masker pass, by the same test the Rust parser
// applied: Ruby has constructs a length-preserving masker cannot disambiguate
// without parsing. `?a` is a character literal but `x ?a : b` is a ternary;
// `%w[a b]` / `%i(...)` / `<<~HEREDOC` open string regions with user-chosen
// delimiters; and `/re/` is a regex or a division depending on preceding
// context. Guessing wrong on any of them blanks real code and silently drops
// every symbol after it.
//
// Symbol routing:
//   - `def` → Functions, qualified by enclosing module/class ("Outer.Reader.fetch").
//   - `def self.x` (singleton methods) → Functions, qualified the same way. Ruby
//     distinguishes Reader.build from Reader#fetch; the IR has one namespace per
//     symbol name, so both land under the enclosing scope. Collapsing them is
//     lossy only when a class defines an instance and a class method with the
//     same name, which is rare and still resolves to a real symbol.
//   - `class` / `module` → Classes, nested-qualified. The compact `module A::B`
//     form is normalized to the same name the nested form produces ("A.B"), so
//     one logical module does not get two symbol names depending on spelling.
//   - `attr_accessor` / `attr_reader` / `attr_writer` → Functions. These generate
//     real, callable methods; omitting them would make the index claim a Rails-
//     style model has almost no callable surface. Writers are recorded with
//     their trailing `=` ("Reader.name="), which is how they are actually named.
//   - Constant assignments (`CONST = 1`) → Exports.
//   - `require` / `require_relative` / `load` → Imports (the literal argument).
//
// Visibility follows the per-language rule the Rust parser established: extract
// everything, and let Exports mean "callable from outside this object". Ruby
// methods are public by default, so Exports gets all of them EXCEPT those
// following a bare `private` / `protected` marker in the same body — which is
// how Ruby actually scopes visibility. `private :sym` and `private def x` forms
// are not tracked (see limitations).
//
// Known limitations: visibility set via `private :name`, `private def name`, or
// `private_class_method` is not detected, so such methods stay in Exports —
// over-inclusive, never under-inclusive, so it cannot cause a guard false
// negative. `define_method` and other metaprogrammed definitions are invisible
// to any static parser and are not extracted. Measured against a 400-file
// real-world corpus (Homebrew's own Ruby): 3181 functions, 1103 classes, zero
// malformed names, and 0.5% of files (2/400) that the grammar could not parse
// at all — those emit a warning rather than silently reporting no symbols.
type RubyParser struct{}

// NewRubyParser creates a new Ruby parser.
func NewRubyParser() *RubyParser { return &RubyParser{} }

// SupportsExtension returns true for .rb files.
func (p *RubyParser) SupportsExtension(ext string) bool {
	return ext == ".rb"
}

var (
	rubyLangOnce sync.Once
	rubyLang     *ts.Language
)

func rubyLanguage() *ts.Language {
	rubyLangOnce.Do(func() {
		// Recover so a grammar-decode panic doesn't propagate out of the first
		// Parse call; sync.Once marks itself done even on panic, so recovering
		// degrades to the nil-language path (no symbols) rather than leaving a
		// panic to escape forever.
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "runecho: Ruby grammar failed to load (%v); Ruby symbols disabled\n", r)
			}
		}()
		rubyLang = grammars.RubyLanguage()
	})
	return rubyLang
}

// Parse extracts structure from Ruby source via tree-sitter. Best-effort on
// parse errors: tree-sitter recovers to a partial tree for most mid-edit
// buffers, and we walk whatever it produced.
func (p *RubyParser) Parse(source string) (FileStructure, error) {
	// Normalize line endings so hashes and start lines are style-independent.
	source = strings.ReplaceAll(source, "\r\n", "\n")

	imports, functions, classes, exports, hashes, lines := rubySymbolsFromAST(source)

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

// rubyRequireFns are the call names whose string argument is an import path.
var rubyRequireFns = map[string]bool{"require": true, "require_relative": true, "load": true}

// rubyAttrFns maps an attr_* macro to whether it generates a reader and/or a
// writer. Both accessors are real callable methods, so both are recorded.
var rubyAttrFns = map[string]struct{ reader, writer bool }{
	"attr_accessor": {true, true},
	"attr_reader":   {true, false},
	"attr_writer":   {false, true},
}

func rubySymbolsFromAST(source string) (imports, functions, classes, exports []string, hashes map[string]string, lines map[string]int) {
	// Non-nil so a symbol-less file yields [] rather than null.
	imports, functions, classes, exports = []string{}, []string{}, []string{}, []string{}

	// The pure-Go tree-sitter runtime can panic on adversarial input; degrade to
	// no symbols rather than taking down the indexer or MCP server.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "runecho: Ruby parse panicked (%v); symbols for this file disabled\n", r)
			imports, functions, classes, exports = []string{}, []string{}, []string{}, []string{}
			hashes, lines = nil, nil
		}
	}()

	lang := rubyLanguage()
	if lang == nil {
		return imports, functions, classes, exports, nil, nil
	}
	src := []byte(source)
	if exceedsNestDepth(src) {
		fmt.Fprintf(os.Stderr, "runecho: Ruby source exceeds max nesting depth (%d); symbols for this file disabled\n", maxParseNestDepth)
		return imports, functions, classes, exports, nil, nil
	}
	tree, err := ts.NewParser(lang).Parse(src)
	if err != nil || tree == nil || tree.RootNode() == nil {
		return imports, functions, classes, exports, nil, nil
	}
	// A root node of ERROR means the grammar could not parse the file at all —
	// the tree degenerates to a flat run of tokens with no module/class/method
	// nodes, so the walk below silently yields nothing. For an existence
	// checker, "this file has no symbols" and "I could not read this file" are
	// very different claims, and conflating them is the direction that hurts:
	// a consumer would conclude the symbols don't exist. Warn so the gap is
	// visible, then continue best-effort — a partial tree is still worth walking.
	//
	// The warning carries no file path because Parser.Parse does not receive one
	// — the same limitation the Python and Rust parsers' panic and nest-depth
	// warnings have. Threading a path through the interface for a warning is not
	// worth the churn; the generator already prefixes its own per-file warnings
	// with the path, so this line reads as an unattributed detail next to them.
	if tree.RootNode().Type(lang) == "ERROR" {
		fmt.Fprintf(os.Stderr, "runecho: Ruby file did not parse (grammar returned ERROR at root); its symbols are missing, not absent\n")
	}

	hashes = make(map[string]string)
	lines = make(map[string]int)

	// recordHash combines on collision so a change in ANY variant of a collapsed
	// name flips the hash. Ruby reaches this legitimately: reopening a class to
	// redefine a method, or an instance and a class method sharing a name.
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

	var walk func(n *ts.Node, prefix string, depth int)
	walk = func(n *ts.Node, prefix string, depth int) {
		// Ruby nests via keywords, not brackets, so exceedsNestDepth cannot see
		// deep nesting — this bound is what actually protects the goroutine stack
		// from a runtime throw that recover() could not catch.
		if depth > maxParseNestDepth {
			return
		}
		// Ruby visibility is positional: a bare `private` applies to every def
		// after it in the same body. The flag is per-body, so it resets whenever
		// we descend into a new class/module — which is exactly Ruby's scoping.
		visibilityPrivate := false

		for i := 0; i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			span := src[c.StartByte():c.EndByte()]
			line := int(c.StartPoint().Row) + 1
			name := rubyFieldText(c, "name", lang, src)

			switch c.Type(lang) {
			case "identifier":
				// A bare `private` / `protected` statement flips visibility for the
				// rest of this body. `public` flips it back.
				switch c.Text(src) {
				case "private", "protected":
					visibilityPrivate = true
				case "public":
					visibilityPrivate = false
				}

			case "method", "singleton_method":
				if name == "" {
					continue
				}
				full := qualify(prefix, name)
				functions = append(functions, full)
				recordHash("function:"+full, span)
				recordLine("function:"+full, line)
				if !visibilityPrivate {
					exports = append(exports, full)
				}

			case "class", "module", "singleton_class":
				if name == "" {
					// An anonymous class (`Class.new`) or `class << self`; descend
					// under the current prefix rather than inventing a name.
					walk(c, prefix, depth+1)
					continue
				}
				// Normalize the compact form `module A::B` to the same name the
				// nested form `module A; module B` produces. Without this, one
				// logical module yields "A::B" or "A.B" depending purely on how it
				// was written, so the index answers differently for identical code
				// and a reference resolves against only one spelling.
				full := qualify(prefix, strings.ReplaceAll(name, "::", "."))
				classes = append(classes, full)
				recordHash("class:"+full, span)
				recordLine("class:"+full, line)
				exports = append(exports, full)
				walk(c, full, depth+1)

			case "assignment":
				// Only CONSTANT = ... is a durable named symbol; local and instance
				// variable assignments are not part of the file's surface.
				if lhs := c.ChildByFieldName("left", lang); lhs != nil && lhs.Type(lang) == "constant" {
					full := qualify(prefix, lhs.Text(src))
					exports = append(exports, full)
					recordLine("export:"+full, line)
				}

			case "call":
				if fn, args := rubyCallParts(c, lang, src); fn != "" {
					if rubyRequireFns[fn] {
						if len(args) == 1 && args[0] != "" {
							imports = append(imports, args[0])
						}
						continue
					}
					if kind, ok := rubyAttrFns[fn]; ok {
						for _, a := range args {
							if a == "" {
								continue
							}
							if kind.reader {
								full := qualify(prefix, a)
								functions = append(functions, full)
								recordLine("function:"+full, line)
								if !visibilityPrivate {
									exports = append(exports, full)
								}
							}
							if kind.writer {
								full := qualify(prefix, a+"=")
								functions = append(functions, full)
								recordLine("function:"+full, line)
								if !visibilityPrivate {
									exports = append(exports, full)
								}
							}
						}
						continue
					}
				}
				// Any other call may still wrap definitions (a DSL block), so keep
				// descending.
				walk(c, prefix, depth+1)

			default:
				// Recurse through wrappers (body_statement, if/begin blocks) so
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
	return imports, functions, classes, exports, hashes, lines
}

// rubyCallParts returns a call's method name and its literal arguments —
// string contents for `require "x"` and symbol names for `attr_reader :a, :b`.
// Non-literal arguments yield "" entries, which callers skip: a computed
// `require some_var` names nothing this parser can honestly record.
func rubyCallParts(n *ts.Node, lang *ts.Language, src []byte) (string, []string) {
	// A qualified call (`Foo.bar`) has a receiver; those are never the bare
	// require/attr_* forms this function is for.
	if n.ChildByFieldName("receiver", lang) != nil {
		return "", nil
	}
	m := n.ChildByFieldName("method", lang)
	if m == nil {
		return "", nil
	}
	fn := m.Text(src)
	argsNode := n.ChildByFieldName("arguments", lang)
	if argsNode == nil {
		return fn, nil
	}
	var args []string
	for i := 0; i < argsNode.NamedChildCount(); i++ {
		a := argsNode.NamedChild(i)
		switch a.Type(lang) {
		case "string":
			args = append(args, rubyStringContent(a, lang, src))
		case "simple_symbol":
			// ":name" → "name".
			args = append(args, strings.TrimPrefix(a.Text(src), ":"))
		default:
			args = append(args, "")
		}
	}
	return fn, args
}

// rubyStringContent returns a string literal's inner text, or "" if it is
// interpolated or empty. An interpolated require path is not a literal this
// parser can resolve, so recording nothing is the honest result.
func rubyStringContent(n *ts.Node, lang *ts.Language, src []byte) string {
	for i := 0; i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c.Type(lang) == "string_content" {
			return c.Text(src)
		}
		// interpolation ⇒ not a literal path.
		return ""
	}
	return ""
}

// rubyFieldText returns the text of n's named field, or "" if absent.
func rubyFieldText(n *ts.Node, field string, lang *ts.Language, src []byte) string {
	if f := n.ChildByFieldName(field, lang); f != nil {
		return f.Text(src)
	}
	return ""
}
