package depindex

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestParseGoModRequires(t *testing.T) {
	gomod := `module github.com/me/proj

go 1.24.0

require (
	golang.org/x/text v0.33.0
	modernc.org/sqlite v1.37.0 // indirect
)

require github.com/single/dep v1.2.3

replace github.com/broken/thing => ../local/thing

replace (
	github.com/other/one => github.com/fork/one v1.0.0
	example.com/two v1.1.0 => ../two
)
`
	versions, replaced := map[string]string{}, map[string]bool{}
	parseGoModRequires(gomod, versions, replaced)

	want := map[string]string{
		"golang.org/x/text":     "v0.33.0",
		"modernc.org/sqlite":    "v1.37.0",
		"github.com/single/dep": "v1.2.3",
	}
	for mod, ver := range want {
		if versions[mod] != ver {
			t.Errorf("versions[%q] = %q, want %q", mod, versions[mod], ver)
		}
	}
	// A replaced module's source is no longer at the cache path go.mod implies,
	// so indexing it would read the WRONG package — the one case where a
	// perfectly formed lookup produces a wrong answer rather than no answer.
	for _, mod := range []string{"github.com/broken/thing", "github.com/other/one", "example.com/two"} {
		if !replaced[mod] {
			t.Errorf("replaced[%q] = false; a replace directive must force an abstain", mod)
		}
	}
	if got := parseGoModulePath(gomod); got != "github.com/me/proj" {
		t.Errorf("parseGoModulePath = %q", got)
	}
}

func TestEscapeGoModulePath(t *testing.T) {
	// The module cache lowercases capitals behind "!" so that case-insensitive
	// filesystems cannot collide two distinct module paths. Getting this wrong
	// means every module with a capital in its path silently fails to resolve.
	tests := map[string]string{
		"github.com/BurntSushi/toml":       "github.com/!burnt!sushi/toml",
		"github.com/google/uuid":           "github.com/google/uuid",
		"gopkg.in/yaml.v3":                 "gopkg.in/yaml.v3",
		"github.com/Masterminds/semver/v3": "github.com/!masterminds/semver/v3",
	}
	for in, want := range tests {
		if got := escapeGoModulePath(in); got != want {
			t.Errorf("escapeGoModulePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGoIndex_AbstainsWithoutGoMod(t *testing.T) {
	idx := NewGoIndex(t.TempDir())
	ps := idx.Lookup("net/http")
	if ps.Res != Unknown {
		t.Fatalf("Res = %v, want Unknown without a go.mod", ps.Res)
	}
}

func TestGoIndex_AbstainsUnderWorkspace(t *testing.T) {
	// A go.work overlay can redirect any module to a local directory that go.mod
	// knows nothing about. Resolving that properly means reimplementing the go
	// command's module resolution, so the whole index goes inert instead.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/x\n\ngo 1.24\n")
	writeFile(t, filepath.Join(dir, "go.work"), "go 1.24\n\nuse .\n")
	idx := NewGoIndex(dir)
	ps := idx.Lookup("net/http")
	if ps.Res != Unknown || !strings.Contains(ps.Reason, "go.work") {
		t.Fatalf("Res = %v (%q), want Unknown citing go.work", ps.Res, ps.Reason)
	}
}

func TestGoIndex_SameRepoPackagesAreNotThisIndexsJob(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/x\n\ngo 1.24\n")
	idx := NewGoIndex(dir)
	ps := idx.Lookup("example.com/x/internal/thing")
	if ps.Res != Unknown || !strings.Contains(ps.Reason, "same-repo") {
		t.Fatalf("Res = %v (%q), want Unknown citing same-repo", ps.Res, ps.Reason)
	}
}

func TestGoIndex_VendorDirWins(t *testing.T) {
	// With a vendor directory the build uses vendored source, so the index must
	// too — reading the module cache instead could index a different version.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/x\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	writeFile(t, filepath.Join(dir, "vendor", "modules.txt"), "# example.com/dep v1.0.0\n")
	writeFile(t, filepath.Join(dir, "vendor", "example.com", "dep", "dep.go"),
		"package dep\n\nfunc Vendored() {}\n")
	idx := NewGoIndex(dir)
	ps := idx.Lookup("example.com/dep")
	if ps.Res != Resolved {
		t.Fatalf("Res = %v (%q), want Resolved from vendor/", ps.Res, ps.Reason)
	}
	if !ps.Has("Vendored") {
		t.Errorf("vendored export not found: %v", ps.Exports)
	}
}

func TestGoIndex_StdlibResolves(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/x\n\ngo 1.24\n")
	idx := NewGoIndex(dir)
	ps := idx.Lookup("strings")
	if ps.Res != Resolved {
		t.Skipf("stdlib source unavailable here: %s", ps.Reason)
	}
	if !ps.Has("Contains") || !ps.Has("Builder") || !ps.Has("NewReplacer") {
		t.Errorf("strings exports incomplete (n=%d)", len(ps.Exports))
	}
	if ps.Has("Containz") {
		t.Errorf("strings.Containz must not resolve")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGoIndex_ResolvesFromModuleCache exercises the composition that unit tests
// of parseGoModRequires and escapeGoModulePath do not: taking an import path
// through longest-module-prefix matching, the pinned version, and the cache's
// case escaping to an actual directory. Every third-party Go lookup goes this
// way, and each piece being individually correct does not prove the join is.
//
// The capitalized module path is deliberate — it is the case that silently
// resolves nothing if the "!" escaping is dropped.
func TestGoIndex_ResolvesFromModuleCache(t *testing.T) {
	cache := t.TempDir()
	pkgRoot := filepath.Join(cache, "example.com", "!fake!dep@v1.2.3")
	writeFile(t, filepath.Join(pkgRoot, "dep.go"),
		"package dep\n\nfunc Root() {}\n")
	writeFile(t, filepath.Join(pkgRoot, "sub", "sub.go"),
		"package sub\n\nfunc Nested() {}\n\ntype Thing struct{}\n")
	// A second, shorter-prefixed module must not win over the longer one.
	writeFile(t, filepath.Join(cache, "example.com", "!fake@v0.1.0", "x.go"),
		"package fake\n\nfunc WrongPackage() {}\n")

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.com/app\n\ngo 1.24\n\n"+
		"require (\n\texample.com/Fake v0.1.0\n\texample.com/FakeDep v1.2.3\n)\n")

	t.Setenv("GOMODCACHE", cache)
	idx := NewGoIndex(repo)

	root := idx.Lookup("example.com/FakeDep")
	if root.Res != Resolved {
		t.Fatalf("root package Res = %v (%q), want Resolved", root.Res, root.Reason)
	}
	if !root.Has("Root") {
		t.Errorf("Has(\"Root\") = false; exports=%v", root.Exports)
	}
	// Longest-prefix matching: this belongs to example.com/FakeDep, not
	// example.com/Fake, so it must resolve under the v1.2.3 tree.
	sub := idx.Lookup("example.com/FakeDep/sub")
	if sub.Res != Resolved {
		t.Fatalf("sub package Res = %v (%q), want Resolved", sub.Res, sub.Reason)
	}
	if !sub.Has("Nested") || !sub.Has("Thing") {
		t.Errorf("sub exports incomplete: %v", sub.Exports)
	}
	if sub.Has("WrongPackage") {
		t.Errorf("resolved against the shorter module prefix — longest-prefix matching is broken")
	}
	// A version that is not in the cache must abstain, never fall back to
	// whatever else happens to be on disk.
	if miss := idx.Lookup("example.com/Absent"); miss.Res != Unknown {
		t.Errorf("absent module Res = %v, want Unknown", miss.Res)
	}
}

// TestGoIndex_ReplacedModuleAbstainsEndToEnd proves the replace directive stops
// resolution even when a plausible cache directory exists — the case where a
// lookup would otherwise succeed against the WRONG source.
func TestGoIndex_ReplacedModuleAbstainsEndToEnd(t *testing.T) {
	cache := t.TempDir()
	writeFile(t, filepath.Join(cache, "example.com", "dep@v1.0.0", "dep.go"),
		"package dep\n\nfunc Stale() {}\n")

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.com/app\n\ngo 1.24\n\n"+
		"require example.com/dep v1.0.0\n\nreplace example.com/dep => ../local/dep\n")

	t.Setenv("GOMODCACHE", cache)
	idx := NewGoIndex(repo)
	ps := idx.Lookup("example.com/dep")
	if ps.Res != Unknown || !strings.Contains(ps.Reason, "replace") {
		t.Fatalf("Res = %v (%q), want Unknown citing the replace directive", ps.Res, ps.Reason)
	}
}

// TestGoPackageExports_FormerScannerFalsePositives feeds the exact source shapes
// that defeated the hand-written column-zero line scanner this package used
// before go/parser. Every one produced a MISSED export, and a missed export is a
// false positive — the guard flags a symbol that really exists.
//
// They are kept as a regression suite rather than deleted with the scanner: if
// anyone is ever tempted to reintroduce a fast approximate extractor, this is the
// bill of shapes it has to pay.
func TestGoPackageExports_FormerScannerFalsePositives(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			// The scanner required a literal space after the keyword.
			"tab after keyword",
			"package p\n\nfunc\tHandle() {}\n\ntype\tThing int\n\nvar\tValue = 1\n",
			[]string{"Handle", "Thing", "Value"},
		},
		{
			// `var(` with no space failed the "var " prefix test entirely.
			"group with no space before paren",
			"package p\n\nvar(\n\tDefault = 1\n)\n\nconst(\n\tMode = 2\n)\n",
			[]string{"Default", "Mode"},
		},
		{
			// The catastrophic one: initial group depth computed to -1 and was
			// never clamped, so the group never closed and EVERY subsequent
			// declaration in the file was silently swallowed.
			"one-line group",
			"package p\n\nvar (A = 1)\n\nfunc After() {}\n",
			[]string{"A", "After"},
		},
		{
			"group closed on the entry line",
			"package p\n\nvar (\n\tB = 1)\n\nfunc After() {}\n",
			[]string{"B", "After"},
		},
		{
			"group opened with its first entry on the same line",
			"package p\n\nvar (C = 1\n\tD = 2\n)\n\nfunc After() {}\n",
			[]string{"C", "D", "After"},
		},
		{
			// The scanner cut at the first `=` and never looked past `;`.
			"semicolon-separated declarations",
			"package p\n\nvar E = 1; var F = 2\n\nfunc Foo() {}; func Bar() {}\n",
			[]string{"E", "F", "Foo", "Bar"},
		},
		{
			// Valid Go, just not gofmt'd — fatal to a column-zero rule.
			"indented top-level declaration",
			"package p\n\n  func Indented() {}\n",
			[]string{"Indented"},
		},
		{
			// No semicolon is inserted after func/type, so this is one statement.
			"declaration keyword and name on separate lines",
			"package p\n\nfunc\nSplitName() {}\n\ntype\nSplitType int\n",
			[]string{"SplitName", "SplitType"},
		},
		{
			// Struct fields inside a type group are not type declarations. The
			// scanner needed explicit bracket-depth tracking to avoid inventing
			// them; the parser knows structurally.
			"type group containing a struct",
			"package p\n\ntype (\n\tOuter struct {\n\t\tField int\n\t\tOther string\n\t}\n\tSecond int\n)\n",
			[]string{"Outer", "Second"},
		},
		{
			// A raw string whose closing backtick is followed by real code — the
			// bug that made group depth never return to zero.
			"raw string closing mid-line inside a group",
			"package p\n\nvar (\n\tSamples = []string{`\nfunc NotReal() {}\n`, \"x\"}\n\tAfterRaw = 2\n)\n",
			[]string{"Samples", "AfterRaw"},
		},
		{
			"generics",
			"package p\n\nfunc Map[T any, U any](in []T) []U { return nil }\n\ntype Pair[K comparable, V any] struct{ k K }\n",
			[]string{"Map", "Pair"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := GoPackageExports([]string{tt.src})
			if !ok {
				t.Fatalf("GoPackageExports declined valid source")
			}
			for _, name := range tt.want {
				if _, in := got[name]; !in {
					t.Errorf("missing export %q (a miss here is a false positive); got %v", name, keysOf(got))
				}
			}
			// Unexported and nested names must not leak in.
			for _, name := range []string{"Field", "Other", "NotReal", "k"} {
				if _, in := got[name]; in {
					t.Errorf("invented export %q", name)
				}
			}
		})
	}
}

func TestGoPackageExports_UnparseableSourceAbstains(t *testing.T) {
	// One unparseable file discards the whole package: reporting the exports of
	// the files that DID parse would be an under-count, which is the
	// false-positive direction.
	if _, ok := GoPackageExports([]string{"package p\n\nfunc Good() {}\n", "package p\n\nfunc ( broken"}); ok {
		t.Fatal("GoPackageExports returned ok on a package with an unparseable file")
	}
}

func TestParseGoModRequires_WhitespaceSeparators(t *testing.T) {
	// go.mod is hand-edited as often as tool-written, and `go mod edit -json`
	// parses a TAB-separated directive as real. Matching only "replace " missed
	// it, and the module then resolved out of the module CACHE instead of
	// abstaining — returning symbols from the copy the replace had replaced away.
	// The one bug in this file that produced a WRONG answer rather than no answer.
	gomod := "module example.com/me\n\ngo 1.24\n\n" +
		"require\texample.com/a v1.0.0\n" +
		"replace\texample.com/a\t=>\t./a\n" +
		"require(\n\texample.com/b v2.0.0\n)\n"
	versions, replaced := map[string]string{}, map[string]bool{}
	parseGoModRequires(gomod, versions, replaced)

	if !replaced["example.com/a"] {
		t.Errorf("tab-separated replace not detected — the module would resolve from the stale cache copy")
	}
	if versions["example.com/b"] != "v2.0.0" {
		t.Errorf("`require(` group with no space not parsed; versions=%v", versions)
	}
	// A word that merely starts with the keyword is not the keyword.
	notDirective := "module example.com/me\n\nrequirements v1.0.0\nreplacements => x\n"
	v2, r2 := map[string]string{}, map[string]bool{}
	parseGoModRequires(notDirective, v2, r2)
	if len(r2) != 0 {
		t.Errorf("`replacements` was treated as a replace directive: %v", r2)
	}
}

func TestGoIndex_OversizedPackageAbstains(t *testing.T) {
	// Latency is bounded by declining a package on its directory stats, before a
	// byte is read. Declining means Unknown, so the check narrows rather than
	// stalling the editor.
	cache := t.TempDir()
	big := strings.Repeat("// filler comment line to inflate the file size\n", 40000)
	writeFile(t, filepath.Join(cache, "example.com", "huge@v1.0.0", "huge.go"),
		"package huge\n\nfunc Real() {}\n"+big)

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"),
		"module example.com/app\n\ngo 1.24\n\nrequire example.com/huge v1.0.0\n")
	t.Setenv("GOMODCACHE", cache)

	ps := NewGoIndex(repo).Lookup("example.com/huge")
	if ps.Res != Unknown {
		t.Fatalf("Res = %v, want Unknown for an oversized package", ps.Res)
	}
	if ps.Has("Real") {
		t.Error("an abstaining package must report no symbols")
	}
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
