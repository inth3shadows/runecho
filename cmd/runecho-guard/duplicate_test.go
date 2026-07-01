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
	if got := wholeFileText(filepath.Join(dir, "missing.go")); got != "" {
		t.Errorf("missing file should yield empty, got %q", got)
	}

	normal := filepath.Join(dir, "normal.go")
	if err := os.WriteFile(normal, []byte("package p\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := wholeFileText(normal); got != "package p\n" {
		t.Errorf("normal file content = %q, want %q", got, "package p\n")
	}

	big := filepath.Join(dir, "big.go")
	if err := os.WriteFile(big, make([]byte, maxInFileBytes+1), 0644); err != nil {
		t.Fatal(err)
	}
	if got := wholeFileText(big); got != "" {
		t.Errorf("oversized file should yield empty, got %d bytes", len(got))
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
