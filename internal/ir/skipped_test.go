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
		SkipCapReached, SkipSymlink, SkipUnreadable, SkipSymlinkDir, SkipUnreadableDir}
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
	if got["broken.go"] == "" {
		t.Error("broken.go: an unresolvable CODE link is a real blind spot and must be reported")
	}
	// No extension: it could be a directory we cannot enter, so it is recorded
	// rather than assumed harmless. Erring toward a spurious entry here is the
	// cheap mistake; erring the other way hides a subtree.
	if got["latest"] == "" {
		t.Error("an extension-less unresolvable link could be a directory; it must be recorded")
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

// TestUnstattableNonCodeStaysOutOfTheFailClosedArray.
//
// A directory at mode 0444 lets readdir succeed while lstat of its children
// fails, so the walk reaches its error branch with info == nil for each child.
// That branch recorded unconditionally, so assets/README.md and assets/logo.png
// entered the array a gate fails closed on — blocking a docs-only edit. The same
// path is hit by the ordinary race of a file vanishing mid-walk during a git
// checkout or an editor's temp-file churn.
func TestUnstattableNonCodeStaysOutOfTheFailClosedArray(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0444 is still traversable")
	}
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package p\nfunc M() {}\n")
	assets := filepath.Join(dir, "assets")
	writeFile(t, dir, "assets/README.md", "# assets\n")
	writeFile(t, dir, "assets/logo.png", "notreallyapng\n")
	writeFile(t, dir, "assets/Widget.java", "class Widget { }\n")
	if err := os.Chmod(assets, 0o444); err != nil { // readable, not traversable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(assets, 0o755) })

	g := NewGenerator(GeneratorConfig{})
	g.warn = func(string, ...any) {}
	_, stats, err := g.Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := skipMap(t, stats)
	if len(got) == 0 {
		t.Skip("this filesystem still stats children of a 0444 directory")
	}
	for _, quiet := range []string{"assets/README.md", "assets/logo.png"} {
		if reason, reported := got[quiet]; reported {
			t.Errorf("%s is not source and must not reach the fail-closed array (got %q)",
				quiet, reason)
		}
	}
	// The code file in the same unreadable directory still counts.
	if got["assets/Widget.java"] == "" && got["assets"] == "" {
		t.Error("nothing recorded for an unreadable directory containing source")
	}
}
