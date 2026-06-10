package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// e6CountTraces returns how many "e6" records decisions.jsonl holds in home.
func e6CountTraces(t *testing.T, home string) int {
	t.Helper()
	f, err := os.Open(filepath.Join(home, "decisions.jsonl"))
	if err != nil {
		return 0 // no log file yet == no traces
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec decisionRecord
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Mode == "e6" {
			n++
		}
	}
	return n
}

// TestE6DebugTrace_Gating verifies the observability contract the dogfood plan
// relies on: the E6 refresh path emits a trace record ONLY under RUNECHO_DEBUG=1
// (so "no complaints" can be distinguished from "never ran"), and writes nothing
// to decisions.jsonl otherwise (so the C3 learned-allow stream stays clean).
func TestE6DebugTrace_Gating(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	// A path with no store/db exercises a deterministic early-return outcome
	// ("no-db") without needing repo setup — the gating, not the branch, is the
	// subject here.
	missing := filepath.Join(t.TempDir(), "x.go")

	// Disabled: no trace, regardless of outcome.
	if got := refreshIRForFile(missing); got != "no-db" {
		t.Fatalf("outcome = %q, want no-db (test precondition)", got)
	}
	if n := e6CountTraces(t, home); n != 0 {
		t.Errorf("trace count with RUNECHO_DEBUG unset = %d, want 0", n)
	}

	// Enabled: exactly one trace, carrying the outcome token.
	t.Setenv("RUNECHO_DEBUG", "1")
	refreshIRForFile(missing)
	if n := e6CountTraces(t, home); n != 1 {
		t.Errorf("trace count with RUNECHO_DEBUG=1 = %d, want 1", n)
	}
}

func e6Write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func e6Has(syms map[string]struct{}, name string) bool {
	_, ok := syms[name]
	return ok
}

func e6CountAuto(t *testing.T, db *snapshot.DB, repoID int64) int {
	t.Helper()
	all, err := db.List(repoID, 1000)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	n := 0
	for _, m := range all {
		if m.Label == "auto" {
			n++
		}
	}
	return n
}

// TestRefreshIRForFile_E6 is the end-to-end proof that the auto-fresh hook closes
// the stale-IR false-positive class (the TruthTrail/FormatTrail finding): a symbol
// added after the baseline snapshot becomes visible to the guard's symbol source
// without a manual reindex, via a single rolling "auto" snapshot.
func TestRefreshIRForFile_E6(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	e6Write(t, filepath.Join(repo, "a.go"), "package x\n\nfunc Existing() {}\n")

	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	top, err := gitutil.TopLevel(repo)
	if err != nil {
		t.Fatalf("toplevel: %v", err)
	}
	id, err := db.EnrollRepo("r", top, top, 0)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if cd, err := gitutil.CommonDir(top); err == nil {
		_ = db.SetRepoCommonDir(id, cd)
	}

	// Baseline: index the repo as it is now (only Existing) and snapshot it.
	gen := ir.NewGenerator(ir.GeneratorConfig{})
	base, _, err := gen.Generate(top)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := base.Save(filepath.Join(top, ".ai", "ir.json")); err != nil {
		t.Fatalf("save ir: %v", err)
	}
	if _, err := db.SaveSnapshot(id, "sess", "test", top, base); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if syms, _ := db.SymbolsForLatestSnapshot(id); e6Has(syms, "BrandNew") {
		t.Fatal("baseline unexpectedly already knows BrandNew")
	}
	db.Close()

	// A new symbol is added mid-session (the stale-IR scenario).
	e6Write(t, filepath.Join(repo, "b.go"), "package x\n\nfunc BrandNew() {}\n")

	// PostToolUse auto-fresh fires for just the edited file.
	if got := refreshIRForFile(filepath.Join(repo, "b.go")); got != "refreshed" {
		t.Errorf("first refresh outcome = %q, want refreshed", got)
	}

	db2, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	syms, err := db2.SymbolsForLatestSnapshot(id)
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	if !e6Has(syms, "BrandNew") {
		t.Error("auto-fresh did not surface the newly added symbol BrandNew")
	}
	if !e6Has(syms, "Existing") {
		t.Error("auto-fresh dropped the pre-existing symbol Existing")
	}
	latest, err := db2.List(id, 1)
	if err != nil || len(latest) == 0 {
		t.Fatalf("list: %v", err)
	}
	if latest[0].Label != "auto" {
		t.Errorf("latest snapshot label = %q, want auto", latest[0].Label)
	}
	if n := e6CountAuto(t, db2, id); n != 1 {
		t.Errorf("auto snapshot count = %d, want 1 after first refresh", n)
	}

	// A second edit must ROLL the auto snapshot (delete prior, write fresh) — not
	// append — so history doesn't bloat with a snapshot per edit.
	e6Write(t, filepath.Join(repo, "b.go"), "package x\n\nfunc BrandNew() {}\nfunc Another() {}\n")
	if got := refreshIRForFile(filepath.Join(repo, "b.go")); got != "refreshed" {
		t.Errorf("second refresh outcome = %q, want refreshed", got)
	}
	syms2, _ := db2.SymbolsForLatestSnapshot(id)
	if !e6Has(syms2, "Another") {
		t.Error("second auto-fresh did not surface Another")
	}
	if n := e6CountAuto(t, db2, id); n != 1 {
		t.Errorf("auto snapshot count = %d after second refresh, want 1 (must roll, not append)", n)
	}
}
