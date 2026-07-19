package depindex

import (
	"os"
	"path/filepath"
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
