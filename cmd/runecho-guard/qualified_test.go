package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
)

// writeGoMod creates a temp dir containing a go.mod with the given module path
// and returns the dir.
func writeGoMod(t *testing.T, module string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+module+"\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestQualifiedViolations_FlagGating(t *testing.T) {
	dir := writeGoMod(t, "github.com/acme/proj")
	whole := []guard.AddedLine{
		{Text: `import "github.com/acme/proj/internal/snap"`, LineNo: 1},
		{Text: "func f() { snap.NoSuchFunc() }", LineNo: 2},
	}
	added := []guard.AddedLine{{Text: "func f() { snap.NoSuchFunc() }", LineNo: 2}}
	known := map[string]struct{}{"RealFunc": {}}

	// Resolve the module path from the real go.mod on disk (the resolution the
	// call sites perform once before invoking the helper).
	modulePath := guard.GoModulePath(dir)
	if modulePath != "github.com/acme/proj" {
		t.Fatalf("GoModulePath = %q, want github.com/acme/proj", modulePath)
	}

	// Flag OFF → no-op regardless of content.
	t.Setenv("RUNECHO_GUARD_QUALIFIED", "")
	if v := qualifiedViolations(guard.LangGo, whole, added, known, modulePath, "internal/x/x.go"); v != nil {
		t.Fatalf("flag off must yield no violations, got %+v", v)
	}

	// Flag ON → the hallucinated same-repo call is flagged and File is stamped.
	t.Setenv("RUNECHO_GUARD_QUALIFIED", "1")
	v := qualifiedViolations(guard.LangGo, whole, added, known, modulePath, "internal/x/x.go")
	if len(v) != 1 {
		t.Fatalf("flag on must flag the hallucination, got %+v", v)
	}
	if v[0].Symbol != "snap.NoSuchFunc" || v[0].File != "internal/x/x.go" {
		t.Errorf("unexpected violation %+v", v[0])
	}

	// Non-Go language is a no-op even with the flag on.
	if v := qualifiedViolations(guard.LangPython, whole, added, known, modulePath, "x.py"); v != nil {
		t.Errorf("non-Go must be a no-op, got %+v", v)
	}

	// Empty module path (no go.mod resolved) abstains even with the flag on.
	if guard.GoModulePath(t.TempDir()) != "" {
		t.Error("a dir without go.mod must resolve to empty module path")
	}
	if v := qualifiedViolations(guard.LangGo, whole, added, known, "", "x.go"); v != nil {
		t.Errorf("empty module path must abstain, got %+v", v)
	}
}
