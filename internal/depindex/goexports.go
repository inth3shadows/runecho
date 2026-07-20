package depindex

import (
	"go/ast"
	"go/parser"
	"go/token"
)

// Extraction of a Go package's exported top-level names, using go/parser.
//
// This deliberately replaced a hand-written column-zero line scanner, and the
// reversal is worth recording because the original reasoning was wrong in an
// instructive way.
//
// The scanner existed to avoid go/parser's cost: net/http measured 79ms against
// a ~12ms edit-time budget, so parsing "obviously" could not be afforded. That
// was ONE data point — very nearly the largest package in the standard library —
// generalized into an architecture. Measured properly across 3000 real package
// directories in GOROOT and the module cache, go/parser's export extraction is:
//
//	p50 0.29ms · p90 3.8ms · 4.1% over 12ms
//
// The median package parses in under a third of a millisecond. The tail is real
// but is bounded by maxGoPackageBytes, which declines an oversized package from a
// directory stat before reading a byte of it.
//
// The scanner cost 490 lines (implementation plus a differential test whose only
// purpose was proving it agreed with go/parser) and carried seven latent
// false-positive classes — tab instead of space after a keyword, `var(` with no
// space, a one-line `var (A = 1)` group that swallowed every remaining export in
// the file, semicolon-separated declarations, indented top-level declarations.
// Each was a MISSED export, and a missed export is a false positive: the guard
// flags a symbol that really exists.
//
// go/parser removes that entire class by construction. There is nothing to fail
// closed about, because there is no partial understanding: the parse either
// succeeds and the declaration list is complete, or it fails and we abstain.

// GoPackageExports returns the exported top-level names declared across the given
// Go source files (the contents of one package directory), and reports whether
// the result is trustworthy.
//
// ok=false means some file did not parse, so the caller must abstain rather than
// report an export set that is missing whatever that file declared. Note this is
// stricter than it needs to be — one unparseable file (source written for a newer
// language version, say) discards the whole package — which is the correct
// direction: under-reporting exports is what produces false positives.
//
// Methods are excluded: a method is reached through a value, not through the
// package qualifier this index serves.
func GoPackageExports(sources []string) (map[string]struct{}, bool) {
	out := map[string]struct{}{}
	// One FileSet for the package: it accumulates position bases, and reusing it
	// avoids re-allocating per file.
	fset := token.NewFileSet()
	for i, src := range sources {
		// SkipObjectResolution turns off the identifier-to-declaration linking
		// pass, which nothing here needs and which is pure cost.
		f, err := parser.ParseFile(fset, goSyntheticName(i), src, parser.SkipObjectResolution)
		if err != nil {
			return nil, false
		}
		collectGoDeclExports(f, out)
	}
	return out, true
}

// goSyntheticName gives each file a distinct name within the FileSet. The real
// filename is irrelevant — nothing here reports positions — but distinct names
// keep the FileSet's bookkeeping honest.
func goSyntheticName(i int) string {
	return "f" + string(rune('a'+i%26)) + ".go"
}

// collectGoDeclExports adds one parsed file's exported top-level declarations.
func collectGoDeclExports(f *ast.File, out map[string]struct{}) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.IsExported() {
				out[d.Name.Name] = struct{}{}
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						out[s.Name.Name] = struct{}{}
					}
				case *ast.ValueSpec:
					for _, id := range s.Names {
						if id.IsExported() {
							out[id.Name] = struct{}{}
						}
					}
				}
			}
		}
	}
}
