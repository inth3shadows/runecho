package parser

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// GoParser implements structural parsing for .go files using the standard
// library's go/parser + go/ast. This is a real AST (CGO-free, no build tags),
// so unlike the former regex pass it emits per-symbol start lines and function
// body hashes — matching the Python parser's FileStructure contract.
//
// Symbol routing (preserved from the regex era):
//   - Exported funcs and methods → Functions. Methods are qualified by receiver
//     type ("Reader.Fetch"), matching the Python parser's scope-qualified names,
//     so identical method names on different types never collide.
//   - Exported types → Classes (struct, interface, alias, etc.); located but not
//     hashed (parity with Python classes — changes surface through members).
//   - Exported interface method signatures → Functions, qualified by the
//     interface type ("Reader.Read"); located and hashed over the signature span
//     so a contract change flips the hash (parity with the JS/TS parser, which
//     records method_signature the same way). Embedded interfaces and type-set
//     constraints carry no method name and are skipped.
//   - Exported top-level var/const → Exports; located, not hashed (no body).
//
// Only exported (capitalized) names are extracted, as before.
type GoParser struct{}

// NewGoParser creates a new Go parser.
func NewGoParser() *GoParser {
	return &GoParser{}
}

// SupportsExtension returns true for .go files.
func (p *GoParser) SupportsExtension(ext string) bool {
	return ext == ".go"
}

// Parse extracts top-level exported structure from Go source via go/ast.
// Best-effort on parse errors: go/parser recovers to a partial *ast.File for
// most single-error (mid-edit) buffers, and we walk whatever declarations it
// produced — honoring the Parser interface's partial-structure contract without
// a separate degraded path.
func (p *GoParser) Parse(source string) (FileStructure, error) {
	// Normalize CRLF→LF so per-symbol body hashes and start lines are
	// line-ending-independent (parity with the Python parser; a CRLF checkout
	// must index identically to an LF one).
	source = strings.ReplaceAll(source, "\r\n", "\n")
	src := []byte(source)

	// Initialize non-nil so a file with no symbols yields [] rather than null,
	// preserving the regex-era contract these slices have always honored.
	imports := []string{}
	functions := []string{}
	classes := []string{}
	exports := []string{}
	hashes := make(map[string]string)
	lines := make(map[string]int)

	// recordLine anchors a symbol at its FIRST definition (parity with Python).
	recordLine := func(key string, line int) {
		if _, ok := lines[key]; !ok {
			lines[key] = line
		}
	}
	// recordHash combines on collision so a change in ANY variant of a collapsed
	// name flips the hash (parity with Python's recordHash). Receiver
	// qualification keeps methods distinct, so a collision here needs two
	// top-level decls with the same exported name — a compile error in valid Go.
	// The combine is order-sensitive, but Parse runs on a single file and walks
	// f.Decls in source order, so the result is deterministic across runs. (Do
	// NOT merge multiple files' FileStructures into one without making this
	// order-independent first.)
	recordHash := func(key string, span []byte) {
		h := hashBytesHex(span)
		if existing, ok := hashes[key]; ok {
			h = hashBytesHex([]byte(existing + h))
		}
		hashes[key] = h
	}

	fset := token.NewFileSet()
	// SkipObjectResolution: we only walk top-level decls, so identifier object
	// resolution is wasted work. The error is intentionally ignored — see doc.
	f, _ := parser.ParseFile(fset, "", source, parser.SkipObjectResolution)
	if f != nil {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				name := d.Name.Name
				if !ast.IsExported(name) {
					continue
				}
				full := name
				if d.Recv != nil && len(d.Recv.List) > 0 {
					// Qualify by receiver type: func (r *Reader) Fetch → "Reader.Fetch".
					full = qualify(receiverTypeName(d.Recv.List[0].Type), name)
				}
				functions = append(functions, full)
				key := "function:" + full
				recordHash(key, nodeSpan(fset, src, d))
				recordLine(key, fset.Position(d.Pos()).Line)
			case *ast.GenDecl:
				collectGenDecl(d, fset, src, &imports, &functions, &classes, &exports, recordLine, recordHash)
			}
		}
	}

	sort.Strings(imports)
	sort.Strings(functions)
	sort.Strings(classes)
	sort.Strings(exports)

	// Nil out empty maps so the IR omits them for files with no spanned symbols
	// (parity with the Python parser and the regex-era nil contract).
	if len(hashes) == 0 {
		hashes = nil
	}
	if len(lines) == 0 {
		lines = nil
	}

	return FileStructure{
		Imports:      deduplicate(imports),
		Functions:    deduplicate(functions),
		Classes:      deduplicate(classes),
		Exports:      deduplicate(exports),
		SymbolHashes: hashes,
		SymbolLines:  lines,
	}, nil
}

// collectGenDecl extracts imports, exported types, and exported top-level
// var/const names from a general declaration. Iterating Specs is what fixes the
// two regex-era bugs for free: `var X, Y = 1, 2` yields both names (a ValueSpec
// carries all of them), and a `var (...)` / `import (...)` block's boundaries are
// owned by the AST, so a nested `)` no longer closes the block early.
func collectGenDecl(d *ast.GenDecl, fset *token.FileSet, src []byte, imports, functions, classes, exports *[]string, recordLine func(string, int), recordHash func(string, []byte)) {
	switch d.Tok {
	case token.IMPORT:
		for _, spec := range d.Specs {
			s, ok := spec.(*ast.ImportSpec)
			if !ok {
				continue
			}
			// Path.Value is a quoted Go string literal (always double-quoted for
			// import paths); Unquote yields the bare path.
			if path, err := strconv.Unquote(s.Path.Value); err == nil {
				*imports = append(*imports, path)
			}
		}
	case token.TYPE:
		for _, spec := range d.Specs {
			s, ok := spec.(*ast.TypeSpec)
			if !ok || !ast.IsExported(s.Name.Name) {
				continue
			}
			*classes = append(*classes, s.Name.Name)
			recordLine("class:"+s.Name.Name, fset.Position(s.Pos()).Line)
			// Descend into an interface body so its method signatures become
			// referenceable as Type.Method (parity with JS/TS interface methods
			// and Python class methods). Only methods carry Names + a FuncType;
			// embedded interfaces and type-set constraints have neither and fall
			// through untouched.
			iface, ok := s.Type.(*ast.InterfaceType)
			if !ok || iface.Methods == nil {
				continue
			}
			for _, m := range iface.Methods.List {
				if _, ok := m.Type.(*ast.FuncType); !ok {
					continue
				}
				for _, name := range m.Names {
					if !ast.IsExported(name.Name) {
						continue
					}
					full := qualify(s.Name.Name, name.Name)
					*functions = append(*functions, full)
					key := "function:" + full
					recordHash(key, nodeSpan(fset, src, m))
					recordLine(key, fset.Position(m.Pos()).Line)
				}
			}
		}
	case token.VAR, token.CONST:
		for _, spec := range d.Specs {
			s, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, nm := range s.Names {
				if !ast.IsExported(nm.Name) {
					continue
				}
				*exports = append(*exports, nm.Name)
				recordLine("export:"+nm.Name, fset.Position(nm.Pos()).Line)
			}
		}
	}
}

// receiverTypeName returns the base type name of a method receiver, unwrapping
// pointers and generic instantiations: *Reader → "Reader", Set[T] → "Set". An
// unresolvable receiver yields "" (the method is then recorded under its bare
// name, an acceptable degradation).
func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver with one type param: Set[T]
		return receiverTypeName(t.X)
	case *ast.IndexListExpr: // generic receiver with multiple params: Map[K, V]
		return receiverTypeName(t.X)
	}
	return ""
}

// nodeSpan returns the exact source bytes covered by n. The offsets come from
// the FileSet, so the span is the declaration as written (func keyword through
// closing brace) — what the body hash is computed over. Returns nil on any
// out-of-range result (defensive; should not happen for a node from this file).
func nodeSpan(fset *token.FileSet, src []byte, n ast.Node) []byte {
	start := fset.Position(n.Pos()).Offset
	end := fset.Position(n.End()).Offset
	if start < 0 || end > len(src) || start > end {
		return nil
	}
	return src[start:end]
}
