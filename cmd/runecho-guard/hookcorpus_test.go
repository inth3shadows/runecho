package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// hookCase is one replayable fixture for a HOOK-LEVEL guard check. Five checks —
// dangling-refs, dropped-import, duplicate-symbol, file-scope, contract — need
// old-vs-new edit text PLUS an enrolled snapshot store and are reachable ONLY
// through the hook entry point (runHookMode). The published corpus in
// internal/guard drives guard.Run in-process, which cannot reach them, so the
// catch-rate it reports describes one check of six (#227). This harness closes
// that gap by replaying such cases as data through runHookMode.
//
// Phase 1 (this file) covers duplicate-symbol; dangling/file-scope/contract are
// tracked follow-ups on #227, each adding its own enrollment shape (a refs index,
// file-scoped symbols, an activated contract) to Enroll.
type hookCase struct {
	Name       string              `json:"name"`
	Desc       string              `json:"desc,omitempty"`
	Check      string              `json:"check"`  // gated check under test, e.g. "duplicate"
	Flags      []string            `json:"flags"`  // env "K=V" that gate the check
	Enroll     map[string][]string `json:"enroll"` // snapshot symbols: repo-relative file -> names
	File       string              `json:"file"`   // edited file, repo-relative
	Old        string              `json:"old"`    // on-disk content BEFORE the edit
	New        string              `json:"new"`    // content being written
	ExpectAsk  bool                `json:"expect_ask"`
	ExpectSyms []string            `json:"expect_symbols,omitempty"`
}

func TestHookCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "hookcorpus", "*.json"))
	if err != nil {
		t.Fatalf("glob hook corpus: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no hook corpus fixtures found under testdata/hookcorpus")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var cases []hookCase
		if err := json.Unmarshal(data, &cases); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, c := range cases {
			c := c
			t.Run(c.Name, func(t *testing.T) { runHookCase(t, c) })
		}
	}
}

func runHookCase(t *testing.T, c hookCase) {
	if len(c.Flags) == 0 {
		t.Fatalf("%s: no gating flags — a hook fixture that runs with the check off proves nothing", c.Name)
	}

	// A temp repo with the edited file on disk holding its PRE-edit content:
	// wholeFileText reads this to diff old-vs-new (PreToolUse fires before the
	// write lands).
	root := t.TempDir()
	gitInit(t, root)
	edited := filepath.Join(root, filepath.FromSlash(c.File))
	if err := os.MkdirAll(filepath.Dir(edited), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(edited, []byte(c.Old), 0o644); err != nil {
		t.Fatal(err)
	}

	enrollSnapshot(t, root, c.Enroll)
	setFlags := flagController(t, c.Flags)
	body := payload(t, "Write", edited, "", c.New, nil)

	if c.ExpectAsk {
		// Anti-vacuous proof (#227's central hazard): with the gating flag OFF the
		// ask must NOT appear. If it does, the fixture is not isolating this check —
		// the ask is coming from somewhere else and the fixture would report a
		// vacuous pass. Only after proving flag-off is silent do we trust flag-on.
		setFlags(false)
		if _, _, d := runHook(t, body); d.Hook.PermissionDec == "ask" {
			t.Fatalf("flag-off produced an ask (%q) — fixture does not isolate the %s check",
				d.Hook.PermissionReason, c.Check)
		}
		setFlags(true)
		_, _, d := runHook(t, body)
		if d.Hook.PermissionDec != "ask" {
			t.Fatalf("flag-on: expected an ask from the %s check, got a defer", c.Check)
		}
		for _, s := range c.ExpectSyms {
			if !strings.Contains(d.Hook.PermissionReason, s) {
				t.Errorf("ask reason does not name expected symbol %q:\n%s", s, d.Hook.PermissionReason)
			}
		}
	} else {
		// True negative: even with the check ON, no ask.
		setFlags(true)
		if _, _, d := runHook(t, body); d.Hook.PermissionDec == "ask" {
			t.Errorf("expected no ask, got:\n%s", d.Hook.PermissionReason)
		}
	}
}

// flagController captures each gating env var's original value once, restores it
// on cleanup, and returns a setter that toggles all of them on/off — so a single
// fixture can be replayed with the check off (isolation proof) then on.
func flagController(t *testing.T, flags []string) func(on bool) {
	t.Helper()
	type kv struct {
		k, v, orig string
		had        bool
	}
	parsed := make([]kv, 0, len(flags))
	for _, f := range flags {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			t.Fatalf("bad flag %q (want K=V)", f)
		}
		orig, had := os.LookupEnv(k)
		parsed = append(parsed, kv{k, v, orig, had})
	}
	t.Cleanup(func() {
		for _, p := range parsed {
			if p.had {
				os.Setenv(p.k, p.orig)
			} else {
				os.Unsetenv(p.k)
			}
		}
	})
	return func(on bool) {
		for _, p := range parsed {
			if on {
				os.Setenv(p.k, p.v)
			} else {
				os.Unsetenv(p.k)
			}
		}
	}
}

// enrollSnapshot stands up a temp central store ($RUNECHO_HOME) and saves one
// snapshot whose per-file symbol sets are `files` (repo-relative path -> names).
// It generalizes enrolledStore (single hardcoded file) to the multi-file layout
// the hook-only checks need — duplicate-symbol resolves candidates by DefsOfName,
// which is keyed on the enrolled file paths.
func enrollSnapshot(t *testing.T, root string, files map[string][]string) string {
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

	fileIR := make(map[string]ir.FileIR, len(files))
	for path, syms := range files {
		fileIR[path] = ir.FileIR{Hash: "h_" + path, Symbols: funcsToSymbols(syms)}
	}
	irData := &ir.IR{Version: ir.IRVersion, Files: fileIR}
	if _, err := db.SaveSnapshot(id, "sess", "test", top, irData); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	return top
}
