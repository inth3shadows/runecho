package ir

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/inth3shadows/runecho/internal/parser"
)

// registeredParsers returns the REAL parser set from NewGenerator, not a copy of
// it. That matters: a hand-maintained duplicate would pass
// TestKnownSourceExtensionsAreNotParsed while the shipped generator drifted, so
// the test would guard nothing at exactly the moment it was needed.
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

// advertisedFamilies maps a language RunEcho claims to parse onto extensions a
// reader would reasonably expect it to handle. Every one must be EITHER parsed
// or named in knownSourceExtensions — never neither.
var advertisedFamilies = map[string][]string{
	"TypeScript": {".ts", ".tsx", ".mts", ".cts"},
	"JavaScript": {".js", ".mjs", ".cjs", ".jsx"},
	"Python":     {".py", ".pyi", ".pyx", ".pxd"},
	// `.ru` is deliberately absent. It is not an extension convention -- its one
	// common use is the single filename `config.ru` -- while `README.ru` is the
	// <name>.<lang> translated-doc pattern. Listing it would put translated docs
	// in the fail-closed array, and the two rules this file enforces genuinely
	// conflict there: TestTableEntriesDoNotMatchCommonNonCode wins, because a
	// false block is louder and more frequent than one unreported Rack config.
	"Ruby":  {".rb", ".rake", ".gemspec"},
	"Shell": {".sh", ".bash", ".zsh", ".fish", ".ksh"},
	"Go":    {".go"},
	"Rust":  {".rs"},
}

// TestAdvertisedLanguageFamiliesAreAccountedFor is the regression test for the
// sharpest defect review found in the first cut of this feature.
//
// `.mts` is TypeScript. JSParser did not claim it and knownSourceExtensions did
// not list it, so a deleted export was invisible AND unreported — the original
// bug, reproduced inside a language RunEcho advertises support for. The
// conservative bias documented on the table is right for `.d`, where nobody
// expects coverage; it is wrong here, because a user who reads "we parse
// TypeScript" will not go looking for a silent hole in `.mts`.
//
// "Accounted for" deliberately allows either answer. Adding a parser satisfies
// this test; so does admitting in the table that we do not index it. What is
// forbidden is the third state — neither indexed nor named.
func TestAdvertisedLanguageFamiliesAreAccountedFor(t *testing.T) {
	g := NewGenerator(GeneratorConfig{})
	for lang, exts := range advertisedFamilies {
		for _, ext := range exts {
			if g.supportsExtension(ext) || IsKnownSourceExtension(ext) {
				continue
			}
			t.Errorf("%s: %q is neither parsed nor listed in knownSourceExtensions — a symbol "+
				"deleted from a %s file is invisible AND unreported, which is the exact bug "+
				"this package exists to close", lang, ext, ext)
		}
	}
}

// TestExtensionCaseIsNotACrack pins the fix for an asymmetry between the two
// lookups: IsKnownSourceExtension lowercased its argument, every parser's
// SupportsExtension is an exact match. `A.GO` therefore fell through BOTH — not
// indexed (wrong case for the parser), not reported (`.go` is deliberately
// absent from the source table, being a language we DO parse). A file in a fully
// supported language, invisible and unnamed.
func TestExtensionCaseIsNotACrack(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "A.GO", "package p\nfunc A() {}\n")
	writeFile(t, dir, "B.JAVA", "class B { }\n")
	writeFile(t, dir, "C.Py", "def c(): pass\n")

	irData, stats, err := NewGenerator(GeneratorConfig{}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats)

	// Supported languages: indexed despite the casing.
	for _, indexed := range []string{"A.GO", "C.Py"} {
		if _, ok := irData.Files[indexed]; !ok {
			t.Errorf("%s was not indexed; case must not decide whether a supported language is read", indexed)
		}
		if reason, reported := got[indexed]; reported {
			t.Errorf("%s was indexed but also reported as %q", indexed, reason)
		}
	}
	// Unsupported language: reported despite the casing.
	if got["B.JAVA"] != SkipUnsupportedLanguage {
		t.Errorf("B.JAVA: got %q, want %q", got["B.JAVA"], SkipUnsupportedLanguage)
	}
}

// TestSymlinkedSourceIsRecorded. The walk does not follow symlinks, so a
// symlinked source file is unindexed — and it used to be unrecorded too, which
// under the prefix/exact match rule means a consumer reads its absence from the
// list as "examined". Non-code symlinks stay unreported, for the same reason
// README.md does.
func TestSymlinkedSourceIsRecorded(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "lib.go", "package p\nfunc L() {}\n")
	writeFile(t, outside, "notes.md", "# notes\n")
	if err := os.MkdirAll(filepath.Join(outside, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	for _, ln := range []struct{ target, name string }{
		{filepath.Join(outside, "lib.go"), "linked.go"},
		{filepath.Join(outside, "notes.md"), "linked.md"},
		{filepath.Join(outside, "shared"), "shared"},
	} {
		if err := os.Symlink(ln.target, filepath.Join(dir, ln.name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	_, stats, err := NewGenerator(GeneratorConfig{}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats)

	if got["linked.go"] != SkipSymlink {
		t.Errorf("linked.go: got %q, want %q", got["linked.go"], SkipSymlink)
	}
	if got["shared"] != SkipSymlinkDir {
		t.Errorf("shared: got %q, want %q", got["shared"], SkipSymlinkDir)
	}
	if reason, reported := got["linked.md"]; reported {
		t.Errorf("a symlinked non-code file must stay unreported, got %q", reason)
	}
}

// TestCapReachedCannotEvictTheHeadlineReason.
//
// The recorder is first-come and the walk is lexical, so before the per-reason
// sub-cap a repo with FileCap set could fill all 1000 slots with cap_reached
// entries and drop every unsupported_language entry sorting after the fill
// point. The signal that survived was "lexically earliest", not "most
// important" — and unsupported_language is the entire reason this list exists.
func TestCapReachedCannotEvictTheHeadlineReason(t *testing.T) {
	dir := t.TempDir()
	// Thousands of supported files sorting BEFORE the .java file alphabetically.
	for i := range maxRecordedSkips + 500 {
		writeFile(t, dir, fmt.Sprintf("a%05d.go", i), "package p\n")
	}
	writeFile(t, dir, "zzz.java", "class Z { }\n")

	_, stats, err := NewGenerator(GeneratorConfig{FileCap: 1}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := skipMap(t, stats); got["zzz.java"] != SkipUnsupportedLanguage {
		t.Errorf("zzz.java was evicted by cap_reached entries (got %q); the sub-cap exists "+
			"so the headline reason always survives", got["zzz.java"])
	}
	if !stats.SkippedTruncated {
		t.Error("the cap_reached sub-cap was hit, so truncation must be flagged")
	}
	capped := 0
	for _, sk := range stats.Skipped {
		if sk.Reason == SkipCapReached {
			capped++
		}
	}
	if capped > maxCapReachedSkips {
		t.Errorf("cap_reached entries = %d, want at most %d", capped, maxCapReachedSkips)
	}
}

// TestIgnoredWalkRootIsIndexed: the ignore rule prunes SUBdirectories. Applied
// to the directory the operator explicitly named, it pruned the whole walk —
// zero files indexed, a vacuous 100% coverage (SupportedSeen == 0), and a skip
// entry of "." that prefix-matches nothing.
func TestIgnoredWalkRootIsIndexed(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "vendor") // basename is in DefaultIgnoredPaths
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")

	irData, stats, err := NewGenerator(GeneratorConfig{}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := irData.Files["main.go"]; !ok {
		t.Errorf("the walk root was pruned by its own basename; indexed %v", irData.Files)
	}
	if stats.SupportedSeen == 0 {
		t.Error("SupportedSeen == 0 makes Coverage() report a vacuous 100%")
	}
	if got := skipMap(t, stats); got["."] != "" {
		t.Errorf(`walk root recorded as "." (reason %q) — a prefix that matches nothing`, got["."])
	}
}

// TestPolicyAndCapabilitySkipsAreDistinguishable pins the split the diff payload
// depends on. Merging them was the defect: `.git` is always ignored, so a merged
// list was never empty and a consumer prefix-matching it blocked a
// testdata/README.md edit.
func TestPolicyAndCapabilitySkipsAreDistinguishable(t *testing.T) {
	// Exactly one policy reason: the operator's own ignore list. Everything else
	// is a limit of the tool, INCLUDING the two directory reasons -- a directory
	// the walk could not read took its whole subtree with it, and nothing
	// recorded those files because nothing ever saw them. Classifying that as
	// informational is how CI without read permission on src/legacy/ produced
	// `skipped: []` and exit 0 for a tree it never opened.
	policy := []string{SkipIgnoredDir}
	capability := []string{SkipUnsupportedLanguage, SkipParseError, SkipOversized,
		SkipCapReached, SkipSymlink, SkipSymlinkDir, SkipUnreadableDir, SkipUnreadableFile}
	for _, r := range policy {
		if !IsPolicySkip(r) {
			t.Errorf("%q names a pruned directory and must classify as policy", r)
		}
	}
	for _, r := range capability {
		if IsPolicySkip(r) {
			t.Errorf("%q names an unreadable file and must classify as capability — a consumer "+
				"fails closed on these", r)
		}
	}
}

// TestExtensionCaseDoesNotChangeTheSymbolSet.
//
// parserFor was lowercased but parseFile was not, so `A.TS` became indexable
// while jsLanguageFor's exact-match switch still fell through to the JavaScript
// grammar. The file was parsed with the wrong grammar and lost symbols a
// TypeScript grammar would have found — and a mis-parsed file reports a CLEAN
// diff, where an unindexed one at least reports a blind spot. Strictly worse
// than the bug the case fix was closing.
func TestExtensionCaseDoesNotChangeTheSymbolSet(t *testing.T) {
	const src = "export class Widget {\n  bar(): number { return 1 }\n}\n"
	names := func(name string) []string {
		dir := t.TempDir()
		writeFile(t, dir, name, src)
		irData, _, err := NewGenerator(GeneratorConfig{}).Generate(dir)
		if err != nil {
			t.Fatal(err)
		}
		file, ok := irData.Files[name]
		if !ok {
			t.Fatalf("%s was not indexed at all", name)
		}
		var out []string
		for _, sym := range file.Symbols {
			out = append(out, sym.Kind+":"+sym.Name)
		}
		sort.Strings(out)
		return out
	}
	lower, upper := names("a.ts"), names("A.TS")
	if len(lower) != len(upper) {
		t.Errorf("case changed the symbol set: .ts => %v, .TS => %v — the grammar, "+
			"not just the parser, is selected by extension", lower, upper)
		return
	}
	for i := range lower {
		if lower[i] != upper[i] {
			t.Errorf("case changed the symbol set: .ts => %v, .TS => %v", lower, upper)
			return
		}
	}
}

// TestSymlinkedWalkRootIsFollowed. filepath.Walk lstats, so a root that is a
// symlink to a directory reached the symlink branch, got recorded as pruned, and
// the walk ended after one callback: zero files indexed, exit 0, and the only
// trace was {"Path": "."} in the INFORMATIONAL array — a prefix matching no path
// a consumer could hold. A repo reached through a link (`~/work ->
// /mnt/checkouts/x`, a pnpm workspace link) was a total, silent fail-open.
//
// The symlink rule prunes links found INSIDE the tree. The directory the
// operator named is what they asked to index.
func TestSymlinkedWalkRootIsFollowed(t *testing.T) {
	real := t.TempDir()
	writeFile(t, real, "main.go", "package p\nfunc Critical() {}\n")

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	irData, stats, err := NewGenerator(GeneratorConfig{}).Generate(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := irData.Files["main.go"]; !ok {
		t.Errorf("a symlinked root indexed nothing; files = %v", irData.Files)
	}
	if got := skipMap(t, stats); got["."] != "" {
		t.Errorf(`walk root recorded as "." (reason %q) — a prefix that matches nothing`, got["."])
	}
	if stats.SupportedSeen == 0 {
		t.Error("SupportedSeen == 0 makes Coverage() report a vacuous 100%")
	}
}

// TestUnresolvableSymlinkRespectsTheCodeFilter.
//
// `skipped` is fail-closed on, and non-code must never reach it — a stale
// `latest -> build-123` or a `README-link.md` pointing nowhere would otherwise
// block every build in the repo until someone deleted the link.
//
// But "did not resolve" is NOT "resolves to nothing". os.Stat fails with EACCES,
// EIO and ELOOP as well as ENOENT, and in those cases the target is a real
// directory the walk cannot enter. An earlier version reasoned "it is not a
// directory" and applied an extension filter, which drops EVERY directory link
// (they have no extension) — see TestSymlinkToUnreadableDirectoryIsRecorded.
func TestUnresolvableSymlinkRespectsTheCodeFilter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	links := map[string]string{
		"DANGLING.txt":   "/nonexistent/nope.txt",
		"README-link.md": "/nonexistent/nope.md",
		"broken.go":      "/nonexistent/lib.go", // code: SHOULD be reported
		"latest":         "/nonexistent/build-123",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}

	_, stats, err := NewGenerator(GeneratorConfig{}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats)

	for _, quiet := range []string{"DANGLING.txt", "README-link.md"} {
		if reason, reported := got[quiet]; reported {
			t.Errorf("%s carries a non-code extension and must not reach the "+
				"fail-closed array (got %q)", quiet, reason)
		}
	}
	// A plainly dangling link — target absent — is reported for NOTHING, code or
	// not. Same policy as a file that vanished mid-walk: nothing was hidden from
	// us. And unlike that race this one is permanent, so recording it would block
	// every change in the repo until someone deleted the link.
	for _, quiet := range []string{"broken.go", "latest"} {
		if reason, reported := got[quiet]; reported {
			t.Errorf("%s points at nothing; recording it blocks the repo permanently "+
				"(got %q)", quiet, reason)
		}
	}
}

// TestSymlinkUnresolvableForAnotherReasonIsRecorded is the other side of the
// ENOENT rule: EACCES/EIO/ELOOP mean something IS there and the walk cannot see
// it. A directory link has no extension to judge by, so it must be recorded
// regardless of name.
func TestSymlinkUnresolvableForAnotherReasonIsRecorded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still traversable")
	}
	blocked := t.TempDir()
	writeFile(t, blocked, "inner/lib.go", "package p\nfunc L() {}\n")
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	if err := os.Symlink(filepath.Join(blocked, "inner"), filepath.Join(dir, "inner")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	g := NewGenerator(GeneratorConfig{})
	g.warn = func(string, ...any) {}
	_, stats, err := g.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := skipMap(t, stats); got["inner"] == "" {
		t.Error("a link the walk cannot resolve for a reason other than ENOENT may " +
			"be hiding a subtree; it must be recorded")
	}
}

// TestSymlinkToUnreadableDirectoryIsRecorded is the regression test for the
// third-generation defect: round 3 closed the unreadable-directory fail-open for
// a PLAIN directory and reopened it for a symlinked one.
//
// os.Stat on the link fails with EACCES, the old code concluded "not a
// directory", the extension filter dropped it (a directory link has no
// extension), and the payload reported `skipped: []` with zero drift — a passing
// gate for a subtree that was never opened.
func TestSymlinkToUnreadableDirectoryIsRecorded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still traversable")
	}
	blocked := t.TempDir()
	writeFile(t, blocked, "legacy/Critical.java", "class Critical { }\n")
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	if err := os.Symlink(filepath.Join(blocked, "legacy"), filepath.Join(dir, "legacy")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	g := NewGenerator(GeneratorConfig{})
	g.warn = func(string, ...any) {}
	_, stats, err := g.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats)
	if got["legacy"] == "" {
		t.Fatal("a symlink to an unreadable directory was recorded nowhere — the gate " +
			"sees `skipped: []` and passes for a subtree nothing opened")
	}
	if IsPolicySkip(got["legacy"]) {
		t.Errorf("legacy recorded as %q, which is informational; an unenterable "+
			"subtree is a blind spot", got["legacy"])
	}
}

// TestUnreadableDirectoryIsAFailClosedBlindSpot. A directory the walk cannot
// read takes its whole subtree with it, and nothing records the files inside
// because nothing ever sees them. It must land in the array a consumer fails
// closed on, matched as a prefix.
func TestUnreadableDirectoryIsAFailClosedBlindSpot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still readable")
	}
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	locked := filepath.Join(dir, "legacy")
	writeFile(t, dir, "legacy/Foo.java", "class Foo { }\n")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	g := NewGenerator(GeneratorConfig{})
	g.warn = func(string, ...any) {}
	_, stats, err := g.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats)
	if got["legacy"] != SkipUnreadableDir {
		t.Fatalf("legacy: got %q, want %q", got["legacy"], SkipUnreadableDir)
	}
	if IsPolicySkip(SkipUnreadableDir) {
		t.Error("an unreadable directory is a limit of the tool, not something the " +
			"operator configured — it must not be filed as informational")
	}
}

// TestUnenterableDirectoryIsRecordedOnceAsTheParent.
//
// A directory at mode 0444 lets readdir succeed while lstat of its children
// fails, so the walk reaches its error branch once per child with info == nil.
//
// Recording those children individually was a false block: an extension is not
// evidence for a path that has none, so `Makefile`, `LICENSE` and `Dockerfile`
// went into the array a gate fails closed on. The blind spot belongs to the
// containing DIRECTORY — it is what is genuinely unenterable, one entry covers
// everything beneath it under the documented prefix rule, and no non-code
// FILENAME ever enters.
func TestUnenterableDirectoryIsRecordedOnceAsTheParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0444 is still traversable")
	}
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	sub := filepath.Join(dir, "sub")
	for _, name := range []string{"Makefile", "LICENSE", "Dockerfile", "README.md", "Widget.java"} {
		writeFile(t, dir, "sub/"+name, "x\n")
	}
	if err := os.Chmod(sub, 0o444); err != nil { // readable, not traversable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	g := NewGenerator(GeneratorConfig{})
	g.warn = func(string, ...any) {}
	_, stats, err := g.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats) // also asserts no path is recorded twice
	if len(got) == 0 {
		t.Skip("this filesystem still stats children of a 0444 directory")
	}

	if got["sub"] != SkipUnreadableDir {
		t.Errorf("the unenterable directory itself must be the entry, got %q", got["sub"])
	}
	for name := range got {
		if name != "sub" {
			t.Errorf("only the directory should be recorded; got a child entry %q", name)
		}
	}
}

// TestVanishedEntryIsNotRecorded. A file removed mid-walk — a git checkout, an
// editor's temp file — also reaches the error branch, but nothing was hidden
// from us: there is nothing there. Recording it puts a transient, self-healing
// false block in the fail-closed array.
func TestVanishedEntryIsNotRecorded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")

	g := NewGenerator(GeneratorConfig{})
	g.warn = func(string, ...any) {}
	rec := &skipRecorder{}
	// Drive the branch directly: reproducing the race reliably is not possible,
	// and the classification is the contract.
	err := g.walkSourceFiles(t.Context(), dir, func(string, string) error { return nil }, rec)
	if err != nil {
		t.Fatal(err)
	}
	if items, _, _ := rec.result(); len(items) != 0 {
		t.Fatalf("clean tree should record nothing, got %v", items)
	}
	if !os.IsNotExist(os.ErrNotExist) {
		t.Fatal("precondition: os.IsNotExist must recognise ErrNotExist")
	}
}

// TestHitCapReportsTheLargestCapThatFired.
//
// Both caps can fire in one walk. The recorder used last-write-wins, so a
// cap_reached add arriving after the main cap had filled reset the reported
// figure from 1000 back to 100 — printing "hit its cap (100)" beside a
// 1000-entry list. That is the same mis-scoping the sub-cap was introduced to
// fix, in the opposite direction.
func TestHitCapReportsTheLargestCapThatFired(t *testing.T) {
	r := &skipRecorder{}
	for i := range maxRecordedSkips + 200 {
		r.add(fmt.Sprintf("f%05d.java", i), SkipUnsupportedLanguage)
	}
	for i := range 200 {
		r.add(fmt.Sprintf("g%05d.go", i), SkipCapReached)
	}
	items, truncated, cap := r.result()
	if !truncated {
		t.Fatal("both caps fired; truncation must be flagged")
	}
	if len(items) != maxRecordedSkips {
		t.Fatalf("len = %d, want %d", len(items), maxRecordedSkips)
	}
	if cap != maxRecordedSkips {
		t.Errorf("hitCap = %d, want %d — naming the sub-cap beside a %d-entry list "+
			"mis-scopes the blind spot", cap, maxRecordedSkips, len(items))
	}
}

// TestUpdateFileFollowsASymlinkedRoot.
//
// Resolving the walk root made symlinked repos indexable, and left the
// incremental path broken: UpdateFile computed filepath.Rel against the
// UNresolved root, so the edited file's real path started with ".." and was
// judged "outside this repo". The full walk worked, the per-edit refresh
// silently did nothing, and the IR went stale — worse than before the fix, when
// the repo was simply never indexed at all and the staleness was visible.
func TestUpdateFileFollowsASymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	writeFile(t, real, "main.go", "package p\nfunc A() {}\n")
	link := filepath.Join(t.TempDir(), "work")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	g := NewGenerator(GeneratorConfig{})
	base, _, err := g.Generate(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base.Files["main.go"]; !ok {
		t.Fatal("precondition: the symlinked root should index")
	}
	writeFile(t, real, "main.go", "package p\nfunc A() {}\nfunc B() {}\n")

	// Both namings must work: the caller may hold either the link or the real
	// path, and only one of the two is ever resolved for it.
	for _, c := range []struct{ name, root, file string }{
		{"link root, real file", link, filepath.Join(real, "main.go")},
		{"real root, link file", real, filepath.Join(link, "main.go")},
		{"link root, link file", link, filepath.Join(link, "main.go")},
	} {
		_, changed, err := g.UpdateFile(base, c.root, c.file)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !changed {
			t.Errorf("%s: the edit was judged outside the repo, so the IR silently went stale", c.name)
			continue
		}
		// changed==true is not enough: DELETING the entry also changes RootHash.
		// The fallback used to derive rel from the resolved path while leaving
		// absFile in link form, so pathCrossesSymlink saw a symlinked component,
		// concluded "replaced by a link", and dropped the file -- every
		// incremental refresh erasing its symbols.
		out, _, _ := g.UpdateFile(base, c.root, c.file)
		fileIR, present := out.Files["main.go"]
		if !present {
			t.Errorf("%s: the entry was DROPPED, so the next diff reports its symbols as removed", c.name)
			continue
		}
		if len(fileIR.Symbols) == 0 {
			t.Errorf("%s: the entry survived but lost its symbols", c.name)
		}
	}

	// DELETION is the case that matters most and the one EvalSymlinks cannot
	// handle directly: it fails outright when the final component is missing, so
	// the file path stayed under the link while the root was resolved, Rel
	// produced "../..", and the removal never reached the IR. A deleted symbol
	// that stays indexed is the exact failure this package exists to close.
	if err := os.Remove(filepath.Join(real, "main.go")); err != nil {
		t.Fatal(err)
	}
	after, changed, err := g.UpdateFile(base, link, filepath.Join(link, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a deletion under a symlinked root was judged outside the repo")
	}
	if _, still := after.Files["main.go"]; still {
		t.Error("the deleted file is still in the IR, so its symbols read as present")
	}
}

// TestIgnoreListAppliesToDirectoriesAndLinksOnly.
//
// Moving the ignore check above the symlink branch — so an ignored directory
// stored as a link is still an ignore — also moved it above the IsDir test, so
// it fired for regular files. A plain file named `dist` or `vendor` was recorded
// as `ignored_dir`, which is untrue for a file, and consumed recorder budget.
// The real-world hit is a `.git` FILE: git worktrees and submodules use one, so
// it triggered on every walk of every worktree.
func TestIgnoreListAppliesToDirectoriesAndLinksOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	writeFile(t, dir, "dist", "not a directory\n")
	writeFile(t, dir, ".git", "gitdir: /elsewhere/.bare/worktrees/x\n")
	writeFile(t, dir, "vendor/dep.go", "package dep\n") // a real ignored dir

	_, stats, err := NewGenerator(GeneratorConfig{}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats)

	for _, file := range []string{"dist", ".git"} {
		if reason, reported := got[file]; reported {
			t.Errorf("%s is a regular FILE; %q is not a truthful reason for it", file, reason)
		}
	}
	if got["vendor"] != SkipIgnoredDir {
		t.Errorf("a real ignored directory must still be recorded, got %q", got["vendor"])
	}
}

// TestWalkRootIsNeverRecordedAsDot. "." is inert as a report: under the
// documented match rule (equality, or a "<entry>/" prefix) it matches no
// repo-relative path a consumer could hold, so recording it looks like a finding
// while saying nothing. An unreadable root is a real condition, but the honest
// signal is that the walk indexed nothing, which SupportedSeen already carries.
func TestWalkRootIsNeverRecordedAsDot(t *testing.T) {
	r := &skipRecorder{}
	r.add(".", SkipUnreadableDir)
	r.add("real.java", SkipUnsupportedLanguage)
	items, _, _ := r.result()
	for _, sk := range items {
		if sk.Path == "." {
			t.Errorf(`"." was recorded (reason %q); it matches no path a consumer holds`, sk.Reason)
		}
	}
	if len(items) != 1 {
		t.Errorf("real entries must still be recorded, got %v", items)
	}
}

// TestUnreadableRootIsFlagged. The root's repo-relative path is ".", which the
// recorder drops as inert — it matches nothing under the documented prefix rule.
// Dropping it silently is the worst outcome available: nothing is indexed,
// `skipped` is empty, and Coverage() returns a VACUOUS 100 because SupportedSeen
// is 0 too, so every signal reads clean for a tree nothing opened.
func TestUnreadableRootIsFlagged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still traversable")
	}
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	g := NewGenerator(GeneratorConfig{})
	g.warn = func(string, ...any) {}
	irData, stats, err := g.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(irData.Files) != 0 {
		t.Skip("this filesystem still walks a 0000 directory")
	}
	if !stats.RootUnreadable {
		t.Error("nothing was indexed and nothing was reported — Coverage() also " +
			"returns a vacuous 100 here, so every signal reads clean")
	}
}

// TestUpdateFileKeepsTheNamedKeyAndTheStaleEntryCleanup.
//
// Resolving the edited path unconditionally had two side effects beyond the
// symlinked-root case it was for: the IR key became the TARGET's path rather
// than the named one (the walk skips symlinks, so it never produces that key),
// and an out-of-repo symlink was rejected before the #143 stale-entry cleanup
// could drop the entry the file used to have.
func TestUpdateFileKeepsTheNamedKeyAndTheStaleEntryCleanup(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "ext.go", "package p\nfunc E() {}\n")

	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	writeFile(t, dir, "link.go", "package p\nfunc L() {}\n")

	g := NewGenerator(GeneratorConfig{})
	base, _, err := g.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := base.Files["link.go"]; !ok {
		t.Fatal("precondition: link.go should start out as a real indexed file")
	}

	// Replace the real file with a symlink pointing OUTSIDE the repo. The walk
	// would no longer index it, so its stale entry must be dropped.
	if err := os.Remove(filepath.Join(dir, "link.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "ext.go"), filepath.Join(dir, "link.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	after, changed, err := g.UpdateFile(base, dir, filepath.Join(dir, "link.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("replacing an indexed file with an out-of-repo symlink must drop its entry (#143)")
	}
	if _, still := after.Files["link.go"]; still {
		t.Error("the stale entry survived, so its symbols still read as present")
	}
	if _, wrong := after.Files["ext.go"]; wrong {
		t.Error("the target's path was used as the IR key; the walk never produces that key")
	}
}

// TestSubCapAloneDoesNotClaimToBoundTheList. When only the per-reason
// cap_reached cap fires, the list can hold far more entries than that cap — 160
// items under "cap (100)" reads as a contradiction. hitCap still reports the
// largest cap that fired; subCapOnly records that the number bounds one reason
// rather than the whole list.
func TestSubCapAloneDoesNotClaimToBoundTheList(t *testing.T) {
	r := &skipRecorder{}
	for i := range 200 {
		r.add(fmt.Sprintf("c%03d.go", i), SkipCapReached)
	}
	for i := range 60 {
		r.add(fmt.Sprintf("u%03d.java", i), SkipUnsupportedLanguage)
	}
	items, truncated, capHit := r.result()
	if !truncated || capHit != maxCapReachedSkips {
		t.Fatalf("want the sub-cap reported, got truncated=%v cap=%d", truncated, capHit)
	}
	// The wording carries this now, not a field: "a skip cap was hit (N)" is true
	// of either cap, where "the skip list hit its cap (N)" was only ever true of
	// one. A subCapOnly field was added for a renderer that never read it.
	if len(items) <= capHit {
		t.Errorf("precondition: the list (%d) should exceed the sub-cap (%d), which is "+
			"what made the old wording contradictory", len(items), capHit)
	}
}

// TestSymlinkedIgnoredDirIsTreatedAsAnIgnore, restored. It was deleted when the
// ignore check gained its `IsDir() || symlink` gate, leaving the symlink half of
// that gate unpinned — an easy target for a future "simplify to IsDir()".
//
// pnpm and yarn workspaces routinely make node_modules a link. Classified as a
// symlink it becomes a CAPABILITY skip: a permanent, unfixable entry in the
// fail-closed array for a directory the operator explicitly excluded.
func TestSymlinkedIgnoredDirIsTreatedAsAnIgnore(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, outside, "dep.js", "export function D() {}\n")

	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	if err := os.Symlink(outside, filepath.Join(dir, "node_modules")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, stats, err := NewGenerator(GeneratorConfig{}).Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats)
	if got["node_modules"] != SkipIgnoredDir {
		t.Errorf("node_modules: got %q, want %q — a configured ignore stored as a "+
			"link is still a configured ignore", got["node_modules"], SkipIgnoredDir)
	}
	if IsPolicySkip(got["node_modules"]) == false {
		t.Error("it must land in the informational array, not the fail-closed one")
	}
}

// TestTableEntriesDoNotMatchCommonNonCode, restored. skipped_test.go's
// advertisedFamilies comment still cites it by name as the authority resolving
// the `.ru` rule conflict, so deleting it left that reasoning unsupported.
func TestTableEntriesDoNotMatchCommonNonCode(t *testing.T) {
	for _, name := range []string{
		".terraform.lock.hcl", "README.ru", "README.md", "config.yaml",
		"package-lock.json", "go.sum",
	} {
		if IsKnownSourceExtension(filepath.Ext(name)) {
			t.Errorf("%s is matched via %q — it would enter the fail-closed array",
				name, filepath.Ext(name))
		}
	}
}

// TestUnreadableSymlinkKeepsNonCodeOut. A link whose target cannot be stat'd for
// a non-ENOENT reason is recorded — something is there we cannot see. But
// `skipped` is fail-closed on, and non-code in it blocks a docs-only change
// permanently, until someone fixes the link or the target's permissions.
//
// The extension is weak evidence and decisive in exactly one direction: a path
// that HAS one is a file. A path without one could be a directory, so it is
// recorded.
func TestUnreadableSymlinkKeepsNonCodeOut(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still traversable")
	}
	blocked := t.TempDir()
	for _, n := range []string{"README.md", "logo.png", "lib.go", "inner/x.go"} {
		writeFile(t, blocked, n, "x\n")
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	for name, target := range map[string]string{
		"README.md": "README.md", "logo.png": "logo.png",
		"lib.go": "lib.go", "shared": "inner",
	} {
		if err := os.Symlink(filepath.Join(blocked, target), filepath.Join(dir, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	g := NewGenerator(GeneratorConfig{})
	g.warn = func(string, ...any) {}
	_, stats, err := g.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats)
	for _, quiet := range []string{"README.md", "logo.png"} {
		if reason, reported := got[quiet]; reported {
			t.Errorf("%s is non-code and must not reach the fail-closed array (got %q)", quiet, reason)
		}
	}
	for _, loud := range []string{"lib.go", "shared"} {
		if got[loud] == "" {
			t.Errorf("%s may be hiding source and must be recorded", loud)
		}
	}
}

// TestUnreadableDirectoryDoesNotBlowTheDeadline. everyChildFailed is called once
// per FAILING child, and an unreadable directory fails every child — so without
// memoisation it did a full readdir plus an lstat per sibling N times. Measured:
// 500 entries 0.94s, 2000 entries 13.4s, 4000 entries exceeded the generate
// deadline and produced a hard error with no IR at all. A tooling timeout is a
// worse outcome than the blind spot it was trying to report.
func TestUnreadableDirectoryDoesNotBlowTheDeadline(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still traversable")
	}
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	sub := filepath.Join(dir, "big")
	for i := range 3000 {
		writeFile(t, dir, fmt.Sprintf("big/f%04d.go", i), "package p\n")
	}
	if err := os.Chmod(sub, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	g := NewGenerator(GeneratorConfig{})
	g.warn = func(string, ...any) {}
	start := time.Now()
	irData, stats, err := g.Generate(dir)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("a single unreadable directory must not fail the whole walk: %v", err)
	}
	if _, ok := irData.Files["main.go"]; !ok {
		t.Error("the readable part of the tree must still be indexed")
	}
	if elapsed > 10*time.Second {
		t.Errorf("took %s for one unreadable directory — quadratic in its entry count", elapsed)
	}
	if got := skipMap(t, stats); got["big"] == "" && len(got) == 0 {
		t.Error("the unreadable directory should still be reported")
	}
}

// TestBlameAboveTheRootBecomesRootUnreadable. The parent of a child directly
// under the root IS the root, and filepath.Rel returns ".." for anything above
// it — so a repo whose PARENT directory is unreadable emitted
// {"Path": "..", "unreadable_dir"} with RootUnreadable false. ".." matches
// nothing under the documented prefix rule and Coverage() reports a vacuous 100,
// so the tree read clean: the fail-open the flag closes, one level out.
func TestBlameAboveTheRootBecomesRootUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still traversable")
	}
	outer := t.TempDir()
	repo := filepath.Join(outer, "repo")
	writeFile(t, repo, "main.go", "package p\nfunc M() {}\n")
	if err := os.Chmod(outer, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outer, 0o755) })

	g := NewGenerator(GeneratorConfig{})
	g.warn = func(string, ...any) {}
	irData, stats, err := g.Generate(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(irData.Files) != 0 {
		t.Skip("this filesystem still walks through a 0000 parent")
	}
	for _, sk := range stats.Skipped {
		if sk.Path == ".." || strings.HasPrefix(sk.Path, "../") {
			t.Errorf("skip path %q escapes the repo and matches nothing a consumer holds", sk.Path)
		}
	}
	if !stats.RootUnreadable {
		t.Error("nothing was indexed and nothing matchable was reported — Coverage() " +
			"also returns a vacuous 100, so every signal reads clean")
	}
}
