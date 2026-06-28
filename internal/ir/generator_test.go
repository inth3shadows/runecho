package ir

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// A cancelled context aborts the walk cleanly: GenerateCtx returns an error that
// wraps context.Canceled and no partial IR. This is the A4 cancellation guard —
// it proves the deadline plumbing actually short-circuits generation rather than
// running to completion and ignoring the context.
func TestGenerator_GenerateCtxCancelled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := NewGenerator(GeneratorConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done before the walk starts

	irData, _, err := g.GenerateCtx(ctx, dir)
	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error should wrap context.Canceled, got: %v", err)
	}
	if irData != nil {
		t.Errorf("cancelled generation must return no IR, got %d files", len(irData.Files))
	}
}

// A past deadline is honored verbatim (not overridden by DefaultGenerateTimeout)
// and surfaces as context.DeadlineExceeded — the per-request MCP budget path.
func TestGenerator_GenerateCtxDeadlineExceeded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := NewGenerator(GeneratorConfig{})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	_, _, err := g.GenerateCtx(ctx, dir)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error should wrap context.DeadlineExceeded, got: %v", err)
	}
}

// GeneratorConfig.GenerateTimeout is honored as the default deadline when the
// caller passes no ctx deadline: a tiny positive timeout trips DeadlineExceeded,
// and the Unbounded sentinel disables the bound so a normal walk completes.
func TestGenerator_ConfigTimeout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tiny := NewGenerator(GeneratorConfig{GenerateTimeout: time.Nanosecond})
	if _, _, err := tiny.Generate(dir); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("tiny config timeout should trip DeadlineExceeded, got: %v", err)
	}

	none := NewGenerator(GeneratorConfig{GenerateTimeout: Unbounded})
	if _, _, err := none.Generate(dir); err != nil {
		t.Fatalf("unbounded config timeout should not bound a normal walk, got: %v", err)
	}
}

// captureWarnings replaces a generator's warn sink with an in-memory collector
// and returns a pointer to the captured lines. Exercises the otherwise-silent
// skip branches (walk-access error, rel-path failure, parse/hash failures).
func captureWarnings(g *Generator) *[]string {
	var lines []string
	g.warn = func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	return &lines
}

// The walk-error callback (a directory that errors during Walk) must route a
// warning through the injected sink and let the walk continue, not abort. On
// Linux an unreadable dir (chmod 000) makes filepath.Walk hand the callback a
// permission error for that path.
func TestGenerate_WalkErrorWarns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 does not deny directory traversal on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses the chmod 000 permission check")
	}

	tmpDir := t.TempDir()
	// A reachable, indexable file so the walk has real work alongside the error.
	if err := os.WriteFile(filepath.Join(tmpDir, "ok.go"), []byte("package x"), 0644); err != nil {
		t.Fatal(err)
	}
	// An unreadable subdirectory: Walk lstat's it fine, then errors trying to
	// read its entries, invoking the callback with err != nil for that path.
	denied := filepath.Join(tmpDir, "denied")
	if err := os.Mkdir(denied, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(denied, "hidden.go"), []byte("package y"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(denied, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(denied, 0755) }) // let t.TempDir cleanup remove it

	gen := NewGenerator(GeneratorConfig{})
	warnings := captureWarnings(gen)

	result, _, err := gen.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate should survive an unreadable subdir, got: %v", err)
	}
	// The walk continued: the reachable file is still indexed.
	if _, ok := result.Files["ok.go"]; !ok {
		t.Error("ok.go should be indexed despite the unreadable sibling dir")
	}
	// And the access error surfaced through the sink, not /dev/null.
	if !slices.ContainsFunc(*warnings, func(s string) bool {
		return strings.Contains(s, "failed to access")
	}) {
		t.Errorf("expected a 'failed to access' warning, got %v", *warnings)
	}
}

// The parse-failure branch must route its warning through the injected sink.
// (The filepath.Rel-failure branch at the relPath site is not practically
// constructible here: walkSourceFiles always passes paths rooted at the cleaned
// absRoot it walks, so Rel cannot fail — left untested by design.)
func TestGenerate_ParseFailureWarns(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "big.go"), []byte("package main // oversized"), 0644); err != nil {
		t.Fatal(err)
	}

	gen := NewGenerator(GeneratorConfig{})
	gen.maxParseBytes = 16 // forces the oversized-file parse failure
	warnings := captureWarnings(gen)

	if _, _, err := gen.Generate(tmpDir); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if !slices.ContainsFunc(*warnings, func(s string) bool {
		return strings.Contains(s, "failed to parse")
	}) {
		t.Errorf("expected a 'failed to parse' warning, got %v", *warnings)
	}
}

func TestGenerator_Generate_Determinism(t *testing.T) {
	// Create temporary test directory with source files
	tmpDir := t.TempDir()

	testFiles := map[string]string{
		"alpha.ts": `
function foo() {}
export { foo };
`,
		"beta.js": `
class Bar {}
export default Bar;
`,
		"gamma.gs": `
const baz = () => {};
export const baz;
`,
	}

	for name, content := range testFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file %s: %v", name, err)
		}
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	// Generate IR 100 times
	results := make([]*IR, 100)
	for i := 0; i < 100; i++ {
		ir, _, err := generator.Generate(tmpDir)
		if err != nil {
			t.Fatalf("Generate failed on iteration %d: %v", i, err)
		}
		results[i] = ir
	}

	// Verify all results are identical
	first := results[0]
	for i := 1; i < 100; i++ {
		if !equalIR(first, results[i]) {
			t.Errorf("IR %d differs from first IR", i)
			t.Logf("First: %+v", first)
			t.Logf("Current: %+v", results[i])
		}
	}
}

func TestGenerator_Generate_JSONDeterminism(t *testing.T) {
	// Create test directory
	tmpDir := t.TempDir()

	testFiles := map[string]string{
		"z.ts": "function z() {}",
		"a.ts": "function a() {}",
		"m.ts": "function m() {}",
	}

	for name, content := range testFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file %s: %v", name, err)
		}
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	// Generate IR and marshal to JSON 100 times
	jsonResults := make([]string, 100)
	for i := 0; i < 100; i++ {
		ir, _, err := generator.Generate(tmpDir)
		if err != nil {
			t.Fatalf("Generate failed on iteration %d: %v", i, err)
		}

		// Marshal to JSON
		data, err := ir.MarshalJSON()
		if err != nil {
			t.Fatalf("Marshal failed on iteration %d: %v", i, err)
		}

		jsonResults[i] = string(data)
	}

	// Verify all JSON outputs are byte-identical
	first := jsonResults[0]
	for i := 1; i < 100; i++ {
		if first != jsonResults[i] {
			t.Errorf("JSON output %d differs from first output", i)
			t.Logf("First:\n%s", first)
			t.Logf("Current:\n%s", jsonResults[i])
			break // Only show first difference
		}
	}
}

func TestGenerator_Generate_IgnoredPaths(t *testing.T) {
	tmpDir := t.TempDir()

	// Create files in ignored directories
	ignoredDirs := []string{"node_modules", "dist", ".git", ".cursor", ".vscode"}
	for _, dir := range ignoredDirs {
		dirPath := filepath.Join(tmpDir, dir)
		if err := os.Mkdir(dirPath, 0755); err != nil {
			t.Fatalf("Failed to create dir %s: %v", dir, err)
		}

		filePath := filepath.Join(dirPath, "ignored.js")
		if err := os.WriteFile(filePath, []byte("function ignored() {}"), 0644); err != nil {
			t.Fatalf("Failed to write ignored file: %v", err)
		}
	}

	// Create file in root (should be included)
	includedPath := filepath.Join(tmpDir, "included.js")
	if err := os.WriteFile(includedPath, []byte("function included() {}"), 0644); err != nil {
		t.Fatalf("Failed to write included file: %v", err)
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	ir, _, err := generator.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Should only have 1 file (included.js)
	if len(ir.Files) != 1 {
		t.Errorf("Expected 1 file, got %d", len(ir.Files))
		for path := range ir.Files {
			t.Logf("  Found: %s", path)
		}
	}
}

func TestGenerator_Generate_OnlySupportedExtensions(t *testing.T) {
	tmpDir := t.TempDir()

	testFiles := map[string]string{
		"test.js":  "function js() {}",
		"test.ts":  "function ts() {}",
		"test.gs":  "function gs() {}",
		"test.py":  "def python(): pass",
		"test.txt": "not code",
		"test.md":  "# Markdown",
	}

	for name, content := range testFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file %s: %v", name, err)
		}
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	ir, _, err := generator.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Should only have 4 files (.js, .ts, .gs, .py) — not .txt or .md
	if len(ir.Files) != 4 {
		t.Errorf("Expected 4 files, got %d", len(ir.Files))
		for path := range ir.Files {
			t.Logf("  Found: %s", path)
		}
	}
}

func TestGenerator_Update_IncrementalUpdate(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial files
	file1 := filepath.Join(tmpDir, "unchanged.js")
	file2 := filepath.Join(tmpDir, "changed.js")

	if err := os.WriteFile(file1, []byte("function unchanged() {}"), 0644); err != nil {
		t.Fatalf("Failed to write file1: %v", err)
	}
	if err := os.WriteFile(file2, []byte("function old() {}"), 0644); err != nil {
		t.Fatalf("Failed to write file2: %v", err)
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	// Generate initial IR
	initialIR, _, err := generator.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Initial generate failed: %v", err)
	}

	// Modify one file
	if err := os.WriteFile(file2, []byte("function new() {}"), 0644); err != nil {
		t.Fatalf("Failed to modify file2: %v", err)
	}

	updatedIR, _, err := generator.Update(initialIR, tmpDir)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Verify unchanged file kept same hash
	unchangedPath := "unchanged.js"
	if updatedIR.Files[unchangedPath].Hash != initialIR.Files[unchangedPath].Hash {
		t.Errorf("Unchanged file hash should not change")
	}

	// Verify changed file has different hash
	changedPath := "changed.js"
	if updatedIR.Files[changedPath].Hash == initialIR.Files[changedPath].Hash {
		t.Errorf("Changed file hash should differ")
	}

	// Verify changed file has updated structure
	if len(updatedIR.Files[changedPath].namesOf("function")) == 0 {
		t.Errorf("Changed file should have parsed functions")
	}
}

// Regression (#33): the Update path streamed the entire file through SHA-256
// (HashFile) before any size check, so a multi-GB file was fully read on every
// Update only to be rejected at parse. Update now applies the maxParseBytes
// guard before HashFile, matching Generate: an oversized file is dropped from
// the IR and counted as a ParseError with an oversized warning. (The avoided
// full read is structural — the guard's placement before HashFile — not
// separately observable from a unit test.)
func TestUpdate_OversizedFileGuardedBeforeHash(t *testing.T) {
	tmpDir := t.TempDir()
	bigPath := filepath.Join(tmpDir, "big.go")
	if err := os.WriteFile(bigPath, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	gen := NewGenerator(GeneratorConfig{})
	// Index while under the cap so the file lands in the initial IR.
	initialIR, _, err := gen.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if _, ok := initialIR.Files["big.go"]; !ok {
		t.Fatalf("expected big.go in initial IR")
	}

	// Lower the cap so the same file is oversized on the next Update.
	gen.maxParseBytes = 4

	warnings := captureWarnings(gen)
	updatedIR, stats, err := gen.Update(initialIR, tmpDir)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if _, ok := updatedIR.Files["big.go"]; ok {
		t.Errorf("oversized big.go should be dropped from updated IR")
	}
	if stats.ParseErrors != 1 {
		t.Errorf("expected ParseErrors=1 for oversized file, got %d", stats.ParseErrors)
	}
	if !slices.ContainsFunc(*warnings, func(s string) bool {
		return strings.Contains(s, "oversized")
	}) {
		t.Errorf("expected an 'oversized' warning, got %v", *warnings)
	}
}

func TestGenerator_Generate_PathNormalization(t *testing.T) {
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "test.js")
	if err := os.WriteFile(testFile, []byte("function test() {}"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	ir, _, err := generator.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Verify all paths use forward slashes (not backslashes)
	for path := range ir.Files {
		for i := 0; i < len(path); i++ {
			if path[i] == '\\' {
				t.Errorf("Path contains backslash: %s", path)
			}
		}
	}
}

func TestGenerator_Generate_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	ir, _, err := generator.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Should have empty files map
	if len(ir.Files) != 0 {
		t.Errorf("Expected 0 files, got %d", len(ir.Files))
	}

	// Should still have version
	if ir.Version != IRVersion {
		t.Errorf("Expected version %d, got %d", IRVersion, ir.Version)
	}
}

// TestGenerate_RefsExtraction verifies IR v2 indexes bare call targets per file
// under the guard's extraction rules: qualified calls, builtins, definition
// lines, and (Go) unexported names are excluded; output is sorted and non-nil.
func TestGenerate_RefsExtraction(t *testing.T) {
	tmpDir := t.TempDir()
	goSrc := "package x\n\nfunc Caller() {\n\tDoThing()\n\tfmt.Println(\"x\")\n\thelper()\n\tZebra()\n\tAlpha()\n}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte(goSrc), 0644); err != nil {
		t.Fatal(err)
	}
	pySrc := "def run():\n    process_order()\n    print(len(x))\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "b.py"), []byte(pySrc), 0644); err != nil {
		t.Fatal(err)
	}
	emptySrc := "package y\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "c.go"), []byte(emptySrc), 0644); err != nil {
		t.Fatal(err)
	}

	result, _, err := NewGenerator(GeneratorConfig{}).Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if got, want := result.Files["a.go"].Refs, []string{"Alpha", "DoThing", "Zebra"}; !slices.Equal(got, want) {
		t.Errorf("a.go refs = %v, want %v (sorted; qualified/unexported/def-line excluded)", got, want)
	}
	if got, want := result.Files["b.py"].Refs, []string{"process_order"}; !slices.Equal(got, want) {
		t.Errorf("b.py refs = %v, want %v (builtins excluded)", got, want)
	}
	if refs := result.Files["c.go"].Refs; refs == nil || len(refs) != 0 {
		t.Errorf("c.go refs = %#v, want non-nil empty slice (stable [] in JSON)", refs)
	}
}

// TestUpdate_VersionMismatchRegenerates: Update must fall back to a full
// Generate for an old-format IR — reusing v1 entries verbatim would leave
// their Refs empty forever.
func TestUpdate_VersionMismatchRegenerates(t *testing.T) {
	tmpDir := t.TempDir()
	src := "package x\n\nfunc Caller() {\n\tDoThing()\n}\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	gen := NewGenerator(GeneratorConfig{})
	current, _, err := gen.Generate(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a v1 artifact: same files, version 1, refs stripped.
	old := &IR{Version: 1, Files: make(map[string]FileIR)}
	for p, f := range current.Files {
		f.Refs = nil
		old.Files[p] = f
	}

	updated, _, err := gen.Update(old, tmpDir)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Version != IRVersion {
		t.Errorf("Version = %d, want %d", updated.Version, IRVersion)
	}
	if got := updated.Files["a.go"].Refs; !slices.Equal(got, []string{"DoThing"}) {
		t.Errorf("refs after v1 Update = %v, want [DoThing] (must regenerate, not reuse)", got)
	}
}

func TestGenerator_Generate_UnicodeNormalization(t *testing.T) {
	// Test that NFD (macOS) and NFC (Linux) filenames produce byte-identical IR JSON
	// This ensures cross-platform determinism.
	//
	// Example: "café" can be represented as:
	// - NFC: café (5 bytes: c a f é)
	// - NFD: café (6 bytes: c a f e ́)
	//
	// Unicode character U+00E9 (LATIN SMALL LETTER E WITH ACUTE):
	// - NFC: \u00e9 (single codepoint)
	// - NFD: \u0065\u0301 (e + combining acute accent)

	// Create two temporary directories
	tmpDirNFC := t.TempDir()
	tmpDirNFD := t.TempDir()

	// Test content (identical)
	testContent := "function test() {}\nexport { test };"

	// Create file with NFC filename in first directory
	// "café.ts" using NFC (composed form)
	filenameNFC := "caf\u00e9.ts" // café using single codepoint
	pathNFC := filepath.Join(tmpDirNFC, filenameNFC)
	if err := os.WriteFile(pathNFC, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to write NFC file: %v", err)
	}

	// Create file with NFD filename in second directory
	// "café.ts" using NFD (decomposed form)
	filenameNFD := "cafe\u0301.ts" // café using e + combining acute
	pathNFD := filepath.Join(tmpDirNFD, filenameNFD)
	if err := os.WriteFile(pathNFD, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to write NFD file: %v", err)
	}

	// Verify the filenames are different byte sequences but represent the same string
	if filenameNFC == filenameNFD {
		t.Fatalf("Test setup error: NFC and NFD filenames should be different byte sequences")
	}

	config := GeneratorConfig{}
	generator := NewGenerator(config)

	// Generate IR for NFC directory
	irNFC, _, err := generator.Generate(tmpDirNFC)
	if err != nil {
		t.Fatalf("Generate failed for NFC: %v", err)
	}

	// Generate IR for NFD directory
	irNFD, _, err := generator.Generate(tmpDirNFD)
	if err != nil {
		t.Fatalf("Generate failed for NFD: %v", err)
	}

	// Marshal both to JSON
	jsonNFC, err := irNFC.MarshalJSON()
	if err != nil {
		t.Fatalf("Marshal failed for NFC: %v", err)
	}

	jsonNFD, err := irNFD.MarshalJSON()
	if err != nil {
		t.Fatalf("Marshal failed for NFD: %v", err)
	}

	// Verify JSON outputs are byte-identical
	if string(jsonNFC) != string(jsonNFD) {
		t.Errorf("NFC and NFD produced different JSON outputs")
		t.Logf("NFC JSON:\n%s", string(jsonNFC))
		t.Logf("NFD JSON:\n%s", string(jsonNFD))

		// Find first difference
		minLen := len(jsonNFC)
		if len(jsonNFD) < minLen {
			minLen = len(jsonNFD)
		}
		for i := 0; i < minLen; i++ {
			if jsonNFC[i] != jsonNFD[i] {
				t.Logf("First byte difference at position %d:", i)
				t.Logf("  NFC: 0x%02x ('%c')", jsonNFC[i], jsonNFC[i])
				t.Logf("  NFD: 0x%02x ('%c')", jsonNFD[i], jsonNFD[i])
				break
			}
		}
	}

	// Verify RootHash is identical
	if irNFC.RootHash != irNFD.RootHash {
		t.Errorf("NFC and NFD produced different RootHash values")
		t.Logf("NFC RootHash: %s", irNFC.RootHash)
		t.Logf("NFD RootHash: %s", irNFD.RootHash)
	}

	// Verify normalized path keys are identical
	if len(irNFC.Files) != len(irNFD.Files) {
		t.Errorf("Different number of files: NFC=%d, NFD=%d", len(irNFC.Files), len(irNFD.Files))
	}

	// Extract and compare path keys
	var pathsNFC []string
	for path := range irNFC.Files {
		pathsNFC = append(pathsNFC, path)
	}
	var pathsNFD []string
	for path := range irNFD.Files {
		pathsNFD = append(pathsNFD, path)
	}

	if len(pathsNFC) == 1 && len(pathsNFD) == 1 {
		if pathsNFC[0] != pathsNFD[0] {
			t.Errorf("Path keys differ:")
			t.Logf("  NFC: %q (bytes: %x)", pathsNFC[0], []byte(pathsNFC[0]))
			t.Logf("  NFD: %q (bytes: %x)", pathsNFD[0], []byte(pathsNFD[0]))
		}
	}
}

// TestGenerate_OversizedFileSkipped verifies that a file exceeding maxParseBytes
// is silently skipped rather than causing Generate to fail.
func TestGenerate_OversizedFileSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	big := filepath.Join(tmpDir, "big.go")
	if err := os.WriteFile(big, []byte("package main // larger than cap"), 0644); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(tmpDir, "small.go")
	if err := os.WriteFile(small, []byte("package x"), 0644); err != nil {
		t.Fatal(err)
	}

	gen := NewGenerator(GeneratorConfig{})
	gen.maxParseBytes = 16 // any real source file exceeds this
	result, _, err := gen.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if _, ok := result.Files["big.go"]; ok {
		t.Error("oversized file should have been skipped but appeared in IR")
	}
	if _, ok := result.Files["small.go"]; !ok {
		t.Error("small file should be in IR but was missing")
	}
}

// Helper functions

func equalIR(a, b *IR) bool {
	if a.Version != b.Version {
		return false
	}
	if a.RootHash != b.RootHash {
		return false
	}
	if len(a.Files) != len(b.Files) {
		return false
	}

	for path, fileA := range a.Files {
		fileB, exists := b.Files[path]
		if !exists {
			return false
		}
		if !equalFileIR(fileA, fileB) {
			return false
		}
	}

	return true
}

// TestGenerate_FileCap verifies that Generate stops after FileCap files and that
// Update respects the same cap, producing consistent (capped) hashes.
func TestGenerate_FileCap(t *testing.T) {
	tmpDir := t.TempDir()
	for i := range 5 {
		name := fmt.Sprintf("f%d.js", i)
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("function x() {}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	gen := NewGenerator(GeneratorConfig{FileCap: 3})
	result, _, err := gen.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(result.Files) != 3 {
		t.Errorf("FileCap=3: got %d files, want 3", len(result.Files))
	}

	// Update with same cap should also produce exactly 3 files.
	result2, _, err := gen.Update(result, tmpDir)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if len(result2.Files) != 3 {
		t.Errorf("Update FileCap=3: got %d files, want 3", len(result2.Files))
	}

	// Zero cap means unlimited.
	genUnlimited := NewGenerator(GeneratorConfig{})
	full, _, err := genUnlimited.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate unlimited failed: %v", err)
	}
	if len(full.Files) != 5 {
		t.Errorf("FileCap=0: got %d files, want 5", len(full.Files))
	}
}

// TestGenerate_FileCap_ParseFailuresDontConsumeBudget verifies the cap counts
// files actually indexed, not files attempted: a leading unparseable file must
// not eat a cap slot, so a cap of 2 still yields 2 successfully-indexed files.
func TestGenerate_FileCap_ParseFailuresDontConsumeBudget(t *testing.T) {
	tmpDir := t.TempDir()
	// a.go sorts first and is oversized → parse failure (must not consume budget).
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package main // oversized!!"), 0644); err != nil {
		t.Fatal(err)
	}
	// b.go, c.go, d.go are small and valid.
	for _, n := range []string{"b.go", "c.go", "d.go"} {
		if err := os.WriteFile(filepath.Join(tmpDir, n), []byte("package x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	gen := NewGenerator(GeneratorConfig{FileCap: 2})
	gen.maxParseBytes = 24 // "package main // oversized" exceeds this; short files pass
	result, stats, err := gen.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(result.Files) != 2 {
		t.Errorf("cap=2 with a leading parse failure: got %d indexed files, want 2", len(result.Files))
	}
	if stats.ParseErrors != 1 {
		t.Errorf("ParseErrors = %d, want 1 (the oversized a.go)", stats.ParseErrors)
	}
	// Honest denominator: all 4 supported files counted even though the cap
	// truncated indexing at 2 — coverage must not read ~100% on a capped walk.
	if stats.SupportedSeen != 4 {
		t.Errorf("SupportedSeen = %d, want 4 (cap must not stop the count)", stats.SupportedSeen)
	}
	if stats.Indexed != 2 {
		t.Errorf("Indexed = %d, want 2", stats.Indexed)
	}
	if _, ok := result.Files["a.go"]; ok {
		t.Error("a.go failed to parse and must not appear in the IR")
	}
}

// TestGenerate_ParseErrorCount verifies that files failing to parse are counted
// and excluded from the IR rather than causing Generate to fail.
func TestGenerate_ParseErrorCount(t *testing.T) {
	tmpDir := t.TempDir()
	small := filepath.Join(tmpDir, "small.go")
	if err := os.WriteFile(small, []byte("package x"), 0644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(tmpDir, "big.go")
	if err := os.WriteFile(big, []byte("package main // oversized"), 0644); err != nil {
		t.Fatal(err)
	}

	gen := NewGenerator(GeneratorConfig{})
	gen.maxParseBytes = 16 // "package main // oversized" (25 bytes) exceeds this
	result, stats, err := gen.Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if stats.ParseErrors != 1 {
		t.Errorf("ParseErrors = %d, want 1", stats.ParseErrors)
	}
	if stats.SupportedSeen != 2 || stats.Indexed != 1 {
		t.Errorf("stats = %+v, want SupportedSeen=2 Indexed=1", stats)
	}
	if _, ok := result.Files["small.go"]; !ok {
		t.Error("small.go should be in IR")
	}
	if _, ok := result.Files["big.go"]; ok {
		t.Error("big.go should be excluded (parse error)")
	}
}
