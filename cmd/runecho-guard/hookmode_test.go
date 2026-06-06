package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// enrolledStore stands up a temp central store ($RUNECHO_HOME), enrolls the git
// repo at root, and saves one snapshot whose symbol set is `funcs`. It returns
// the enrolled working-tree path (the path lookupSymbolsFor resolves edits
// against). RUNECHO_HOME is set for the duration of the test so the production
// store-resolution path (runechoDir → history.db) is exercised end-to-end.
func enrolledStore(t *testing.T, root string, funcs []string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer db.Close()

	top, err := gitTopLevelFor(root)
	if err != nil {
		t.Fatalf("gitTopLevelFor: %v", err)
	}
	id, err := db.EnrollRepo("r", top, top, 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	// Pin common_dir so resolveRepo takes the O(1) fast path (matches steady state).
	if cd, err := gitutil.CommonDir(top); err == nil {
		_ = db.SetRepoCommonDir(id, cd)
	}

	irData := &ir.IR{
		Version: ir.IRVersion,
		Files: map[string]ir.FileIR{
			"known.go": {Hash: "deadbeef", Functions: funcs},
		},
	}
	if _, err := db.SaveSnapshot(id, "sess", "test", top, irData); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	return top
}

// payload renders a PreToolUse tool-call JSON body for the given tool/file/text.
func payload(t *testing.T, tool, filePath, newString, content string, edits []string) string {
	t.Helper()
	in := map[string]any{"file_path": filePath}
	if newString != "" {
		in["new_string"] = newString
	}
	if content != "" {
		in["content"] = content
	}
	if edits != nil {
		var es []map[string]string
		for _, e := range edits {
			es = append(es, map[string]string{"new_string": e})
		}
		in["edits"] = es
	}
	b, err := json.Marshal(map[string]any{"tool_name": tool, "tool_input": in})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// decision is the parsed hook output. permissionDecision/additionalContext are
// "" when absent (a bare defer emits nothing at all).
type decision struct {
	Hook struct {
		EventName         string `json:"hookEventName"`
		PermissionDec     string `json:"permissionDecision"`
		PermissionReason  string `json:"permissionDecisionReason"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// runHook drives runHookMode with the given stdin body and returns exit code,
// raw output, and the parsed decision (empty if no output).
func runHook(t *testing.T, stdin string) (int, string, decision) {
	t.Helper()
	var out bytes.Buffer
	code := runHookMode(strings.NewReader(stdin), &out)
	var d decision
	raw := out.String()
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
		}
	}
	return code, raw, d
}

// The contract: the hook ALWAYS exits 0 and NEVER emits an "allow" — every input
// resolves to ask (block-soft), defer (silence), or defer+context (advisory).
func TestRunHookMode_Contract(t *testing.T) {
	// One enrolled repo shared by the cases that need a live store. Known.go
	// declares KnownFunc; HallucinatedFunc is deliberately absent.
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	goFile := filepath.Join(repoRoot, "main.go")

	tests := []struct {
		name        string
		stdin       string
		wantOut     bool   // expect any output at all
		wantDecide  string // expected permissionDecision ("" = none)
		wantContext bool   // expect additionalContext present
		reasonHas   string // substring expected in permissionDecisionReason
	}{
		{
			name:  "malformed JSON defers silently",
			stdin: "{not json",
		},
		{
			name:  "unknown tool_name defers (empty text)",
			stdin: payload(t, "Read", goFile, "x := KnownFunc()", "", nil),
		},
		{
			name:  "empty text defers",
			stdin: payload(t, "Edit", goFile, "", "", nil),
		},
		{
			name:  "empty file_path defers",
			stdin: payload(t, "Edit", "", "x := KnownFunc()", "", nil),
		},
		{
			name:  "null-byte path defers",
			stdin: payload(t, "Edit", "main\x00.go", "x := KnownFunc()", "", nil),
		},
		{
			name:  "over-4096-byte path defers",
			stdin: payload(t, "Edit", strings.Repeat("a", 5000)+".go", "x := KnownFunc()", "", nil),
		},
		{
			name:  "unsupported language ext defers",
			stdin: payload(t, "Edit", filepath.Join(repoRoot, "notes.txt"), "x := KnownFunc()", "", nil),
		},
		{
			name:  "un-enrolled repo path defers",
			stdin: payload(t, "Edit", filepath.Join(t.TempDir(), "other.go"), "x := KnownFunc()", "", nil),
		},
		{
			name:       "enrolled repo + hallucinated symbol asks with details",
			stdin:      payload(t, "Edit", goFile, "y := HallucinatedFunc()", "", nil),
			wantOut:    true,
			wantDecide: "ask",
			reasonHas:  "HallucinatedFunc",
		},
		{
			name:  "enrolled repo + known symbol defers",
			stdin: payload(t, "Edit", goFile, "z := KnownFunc()", "", nil),
		},
		{
			name:       "MultiEdit with hallucinated symbol in edits array asks",
			stdin:      payload(t, "MultiEdit", goFile, "", "", []string{"a := KnownFunc()", "b := AnotherMissing()"}),
			wantOut:    true,
			wantDecide: "ask",
			reasonHas:  "AnotherMissing",
		},
		{
			name:  "Write tool with content field, known symbol defers",
			stdin: payload(t, "Write", goFile, "", "package main\nfunc f() { KnownFunc() }", nil),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, raw, d := runHook(t, tc.stdin)
			if code != 0 {
				t.Errorf("exit code = %d, want 0 (hook must never exit nonzero)", code)
			}
			if d.Hook.PermissionDec == "allow" {
				t.Error("hook emitted permissionDecision=allow — it must never auto-approve")
			}
			gotOut := strings.TrimSpace(raw) != ""
			if gotOut != tc.wantOut {
				t.Errorf("output present = %v, want %v (raw=%q)", gotOut, tc.wantOut, raw)
			}
			if d.Hook.PermissionDec != tc.wantDecide {
				t.Errorf("permissionDecision = %q, want %q", d.Hook.PermissionDec, tc.wantDecide)
			}
			if tc.wantContext && d.Hook.AdditionalContext == "" {
				t.Error("expected additionalContext, got none")
			}
			if tc.reasonHas != "" && !strings.Contains(d.Hook.PermissionReason, tc.reasonHas) {
				t.Errorf("reason %q does not mention %q", d.Hook.PermissionReason, tc.reasonHas)
			}
		})
	}
}

// A clean check against a STALE IR defers but attaches an advisory note via
// additionalContext (the hookDeferStale path) — never blocks.
func TestRunHookMode_StaleIRDefersWithContext(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	// Force any fresh snapshot to read as stale.
	t.Setenv("RUNECHO_GUARD_MAX_AGE", "1ns")

	goFile := filepath.Join(repoRoot, "main.go")
	code, raw, d := runHook(t, payload(t, "Edit", goFile, "z := KnownFunc()", "", nil))
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if d.Hook.PermissionDec != "" {
		t.Errorf("stale-but-clean must defer, not %q", d.Hook.PermissionDec)
	}
	if d.Hook.AdditionalContext == "" {
		t.Fatalf("expected stale advisory in additionalContext, got %q", raw)
	}
	if !strings.Contains(d.Hook.AdditionalContext, "stale") {
		t.Errorf("advisory = %q, want a staleness note", d.Hook.AdditionalContext)
	}
}

// A store migrated by a newer binary disables validation; lookupSymbolsFor
// returns a warn that the hook surfaces via additionalContext (still a defer,
// never a block) — the schema-skew-must-be-loud path.
func TestRunHookMode_SchemaNewerSurfacesWarning(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})

	// Bump user_version past what this binary understands, simulating a store
	// written by a newer runecho. OpenFast then returns ErrSchemaNewer.
	home := os.Getenv("RUNECHO_HOME")
	raw, err := sql.Open("sqlite", filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 9999"); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	raw.Close()

	goFile := filepath.Join(repoRoot, "main.go")
	code, out, d := runHook(t, payload(t, "Edit", goFile, "z := KnownFunc()", "", nil))
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if d.Hook.PermissionDec != "" {
		t.Errorf("schema-skew must defer, not %q", d.Hook.PermissionDec)
	}
	if d.Hook.AdditionalContext == "" || !strings.Contains(d.Hook.AdditionalContext, "DISABLED") {
		t.Errorf("expected a loud schema-skew advisory, got %q", out)
	}
}
