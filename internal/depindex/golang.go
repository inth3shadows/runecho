package depindex

import (
	"go/build"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// Go dependency resolution.
//
// Where the Python resolver's hard problem is DYNAMISM (a module's attributes may
// not exist until runtime), Go's is IDENTITY: the same import path resolves to
// different source depending on the module version, a `replace` directive, a
// vendor directory, or a go.work overlay. Indexing the wrong copy of a package is
// a direct false-positive source, so every one of those is either handled exactly
// or made to abstain:
//
//   - go.work present        → Unknown for everything (workspace overlays can
//                              redirect any module; resolving them properly means
//                              reimplementing the go command)
//   - `replace` on a module  → Unknown for that module (the cache path is no
//                              longer where its source lives)
//   - vendor/ present        → resolve from vendor/, which is what the build uses
//   - otherwise              → module cache at the version go.mod pins
//
// Unlike Python there is no venv-discovery problem: go.mod is unambiguous and
// sits in the repo.

// maxGoPackageFiles bounds how many .go files one package directory contributes.
// Real packages are far under this; a directory over it is generated or vendored
// oddly, and reading a prefix of it would produce an export set that is missing
// names — the false-positive direction. So exceeding the cap abstains.
const maxGoPackageFiles = 400

// maxGoFileSize skips a single implausibly large generated file, abstaining
// rather than spending the guard's budget on it.
const maxGoFileSize = 4 << 20

// maxGoBytesPerRun bounds the total Go source one index will scan. Scanning is
// linear in bytes (~6ms for net/http's 945 KB), and some packages are far larger:
// golang.org/x/text/unicode/norm carries ~2.3 MB of generated Unicode tables and
// alone costs ~38ms, three times the guard's whole ~12ms edit-time budget.
//
// The budget is measured in BYTES rather than milliseconds so the verdict stays a
// pure function of the input, the same reasoning as maxResolvesPerRun. A package
// that would blow it resolves Unknown, so the check narrows instead of stalling
// the editor. Sizes come from directory stats, so an oversized package is
// declined before any of it is read.
//
// Sized to admit net/http — the stdlib package most worth validating — while
// declining the generated-table packages nobody calls by hallucinated name.
const maxGoBytesPerRun = 2 << 20

// reGoRequire matches one `require` line, in block or single form, capturing the
// module path and version.
var reGoRequire = regexp.MustCompile(`^\s*(?:require\s+)?([^\s/][^\s]*)\s+(v[^\s]+)`)

// reGoReplace matches a `replace` directive's LEFT-hand module path — the module
// whose source has moved somewhere go.mod alone cannot tell us.
var reGoReplace = regexp.MustCompile(`^\s*(?:replace\s+)?([^\s/][^\s]*)(?:\s+v[^\s]+)?\s*=>`)

// GoIndex resolves import paths to exported symbol sets for one Go module.
// Construct with NewGoIndex; the zero value resolves nothing.
type GoIndex struct {
	modulePath string            // this repo's own module path (never indexed here)
	versions   map[string]string // module path → version, from go.mod
	replaced   map[string]bool   // module paths with a replace directive → abstain
	vendorDir  string            // non-empty when the build uses vendoring
	modCache   string
	goroot     string
	reason     string // why the index is inert ("" when usable)

	mu           sync.Mutex
	cache        map[string]PackageSymbols
	resolves     int
	bytesScanned int
}

// NewGoIndex builds an index for the module governing startDir. It never returns
// nil: with no go.mod, or with a go.work overlay, every Lookup returns Unknown.
func NewGoIndex(startDir string) *GoIndex {
	idx := &GoIndex{
		versions: map[string]string{},
		replaced: map[string]bool{},
		cache:    map[string]PackageSymbols{},
		modCache: goModCacheDir(),
		goroot:   goRootDir(),
	}
	root := findGoModRoot(startDir)
	if root == "" {
		idx.reason = "no go.mod found"
		return idx
	}
	// A go.work overlay can redirect any module in the workspace to a local
	// directory that go.mod knows nothing about. Rather than reimplement its
	// resolution, refuse to index at all — a wrong package is worse than none.
	if workRoot := findFileUpward(startDir, "go.work"); workRoot != "" {
		idx.reason = "go.work workspace at " + workRoot + " can redirect modules"
		return idx
	}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		idx.reason = "go.mod unreadable: " + err.Error()
		return idx
	}
	idx.modulePath = parseGoModulePath(string(data))
	parseGoModRequires(string(data), idx.versions, idx.replaced)

	if isFile(filepath.Join(root, "vendor", "modules.txt")) {
		idx.vendorDir = filepath.Join(root, "vendor")
	}
	return idx
}

// Lookup resolves a Go import path to the exported names of that package.
func (idx *GoIndex) Lookup(importPath string) PackageSymbols {
	if idx.reason != "" {
		return unknown("%s", idx.reason)
	}
	idx.mu.Lock()
	if ps, ok := idx.cache[importPath]; ok {
		idx.mu.Unlock()
		return ps
	}
	if idx.resolves >= maxResolvesPerRun {
		idx.mu.Unlock()
		return unknown("resolve budget exhausted (%d packages)", maxResolvesPerRun)
	}
	idx.resolves++
	idx.mu.Unlock()

	ps := idx.resolve(importPath)

	idx.mu.Lock()
	idx.cache[importPath] = ps
	idx.mu.Unlock()
	return ps
}

// resolve locates the package's source directory and scans its exports.
func (idx *GoIndex) resolve(importPath string) PackageSymbols {
	dir, ps, ok := idx.packageDir(importPath)
	if !ok {
		return ps
	}
	size, ok, reason := goPackageSourceSize(dir)
	if !ok {
		return unknown("%s: %s", importPath, reason)
	}
	if !idx.spendBytes(size) {
		return unknown("%s: would exceed the %d-byte scan budget for this run", importPath, maxGoBytesPerRun)
	}
	sources, ok, reason := readGoPackageSources(dir)
	if !ok {
		return unknown("%s: %s", importPath, reason)
	}
	if len(sources) == 0 {
		return unknown("%s: no Go source in %s", importPath, dir)
	}
	exports, ok := GoPackageExports(sources)
	if !ok {
		return partial("%s: source could not be scanned confidently", importPath)
	}
	return PackageSymbols{Res: Resolved, Exports: exports}
}

// packageDir maps an import path to the directory holding its source.
func (idx *GoIndex) packageDir(importPath string) (string, PackageSymbols, bool) {
	if importPath == "" || strings.Contains(importPath, "..") {
		return "", unknown("malformed import path %q", importPath), false
	}
	// The repo's own packages are the same-repo case, already covered by
	// GoQualifiedViolations against the repo's IR. Not this index's job.
	if idx.modulePath != "" && (importPath == idx.modulePath ||
		strings.HasPrefix(importPath, idx.modulePath+"/")) {
		return "", unknown("%s is a same-repo package", importPath), false
	}

	// Standard library: no dot in the first path element (net/http, encoding/json).
	first := importPath
	if i := strings.IndexByte(first, '/'); i >= 0 {
		first = first[:i]
	}
	if !strings.Contains(first, ".") {
		if idx.goroot == "" {
			return "", unknown("GOROOT unknown; cannot resolve stdlib %q", importPath), false
		}
		dir := filepath.Join(idx.goroot, "src", filepath.FromSlash(importPath))
		if !isDir(dir) {
			return "", unknown("%s: not found in GOROOT/src", importPath), false
		}
		return dir, PackageSymbols{}, true
	}

	// Vendored builds use vendor/ and nothing else.
	if idx.vendorDir != "" {
		dir := filepath.Join(idx.vendorDir, filepath.FromSlash(importPath))
		if !isDir(dir) {
			return "", unknown("%s: not vendored", importPath), false
		}
		return dir, PackageSymbols{}, true
	}

	// Longest module prefix wins: import path github.com/a/b/sub belongs to
	// module github.com/a/b, not github.com/a.
	modPath, version := idx.moduleFor(importPath)
	if modPath == "" {
		return "", unknown("%s: no module in go.mod provides it", importPath), false
	}
	if idx.replaced[modPath] {
		return "", unknown("%s: module %s has a replace directive", importPath, modPath), false
	}
	if idx.modCache == "" {
		return "", unknown("module cache location unknown"), false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(importPath, modPath), "/")
	dir := filepath.Join(idx.modCache, filepath.FromSlash(escapeGoModulePath(modPath)+"@"+version))
	if rest != "" {
		dir = filepath.Join(dir, filepath.FromSlash(rest))
	}
	if !isDir(dir) {
		return "", unknown("%s: not in module cache (%s)", importPath, dir), false
	}
	return dir, PackageSymbols{}, true
}

// moduleFor returns the longest module path in go.mod that prefixes importPath.
func (idx *GoIndex) moduleFor(importPath string) (string, string) {
	best, bestVer := "", ""
	for mod, ver := range idx.versions {
		if importPath != mod && !strings.HasPrefix(importPath, mod+"/") {
			continue
		}
		if len(mod) > len(best) {
			best, bestVer = mod, ver
		}
	}
	return best, bestVer
}

// spendBytes claims n bytes of the per-run scan budget, reporting false when the
// budget cannot cover it. Checked BEFORE reading, so an oversized package costs a
// directory stat rather than a multi-megabyte read.
func (idx *GoIndex) spendBytes(n int) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if idx.bytesScanned+n > maxGoBytesPerRun {
		return false
	}
	idx.bytesScanned += n
	return true
}

// goPackageSourceSize totals the bytes of a package directory's non-test .go
// files without reading them, so the scan budget can be checked up front.
func goPackageSourceSize(dir string) (int, bool, string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, false, "unreadable: " + err.Error()
	}
	total, count := 0, 0
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		count++
		if count > maxGoPackageFiles {
			return 0, false, "package has more source files than the cap allows"
		}
		info, err := e.Info()
		if err != nil {
			return 0, false, "stat failed for " + name
		}
		if info.Size() > maxGoFileSize {
			return 0, false, name + " exceeds the size cap"
		}
		total += int(info.Size())
	}
	return total, true, ""
}

// readGoPackageSources reads a package directory's non-test .go files. It refuses
// (ok=false) rather than returning a subset, because a partial file list yields a
// partial export set, which is the false-positive direction.
func readGoPackageSources(dir string) ([]string, bool, string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, "unreadable: " + err.Error()
	}
	var sources []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if len(sources) >= maxGoPackageFiles {
			return nil, false, "package has more source files than the cap allows"
		}
		info, err := e.Info()
		if err != nil {
			return nil, false, "stat failed for " + name
		}
		if info.Size() > maxGoFileSize {
			return nil, false, name + " exceeds the size cap"
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, false, "read failed for " + name
		}
		sources = append(sources, string(data))
	}
	return sources, true, ""
}

// parseGoModulePath extracts the `module` directive's path.
func parseGoModulePath(gomod string) string {
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module"); ok {
			path := strings.Trim(strings.TrimSpace(rest), `"`)
			if i := strings.Index(path, "//"); i >= 0 {
				path = strings.TrimSpace(path[:i])
			}
			if path != "" {
				return path
			}
		}
	}
	return ""
}

// parseGoModRequires fills versions from `require` directives and replaced from
// `replace` directives, handling both the single-line and parenthesized forms.
//
// Deliberately hand-parsed rather than shelling out to `go list`: a subprocess
// costs tens of milliseconds the guard does not have, and go.mod's grammar for
// these two directives is small enough to read directly.
func parseGoModRequires(gomod string, versions map[string]string, replaced map[string]bool) {
	block := ""
	for _, raw := range strings.Split(gomod, "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		switch {
		case block != "" && line == ")":
			block = ""
			continue
		case block == "" && (line == "require (" || line == "require("):
			block = "require"
			continue
		case block == "" && (line == "replace (" || line == "replace("):
			block = "replace"
			continue
		}
		isReplace := block == "replace" || strings.HasPrefix(line, "replace ")
		if isReplace {
			if m := reGoReplace.FindStringSubmatch(line); m != nil {
				replaced[m[1]] = true
			}
			continue
		}
		if block == "require" || strings.HasPrefix(line, "require ") {
			if m := reGoRequire.FindStringSubmatch(line); m != nil {
				versions[m[1]] = m[2]
			}
		}
	}
}

// escapeGoModulePath applies the module cache's case encoding: every uppercase
// letter becomes "!" + its lowercase form, so github.com/BurntSushi/toml lives at
// github.com/!burnt!sushi/toml. Without this, any module with a capital letter in
// its path silently fails to resolve.
func escapeGoModulePath(path string) string {
	var b strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c >= 'A' && c <= 'Z' {
			b.WriteByte('!')
			b.WriteByte(c - 'A' + 'a')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// findGoModRoot walks up from startDir to the directory containing go.mod.
func findGoModRoot(startDir string) string { return findFileUpward(startDir, "go.mod") }

// findFileUpward returns the nearest ancestor directory (starting at dir) that
// contains name, or "".
//
// dir is made absolute first: filepath.Dir(".") is ".", so a relative start would
// terminate the walk on its first step and silently find nothing.
func findFileUpward(dir, name string) string {
	dir = absDir(dir)
	for i := 0; i < maxVenvWalk; i++ {
		if isFile(filepath.Join(dir, name)) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// goModCacheDir resolves the module cache without shelling out to `go env`,
// which would cost tens of milliseconds the guard does not have. The precedence
// mirrors cmd/go: GOMODCACHE, then GOPATH/pkg/mod, then the default GOPATH.
func goModCacheDir() string {
	if mc := os.Getenv("GOMODCACHE"); mc != "" {
		return mc
	}
	if gp := os.Getenv("GOPATH"); gp != "" {
		if i := strings.IndexByte(gp, filepath.ListSeparator); i >= 0 {
			gp = gp[:i] // GOPATH may be a list; cmd/go uses the first entry
		}
		return filepath.Join(gp, "pkg", "mod")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "go", "pkg", "mod")
}

// goRootDir resolves GOROOT from the environment, falling back to the value the
// running binary was built with.
func goRootDir() string {
	if gr := os.Getenv("GOROOT"); gr != "" {
		return gr
	}
	if build.Default.GOROOT != "" {
		return build.Default.GOROOT
	}
	return runtime.GOROOT()
}
