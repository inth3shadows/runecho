package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDroppedImport_GoEditIsSkippedNotOK pins the #414/#417 perf-fix lang gate
// (verify.go, guarded by guard.DroppedImportSupportedLang): dropped-import has
// no support for Go at all (Go imports are package-qualified, so a dropped
// import surfaces as a qualified reference elsewhere), so a Go edit with the
// check enabled must record "skipped", never "ok". Before the fix, every Go
// edit paid the wholeFileBoundNames fold AND recorded "ok" — inflating the
// #415 dogfood window's Ran count for a language dropped-import can never
// fire on.
func TestDroppedImport_GoEditIsSkippedNotOK(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_DROPPED_IMPORT", "1")
	goFile := filepath.Join(repoRoot, "main.go")

	code, _, d := runHook(t, payload(t, "Edit", goFile, "z := KnownFunc()", "", nil))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("expected a clean defer, got ask: %+v", d)
	}
	rec := readLastDecisionLog(t)
	checks, _ := rec["checks"].(map[string]any)
	if checks == nil {
		t.Fatalf("record has no \"checks\" field: %v", rec)
	}
	if got, _ := checks["dropped-import"].(string); got != "skipped" {
		t.Errorf("checks[dropped-import] = %q, want %q (Go is not a dropped-import-supported language)", got, "skipped")
	}
}

// TestDroppedImport_PythonEditStillFiresAfterLazyPreBound proves the #414/#417
// laziness fix (preBound is now a callback, only invoked once an import is
// found missing from the new text) did not silently disable the slow path:
// an Edit (not Write — the case that actually needs preBound(), since a
// Write's newLines already is the whole file) that genuinely drops a used
// import must still fire.
func TestDroppedImport_PythonEditStillFiresAfterLazyPreBound(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_DROPPED_IMPORT", "1")

	pyFile := filepath.Join(repoRoot, "m.py")
	before := "from os import path\n\ndef go():\n    return path.join('a')\n"
	if err := os.WriteFile(pyFile, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	after := "def go():\n    return path.join('a')\n" // import dropped, use survives

	code, _, d := runHook(t, payloadOld(t, "Edit", pyFile, before, after, "", nil))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("expected an ask (dropped import still used), got %+v", d)
	}
	rec := readLastDecisionLog(t)
	checks, _ := rec["checks"].(map[string]any)
	if got, _ := checks["dropped-import"].(string); got != "violation" {
		t.Errorf("checks[dropped-import] = %q, want %q (reasons=%v checks=%v)", got, "violation", rec["check_reasons"], checks)
	}
}

// TestDroppedImport_GoWriteToOversizedFileSkippedUnderStrict pins the
// consequence, under RUNECHO_GUARD_STRICT=1, of the #414/#417 outer-lang-gate
// fix at verify.go (guard.DroppedImportSupportedLang(lang), the same fix
// TestDroppedImport_GoEditIsSkippedNotOK above pins on a small file): the
// outer gate short-circuits a Go edit to VerdictSkipped BEFORE the
// `!oldTextDefinitive` branch is ever reached, so an oversized pre-edit .go
// file must record dropped-import as skipped — not as an Unknown
// "oversized-pre-edit-file" the way it would if the Go path reached that
// branch (the mutation this test's own header documents catching).
//
// Consequences pinned here, all following from Skipped carrying no Reason:
// check_reasons has no dropped-import entry, the decision reason is not
// check-degraded (countDegradedUnknown never sees an Unknown for this
// check), and hookrender's strict-mode "could not run to completion"
// advisory is not emitted for it.
//
// Mutation: temporarily move the `guard.DroppedImportSupportedLang(lang)`
// half of verify.go's outer gate out of the `if`, so a Go Write reaches
// `if !oldTextDefinitive { ... VerdictUnknown, "oversized-pre-edit-file" }`
// — wholeFileText(filePath) returns definitive=false for any file over
// maxInFileBytes regardless of language, so every assertion below fails.
// Reverted after confirming the failure (not left in the tree).
func TestDroppedImport_GoWriteToOversizedFileSkippedUnderStrict(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_DROPPED_IMPORT", "1")
	t.Setenv("RUNECHO_GUARD_STRICT", "1")
	goFile := bigPreEditFile(t, repoRoot, "main.go", "package main\n\n")

	code, raw, d := runHook(t, payload(t, "Write", goFile, "",
		"package main\n\nfunc F() { KnownFunc() }\n", nil))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("expected a clean defer, got ask: %+v", d)
	}
	rec := readLastDecisionLog(t)
	checks, _ := rec["checks"].(map[string]any)
	if got, _ := checks["dropped-import"].(string); got != "skipped" {
		t.Errorf("checks[dropped-import] = %q, want skipped (Go is not a dropped-import-supported language)", got)
	}
	reasons, _ := rec["check_reasons"].(map[string]any)
	if got, has := reasons["dropped-import"]; has {
		t.Errorf("check_reasons[dropped-import] = %v, want no entry at all", got)
	}
	if got, _ := rec["reason"].(string); got == "check-degraded" {
		t.Errorf("decision reason = %q, want anything but check-degraded", got)
	}
	if strings.Contains(raw, "could not run to completion") {
		t.Errorf("hook output carries the strict-mode check-degraded advisory:\n%s", raw)
	}
}

// TestDroppedImport_PythonEditUnreadableFileButNothingDroppedIsOK pins the
// #414/#417 fix that made preEditReason (verify.go's shared "the pre-edit
// file was oversized/unreadable" signal) apply to dropped-import ONLY when
// preBound was actually invoked — i.e. only when DroppedImportRefsLinesWithBound
// found a genuine candidate (an import present in the old text and missing
// from the new one) that needed the whole-file rebind context preBound
// supplies. Before this fix, the caller built preBound (then a plain value,
// not a lazy callback) unconditionally for every non-Write edit, so an
// edit that dropped nothing still got tagged "unknown" purely because the
// unrelated pre-edit file happened to be unreadable.
//
// The edit below drops no import (its old_string is plain code, no import
// line at all), so oldImps is empty and DroppedImportRefsLinesWithBound
// returns at its very first gate — preBound is never invoked. The answer is
// definitive regardless of the file's readability, so this must be "ok", not
// "unknown".
//
// Mutation: hardcode preBoundInvoked = true in verify.go (ignore whether the
// closure actually ran). This test must fail — checks[dropped-import]
// becomes "unknown" instead of "ok". Reverted after confirming the failure.
func TestDroppedImport_PythonEditUnreadableFileButNothingDroppedIsOK(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_DROPPED_IMPORT", "1")
	pyFile := bigPreEditFile(t, repoRoot, "m.py", "import os\n\n")

	code, _, d := runHook(t, payloadOld(t, "Edit", pyFile, "x = 1\n", "x = 2\n", "", nil))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("expected a clean defer, got ask: %+v", d)
	}
	rec := readLastDecisionLog(t)
	checks, _ := rec["checks"].(map[string]any)
	if got, _ := checks["dropped-import"].(string); got != "ok" {
		t.Errorf("checks[dropped-import] = %q, want ok (no import was dropped, so preBound/fileLines is never consulted)", got)
	}
	reasons, _ := rec["check_reasons"].(map[string]any)
	if got, has := reasons["dropped-import"]; has {
		t.Errorf("check_reasons[dropped-import] = %v, want no entry at all", got)
	}
}

// TestDroppedImport_PythonEditUnreadableFileDropsUsedImportStillFires is the
// companion negative case for the fix above: when an import genuinely IS
// dropped (present in old_string, absent from new_string) and its name is
// still used in the new text, the check must still fire even though the
// pre-edit file (whose whole-file context preBound would have supplied) is
// unreadable. Matches HEAD's (pre-#414/#417) behavior for the same scenario:
// HEAD built preBound eagerly from the same unreadable fileLines, which
// wholeFileBoundNames turns into nil (no extra context) either way — so
// nothing rescues `path`, and classifyResult's "found takes precedence over
// reason" rule (checkresult.go) reports a Violation regardless of
// preEditReason. Confirmed against `git show HEAD:cmd/runecho-guard/verify.go`.
func TestDroppedImport_PythonEditUnreadableFileDropsUsedImportStillFires(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_DROPPED_IMPORT", "1")
	pyFile := bigPreEditFile(t, repoRoot, "m.py", "import os\n\n")

	before := "import os\n\ndef go():\n    return os.path.join('a')\n"
	after := "def go():\n    return os.path.join('a')\n" // import dropped, use survives
	code, _, d := runHook(t, payloadOld(t, "Edit", pyFile, before, after, "", nil))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("expected an ask (dropped import still used, unreadable pre-edit file notwithstanding), got %+v", d)
	}
	rec := readLastDecisionLog(t)
	checks, _ := rec["checks"].(map[string]any)
	if got, _ := checks["dropped-import"].(string); got != "violation" {
		t.Errorf("checks[dropped-import] = %q, want violation (reasons=%v checks=%v)", got, rec["check_reasons"], checks)
	}
}
