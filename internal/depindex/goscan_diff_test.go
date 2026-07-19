package depindex

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Differential test: the line scanner in goscan.go must agree EXACTLY with
// go/parser about a package's exported top-level names.
//
// This is the only thing standing between "the scanner is fast" and "the scanner
// is correct". A name go/parser finds and the scanner misses becomes a FALSE
// POSITIVE in the guard — valid code flagged for calling a symbol that does
// exist. A name the scanner invents is harmless by comparison (it only
// suppresses a violation), but is still reported here, since inventing names
// means the scanner is misreading structure and the next misread may go the
// other way.
//
// It runs over real packages — GOROOT and the local module cache — rather than
// fixtures, because the hole in a column-zero scanner is unusual FORMATTING, and
// no hand-written fixture will contain formatting its author did not think of.
// Fixtures pin behaviour; this discovers it.
//
// Skipped when GOROOT/module cache are unavailable (CI without a module cache
// still runs the fixture tests in goscan_test.go).

// astPackageExports is the oracle: go/parser's view of a package directory's
// exported top-level names. Deliberately a separate, naive implementation — if
// it shared code with the scanner the comparison would prove nothing.
func astPackageExports(t *testing.T, dir string) (map[string]struct{}, []string, bool) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, false
	}
	fset := token.NewFileSet()
	out := map[string]struct{}{}
	var sources []string
	found := false
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, false
		}
		f, err := parser.ParseFile(fset, name, data, parser.SkipObjectResolution)
		if err != nil {
			// A file go/parser rejects is not valid Go for this toolchain
			// (build-tagged for another language version, or generated oddly).
			// The oracle cannot speak for the package, so skip the whole dir.
			return nil, nil, false
		}
		found = true
		sources = append(sources, string(data))
		for _, d := range f.Decls {
			switch d := d.(type) {
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
	return out, sources, found
}

func TestGoScannerMatchesAST(t *testing.T) {
	roots := goDiffRoots(t)
	if len(roots) == 0 {
		t.Skip("no GOROOT/src or module cache available")
	}

	const maxDirs = 1500 // keep the run to a few seconds
	dirs := goPackageDirs(t, roots, maxDirs)
	if len(dirs) == 0 {
		t.Skip("no Go package directories found")
	}

	checked, missedDirs, extraDirs := 0, 0, 0
	for _, dir := range dirs {
		want, sources, ok := astPackageExports(t, dir)
		if !ok || len(sources) == 0 {
			continue
		}
		got, ok := GoPackageExports(sources)
		if !ok {
			// The scanner declined; that degrades to Partial in the resolver, so
			// it is safe — but note it, because declining everything would make
			// this test vacuous.
			continue
		}
		checked++

		var missing, extra []string
		for name := range want {
			if _, in := got[name]; !in {
				missing = append(missing, name)
			}
		}
		for name := range got {
			if _, in := want[name]; !in {
				extra = append(extra, name)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)

		// A MISSING name is the false-positive direction and is a hard failure.
		if len(missing) > 0 {
			missedDirs++
			if missedDirs <= 10 {
				t.Errorf("%s: scanner MISSED %d exported name(s) go/parser found: %v",
					shortDir(dir), len(missing), truncate(missing))
			}
		}
		// An EXTRA name only suppresses violations, but still signals a misread.
		if len(extra) > 0 {
			extraDirs++
			if extraDirs <= 10 {
				t.Errorf("%s: scanner INVENTED %d name(s) go/parser did not find: %v",
					shortDir(dir), len(extra), truncate(extra))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no packages were actually compared — the test is vacuous")
	}
	t.Logf("compared %d package dirs; dirs with missing names=%d, with invented names=%d",
		checked, missedDirs, extraDirs)
}

func goDiffRoots(t *testing.T) []string {
	t.Helper()
	var roots []string
	if gr := os.Getenv("GOROOT"); gr != "" && isDir(filepath.Join(gr, "src")) {
		roots = append(roots, filepath.Join(gr, "src"))
	}
	if mc := goModCacheDir(); mc != "" && isDir(mc) {
		roots = append(roots, mc)
	}
	return roots
}

// goPackageDirs walks the roots collecting directories that contain .go files,
// stopping at limit so the test stays a few seconds rather than minutes.
func goPackageDirs(t *testing.T, roots []string, limit int) []string {
	t.Helper()
	var dirs []string
	seen := map[string]bool{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || len(dirs) >= limit {
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			base := d.Name()
			if base == "testdata" || base == ".git" || strings.HasPrefix(base, "_") {
				return filepath.SkipDir
			}
			ents, err := os.ReadDir(path)
			if err != nil {
				return nil
			}
			for _, e := range ents {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
					if !seen[path] {
						seen[path] = true
						dirs = append(dirs, path)
					}
					break
				}
			}
			return nil
		})
	}
	return dirs
}

func shortDir(dir string) string {
	parts := strings.Split(filepath.ToSlash(dir), "/")
	if len(parts) > 3 {
		return strings.Join(parts[len(parts)-3:], "/")
	}
	return dir
}

func truncate(ss []string) []string {
	if len(ss) > 8 {
		return ss[:8]
	}
	return ss
}
