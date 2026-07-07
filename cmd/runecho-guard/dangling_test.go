package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// --- pure unit tests (no store) ---

func TestDeletedDefs(t *testing.T) {
	tests := []struct {
		name     string
		old, new string
		want     []string
	}{
		{"removed def", "func DoThing() {}", "", []string{"DoThing"}},
		{"no defs removed", "x := callIt()", "y := other()", nil},
		{"redefined in place (rename body, same name)", "func DoThing() { a() }", "func DoThing() { b() }", nil},
		{"removed one, kept other", "func A() {}\nfunc B() {}", "func B() {}", []string{"A"}},
		{"empty old", "", "func New() {}", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deletedDefs(guard.LangGo, tt.old, tt.new)
			if len(got) != len(tt.want) {
				t.Fatalf("deletedDefs = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("deletedDefs = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestHookOldText(t *testing.T) {
	if got := hookOldText("Edit", "old code", nil); got != "old code" {
		t.Errorf("Edit old = %q", got)
	}
	if got := hookOldText("Write", "ignored", nil); got != "" {
		t.Errorf("Write has no old_string, got %q", got)
	}
	edits := []editOp{{OldString: "a()"}, {OldString: ""}, {OldString: "b()"}}
	if got := hookOldText("MultiEdit", "", edits); got != "a()\nb()" {
		t.Errorf("MultiEdit old = %q, want non-empty joined", got)
	}
	if got := hookOldText("Read", "x", edits); got != "" {
		t.Errorf("unhandled tool should yield empty, got %q", got)
	}
}

func TestAskReason(t *testing.T) {
	cases := []struct {
		v, d, i, u bool
		want       string
	}{
		{true, false, false, false, "violations"},
		{false, true, false, false, "dangling"},
		{false, false, true, false, "dropped-import"},
		{false, false, false, true, "duplicate-symbol"},
		{true, true, false, false, "violations+dangling"},
		{true, false, true, false, "violations+dropped-import"},
		{true, false, false, true, "violations+duplicate-symbol"},
		{false, true, true, false, "dangling+dropped-import"},
		{true, true, true, true, "violations+dangling+dropped-import+duplicate-symbol"},
		{false, false, false, false, "violations"}, // not called in practice when all false
	}
	for _, c := range cases {
		if got := askReason(c.v, c.d, c.i, c.u); got != c.want {
			t.Errorf("askReason(%v,%v,%v,%v) = %q, want %q", c.v, c.d, c.i, c.u, got, c.want)
		}
	}
}

func TestExcludeSelf(t *testing.T) {
	got := excludeSelf([]string{"a.go", "b.go", "self.go"}, "self.go")
	if len(got) != 2 || got[0] != "a.go" || got[1] != "b.go" {
		t.Errorf("excludeSelf dropped wrong entry: %v", got)
	}
	// Empty self disables exclusion (safe-but-noisier direction).
	if got := excludeSelf([]string{"a.go"}, ""); len(got) != 1 {
		t.Errorf("empty self should exclude nothing, got %v", got)
	}
}

// --- store-backed tests ---

// enrolledStoreWithFiles stands up a temp store, enrolls the git repo at root,
// and saves one snapshot built from files (path -> FileIR, with Symbols and
// Refs). Returns the enrolled working-tree path. Mirrors enrolledStore but lets
// a test seed the V6 refs index that E1 reads.
func enrolledStoreWithFiles(t *testing.T, root string, files map[string]ir.FileIR) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer db.Close()

	top, err := gitutil.TopLevel(root)
	if err != nil {
		t.Fatalf("gitutil.TopLevel: %v", err)
	}
	id, err := db.EnrollRepo("r", top, top, 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	if cd, err := gitutil.CommonDir(top); err == nil {
		_ = db.SetRepoCommonDir(id, cd)
	}
	irData := &ir.IR{Version: ir.IRVersion, Files: files}
	if _, err := db.SaveSnapshot(id, "sess", "test", top, irData); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	return top
}

// payloadOld is payload() plus old_string support (Edit + per-edit MultiEdit).
func payloadOld(t *testing.T, tool, filePath, oldString, newString, content string, edits []editOp) string {
	t.Helper()
	in := map[string]any{"file_path": filePath}
	if oldString != "" {
		in["old_string"] = oldString
	}
	if newString != "" {
		in["new_string"] = newString
	}
	if content != "" {
		in["content"] = content
	}
	if edits != nil {
		in["edits"] = edits
	}
	b, err := json.Marshal(map[string]any{"tool_name": tool, "tool_input": in})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// defAndRefFiles builds a two-file snapshot: known.go defines def, caller refs it.
func defAndRefFiles(def, callerRef string) map[string]ir.FileIR {
	return map[string]ir.FileIR{
		"known.go":  {Hash: "h1", Symbols: funcsToSymbols([]string{def})},
		"caller.go": {Hash: "h2", Refs: []string{callerRef}},
	}
}

func TestDangling_RemovedDefStillReferenced_Asks(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")

	file := filepath.Join(top, "known.go")
	in := payloadOld(t, "Edit", file, "func DoThing() {}", "", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("want ask, got %q", d.Hook.PermissionDec)
	}
	if !strings.Contains(d.Hook.PermissionReason, "DoThing") || !strings.Contains(d.Hook.PermissionReason, "caller.go") {
		t.Errorf("reason should name the symbol and its referrer:\n%s", d.Hook.PermissionReason)
	}
	if rec := readLastDecisionLog(t); rec["reason"] != "dangling" {
		t.Errorf("decision reason = %v, want dangling", rec["reason"])
	}
}

// TestDangling_AskIsNotLearnEligible pins F2: a dangling ask must record the
// flagged symbol under `symbols` (observability) but NOT under `learn_symbols`.
// Only hallucination-origin (violations) names are learn-eligible — if a dangling
// approval trained the learned-allow store, it would later blind guard.Run's
// hallucination check to a genuine hallucination of that same deleted name.
func TestDangling_AskIsNotLearnEligible(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")

	file := filepath.Join(top, "known.go")
	in := payloadOld(t, "Edit", file, "func DoThing() {}", "", "", nil)
	_, _, d := runHook(t, in)
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("want ask, got %q", d.Hook.PermissionDec)
	}

	rec := readLastDecisionLog(t)
	if rec["reason"] != "dangling" {
		t.Fatalf("decision reason = %v, want dangling", rec["reason"])
	}
	syms, _ := rec["symbols"].([]any)
	if len(syms) != 1 || syms[0] != "DoThing" {
		t.Errorf("symbols = %v, want [DoThing] for observability", rec["symbols"])
	}
	if learn, ok := rec["learn_symbols"]; ok {
		if ls, _ := learn.([]any); len(ls) != 0 {
			t.Errorf("learn_symbols = %v, want empty — a dangling ask must not train learned-allow", learn)
		}
	}
}

func TestDangling_ReferencedOnlyBySelf_Defers(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	// Only known.go references DoThing — deleting def + its sole (same-file) use
	// is legitimate, so no warning.
	files := map[string]ir.FileIR{
		"known.go": {Hash: "h1", Symbols: funcsToSymbols([]string{"DoThing"}), Refs: []string{"DoThing"}},
	}
	top := enrolledStoreWithFiles(t, repoRoot, files)
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")

	file := filepath.Join(top, "known.go")
	in := payloadOld(t, "Edit", file, "func DoThing() {}", "// removed", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("self-only referrer should not ask; reason: %s", d.Hook.PermissionReason)
	}
}

func TestDangling_RedefinedInPlace_Defers(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")

	file := filepath.Join(top, "known.go")
	// Same name in old and new → in-place edit, not a deletion.
	in := payloadOld(t, "Edit", file, "func DoThing() { a() }", "func DoThing() { b() }", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("in-place redefinition should not ask; reason: %s", d.Hook.PermissionReason)
	}
}

func TestDangling_WriteDropsReferencedDef_Asks(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")

	// Pre-edit on-disk file defines DoThing; the Write content drops it.
	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc DoThing() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Write", file, "", "", "package p\nfunc Other() {}\n", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("Write dropping a referenced def should ask, got %q", d.Hook.PermissionDec)
	}
	if !strings.Contains(d.Hook.PermissionReason, "DoThing") {
		t.Errorf("reason should name DoThing:\n%s", d.Hook.PermissionReason)
	}
}

// TestDangling_WriteWipesReferencedDef_Asks pins #7: a full-file-deletion Write
// (empty content) removes every definition, but the empty-input guard used to
// bail before the pre-edit on-disk file — the documented deletion source for
// Write — was ever read, silently skipping the dangling-ref check. Wiping a file
// is exactly when the check matters most.
func TestDangling_WriteWipesReferencedDef_Asks(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")

	// Pre-edit on-disk file defines DoThing; the Write wipes it to empty content.
	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc DoThing() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Write", file, "", "", "", nil) // content "" = full wipe
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("Write wiping a referenced def should ask, got %q", d.Hook.PermissionDec)
	}
	if !strings.Contains(d.Hook.PermissionReason, "DoThing") {
		t.Errorf("reason should name DoThing:\n%s", d.Hook.PermissionReason)
	}
}

// TestDangling_WriteCreatesEmptyFile_FastDefer pins review finding #4: a Write
// that CREATES a new empty file (path absent, content="") has nothing to delete,
// so it must take the cheap empty-input defer rather than paying DB open + file
// reads for the deletion checks. The os.Stat guard keeps it on the fast path;
// before that guard it fell through to the deletion machinery and defer-with-no-
// finding instead (reason != "empty-input").
func TestDangling_WriteCreatesEmptyFile_FastDefer(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")

	file := filepath.Join(top, "brand_new.go") // does not exist on disk
	in := payloadOld(t, "Write", file, "", "", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("new empty-file Write should not ask, got: %s", d.Hook.PermissionReason)
	}
	if rec := readLastDecisionLog(t); rec["reason"] != "empty-input" {
		t.Errorf("new empty-file Write should fast-defer with reason=empty-input, got %v", rec["reason"])
	}
}

func TestDangling_MultiEditRemovesDef_Asks(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")

	file := filepath.Join(top, "known.go")
	edits := []editOp{{OldString: "func DoThing() {}", NewString: ""}}
	in := payloadOld(t, "MultiEdit", file, "", "", "", edits)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("MultiEdit removing a referenced def should ask, got %q", d.Hook.PermissionDec)
	}
}

func TestDangling_GateOff_NoCheck(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "") // explicitly off

	file := filepath.Join(top, "known.go")
	// new_string non-empty so the edit isn't dropped as empty-input; E1 simply
	// must not fire.
	in := payloadOld(t, "Edit", file, "func DoThing() {}", "// gone", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("E1 must be inert when gated off; got ask: %s", d.Hook.PermissionReason)
	}
}

func TestDangling_CombinedWithHallucination_SingleAsk(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")

	file := filepath.Join(top, "known.go")
	// Removes DoThing (dangling) AND references an unknown symbol (hallucination).
	in := payloadOld(t, "Edit", file, "func DoThing() {}", "x := Hallucinated()", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("want ask, got %q", d.Hook.PermissionDec)
	}
	if !strings.Contains(d.Hook.PermissionReason, "DoThing") || !strings.Contains(d.Hook.PermissionReason, "Hallucinated") {
		t.Errorf("ask should list both findings:\n%s", d.Hook.PermissionReason)
	}
	if rec := readLastDecisionLog(t); rec["reason"] != "violations+dangling" {
		t.Errorf("decision reason = %v, want violations+dangling", rec["reason"])
	}
}
