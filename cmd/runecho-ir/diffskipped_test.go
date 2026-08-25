package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/ir"
)

// diffSkipped runs `diff --since=<label> --json` and returns the decoded payload.
func diffSkipped(t *testing.T, home, label, dir string) map[string]any {
	t.Helper()
	code, out, stderr := runWith(t, home, []string{"runecho-ir", "diff", "--since=" + label, "--json", dir})
	if code != 0 {
		t.Fatalf("diff --json: code %d, stderr %q", code, stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("payload is not JSON (%v): %s", err, out)
	}
	return payload
}

// TestDiffJSON_NamesTheUnindexedLanguage is the end-to-end statement of the bug.
//
// Before this feature: deleting a method from a .java file produced
// total_removed: 0, an empty files list, exit 0 and empty stderr — a full-strength
// "no structural drift" for a change nothing looked at. The removal is STILL
// invisible (no Java parser exists), and that is fine; what must not happen is
// the payload staying silent about it.
func TestDiffJSON_NamesTheUnindexedLanguage(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	javaPath := filepath.Join(dir, "Widget.java")
	if err := os.WriteFile(javaPath, []byte("class Widget { public int alive(){return 1;} }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=gate-base", dir}); code != 0 {
		t.Fatalf("snapshot: %d %s", code, stderr)
	}

	// Delete the method. Invisible to the symbol diff, by construction.
	if err := os.WriteFile(javaPath, []byte("class Widget { }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	payload := diffSkipped(t, home, "gate-base", dir)

	if total, _ := payload["total_removed"].(float64); total != 0 {
		t.Fatalf("precondition changed: total_removed = %v, expected 0 (Java is not parsed)", total)
	}
	skipped, ok := payload["skipped"].([]any)
	if !ok {
		t.Fatalf("`skipped` missing from a --since payload; the live walk measured it: %v", payload)
	}
	found := false
	for _, entry := range skipped {
		e := entry.(map[string]any)
		if e["Path"] == "Widget.java" {
			found = true
			if e["Reason"] != ir.SkipUnsupportedLanguage {
				t.Errorf("Widget.java reason = %v, want %q", e["Reason"], ir.SkipUnsupportedLanguage)
			}
		}
	}
	if !found {
		t.Errorf("Widget.java not named in `skipped`: %v", skipped)
	}
	// stub.go (from irGitInit) IS indexed, so it must not be listed — a skip list
	// that also names indexed files cannot be failed closed on.
	for _, entry := range skipped {
		if entry.(map[string]any)["Path"] == "stub.go" {
			t.Error("stub.go is indexed but appears in `skipped`")
		}
	}
	if payload["skipped_truncated"] != false {
		t.Errorf("skipped_truncated = %v, want false", payload["skipped_truncated"])
	}
}

// TestDiffJSON_NonCodeIsNotReported: the false-block guard, and the reason the
// payload carries TWO arrays.
//
// The first version of this feature put ignored directories in `skipped`
// alongside unreadable files. Review found the consequence: `.git` is in
// DefaultIgnoredPaths, so `skipped` was never empty in any repo, and a gate
// prefix-matching it blocked an edit to `testdata/README.md` -- a
// documentation-only false block, exactly what the language table exists to
// prevent, arriving through the other reason code.
//
// `skipped` now carries only files the indexer could not read. Pruned
// directories live in `ignored_paths`, where a consumer applies its own policy.
func TestDiffJSON_NonCodeIsNotReported(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	for name, body := range map[string]string{
		"README.md":     "# docs\n",
		"config.yaml":   "a: 1\n",
		"data.json":     "{}\n",
		"notes.txt":     "hello\n",
		"Makefile.lock": "x\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The path that produced the false block: non-code inside an ignored
	// directory that a real repo (this one included) actually has.
	if err := os.MkdirAll(filepath.Join(dir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "testdata", "README.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=gate-base", dir}); code != 0 {
		t.Fatalf("snapshot: %d %s", code, stderr)
	}

	payload := diffSkipped(t, home, "gate-base", dir)

	skipped, ok := payload["skipped"].([]any)
	if !ok {
		t.Fatalf("`skipped` missing: %v", payload)
	}
	if len(skipped) != 0 {
		t.Errorf("no source file was declined, so `skipped` must be EMPTY -- a consumer "+
			"fails closed on it. got %v", skipped)
	}

	ignored, ok := payload["ignored_paths"].([]any)
	if !ok {
		t.Fatalf("`ignored_paths` missing: %v", payload)
	}
	// Named explicitly so a future filter that drops them is a deliberate change
	// with a failing test, not a silent one.
	for _, want := range []string{".git", "testdata"} {
		if !hasSkip(ignored, want, ir.SkipIgnoredDir) {
			t.Errorf("%s should be reported in ignored_paths; got %v", want, ignored)
		}
	}
}

// hasSkip reports whether the decoded skip list contains path with reason.
func hasSkip(skipped []any, path, reason string) bool {
	for _, entry := range skipped {
		e := entry.(map[string]any)
		if e["Path"] == path && e["Reason"] == reason {
			return true
		}
	}
	return false
}

// TestDiffJSON_IgnoredRootIsIndexedNotPruned pins the fix for a repo whose root
// directory is itself named in the ignore list.
//
// The ignore rule prunes SUBdirectories. Applied to the directory the operator
// explicitly pointed at, it pruned the entire walk: zero files indexed, a
// vacuous 100% coverage (SupportedSeen == 0), and a skip entry of "." -- a
// prefix that matches no path a consumer could hold. A checkout literally named
// `testdata`, `vendor`, or `dist` silently indexed nothing.
func TestDiffJSON_IgnoredRootIsIndexedNotPruned(t *testing.T) {
	home := t.TempDir()
	parent := t.TempDir()
	dir := filepath.Join(parent, "testdata") // basename is in DefaultIgnoredPaths
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	irGitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package p\nfunc Critical() {}\nfunc Other() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=gate-base", dir}); code != 0 {
		t.Fatalf("snapshot: %d %s", code, stderr)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package p\nfunc Other() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	payload := diffSkipped(t, home, "gate-base", dir)

	// The removal must be SEEN, not merely reported as unexamined.
	if total, _ := payload["total_removed"].(float64); total != 1 {
		t.Errorf("total_removed = %v, want 1 — the root was pruned, so nothing was indexed", total)
	}
	if ignored, _ := payload["ignored_paths"].([]any); hasSkip(ignored, ".", ir.SkipIgnoredDir) {
		t.Error(`the walk root was recorded as "." — a prefix that matches nothing`)
	}
}

// TestDiffJSON_TwoIDModeOmitsSkipped: the two-ID mode compares two stored
// indexes and never walks the filesystem, so it cannot answer "what did you
// decline?". Absence is the answer; an empty list would be a fail-open.
func TestDiffJSON_TwoIDModeOmitsSkipped(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "Widget.java"), []byte("class Widget { }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ids := make([]string, 0, 2)
	for _, label := range []string{"a", "b"} {
		code, out, stderr := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=" + label, dir})
		if code != 0 {
			t.Fatalf("snapshot %s: %d %s", label, code, stderr)
		}
		id, found := strings.CutPrefix(strings.Fields(out)[2], "id=")
		if !found {
			t.Fatalf("could not read the snapshot id from %q", out)
		}
		ids = append(ids, id)
	}

	code, out, stderr := runWith(t, home, []string{"runecho-ir", "diff", "--json", ids[0], ids[1], dir})
	if code != 0 {
		t.Fatalf("two-ID diff: %d %s", code, stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("not JSON (%v): %s", err, out)
	}
	if _, present := payload["skipped"]; present {
		t.Error("`skipped` present in two-ID mode, which never walked the filesystem")
	}
}

// TestDiffText_NamesTheUnindexedLanguage: the human surface must not be less
// honest than its own JSON. The zero-drift branch prints "No structural
// changes." — true here only because nothing was read.
func TestDiffText_NamesTheUnindexedLanguage(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "Widget.java"), []byte("class Widget { }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=gate-base", dir}); code != 0 {
		t.Fatalf("snapshot: %d %s", code, stderr)
	}

	code, out, stderr := runWith(t, home, []string{"runecho-ir", "diff", "--since=gate-base", dir})
	if code != 0 {
		t.Fatalf("diff: %d %s", code, stderr)
	}
	if !strings.Contains(out, "NOT EXAMINED") || !strings.Contains(out, "Widget.java") {
		t.Errorf("text output must name what went unexamined, got:\n%s", out)
	}
}

// TestVerify_NamesTheUnindexedLanguage: `verify` does its own live walk and is
// the most gate-like command in the tool, so it was the worst place for the
// headline bug to survive. It discarded the walk's stats and called DiffLive,
// printing a bare "No structural changes." for a repo whose only code is
// unparsed — the exact sentence `diff` was fixed to qualify.
func TestVerify_NamesTheUnindexedLanguage(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "Widget.java"), []byte("class Widget { }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=session-start", dir}); code != 0 {
		t.Fatalf("snapshot: %d %s", code, stderr)
	}

	code, out, stderr := runWith(t, home, []string{"runecho-ir", "verify", dir})
	if code != 0 {
		t.Fatalf("verify: %d %s", code, stderr)
	}
	if !strings.Contains(out, "NOT EXAMINED") || !strings.Contains(out, "Widget.java") {
		t.Errorf("verify must name what went unexamined, got:\n%s", out)
	}
}

// TestDiffText_CleanRepoIsTerminatedByExactlyOneNewline pins the most common
// output in the tool, in both directions.
//
// Both readings have been wrong once. The zero-drift branch first gained an
// unconditional "\n%s", adding a blank line for a fully-indexed repo; the
// over-correction dropped the terminator entirely, so `diff` and `verify` (both
// fmt.Print) ran the shell prompt onto the last line. An earlier version of this
// test only checked for the absence of "\n\n", which zero newlines also
// satisfies -- so it pinned one direction and missed the other.
func TestDiffText_CleanRepoIsTerminatedByExactlyOneNewline(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir) // stub.go only: fully indexed, nothing declined
	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=gate-base", dir}); code != 0 {
		t.Fatalf("snapshot: %d %s", code, stderr)
	}
	code, out, _ := runWith(t, home, []string{"runecho-ir", "diff", "--since=gate-base", dir})
	if code != 0 {
		t.Fatalf("diff: %d", code)
	}
	if !strings.HasSuffix(out, "No structural changes.\n") {
		t.Errorf("clean-repo output must end with the sentence and exactly one newline:\n%q", out)
	}
	if strings.HasSuffix(out, "\n\n") {
		t.Errorf("clean-repo output ends with a blank line:\n%q", out)
	}
}

// TestDiffText_SkipBlockIsAlsoTerminated: the same guarantee on the other
// branch, so a fix to one cannot silently regress the other.
func TestDiffText_SkipBlockIsAlsoTerminated(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "Widget.java"), []byte("class Widget { }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=gate-base", dir}); code != 0 {
		t.Fatalf("snapshot: %d %s", code, stderr)
	}
	code, out, _ := runWith(t, home, []string{"runecho-ir", "diff", "--since=gate-base", dir})
	if code != 0 {
		t.Fatalf("diff: %d", code)
	}
	if !strings.HasSuffix(out, "\n") || strings.HasSuffix(out, "\n\n") {
		t.Errorf("skip-block output must end with exactly one newline:\n%q", out)
	}
}

// TestDiffJSON_UnindexedTypeScriptVariantIsNamed. `.mts` is TypeScript, JSParser
// does not claim it, and it was missing from knownSourceExtensions too — so a
// deleted export was invisible AND unreported, reproducing the original bug in a
// language RunEcho advertises support for. `.java` going unindexed is a
// capability limit a reader can reason about; this is not.
func TestDiffJSON_UnindexedTypeScriptVariantIsNamed(t *testing.T) {
	home := t.TempDir()
	dir := t.TempDir()
	irGitInit(t, dir)
	mts := filepath.Join(dir, "mod.mts")
	if err := os.WriteFile(mts, []byte("export function T(): number { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=gate-base", dir}); code != 0 {
		t.Fatalf("snapshot: %d %s", code, stderr)
	}
	if err := os.WriteFile(mts, []byte("export const x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	payload := diffSkipped(t, home, "gate-base", dir)
	skipped, _ := payload["skipped"].([]any)
	if !hasSkip(skipped, "mod.mts", ir.SkipUnsupportedLanguage) {
		t.Errorf("mod.mts must be named in `skipped`; got %v", skipped)
	}
}
