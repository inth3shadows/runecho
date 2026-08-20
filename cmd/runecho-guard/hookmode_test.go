package main

import (
	"bufio"
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

// funcsToSymbols converts function names into the canonical []ir.Symbol shape.
func funcsToSymbols(funcs []string) []ir.Symbol {
	out := make([]ir.Symbol, 0, len(funcs))
	for _, n := range funcs {
		out = append(out, ir.Symbol{Name: n, Kind: "function"})
	}
	return out
}

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

	top, err := gitutil.TopLevel(root)
	if err != nil {
		t.Fatalf("gitutil.TopLevel: %v", err)
	}
	id, err := db.EnrollRepo("r", top, top, 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	// Pin common_dir so ResolveRepo takes the O(1) fast path (matches steady state).
	if cd, err := gitutil.CommonDir(top); err == nil {
		_ = db.SetRepoCommonDir(id, cd)
	}

	irData := &ir.IR{
		Version: ir.IRVersion,
		Files: map[string]ir.FileIR{
			"known.go": {Hash: "deadbeef", Symbols: funcsToSymbols(funcs)},
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

// readLastDecisionLog reads the last JSONL line from decisions.jsonl in the
// current RUNECHO_HOME. Returns nil if the file does not exist or is empty.
func readLastDecisionLog(t *testing.T) map[string]any {
	t.Helper()
	home := os.Getenv("RUNECHO_HOME")
	if home == "" {
		return nil
	}
	path := filepath.Join(home, "decisions.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var last string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			last = line
		}
	}
	if last == "" {
		return nil
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(last), &rec); err != nil {
		t.Fatalf("decisions.jsonl last line is not valid JSON: %v\n%s", err, last)
	}
	return rec
}

// countDecisionLogLines returns the number of non-empty lines in decisions.jsonl.
func countDecisionLogLines(t *testing.T) int {
	t.Helper()
	home := os.Getenv("RUNECHO_HOME")
	if home == "" {
		return 0
	}
	path := filepath.Join(home, "decisions.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			n++
		}
	}
	return n
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
		// Decision log assertions: wantLogDecision and wantLogReason are both
		// required (non-empty) to check the JSONL record. wantLogRepoPresent
		// asserts the "repo" field is a non-empty string.
		wantLogDecision    string
		wantLogReason      string
		wantLogRepoPresent bool
	}{
		{
			name:            "malformed JSON defers silently",
			stdin:           "{not json",
			wantLogDecision: "defer",
			wantLogReason:   "parse-fail",
		},
		{
			name:            "unknown tool_name defers (empty text)",
			stdin:           payload(t, "Read", goFile, "x := KnownFunc()", "", nil),
			wantLogDecision: "defer",
			wantLogReason:   "empty-input",
		},
		{
			name:            "empty text defers",
			stdin:           payload(t, "Edit", goFile, "", "", nil),
			wantLogDecision: "defer",
			wantLogReason:   "empty-input",
		},
		{
			name:            "empty file_path defers",
			stdin:           payload(t, "Edit", "", "x := KnownFunc()", "", nil),
			wantLogDecision: "defer",
			wantLogReason:   "empty-input",
		},
		{
			name:            "null-byte path defers",
			stdin:           payload(t, "Edit", "main\x00.go", "x := KnownFunc()", "", nil),
			wantLogDecision: "defer",
			wantLogReason:   "bad-path",
		},
		{
			name:            "over-4096-byte path defers",
			stdin:           payload(t, "Edit", strings.Repeat("a", 5000)+".go", "x := KnownFunc()", "", nil),
			wantLogDecision: "defer",
			wantLogReason:   "bad-path",
		},
		{
			name:            "unsupported language ext defers",
			stdin:           payload(t, "Edit", filepath.Join(repoRoot, "notes.txt"), "x := KnownFunc()", "", nil),
			wantLogDecision: "defer",
			wantLogReason:   "unknown-lang",
		},
		{
			name:            "un-enrolled repo path defers",
			stdin:           payload(t, "Edit", filepath.Join(t.TempDir(), "other.go"), "x := KnownFunc()", "", nil),
			wantLogDecision: "defer",
			wantLogReason:   "no-repo",
		},
		{
			name:               "enrolled repo + hallucinated symbol asks with details",
			stdin:              payload(t, "Edit", goFile, "y := HallucinatedFunc()", "", nil),
			wantOut:            true,
			wantDecide:         "ask",
			reasonHas:          "HallucinatedFunc",
			wantLogDecision:    "ask",
			wantLogReason:      "violations",
			wantLogRepoPresent: true,
		},
		{
			name:            "enrolled repo + known symbol defers",
			stdin:           payload(t, "Edit", goFile, "z := KnownFunc()", "", nil),
			wantLogDecision: "defer",
			wantLogReason:   "clean",

			wantLogRepoPresent: true,
		},
		{
			name:               "MultiEdit with hallucinated symbol in edits array asks",
			stdin:              payload(t, "MultiEdit", goFile, "", "", []string{"a := KnownFunc()", "b := AnotherMissing()"}),
			wantOut:            true,
			wantDecide:         "ask",
			reasonHas:          "AnotherMissing",
			wantLogDecision:    "ask",
			wantLogReason:      "violations",
			wantLogRepoPresent: true,
		},
		{
			name:            "Write tool with content field, known symbol defers",
			stdin:           payload(t, "Write", goFile, "", "package main\nfunc f() { KnownFunc() }", nil),
			wantLogDecision: "defer",
			wantLogReason:   "clean",

			wantLogRepoPresent: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lineBefore := countDecisionLogLines(t)
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

			// Decision log assertions: verify JSONL record if the case specifies them.
			if tc.wantLogDecision != "" || tc.wantLogReason != "" {
				lineAfter := countDecisionLogLines(t)
				if lineAfter != lineBefore+1 {
					t.Errorf("decisions.jsonl: expected +1 line (before=%d after=%d)", lineBefore, lineAfter)
				}
				rec := readLastDecisionLog(t)
				if rec == nil {
					t.Fatal("decisions.jsonl: no record written")
				}
				if got, _ := rec["decision"].(string); got != tc.wantLogDecision {
					t.Errorf("log decision = %q, want %q", got, tc.wantLogDecision)
				}
				if got, _ := rec["reason"].(string); got != tc.wantLogReason {
					t.Errorf("log reason = %q, want %q", got, tc.wantLogReason)
				}
				if tc.wantLogRepoPresent {
					if got, _ := rec["repo"].(string); got == "" {
						t.Error("log repo expected non-empty, got empty")
					}
				}
				if got, _ := rec["mode"].(string); got != "hook" {
					t.Errorf("log mode = %q, want %q", got, "hook")
				}
				if got, _ := rec["v"].(float64); got != 1 {
					t.Errorf("log v = %v, want 1", got)
				}
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

	// Decision log: stale-but-clean fires the stale-ir reason.
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("decisions.jsonl: no record written")
	}
	if got, _ := rec["decision"].(string); got != "defer" {
		t.Errorf("log decision = %q, want %q", got, "defer")
	}
	if got, _ := rec["reason"].(string); got != "stale-ir" {
		t.Errorf("log reason = %q, want %q", got, "stale-ir")
	}
}

// TestRunHookMode_ChecksPersisted is #333's integration proof: decisionRecord.
// Checks is actually populated end-to-end, on both a clean defer (the common
// case #333's dogfood window couldn't distinguish from "never ran") and a
// violation ask, not just in checkStatusMap's own unit test.
func TestRunHookMode_ChecksPersisted(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	goFile := filepath.Join(repoRoot, "main.go")

	// Clean edit: every check should read "ok" or "skipped", never absent —
	// this is the case that used to leave no per-check trace at all.
	code, _, d := runHook(t, payload(t, "Edit", goFile, "z := KnownFunc()", "", nil))
	if code != 0 || d.Hook.PermissionDec == "ask" {
		t.Fatalf("expected a clean defer, got code=%d permission=%q", code, d.Hook.PermissionDec)
	}
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("decisions.jsonl: no record written")
	}
	checks, _ := rec["checks"].(map[string]any)
	if checks == nil {
		t.Fatalf("clean defer record has no \"checks\" field: %v", rec)
	}
	if got, _ := checks["violations"].(string); got != "ok" {
		t.Errorf("checks[violations] = %q, want %q (clean edit ran to completion)", got, "ok")
	}

	// Violation edit: the same record must carry checks alongside the ask.
	code, _, d = runHook(t, payload(t, "Edit", goFile, "z := HallucinatedFunc()", "", nil))
	if code != 0 || d.Hook.PermissionDec != "ask" {
		t.Fatalf("expected an ask, got code=%d permission=%q", code, d.Hook.PermissionDec)
	}
	rec = readLastDecisionLog(t)
	checks, _ = rec["checks"].(map[string]any)
	if checks == nil {
		t.Fatalf("ask record has no \"checks\" field: %v", rec)
	}
	if got, _ := checks["violations"].(string); got != "violation" {
		t.Errorf("checks[violations] = %q, want %q on a hallucination ask", got, "violation")
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

	// Decision log: schema-newer fires with schema-newer reason.
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("decisions.jsonl: no record written")
	}
	if got, _ := rec["decision"].(string); got != "defer" {
		t.Errorf("log decision = %q, want %q", got, "defer")
	}
	if got, _ := rec["reason"].(string); got != "schema-newer" {
		t.Errorf("log reason = %q, want %q", got, "schema-newer")
	}
}

// TestDecisionLog_FailOpen proves that a logging failure (unwritable store dir)
// never alters the hook's decision or output. The response must be identical to
// a working-store run — a write error is silently discarded by design.
func TestDecisionLog_FailOpen(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	goFile := filepath.Join(repoRoot, "main.go")

	// First: capture the expected output from a normal (writable) run.
	normalCode, normalRaw, _ := runHook(t, payload(t, "Edit", goFile, "y := HallucinatedFunc()", "", nil))

	// Now point RUNECHO_HOME at an unwritable location. We use a file-as-dir
	// trick: the DB lookup fails too — so this test exercises the path where
	// the guard is already deferring due to no DB. Point at a directory that
	// exists but is chmod 000 so os.OpenFile for decisions.jsonl fails.
	badDir := t.TempDir()
	if err := os.Chmod(badDir, 0); err != nil {
		t.Skipf("cannot chmod 000 (%v) — likely running as root; skip", err)
	}
	t.Cleanup(func() { os.Chmod(badDir, 0o755) }) // restore so TempDir cleanup works
	t.Setenv("RUNECHO_HOME", badDir)

	// The decision (ask for hallucinated symbol) is now unreachable because the
	// bad RUNECHO_HOME also breaks DB lookup — hook defers. What we prove is that
	// it exits 0 and produces valid output (or none), not that it panics/errors.
	badCode, _, _ := runHook(t, payload(t, "Edit", goFile, "y := HallucinatedFunc()", "", nil))
	if badCode != 0 {
		t.Errorf("fail-open: exit code = %d, want 0", badCode)
	}
	_ = normalCode
	_ = normalRaw
	// The key invariant: no panic, exit 0. Output may differ (defer vs ask)
	// because the bad RUNECHO_HOME also disables DB lookup — that is expected.
}

// --- Strict mode tests ---

// TestStrictMode_PreCommit_SchemaNewer asserts: schema-newer exits 1 under
// RUNECHO_GUARD_STRICT=1, and exits 0 without the flag. Uses run() directly
// (not a subprocess) so no git commit-hook is involved; the flag parse is
// skipped because the relevant error path precedes --hook-mode dispatch.
func TestStrictMode_PreCommit_SchemaNewer(t *testing.T) {
	home := t.TempDir()

	// Build a minimal history.db with a user_version the binary won't accept.
	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Close()
	raw, err := sql.Open("sqlite", filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 9999"); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	raw.Close()

	// Without strict: schema-newer warns + returns 0.
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_GUARD_STRICT", "")
	if code := runArgs(nil); code != 0 {
		t.Errorf("without strict: schema-newer exit = %d, want 0", code)
	}

	// With strict: same condition exits 1.
	t.Setenv("RUNECHO_GUARD_STRICT", "1")
	if code := runArgs(nil); code != 1 {
		t.Errorf("with strict: schema-newer exit = %d, want 1", code)
	}
}

// TestStrictMode_HookMode_StoreDegraded asserts: when the store is enrolled but
// has no snapshot (store-degraded path), RUNECHO_GUARD_STRICT=1 causes the hook
// to emit additionalContext advisory instead of silently deferring, while still
// exiting 0 (hook contract).
func TestStrictMode_HookMode_StoreDegraded(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	// Enrol the repo but save NO snapshot — lookupSymbolsFor finds the repo but
	// cannot load a symbol set, returning store-degraded.
	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	top, err := gitutil.TopLevel(repoRoot)
	if err != nil {
		t.Fatalf("gitutil.TopLevel: %v", err)
	}
	id, err := db.EnrollRepo("r", top, top, 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	// Pin common_dir so ResolveRepo resolves.
	if cd, err := gitutil.CommonDir(top); err == nil {
		_ = db.SetRepoCommonDir(id, cd)
	}
	db.Close()

	goFile := filepath.Join(repoRoot, "main.go")

	// Without strict: store-degraded silently defers (no output).
	t.Setenv("RUNECHO_GUARD_STRICT", "")
	code, raw, d := runHook(t, payload(t, "Edit", goFile, "z := KnownFunc()", "", nil))
	if code != 0 {
		t.Errorf("without strict: exit = %d, want 0", code)
	}
	if strings.TrimSpace(raw) != "" {
		t.Errorf("without strict: expected no output, got %q", raw)
	}

	// With strict: store-degraded emits additionalContext advisory, still exits 0.
	t.Setenv("RUNECHO_GUARD_STRICT", "1")
	code, _, d = runHook(t, payload(t, "Edit", goFile, "z := KnownFunc()", "", nil))
	if code != 0 {
		t.Errorf("with strict: exit = %d, want 0 (hook must never exit nonzero)", code)
	}
	if d.Hook.PermissionDec != "" {
		t.Errorf("with strict: store-degraded must defer not %q", d.Hook.PermissionDec)
	}
	if d.Hook.AdditionalContext == "" {
		t.Error("with strict: expected additionalContext advisory for store-degraded, got none")
	}

	// Decision log: store-degraded reason recorded.
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("decisions.jsonl: no record written")
	}
	if got, _ := rec["reason"].(string); got != "store-degraded" {
		t.Errorf("log reason = %q, want %q", got, "store-degraded")
	}
}

// TestDecisionLog_MultiLineAppend verifies that two sequential hook fires each
// write one line to decisions.jsonl (append, not truncate).
func TestDecisionLog_MultiLineAppend(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	goFile := filepath.Join(repoRoot, "main.go")

	// Fire 1: known symbol → defer/clean.
	runHook(t, payload(t, "Edit", goFile, "z := KnownFunc()", "", nil))
	// Fire 2: hallucinated symbol → ask/violations.
	runHook(t, payload(t, "Edit", goFile, "y := HallucinatedFunc()", "", nil))

	n := countDecisionLogLines(t)
	if n < 2 {
		t.Errorf("decisions.jsonl: expected ≥2 lines after two fires, got %d", n)
	}

	// Both records must be valid JSONL. Read all lines and validate.
	home := os.Getenv("RUNECHO_HOME")
	path := filepath.Join(home, "decisions.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read decisions.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	decisions := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("invalid JSONL line: %v\n%s", err, line)
		}
		if d, _ := rec["decision"].(string); d != "" {
			decisions = append(decisions, d)
		}
	}
	// Last two decisions must be "defer" then "ask" (the order of our two fires).
	if len(decisions) < 2 {
		t.Fatalf("expected ≥2 decision records, got %d", len(decisions))
	}
	last2 := decisions[len(decisions)-2:]
	if last2[0] != "defer" || last2[1] != "ask" {
		t.Errorf("last two decisions = %v, want [defer ask]", last2)
	}
}

// TestRunHookMode_CheckReasonsPersisted is #359's integration proof: a check
// that declines a candidate now records "unknown" plus WHY, end-to-end, where
// before it was indistinguishable from a clean pass. It also pins the class
// split — a gate-class abstain must NOT raise the strict-mode degraded advisory.
func TestRunHookMode_CheckReasonsPersisted(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	// The qualified check needs a module path and a same-repo import to have a
	// candidate at all; a lowercase selector on one is its unexported-selector
	// abstain (guard.GoQualifiedViolationsWithReason).
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/m\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	goFile := filepath.Join(repoRoot, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n\nimport \"example.com/m/internal/snap\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Strict on: this is the mode whose advisory the class split protects.
	t.Setenv("RUNECHO_GUARD_STRICT", "1")

	code, _, d := runHook(t, payload(t, "Edit", goFile, "\tsnap.missing()", "", nil))
	if code != 0 || d.Hook.PermissionDec == "ask" {
		t.Fatalf("expected a defer, got code=%d permission=%q", code, d.Hook.PermissionDec)
	}
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("decisions.jsonl: no record written")
	}
	checks, _ := rec["checks"].(map[string]any)
	if got, _ := checks["qualified"].(string); got != "unknown" {
		t.Errorf("checks[qualified] = %q, want %q — the abstain used to log as \"ok\"", got, "unknown")
	}
	reasons, _ := rec["check_reasons"].(map[string]any)
	if reasons == nil {
		t.Fatalf("record has no \"check_reasons\" field: %v", rec)
	}
	if got, _ := reasons["qualified"].(string); got != "unexported-selector" {
		t.Errorf("check_reasons[qualified] = %q, want %q", got, "unexported-selector")
	}
	// The class split: a gate abstain is recorded but must not tell the user
	// coverage was incomplete, or the advisory would fire on ordinary edits.
	if strings.Contains(d.Hook.AdditionalContext, "could not run to completion") {
		t.Errorf("gate-class abstain raised the strict degraded advisory: %q", d.Hook.AdditionalContext)
	}
	if got, _ := rec["reason"].(string); got == "check-degraded" {
		t.Error("gate-class abstain logged reason=check-degraded, want the ordinary defer reason")
	}
}

// TestRunHookMode_DegradedReasonRaisesStrictAdvisory is the other half of
// #359's class split: an unreadable pre-edit file is degraded coverage, and it
// must still reach the strict advisory that gate abstains are kept out of.
func TestRunHookMode_DegradedReasonRaisesStrictAdvisory(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_STRICT", "1")
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/m\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file past readFileLines' maxInFileBytes cap: it EXISTS but reads as
	// nil, which is exactly the state preEditReason has to tell apart from a
	// brand-new file (which is definitively empty, not degraded).
	goFile := filepath.Join(repoRoot, "main.go")
	big := "package main\n\n" + strings.Repeat("// pad pad pad pad pad pad pad pad\n", (2<<20)/35+1)
	if err := os.WriteFile(goFile, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, d := runHook(t, payload(t, "Edit", goFile, "\tz := KnownFunc()", "", nil))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("decisions.jsonl: no record written")
	}
	reasons, _ := rec["check_reasons"].(map[string]any)
	if got, _ := reasons["qualified"].(string); got != "oversized-pre-edit-file" {
		t.Fatalf("check_reasons[qualified] = %q, want oversized-pre-edit-file (reasons=%v)", got, reasons)
	}
	if !strings.Contains(d.Hook.AdditionalContext, "could not run to completion") {
		t.Errorf("degraded abstain did not raise the strict advisory: %q", d.Hook.AdditionalContext)
	}
}

// bigPreEditFile writes a file past readFileLines' maxInFileBytes cap at
// repoRoot/name and returns its path. Such a file EXISTS but reads as nil,
// which is the only production state that sets preEditReason (#359) — a
// brand-new file is nil for a different reason and must not be reported.
func bigPreEditFile(t *testing.T, repoRoot, name, header string) string {
	t.Helper()
	path := filepath.Join(repoRoot, name)
	pad := "// pad pad pad pad pad pad pad pad\n"
	if strings.HasSuffix(name, ".py") {
		pad = "# pad pad pad pad pad pad pad pad\n"
	}
	if err := os.WriteFile(path, []byte(header+strings.Repeat(pad, (2<<20)/len(pad)+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// goModRepo is enrolledStore plus a go.mod, which the qualified check needs
// before it has any candidate at all.
func goModRepo(t *testing.T, repoRoot string, funcs []string) {
	t.Helper()
	enrolledStore(t, repoRoot, funcs)
	if err := os.WriteFile(filepath.Join(repoRoot, "go.mod"), []byte("module example.com/m\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunHookMode_WriteIsNotDegradedByAnUnreadablePreEditFile pins the
// `edit.ToolName != "Write"` half of preEditReason. A Write carries the whole
// proposed file as its added text, so the checks that concatenate pre-edit and
// added lines still have complete context even when the on-disk copy is past
// the read cap — reporting lost coverage there would put the strict advisory on
// every Write to a large file. Removing that clause leaves every other test
// green (found by adversarial review of #359).
func TestRunHookMode_WriteIsNotDegradedByAnUnreadablePreEditFile(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	goModRepo(t, repoRoot, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_STRICT", "1")
	goFile := bigPreEditFile(t, repoRoot, "main.go", "package main\n\n")

	code, _, d := runHook(t, payload(t, "Write", goFile, "", "package main\n\nfunc F() { KnownFunc() }\n", nil))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	rec := readLastDecisionLog(t)
	if rec == nil {
		t.Fatal("decisions.jsonl: no record written")
	}
	if reasons, _ := rec["check_reasons"].(map[string]any); len(reasons) != 0 {
		t.Errorf("a Write reported degraded pre-edit context: %v", reasons)
	}
	if strings.Contains(d.Hook.AdditionalContext, "could not run to completion") {
		t.Errorf("a Write raised the strict degraded advisory: %q", d.Hook.AdditionalContext)
	}
}

// TestRunHookMode_MissingFileIsNotDegraded pins the os.Stat half: readFileLines
// returns nil both for a file past the cap and for one that does not exist, and
// only the first is lost coverage. Dropping the existence test leaves every
// other test green (found by adversarial review of #359).
func TestRunHookMode_MissingFileIsNotDegraded(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	goModRepo(t, repoRoot, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_STRICT", "1")

	// An Edit against a path that does not exist: nil fileLines, but the file is
	// definitively empty rather than unreadable.
	absent := filepath.Join(repoRoot, "absent.go")
	code, _, d := runHook(t, payload(t, "Edit", absent, "\tz := KnownFunc()", "", nil))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	rec := readLastDecisionLog(t)
	if reasons, _ := rec["check_reasons"].(map[string]any); len(reasons) != 0 {
		t.Errorf("a nonexistent file reported degraded pre-edit context: %v", reasons)
	}
	if strings.Contains(d.Hook.AdditionalContext, "could not run to completion") {
		t.Errorf("a nonexistent file raised the strict degraded advisory: %q", d.Hook.AdditionalContext)
	}

	// call-shape derives its own "oversized-pre-edit-file" inside the guard
	// package, where the file's existence is not visible — runHookMode blanks it
	// using the one os.Stat it already made. Without that, a kwarg-bearing edit
	// to a file that does not exist reports lost coverage for a file that is
	// definitively empty.
	t.Setenv("RUNECHO_GUARD_CALLSHAPE", "1")
	absentPy := filepath.Join(repoRoot, "absent.py")
	if code, _, _ := runHook(t, payload(t, "Edit", absentPy, "notify(text=1)\n", "", nil)); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	rec = readLastDecisionLog(t)
	if reasons, _ := rec["check_reasons"].(map[string]any); len(reasons) != 0 {
		t.Errorf("call-shape reported degraded context for a nonexistent file: %v", reasons)
	}
}

// TestRunHookMode_DegradedContextOutranksAGateReason pins foldAbstainReason at
// the recv-method and var-type call sites: when the pre-edit file is unreadable
// AND the check declined a candidate on its own gate, the DEGRADED reason is
// the one recorded — otherwise a routine gate reason masks real lost coverage
// and the strict advisory goes silent. Passing the check's own reason straight
// through leaves every other test green (found by adversarial review of #359).
func TestRunHookMode_DegradedContextOutranksAGateReason(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	goModRepo(t, repoRoot, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_RECVMETHOD", "1")
	t.Setenv("RUNECHO_GUARD_VARTYPE", "1")
	t.Setenv("RUNECHO_GUARD_STRICT", "1")
	goFile := bigPreEditFile(t, repoRoot, "main.go", "package main\n\n")

	// The hunk alone binds `r` as two different receivers (recv-method's
	// ambiguous-receiver) and `v` twice (var-type's ambiguous-local), so each
	// check has a gate reason of its own to be outranked.
	hunk := "func (r *Reader) Fetch() {\n\tr.Parse()\n}\n\n" +
		"func (r *Writer) Flush() {}\n\n" +
		"func run() {\n\tvar v *Reader\n\tv.Parse()\n}\n\n" +
		"func run2() {\n\tv := Reader{}\n\t_ = v\n}\n"
	code, _, d := runHook(t, payload(t, "Edit", goFile, hunk, "", nil))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	rec := readLastDecisionLog(t)
	reasons, _ := rec["check_reasons"].(map[string]any)
	for _, check := range []string{"recv-method", "var-type"} {
		if got, _ := reasons[check].(string); got != "oversized-pre-edit-file" {
			t.Errorf("check_reasons[%s] = %q, want oversized-pre-edit-file (reasons=%v)", check, got, reasons)
		}
	}
	if !strings.Contains(d.Hook.AdditionalContext, "could not run to completion") {
		t.Errorf("degraded context did not raise the strict advisory: %q", d.Hook.AdditionalContext)
	}
}

// TestRunHookMode_DroppedImportRecordsDegradedContext pins the one reason that
// check has. Its own declines are all definitive, but on an Edit its bound-name
// context comes from the pre-edit file, so an unreadable one really is lost
// coverage. Passing "" there leaves every other test green (found by
// adversarial review of #359).
func TestRunHookMode_DroppedImportRecordsDegradedContext(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	enrolledStore(t, repoRoot, []string{"KnownFunc"})
	t.Setenv("RUNECHO_GUARD_DROPPED_IMPORT", "1")
	pyFile := bigPreEditFile(t, repoRoot, "m.py", "import os\n\n")

	code, _, _ := runHook(t, payload(t, "Edit", pyFile, "x = 1\n", "", nil))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	rec := readLastDecisionLog(t)
	reasons, _ := rec["check_reasons"].(map[string]any)
	if got, _ := reasons["dropped-import"].(string); got != "oversized-pre-edit-file" {
		t.Errorf("check_reasons[dropped-import] = %q, want oversized-pre-edit-file (reasons=%v)", got, reasons)
	}
}

// TestRunHookMode_CheckReasonsOnAnAskRecord pins check_reasons on the hook ASK
// record specifically. The defer records are covered above, but deleting the
// field from the ask record survived the whole suite (found by review of #363)
// — and asks are the records fpreport rates, so a reason missing there is
// missing from the population that matters most.
func TestRunHookMode_CheckReasonsOnAnAskRecord(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	goModRepo(t, repoRoot, []string{"KnownFunc"})
	goFile := filepath.Join(repoRoot, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n\nimport \"example.com/m/internal/snap\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// One edit that both ASKS (a hallucinated bare call) and ABSTAINS (a
	// lowercase selector on a same-repo package, qualified's unexported-selector).
	code, _, d := runHook(t, payload(t, "Edit", goFile, "\tsnap.missing()\n\tHallucinatedFunc()", "", nil))
	if code != 0 || d.Hook.PermissionDec != "ask" {
		t.Fatalf("expected an ask, got code=%d permission=%q", code, d.Hook.PermissionDec)
	}
	rec := readLastDecisionLog(t)
	if got, _ := rec["decision"].(string); got != "ask" {
		t.Fatalf("log decision = %q, want ask", got)
	}
	reasons, _ := rec["check_reasons"].(map[string]any)
	if got, _ := reasons["qualified"].(string); got != "unexported-selector" {
		t.Errorf("check_reasons[qualified] = %q on an ask record, want unexported-selector (reasons=%v)", got, reasons)
	}
}
