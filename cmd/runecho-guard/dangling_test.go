package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
		f    firedChecks
		want string
	}{
		{firedChecks{Violations: true}, "violations"},
		{firedChecks{Dangling: true}, "dangling"},
		{firedChecks{Dropped: true}, "dropped-import"},
		{firedChecks{Duplicate: true}, "duplicate-symbol"},
		{firedChecks{CallShape: true}, "call-shape"},
		// #268: these three used to be indistinguishable from Violations, which
		// is what made their own false-positive rate unmeasurable.
		{firedChecks{FileScope: true}, "file-scope"},
		{firedChecks{Qualified: true}, "qualified"},
		{firedChecks{DepsGo: true}, "deps-go"},
		{firedChecks{Violations: true, Dangling: true}, "violations+dangling"},
		{firedChecks{Violations: true, Dropped: true}, "violations+dropped-import"},
		{firedChecks{Violations: true, Duplicate: true}, "violations+duplicate-symbol"},
		{firedChecks{Violations: true, CallShape: true}, "violations+call-shape"},
		{firedChecks{Dangling: true, Dropped: true}, "dangling+dropped-import"},
		// The pre-#268 ordering must be preserved for combinations that predate
		// it, or every historical bucket becomes incomparable rather than just
		// the ones that involve a new term.
		{firedChecks{Violations: true, Dangling: true, Dropped: true, Duplicate: true, CallShape: true},
			"violations+dangling+dropped-import+duplicate-symbol+call-shape"},
		// The new terms sit between violations and dangling, same family first.
		{firedChecks{Violations: true, FileScope: true, Dangling: true}, "violations+file-scope+dangling"},
		{firedChecks{FileScope: true, Qualified: true, DepsGo: true}, "file-scope+qualified+deps-go"},
		{firedChecks{}, "violations"}, // not called in practice when all false
	}
	for _, c := range cases {
		if got := askReason(c.f); got != c.want {
			t.Errorf("askReason(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
}

// firedOnly returns a firedChecks with exactly the named bool field set, so the
// two tests below can enumerate the struct by REFLECTION rather than by a
// hand-written list.
//
// That choice is the point of this helper. A table someone must remember to
// extend is not a pin, it is a habit — and an unextended one is precisely the
// hole this file's gate change was written to close (#268 added three fields;
// two of their assignments shipped with nothing pinning them). Add a field to
// firedChecks and forget it in anyNonViolation or askReason, and reflection
// fails the build rather than staying quietly green.
func firedOnly(t *testing.T, field string) firedChecks {
	t.Helper()
	var f firedChecks
	v := reflect.ValueOf(&f).Elem().FieldByName(field)
	if !v.IsValid() || v.Kind() != reflect.Bool {
		t.Fatalf("firedChecks has no bool field %q", field)
	}
	v.SetBool(true)
	return f
}

// firedCheckFields lists every field of firedChecks, in declaration order.
func firedCheckFields(t *testing.T) []string {
	t.Helper()
	rt := reflect.TypeOf(firedChecks{})
	names := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Type.Kind() != reflect.Bool {
			t.Fatalf("firedChecks.%s is not a bool — these tests assume a flat bool struct", rt.Field(i).Name)
		}
		names = append(names, rt.Field(i).Name)
	}
	if len(names) < 2 {
		t.Fatal("reflection found almost no fields — the walk is broken, not the struct")
	}
	return names
}

func TestFiredChecksAnyNonViolation(t *testing.T) {
	if (firedChecks{}).anyNonViolation() {
		t.Error("zero firedChecks must report nothing fired — it half-gates the clean-path return")
	}
	// Every field except Violations must count. Four of them (dangling, dropped,
	// duplicate, call-shape) are the ONLY record that their check fired, so a
	// field omitted here makes the hook take the clean path with a real finding
	// in hand and silently drop the ask. The other three also merge into
	// `violations`, where len() catches them — their presence here is deliberate
	// redundancy, and asserting it keeps the two halves of the gate from silently
	// drifting apart.
	for _, name := range firedCheckFields(t) {
		if name == "Violations" {
			continue
		}
		if !firedOnly(t, name).anyNonViolation() {
			t.Errorf("anyNonViolation() ignores %s — an ask with only that finding would be dropped", name)
		}
	}
	// Violations must NOT be counted here: the gate reads len(violations)
	// directly, and folding the flag back in would restore the bookkeeping
	// dependency this split exists to remove.
	if (firedChecks{Violations: true}).anyNonViolation() {
		t.Error("anyNonViolation() counts Violations — the gate must read the slice, not the flag")
	}
}

// TestAskReasonNamesEveryField is the log-side twin of the test above: every
// field must map to its own decision-log bucket. A field askReason does not know
// about falls through to the zero-value "violations" fallback, which is exactly
// the mislabelling #268 was opened to fix — so a duplicate term is the failure
// signal, not merely an empty one.
func TestAskReasonNamesEveryField(t *testing.T) {
	seen := map[string]string{}
	for _, name := range firedCheckFields(t) {
		got := askReason(firedOnly(t, name))
		if got == "" {
			t.Errorf("askReason names nothing for %s", name)
			continue
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("askReason returns %q for both %s and %s — %s has no bucket of its own, so "+
				"its false-positive rate is unmeasurable and it inflates %s's", got, prev, name, name, prev)
			continue
		}
		seen[got] = name
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
	// The bodies call a builtin rather than arbitrary `a()`/`b()` filler: with
	// unexported Go references now checked, an invented lowercase call is a real
	// violation and would make this test fail for a reason unrelated to dangling.
	in := payloadOld(t, "Edit", file, "func DoThing() { println(1) }", "func DoThing() { println(2) }", "", nil)
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

// TestDangling_WriteOversizedOldFile_StrictAdvisory pins the F22 fix: a Write
// replacing an existing file the guard cannot read definitively (here: over the
// maxInFileBytes cap) used to fabricate oldText="" — deletedDefs then saw
// "nothing deleted" and the E1/dropped-import checks silently passed, exactly
// the false "clean" E5 already avoids via wholeFileText's definitive flag.
// Under strict mode that degradation must surface as an advisory (reason
// store-degraded), not a silent defer.
func TestDangling_WriteOversizedOldFile_StrictAdvisory(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")
	t.Setenv("RUNECHO_GUARD_STRICT", "1")

	// Pre-edit on-disk file defines DoThing but exceeds the in-file read cap.
	big := "package p\nfunc DoThing() {}\n//" + strings.Repeat("x", maxInFileBytes)
	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte(big), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Write", file, "", "", "package p\n", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("degraded deletion-side check must not ask, got: %s", d.Hook.PermissionReason)
	}
	if !strings.Contains(d.Hook.AdditionalContext, "deletion-side") {
		t.Errorf("strict mode should surface a degraded-coverage advisory, got context %q", d.Hook.AdditionalContext)
	}
	if rec := readLastDecisionLog(t); rec["reason"] != "check-degraded" {
		t.Errorf("decision log reason = %v, want check-degraded", rec["reason"])
	}
}

// TestDangling_RefsQueryError_StrictAdvisory pins the F21 fix: a per-symbol
// RefsToName failure inside checkDanglingRefs was silently swallowed
// (`continue`), so a broken/partial store read as a clean pass on the exact
// signal E1 exists for. The failure must be counted and, under strict mode,
// surfaced as the same degraded-coverage advisory.
func TestDangling_RefsQueryError_StrictAdvisory(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))
	t.Setenv("RUNECHO_GUARD_DANGLING", "1")
	t.Setenv("RUNECHO_GUARD_STRICT", "1")

	// Break the refs index out from under E1 (the snapshot row itself stays
	// valid, so openLatestSnapshot succeeds and only the per-symbol query fails).
	dbPath := filepath.Join(os.Getenv("RUNECHO_HOME"), "history.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw store: %v", err)
	}
	if _, err := raw.Exec("DROP TABLE refs"); err != nil {
		raw.Close()
		t.Fatalf("drop refs: %v", err)
	}
	raw.Close()

	file := filepath.Join(top, "known.go")
	if err := os.WriteFile(file, []byte("package p\nfunc DoThing() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	in := payloadOld(t, "Edit", file, "func DoThing() {}", "", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("query-error path must not ask, got: %s", d.Hook.PermissionReason)
	}
	if !strings.Contains(d.Hook.AdditionalContext, "deletion-side") {
		t.Errorf("strict mode should surface the swallowed query error as an advisory, got context %q", d.Hook.AdditionalContext)
	}
	if rec := readLastDecisionLog(t); rec["reason"] != "check-degraded" {
		t.Errorf("decision log reason = %v, want check-degraded", rec["reason"])
	}
}

// TestOpenLatestSnapshot_StoreError_CountsDegraded pins #138: a STORE-LEVEL failure
// in openLatestSnapshot — here db.List failing because the snapshots table is gone —
// must be reported as degraded so checkDanglingRefs/checkDuplicateDefs return
// queryErrs>0, not the silent (nil, 0) that used to make a broken store read as a
// clean E1/E5 pass. This is the layer above the per-symbol query the F21 test covers.
func TestOpenLatestSnapshot_StoreError_CountsDegraded(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, defAndRefFiles("DoThing", "DoThing"))

	// Drop the snapshots table so ResolveRepo still succeeds (repos intact) but
	// db.List fails — exactly the store-level path that folded into a silent ok=false.
	dbPath := filepath.Join(os.Getenv("RUNECHO_HOME"), "history.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw store: %v", err)
	}
	if _, err := raw.Exec("DROP TABLE snapshots"); err != nil {
		raw.Close()
		t.Fatalf("drop snapshots: %v", err)
	}
	raw.Close()

	file := filepath.Join(top, "known.go")
	if warns, qErrs := checkDanglingRefs(filepath.Dir(file), file, []string{"DoThing"}); qErrs != 1 || warns != nil {
		t.Errorf("checkDanglingRefs on store-level failure: want (nil, 1), got (%v, %d)", warns, qErrs)
	}
	if warns, qErrs := checkDuplicateDefs(guard.LangGo, filepath.Dir(file), file, []string{"DoThing"}, false); qErrs != 1 || warns != nil {
		t.Errorf("checkDuplicateDefs on store-level failure: want (nil, 1), got (%v, %d)", warns, qErrs)
	}
}
