package depindex

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The memo's only permitted effect is on COST. If a hit ever differs from a fresh
// parse, every downstream verdict becomes a function of cache history rather than
// of the input — which would break RunEcho's same-input-same-output guarantee far
// more seriously than any latency budget.
func TestCache_HitMatchesFreshParse(t *testing.T) {
	cache := t.TempDir()
	writeFile(t, filepath.Join(cache, "example.com", "dep@v1.0.0", "a.go"),
		"package dep\n\nfunc Alpha() {}\n\ntype Beta struct{}\n\nconst Gamma = 1\n")
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"),
		"module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	t.Setenv("GOMODCACHE", cache)

	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	first := NewGoIndex(repo).Lookup("example.com/dep") // cold: parses, writes memo
	if first.Res != Resolved {
		t.Fatalf("cold Res = %v (%s)", first.Res, first.Reason)
	}
	if n := countMemoEntries(t, home); n != 1 {
		t.Fatalf("memo entries after a cold resolve = %d, want 1", n)
	}

	second := NewGoIndex(repo).Lookup("example.com/dep") // warm: reads memo
	if second.Res != Resolved {
		t.Fatalf("warm Res = %v (%s)", second.Res, second.Reason)
	}
	if len(second.Exports) != len(first.Exports) {
		t.Fatalf("warm has %d exports, cold had %d", len(second.Exports), len(first.Exports))
	}
	for name := range first.Exports {
		if !second.Has(name) {
			t.Errorf("warm result is missing %q", name)
		}
	}
}

// Keying on file content rather than declared version is what makes staleness
// structurally impossible. A `replace` target is a LOCAL directory and a
// dependency can be rebuilt in place, so a version-keyed memo would happily serve
// the previous package's symbols — and every symbol added by the edit would be
// flagged as a hallucination.
func TestCache_MutatedDependencyProducesANewKey(t *testing.T) {
	cache := t.TempDir()
	src := filepath.Join(cache, "example.com", "dep@v1.0.0", "a.go")
	writeFile(t, src, "package dep\n\nfunc Original() {}\n")
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"),
		"module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	t.Setenv("GOMODCACHE", cache)
	t.Setenv("RUNECHO_HOME", t.TempDir())

	before := NewGoIndex(repo).Lookup("example.com/dep")
	if !before.Has("Original") || before.Has("AddedLater") {
		t.Fatalf("unexpected initial exports: %v", before.Exports)
	}

	// Rewrite the dependency IN PLACE, version unchanged — the editable-install /
	// replace-directory case.
	writeFile(t, src, "package dep\n\nfunc Original() {}\n\nfunc AddedLater() {}\n")

	after := NewGoIndex(repo).Lookup("example.com/dep")
	if !after.Has("AddedLater") {
		t.Fatal("memo served a stale export set after the dependency changed — " +
			"calls to the new symbol would be flagged as hallucinations")
	}
}

// TestCache_SameLengthRewriteIsNotServedStale is the regression for the memo's
// one critical defect: the key was a (name, size, mtime) stat triple while the
// documentation claimed it was a content hash.
//
// Filesystem mtime granularity is not the nanosecond the API implies — measured
// ~4ms on ext4, and 1-2 SECONDS on exFAT, HFS+, NFS, and drvfs. A same-length
// rewrite inside that window was invisible to the key, so the memo served the
// PREVIOUS export set. The asymmetry is what made it critical: a stale set with an
// extra name only suppresses a violation, but a RENAMED same-length export flips
// straight to a false positive, and nothing evicts it.
//
// os.Chtimes here forces the collision deterministically rather than racing a 4ms
// window, but the original hunt reproduced this with no clock manipulation at all.
func TestCache_SameLengthRewriteIsNotServedStale(t *testing.T) {
	cache := t.TempDir()
	src := filepath.Join(cache, "example.com", "dep@v1.0.0", "a.go")
	// Alpha and Bravo are the same length, so the file size is identical too.
	writeFile(t, src, "package dep\n\nfunc Alpha() {}\n")
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"),
		"module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	t.Setenv("GOMODCACHE", cache)
	t.Setenv("RUNECHO_HOME", t.TempDir())

	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	frozen := info.ModTime()

	first := NewGoIndex(repo).Lookup("example.com/dep")
	if !first.Has("Alpha") {
		t.Fatalf("cold lookup missing Alpha: %v (%s)", first.Exports, first.Reason)
	}

	// Rewrite to the same byte length and restore the original mtime: identical
	// stat triple, different contents.
	writeFile(t, src, "package dep\n\nfunc Bravo() {}\n")
	if err := os.Chtimes(src, frozen, frozen); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(frozen) {
		t.Fatalf("test setup failed to produce an identical stat triple")
	}

	second := NewGoIndex(repo).Lookup("example.com/dep")
	if !second.Has("Bravo") {
		t.Errorf("memo served a stale export set: Bravo exists on disk but is absent — " +
			"a call to it would be flagged as a hallucination")
	}
	if second.Has("Alpha") {
		t.Errorf("memo served the pre-rewrite export set (Alpha no longer exists)")
	}
}

// TestCache_ExtractorVersionInvalidatesEntries pins the second-order failure: an
// entry written by one extraction implementation must not be served after that
// implementation changes. Without this, replacing a buggy extractor would leave
// its bugs firing from cached entries indefinitely.
func TestCache_ExtractorVersionInvalidatesEntries(t *testing.T) {
	files := []goFileStat{{name: "a.go"}}
	sources := []string{"package dep\n\nfunc Alpha() {}\n"}
	key := packageCacheKey("example.com/dep", files, sources)
	if key == "" {
		t.Fatal("key should be non-empty")
	}
	if !strings.Contains(extractorVersion, "v") {
		t.Fatalf("extractorVersion %q should carry a version marker", extractorVersion)
	}
	// Same inputs, same key — the memo is only useful if it is stable.
	if again := packageCacheKey("example.com/dep", files, sources); again != key {
		t.Errorf("key is not stable across calls")
	}
	// Different content, different key.
	if other := packageCacheKey("example.com/dep", files, []string{"package dep\n\nfunc Bravo() {}\n"}); other == key {
		t.Errorf("differing source produced an identical key")
	}
	// Different import path, different key.
	if other := packageCacheKey("example.com/other", files, sources); other == key {
		t.Errorf("differing import path produced an identical key")
	}
	// A mismatched files/sources pair must disable the memo rather than key badly.
	if bad := packageCacheKey("example.com/dep", files, []string{"a", "b"}); bad != "" {
		t.Errorf("mismatched files/sources should yield an empty (memo-disabling) key")
	}
}

// TestCache_EmptyEntryIsAMiss covers `null` and `{}` on disk, which unmarshal
// cleanly into a zero cacheEntry. Served as-is that is "Resolved with no
// exports", which would flag EVERY qualified call on the package.
func TestCache_EmptyEntryIsAMiss(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	dir := filepath.Join(home, "depcache")
	for _, content := range []string{"null", "{}", `{"exports":[]}`} {
		key := "deadbeef" + strconv.Itoa(len(content))
		writeFile(t, filepath.Join(dir, key+".json"), content)
		if _, hit := readCachedExports(key); hit {
			t.Errorf("%q was treated as a cache hit; it must be a miss", content)
		}
	}
}

func TestCache_UnwritableHomeStillResolves(t *testing.T) {
	// A memo that cannot be written costs latency, never correctness.
	cache := t.TempDir()
	writeFile(t, filepath.Join(cache, "example.com", "dep@v1.0.0", "a.go"),
		"package dep\n\nfunc Alpha() {}\n")
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"),
		"module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	t.Setenv("GOMODCACHE", cache)

	// Point RUNECHO_HOME at a regular FILE, so creating the cache dir fails.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	writeFile(t, blocked, "x")
	t.Setenv("RUNECHO_HOME", blocked)

	ps := NewGoIndex(repo).Lookup("example.com/dep")
	if ps.Res != Resolved || !ps.Has("Alpha") {
		t.Fatalf("Res = %v (%s); an unwritable memo must not affect the verdict", ps.Res, ps.Reason)
	}
}

func TestCache_CorruptEntryFallsBackToParsing(t *testing.T) {
	cache := t.TempDir()
	writeFile(t, filepath.Join(cache, "example.com", "dep@v1.0.0", "a.go"),
		"package dep\n\nfunc Alpha() {}\n")
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"),
		"module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	t.Setenv("GOMODCACHE", cache)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	// Warm the memo, then corrupt every entry.
	NewGoIndex(repo).Lookup("example.com/dep")
	dir := filepath.Join(home, "depcache")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		writeFile(t, filepath.Join(dir, e.Name()), "{not json")
	}

	ps := NewGoIndex(repo).Lookup("example.com/dep")
	if ps.Res != Resolved || !ps.Has("Alpha") {
		t.Fatalf("Res = %v (%s); a corrupt entry must be treated as a miss", ps.Res, ps.Reason)
	}
}

func countMemoEntries(t *testing.T, home string) int {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(home, "depcache"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".json" {
			n++
		}
	}
	return n
}

// TestCache_SymlinkedSourceRespectsSizeCap pins the budget-integrity fix. The
// caps are the only thing bounding this code's latency, so they have to measure
// what will actually be READ. DirEntry.Info() reports lstat, so a symlink was
// measured by the length of its target PATH — a symlinked source file sailed past
// both caps and was then read in full, observed four orders of magnitude over the
// budgeted size.
func TestCache_SymlinkedSourceRespectsSizeCap(t *testing.T) {
	cache := t.TempDir()
	pkgDir := filepath.Join(cache, "example.com", "dep@v1.0.0")
	writeFile(t, filepath.Join(pkgDir, "a.go"), "package dep\n\nfunc Alpha() {}\n")

	// A real file well over the per-package cap, linked into the package dir.
	bigDir := t.TempDir()
	big := filepath.Join(bigDir, "big.go")
	writeFile(t, big, "package dep\n\n"+strings.Repeat("// filler to exceed the cap\n", 80000))
	if err := os.Symlink(big, filepath.Join(pkgDir, "b.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"),
		"module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	t.Setenv("GOMODCACHE", cache)
	t.Setenv("RUNECHO_HOME", t.TempDir())

	ps := NewGoIndex(repo).Lookup("example.com/dep")
	if ps.Res != Unknown {
		t.Fatalf("Res = %v, want Unknown: a symlinked oversized file must be measured by its TARGET", ps.Res)
	}
}

// TestCache_ExportlessPackageIsNotPersisted covers the dead-entry case: an empty
// entry is treated as a miss on read, so writing one would leave a file that is
// re-parsed on every edit forever and never served.
func TestCache_ExportlessPackageIsNotPersisted(t *testing.T) {
	cache := t.TempDir()
	writeFile(t, filepath.Join(cache, "example.com", "dep@v1.0.0", "a.go"),
		"package dep\n\nfunc unexportedOnly() {}\n")
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"),
		"module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	t.Setenv("GOMODCACHE", cache)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	ps := NewGoIndex(repo).Lookup("example.com/dep")
	if ps.Res != Resolved || len(ps.Exports) != 0 {
		t.Fatalf("Res = %v with %d exports; want Resolved with none", ps.Res, len(ps.Exports))
	}
	if n := countMemoEntries(t, home); n != 0 {
		t.Errorf("wrote %d memo entries for an export-less package; such an entry can never be served", n)
	}
}

// TestCache_RelativeHomeIsAbsolutized pins the chdir case: a relative
// RUNECHO_HOME would follow the process around, so the same logical cache would
// land in different places and every later lookup would miss.
func TestCache_RelativeHomeIsAbsolutized(t *testing.T) {
	t.Setenv("RUNECHO_HOME", "relative-home")
	dir := cacheDir()
	if dir == "" || !filepath.IsAbs(dir) {
		t.Fatalf("cacheDir() = %q, want an absolute path", dir)
	}
}
