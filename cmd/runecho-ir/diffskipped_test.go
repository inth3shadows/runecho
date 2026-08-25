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

// TestDiffJSON_NonCodeIsNotReported: the false-block guard. Reporting README.md
// would make a consumer that fails closed on `skipped` block every docs change,
// which is exactly why the reporting could not exist before the language table.
//
// `.git` DOES appear, and deliberately: it is an ignored DIRECTORY, a different
// class of fact from a non-code file. The two reasons answer different
// questions — "I have no parser for this" is a capability limit, "I was
// configured not to look here" is policy — and filtering the latter would mean
// runecho second-guessing which of the operator's own ignore rules matter. The
// consumer decides that; the oracle reports what it did.
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
	if code, _, stderr := runWith(t, home, []string{"runecho-ir", "snapshot", "--label=gate-base", dir}); code != 0 {
		t.Fatalf("snapshot: %d %s", code, stderr)
	}

	payload := diffSkipped(t, home, "gate-base", dir)
	skipped, ok := payload["skipped"].([]any)
	if !ok {
		t.Fatalf("`skipped` missing: %v", payload)
	}
	for _, entry := range skipped {
		e := entry.(map[string]any)
		if e["Reason"] != ir.SkipIgnoredDir {
			t.Errorf("no source file was declined, so the only skips should be ignored "+
				"directories; got %v", e)
		}
	}
	// Named explicitly so a future filter that drops it is a deliberate change
	// with a failing test, not a silent one.
	if !hasSkip(skipped, ".git", ir.SkipIgnoredDir) {
		t.Errorf(".git should be reported as an ignored directory; got %v", skipped)
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
