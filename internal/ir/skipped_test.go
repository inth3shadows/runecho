package ir

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/parser"
)

// registeredParsers mirrors the set wired in NewGenerator. If a parser is added
// there, add it here too — TestKnownSourceExtensionsAreNotParsed is the reason
// this duplicate exists, and it is only load-bearing if it stays in sync.
func registeredParsers() []parser.Parser {
	return NewGenerator(GeneratorConfig{}).parsers
}

// TestKnownSourceExtensionsAreNotParsed pins the table's one invariant: an entry
// in knownSourceExtensions must be an extension NO registered parser handles.
//
// A stale entry is worse than a missing one. Missing means a blind spot goes
// unreported — the behaviour that existed before this feature. Stale means a
// file that WAS indexed gets reported as unexamined, so a consumer that fails
// closed on the skip list blocks a change the indexer actually inspected. That
// is a false block, and it would be blamed on the gate rather than on this table.
func TestKnownSourceExtensionsAreNotParsed(t *testing.T) {
	for ext := range knownSourceExtensions {
		for _, p := range registeredParsers() {
			if p.SupportsExtension(ext) {
				t.Errorf("%q is in knownSourceExtensions but %T parses it — delete the entry, "+
					"or the indexer will report indexed files as unexamined", ext, p)
			}
		}
	}
}

// TestKnownSourceExtensionsAreLowercase pins the storage form the case-insensitive
// lookup assumes: IsKnownSourceExtension lowercases its argument, so an uppercase
// KEY here could never be hit.
func TestKnownSourceExtensionsAreLowercase(t *testing.T) {
	for ext := range knownSourceExtensions {
		if ext != strings.ToLower(ext) {
			t.Errorf("key %q is not lowercase — IsKnownSourceExtension lowercases its input, so this entry is dead", ext)
		}
		if !strings.HasPrefix(ext, ".") {
			t.Errorf("key %q has no leading dot — filepath.Ext always returns one, so this entry is dead", ext)
		}
	}
}

func TestIsKnownSourceExtensionIsCaseInsensitive(t *testing.T) {
	for _, ext := range []string{".java", ".JAVA", ".Java", ".R", ".S", ".C"} {
		if !IsKnownSourceExtension(ext) {
			t.Errorf("IsKnownSourceExtension(%q) = false, want true", ext)
		}
	}
	for _, ext := range []string{".md", ".png", ".json", ".yaml", ".txt", ".lock", ""} {
		if IsKnownSourceExtension(ext) {
			t.Errorf("IsKnownSourceExtension(%q) = true, want false — non-code must never be reported "+
				"as a blind spot, or a docs-only change looks unexamined", ext)
		}
	}
	// The supported languages must not appear: they ARE indexed, so reporting
	// them would be the stale-entry failure TestKnownSourceExtensionsAreNotParsed
	// guards, restated at the lookup.
	for _, ext := range []string{".go", ".py", ".js", ".ts", ".rs", ".rb", ".sh"} {
		if IsKnownSourceExtension(ext) {
			t.Errorf("IsKnownSourceExtension(%q) = true — that language is indexed", ext)
		}
	}
}

// skipMap indexes a Stats skip list by path for assertions.
func skipMap(t *testing.T, s Stats) map[string]string {
	t.Helper()
	m := make(map[string]string, len(s.Skipped))
	for _, sk := range s.Skipped {
		if prev, dup := m[sk.Path]; dup {
			t.Errorf("path %q recorded twice (%q then %q); a walk visits each path once", sk.Path, prev, sk.Reason)
		}
		m[sk.Path] = sk.Reason
	}
	return m
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestUnsupportedLanguageIsRecorded is the issue this whole feature exists for:
// a Java file is absent from the IR at exit 0 with empty stderr, so a function
// deleted from it is invisible. It must now be named.
func TestUnsupportedLanguageIsRecorded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc A() {}\n")
	writeFile(t, dir, "Widget.java", "class Widget { public int alive(){return 1;} }\n")
	writeFile(t, dir, "README.md", "# docs\n")
	writeFile(t, dir, "config.yaml", "a: 1\n")

	_, stats, err := NewGenerator(GeneratorConfig{}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats)

	if got["Widget.java"] != SkipUnsupportedLanguage {
		t.Errorf("Widget.java: got reason %q, want %q", got["Widget.java"], SkipUnsupportedLanguage)
	}
	// The other half of the contract, and the reason a consumer can fail closed
	// on this list at all: non-code is NOT reported. If README.md appeared here,
	// a gate that blocks on skipped paths would block every documentation change.
	for _, quiet := range []string{"README.md", "config.yaml", "main.go"} {
		if reason, reported := got[quiet]; reported {
			t.Errorf("%s must not be reported as skipped (got %q)", quiet, reason)
		}
	}
	if stats.SkippedTruncated {
		t.Error("four files cannot truncate a 1000-entry list")
	}
}

// TestUnsupportedLanguageIsRecordedOnUpdate pins the Update path too. Update and
// Generate are separate walks with separate closures, and the auto-fresh hooks
// run Update — a skip list that only worked on a cold index would be silently
// empty for every incremental reindex.
func TestUnsupportedLanguageIsRecordedOnUpdate(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc A() {}\n")
	writeFile(t, dir, "Widget.java", "class Widget { }\n")

	g := NewGenerator(GeneratorConfig{})
	first, _, err := g.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, stats, err := g.Update(first, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := skipMap(t, stats); got["Widget.java"] != SkipUnsupportedLanguage {
		t.Errorf("Update: Widget.java got reason %q, want %q", got["Widget.java"], SkipUnsupportedLanguage)
	}
}

// TestIgnoredDirIsRecordedAsTheDirectory: filepath.SkipDir means the walk never
// descends, so the contents cannot be enumerated without defeating the pruning.
// The directory itself is the entry, and a consumer must prefix-match.
func TestIgnoredDirIsRecordedAsTheDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc A() {}\n")
	writeFile(t, dir, "vendor/dep/dep.go", "package dep\nfunc B() {}\n")
	writeFile(t, dir, "node_modules/pkg/index.js", "export function C() {}\n")

	_, stats, err := NewGenerator(GeneratorConfig{}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats)

	for _, d := range []string{"vendor", "node_modules"} {
		if got[d] != SkipIgnoredDir {
			t.Errorf("%s: got reason %q, want %q", d, got[d], SkipIgnoredDir)
		}
	}
	// Contents must NOT be listed — the walk never saw them, and claiming
	// otherwise would imply an enumeration that did not happen.
	for _, inside := range []string{"vendor/dep/dep.go", "node_modules/pkg/index.js"} {
		if _, reported := got[inside]; reported {
			t.Errorf("%s listed individually; SkipDir means the walk never descended", inside)
		}
	}
}

// TestParseFailureReasonClassifies pins parseFailureReason directly.
//
// Tested as a unit rather than through a deliberately-corrupt file because the
// parsers are tree-sitter-based and error-tolerant: garbage bytes in a .go file
// still yield a tree, so a "broken source" fixture asserts nothing and quietly
// turns into a t.Skip. The classifier's two branches are the actual contract.
func TestParseFailureReasonClassifies(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "small.go", "package p\n")
	writeFile(t, dir, "big.go", "package p\n"+strings.Repeat("// filler\n", 200))

	g := NewGenerator(GeneratorConfig{})
	g.maxParseBytes = 64

	if got := g.parseFailureReason(filepath.Join(dir, "big.go")); got != SkipOversized {
		t.Errorf("oversized file classified %q, want %q", got, SkipOversized)
	}
	if got := g.parseFailureReason(filepath.Join(dir, "small.go")); got != SkipParseError {
		t.Errorf("in-budget file classified %q, want %q", got, SkipParseError)
	}
	// A path that vanished mid-walk: all the caller may conclude is "not
	// indexed", and claiming "oversized" about a file we cannot stat would be a
	// fabricated diagnosis.
	if got := g.parseFailureReason(filepath.Join(dir, "gone.go")); got != SkipParseError {
		t.Errorf("unstatable file classified %q, want %q", got, SkipParseError)
	}
}

// TestUnreadableFileIsRecorded exercises the same branch end to end through a
// real walk, via the one parse failure that is reliably reproducible.
func TestUnreadableFileIsRecorded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still readable, so the parse failure cannot be provoked")
	}
	dir := t.TempDir()
	writeFile(t, dir, "ok.go", "package p\nfunc A() {}\n")
	locked := filepath.Join(dir, "locked.go")
	writeFile(t, dir, "locked.go", "package p\nfunc B() {}\n")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	g := NewGenerator(GeneratorConfig{})
	g.warn = func(string, ...any) {} // silence the expected diagnostic
	irData, stats, err := g.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, indexed := irData.Files["locked.go"]; indexed {
		t.Fatal("locked.go was indexed despite mode 0000 — the fixture no longer provokes a failure")
	}
	if got := skipMap(t, stats); got["locked.go"] != SkipParseError {
		t.Errorf("locked.go: got reason %q, want %q", got["locked.go"], SkipParseError)
	}
}

// TestOversizedIsRecorded pins parseFailureReason's classification. Generate has
// no separate size guard — parseFile rejects the file — so without the re-stat
// an oversized file would be reported as a parse error and send the operator
// looking for a syntax bug in a generated blob.
func TestOversizedIsRecorded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "huge.go", "package p\n"+strings.Repeat("// filler\n", 2000))

	g := NewGenerator(GeneratorConfig{})
	g.maxParseBytes = 100
	g.warn = func(string, ...any) {}

	_, stats, err := g.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := skipMap(t, stats); got["huge.go"] != SkipOversized {
		t.Errorf("Generate: huge.go got reason %q, want %q", got["huge.go"], SkipOversized)
	}

	// The Update path guards size explicitly before hashing; it must reach the
	// same reason by the other route.
	empty := &IR{Version: IRVersion, Files: map[string]FileIR{}}
	_, upStats, err := g.Update(empty, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := skipMap(t, upStats); got["huge.go"] != SkipOversized {
		t.Errorf("Update: huge.go got reason %q, want %q", got["huge.go"], SkipOversized)
	}
}

// TestCapReachedIsRecorded: FileCap stops parse work but keeps counting, so the
// unparsed remainder is a real blind spot the coverage percentage already
// implies. Naming the paths makes it actionable.
func TestCapReachedIsRecorded(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		writeFile(t, dir, fmt.Sprintf("f%d.go", i), fmt.Sprintf("package p\nfunc F%d() {}\n", i))
	}
	_, stats, err := NewGenerator(GeneratorConfig{FileCap: 2}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	capped := 0
	for _, sk := range stats.Skipped {
		if sk.Reason == SkipCapReached {
			capped++
		}
	}
	if capped != 3 {
		t.Errorf("cap 2 of 5 files: got %d cap_reached entries, want 3 (skips=%v)", capped, stats.Skipped)
	}
}

// TestSkipListTruncatesLoudly: absence from a truncated list must not read as
// "indexed". The flag is the only thing separating those two, so it is pinned.
func TestSkipListTruncatesLoudly(t *testing.T) {
	dir := t.TempDir()
	for i := range MaxRecordedSkips + 25 {
		writeFile(t, dir, fmt.Sprintf("C%d.java", i), "class C { }\n")
	}
	_, stats, err := NewGenerator(GeneratorConfig{}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Skipped) != MaxRecordedSkips {
		t.Errorf("skip list length = %d, want the cap %d", len(stats.Skipped), MaxRecordedSkips)
	}
	if !stats.SkippedTruncated {
		t.Error("SkippedTruncated = false on a capped list — a consumer would read the missing " +
			"25 paths as 'indexed', which is the fail-open this flag exists to prevent")
	}
}

// TestSkipListIsSorted: the payload is a machine contract whose bytes get
// compared, and runecho's determinism guarantee covers it.
func TestSkipListIsSorted(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"z.java", "a.php", "m.c", "b/inner.kt"} {
		writeFile(t, dir, name, "// x\n")
	}
	_, stats, err := NewGenerator(GeneratorConfig{}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, len(stats.Skipped))
	for i, sk := range stats.Skipped {
		paths[i] = sk.Path
	}
	if !sort.StringsAreSorted(paths) {
		t.Errorf("skip list is not sorted by path: %v", paths)
	}
}

// TestNoSkipsMeansEmptyNotNil is the shape half of the contract: a fully indexed
// repo reports an empty list, never a missing one, so "we looked and found
// nothing" stays distinguishable at the type level.
func TestNoSkipsOnAFullyIndexedRepo(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc A() {}\n")
	writeFile(t, dir, "README.md", "# docs\n")

	_, stats, err := NewGenerator(GeneratorConfig{}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Skipped) != 0 {
		t.Errorf("expected no skips, got %v", stats.Skipped)
	}
	if stats.SkippedTruncated {
		t.Error("SkippedTruncated set with nothing skipped")
	}
}
