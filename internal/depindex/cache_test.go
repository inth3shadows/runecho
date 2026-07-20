package depindex

import (
	"os"
	"path/filepath"
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
