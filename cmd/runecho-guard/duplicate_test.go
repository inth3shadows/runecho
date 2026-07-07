package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/ir"
)

// --- pure unit tests (no store) ---

func TestAddedDefs(t *testing.T) {
	tests := []struct {
		name     string
		old, new string
		want     []string
	}{
		{"new def", "", "func DoThing() {}", []string{"DoThing"}},
		{"no defs added", "x := callIt()", "y := other()", nil},
		{"redefined in place (rename body, same name)", "func DoThing() { a() }", "func DoThing() { b() }", nil},
		{"added one, kept other", "func A() {}", "func A() {}\nfunc B() {}", []string{"B"}},
		{"empty new", "func Old() {}", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addedDefs(guard.LangGo, tt.old, tt.new)
			if len(got) != len(tt.want) {
				t.Fatalf("addedDefs = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("addedDefs = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestWholeFileText(t *testing.T) {
	dir := t.TempDir()
	// Missing file (new file being created): "" is a definitive, correct
	// answer — nothing was defined before.
	if got, definitive := wholeFileText(filepath.Join(dir, "missing.go")); got != "" || !definitive {
		t.Errorf("missing file = (%q, %v), want (\"\", true)", got, definitive)
	}

	normal := filepath.Join(dir, "normal.go")
	if err := os.WriteFile(normal, []byte("package p\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, definitive := wholeFileText(normal); got != "package p\n" || !definitive {
		t.Errorf("normal file = (%q, %v), want (%q, true)", got, definitive, "package p\n")
	}

	// Existing file too large to read: "" is NOT a safe stand-in for "nothing
	// defined" — the caller must treat this as indefinite and skip the check,
	// not run addedDefs against an artificially-empty old side.
	big := filepath.Join(dir, "big.go")
	if err := os.WriteFile(big, make([]byte, maxInFileBytes+1), 0644); err != nil {
		t.Fatal(err)
	}
	if got, definitive := wholeFileText(big); got != "" || definitive {
		t.Errorf("oversized existing file = (%d bytes, %v), want (0 bytes, false)", len(got), definitive)
	}
}

// --- store-backed tests ---

// defAndDupFiles builds a two-file snapshot: known.go defines placeholder,
// other.go already defines name — the duplicate-definition scenario E5 flags
// when known.go's edit introduces name too.
func defAndDupFiles(name string) map[string]ir.FileIR {
	return map[string]ir.FileIR{
		"known.go": {Hash: "h1", Symbols: funcsToSymbols([]string{"Placeholder"})},
		"other.go": {Hash: "h2", Symbols: funcsToSymbols([]string{name})},
	}
}

func TestDuplicate_NewFuncMatchesOtherFile_Asks(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndDupFiles("DoThing"))
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "func Placeholder() {}", "func Placeholder() {}\nfunc DoThing() {}", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("want ask, got %q", d.Hook.PermissionDec)
	}
	if !strings.Contains(d.Hook.PermissionReason, "DoThing") || !strings.Contains(d.Hook.PermissionReason, "other.go") {
		t.Errorf("reason should name the symbol and its other definer:\n%s", d.Hook.PermissionReason)
	}
	if rec := readLastDecisionLog(t); rec["reason"] != "duplicate-symbol" {
		t.Errorf("decision reason = %v, want duplicate-symbol", rec["reason"])
	}
}

// TestDuplicate_CrossPackageSameName_Defers pins the package-scoping fix: a name
// shared across directories (different Go packages) is not a duplicate. Editing
// cmd/a/main.go to add `main` while cmd/b/main.go also defines `main` must NOT
// ask — the cross-package `main`/`Load` collisions were the non-test dogfood FPs.
func TestDuplicate_CrossPackageSameName_Defers(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	files := map[string]ir.FileIR{
		"cmd/a/main.go": {Hash: "h1", Symbols: funcsToSymbols([]string{"Placeholder"})},
		"cmd/b/main.go": {Hash: "h2", Symbols: funcsToSymbols([]string{"main"})},
	}
	top := enrolledStoreWithFiles(t, repoRoot, files)
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	file := filepath.Join(top, "cmd", "a", "main.go")
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("package main\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "func Placeholder() {}", "func Placeholder() {}\nfunc main() {}", "", nil)
	_, _, d := runHook(t, in)
	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("cross-package same name should not ask; reason: %s", d.Hook.PermissionReason)
	}
}

// TestDuplicate_TestFileEdit_Defers pins the test-file exclusion: a symbol added
// to a _test.go file that also exists in another test file is a conventional name
// collision, not a reimplementation — no ask. Both files are in the same dir, so
// only the test-file skip (not package-scoping) can suppress this.
func TestDuplicate_TestFileEdit_Defers(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	files := map[string]ir.FileIR{
		"foo_test.go": {Hash: "h1", Symbols: funcsToSymbols([]string{"Placeholder"})},
		"bar_test.go": {Hash: "h2", Symbols: funcsToSymbols([]string{"TestBar"})},
	}
	top := enrolledStoreWithFiles(t, repoRoot, files)
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	file := filepath.Join(top, "foo_test.go")
	if err := os.WriteFile(file, []byte("package p\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "func Placeholder() {}", "func Placeholder() {}\nfunc TestBar() {}", "", nil)
	_, _, d := runHook(t, in)
	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("duplicate introduced in a test file should not ask; reason: %s", d.Hook.PermissionReason)
	}
}

func TestDuplicate_MatchOnlyInSameFile_Defers(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	// The only recorded definer of DoThing is known.go itself (a stale-snapshot
	// scenario: it was indexed there before, current on-disk content doesn't have
	// it yet). Re-adding it must not ask — self-exclusion.
	files := map[string]ir.FileIR{
		"known.go": {Hash: "h1", Symbols: funcsToSymbols([]string{"DoThing"})},
	}
	top := enrolledStoreWithFiles(t, repoRoot, files)
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\n// DoThing removed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "// DoThing removed", "func DoThing() {}", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("self-only definer should not ask; reason: %s", d.Hook.PermissionReason)
	}
}

func TestDuplicate_RedefinedInPlace_Defers(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndDupFiles("DoThing"))
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	// known.go already defines DoThing on disk, elsewhere in the file from the
	// hunk this edit touches — proving addedDefs reads the WHOLE pre-edit file
	// (wholeFileText), not just the replaced fragment. A naive hunk-scoped
	// comparison would see "DoThing" only in new_string (not in old_string,
	// which mentions only Other) and misfire; the whole-file comparison
	// correctly sees DoThing was already present in the file and stays silent.
	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc DoThing() { a() }\nfunc Other() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "func Other() {}", "func Other() {}\nfunc DoThing() { c() }", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("a name already defined elsewhere in the file should not ask; reason: %s", d.Hook.PermissionReason)
	}
}

func TestDuplicate_WriteIntroducesDup_Asks(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndDupFiles("DoThing"))
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Write", file, "", "", "package p\nfunc DoThing() {}\n", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("Write introducing a duplicate def should ask, got %q", d.Hook.PermissionDec)
	}
	if !strings.Contains(d.Hook.PermissionReason, "DoThing") {
		t.Errorf("reason should name DoThing:\n%s", d.Hook.PermissionReason)
	}
}

func TestDuplicate_MultiEditAddsDup_Asks(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndDupFiles("DoThing"))
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	edits := []editOp{{OldString: "func Placeholder() {}", NewString: "func Placeholder() {}\nfunc DoThing() {}"}}
	in := payloadOld(t, "MultiEdit", file, "", "", "", edits)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("MultiEdit introducing a duplicate def should ask, got %q", d.Hook.PermissionDec)
	}
}

func TestDuplicate_GateOff_NoCheck(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndDupFiles("DoThing"))
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "") // explicitly off

	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "func Placeholder() {}", "func Placeholder() {}\nfunc DoThing() {}", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("E5 must be inert when gated off; got ask: %s", d.Hook.PermissionReason)
	}
}

func TestDuplicate_CombinedWithHallucination_SingleAsk(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndDupFiles("DoThing"))
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Introduces a duplicate (DoThing, already defined in other.go) AND
	// references an unknown symbol (Hallucinated) in the same edit. The call
	// sits on its own line — a call fused onto the same line as its enclosing
	// func declaration is not scanned as a reference by ExtractRefs.
	in := payloadOld(t, "Edit", file, "func Placeholder() {}",
		"func Placeholder() {}\nfunc DoThing() {\n\tx := Hallucinated()\n}", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("want ask, got %q", d.Hook.PermissionDec)
	}
	if !strings.Contains(d.Hook.PermissionReason, "DoThing") || !strings.Contains(d.Hook.PermissionReason, "Hallucinated") {
		t.Errorf("ask should list both findings:\n%s", d.Hook.PermissionReason)
	}
	if rec := readLastDecisionLog(t); rec["reason"] != "violations+duplicate-symbol" {
		t.Errorf("decision reason = %v, want violations+duplicate-symbol", rec["reason"])
	}
}

func TestDuplicate_KindImportExcluded_Defers(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	files := map[string]ir.FileIR{
		"known.go": {Hash: "h1", Symbols: funcsToSymbols([]string{"Placeholder"})},
		// The only other-file match is an import-kind row (a module reference,
		// e.g. `import DoThing`), not a definition — must not count.
		"other.go": {Hash: "h2", Symbols: []ir.Symbol{{Name: "DoThing", Kind: "import"}}},
	}
	top := enrolledStoreWithFiles(t, repoRoot, files)
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "func Placeholder() {}", "func Placeholder() {}\nfunc DoThing() {}", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("import-kind-only match should not ask; reason: %s", d.Hook.PermissionReason)
	}
}

// TestDuplicate_ExportOnlyMatchExcluded_Defers pins the fix for the
// re-export/barrel-file false positive: an export-only row (no class/function
// row for the same name) is not trusted as a definition, since it may be a
// pure re-export pass-through (e.g. JS/TS `export { DoThing } from './impl'`)
// with no local definition at all — see DefsOfName's doc comment.
func TestDuplicate_ExportOnlyMatchExcluded_Defers(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	files := map[string]ir.FileIR{
		"known.go": {Hash: "h1", Symbols: funcsToSymbols([]string{"Placeholder"})},
		// The only other-file match is an export-only row — could be a genuine
		// value export or a pure re-export; either way, not a trusted definer.
		"barrel.go": {Hash: "h2", Symbols: []ir.Symbol{{Name: "DoThing", Kind: "export"}}},
	}
	top := enrolledStoreWithFiles(t, repoRoot, files)
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "func Placeholder() {}", "func Placeholder() {}\nfunc DoThing() {}", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("export-only match should not ask; reason: %s", d.Hook.PermissionReason)
	}
}

// TestDuplicate_OversizedFile_SkipsRatherThanFalsePositive pins the fix for
// wholeFileText's fail-open inversion: an existing file too large to read
// must skip the check entirely, not treat its unreadable content as "nothing
// defined" (which would misreport every def in the edit as newly added).
func TestDuplicate_OversizedFile_SkipsRatherThanFalsePositive(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndDupFiles("DoThing"))
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	// known.go already (genuinely) defines DoThing, but the file exceeds
	// maxInFileBytes, so wholeFileText can't read it. Without the fix, this
	// falsely reports DoThing as newly-added and asks; with the fix, the
	// check is skipped entirely (defer), since the file's true prior content
	// can't be determined.
	padding := strings.Repeat("// pad\n", maxInFileBytes/len("// pad\n")+1)
	content := padding + "func DoThing() {}\n"
	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "// pad", "// pad\nfunc DoThing() {}", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("oversized existing file should skip (not false-positive) the duplicate check; reason: %s", d.Hook.PermissionReason)
	}
}

// TestDuplicate_PureDeletionEdit_HitsEmptyInputFastPath pins the fix for the
// removedText gate: E5 never uses removedText (it reads the whole file
// itself via wholeFileText), so a pure-deletion Edit (non-empty old_string,
// empty new_string) with ONLY RUNECHO_GUARD_DUPLICATE=1 set must still hit
// the cheap empty-input fast path — not fall through into the full
// store/lookup pipeline for a no-op edit.
func TestDuplicate_PureDeletionEdit_HitsEmptyInputFastPath(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndDupFiles("DoThing"))
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")
	// This test's whole point is isolating "only E5 enabled" — explicitly
	// clear the other two gates so an ambient RUNECHO_GUARD_DANGLING=1 (e.g.
	// from a dogfooding shell's exported env) can't mask the fast-path bug
	// this pins by making removedText non-empty for an unrelated reason.
	t.Setenv("RUNECHO_GUARD_DANGLING", "")
	t.Setenv("RUNECHO_GUARD_DROPPED_IMPORT", "")

	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "func Placeholder() {}", "", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("pure-deletion edit should not ask; reason: %s", d.Hook.PermissionReason)
	}
	if rec := readLastDecisionLog(t); rec["reason"] != "empty-input" {
		t.Errorf("decision reason = %v, want empty-input (fast path should fire, not fall through to the store lookup)", rec["reason"])
	}
}
