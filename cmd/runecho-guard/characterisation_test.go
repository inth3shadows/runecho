// characterisation_test.go — a byte-level snapshot of what the hook SAYS and
// LOGS, across every arm of runHookMode.
//
// This exists for one job: #394 splits runHookMode into a core (verifyEdit) and
// a renderer (renderHookDecision) so a second surface can consume the same
// verdicts. That split is supposed to be a pure move — every existing consumer
// must see identical bytes on stdout and an identical decisions.jsonl record.
// "Supposed to be" is not a test, and the individual TestRunHookMode_* tests
// each assert a FEATURE (this arm defers, that one names the symbol), which is
// exactly the shape that stays green while the surrounding bytes shift.
//
// So this asserts the bytes themselves, for twelve inputs spanning all four
// bail sites, the clean-defer epilogue, the ask epilogue, and the three
// degraded arms. It is deliberately dumb: it knows nothing about what the guard
// should say, only that it must keep saying it. Written BEFORE the refactor and
// its golden captured from the pre-refactor binary, or it proves nothing.
//
// Regenerate deliberately, never reflexively:
//
//	RUNECHO_GOLDEN_UPDATE=1 go test ./cmd/runecho-guard/ -run Characterisation
//
// A diff here during the #394 refactor is a BUG in the refactor. A diff here in
// any later change is a deliberate behaviour change that belongs in its own
// commit with the golden update visible in review.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// charCase is one hook invocation: a setup that returns the stdin payload, plus
// whatever paths need scrubbing out of the output so the golden is stable across
// machines and runs.
type charCase struct {
	name  string
	setup func(t *testing.T) (stdin string, scrub map[string]string)
}

func TestRunHookMode_Characterisation(t *testing.T) {
	cases := []charCase{
		{"clean-edit-enrolled", func(t *testing.T) (string, map[string]string) {
			repo := t.TempDir()
			gitInit(t, repo)
			enrolledStore(t, repo, []string{"KnownFunc"})
			return payload(t, "Edit", filepath.Join(repo, "main.go"), "z := KnownFunc()\n", "", nil),
				scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
		{"hallucination-write-with-suggestions", func(t *testing.T) (string, map[string]string) {
			repo := t.TempDir()
			gitInit(t, repo)
			enrolledStore(t, repo, []string{"ProcessData"})
			return payload(t, "Write", filepath.Join(repo, "main.go"), "",
					"package main\n\nfunc x() { ProcesData() }\n", nil),
				scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
		{"stale-ir-but-clean", func(t *testing.T) (string, map[string]string) {
			repo := t.TempDir()
			gitInit(t, repo)
			enrolledStore(t, repo, []string{"KnownFunc"})
			t.Setenv("RUNECHO_GUARD_MAX_AGE", "1ns")
			return payload(t, "Edit", filepath.Join(repo, "main.go"), "z := KnownFunc()\n", "", nil),
				scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
		{"oversized-pre-edit-file-strict", func(t *testing.T) (string, map[string]string) {
			repo := t.TempDir()
			gitInit(t, repo)
			enrolledStore(t, repo, []string{"KnownFunc"})
			// Python + file-scope, not Go + qualified: qualified SKIPS on a repo
			// with no go.mod ("no-module-path") and so never reaches the
			// oversized arm. file-scope has an explicit oversized-pre-edit-file
			// branch, which is the degraded Unknown this case exists to pin.
			// Strict, so it lands on check-degraded — case 1 already pins "clean".
			t.Setenv("RUNECHO_GUARD_STRICT", "1")
			t.Setenv("RUNECHO_GUARD_FILESCOPE", "1")
			big := filepath.Join(repo, "big.py")
			// Past maxInFileBytes, so the pre-edit context is unavailable and the
			// checks that need it record a degraded Unknown.
			if err := os.WriteFile(big, bytes.Repeat([]byte("# filler\n"), 300000), 0o644); err != nil {
				t.Fatal(err)
			}
			return payload(t, "Edit", big, "x = KnownFunc()\n", "", nil), scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
		{"unenrolled-repo", func(t *testing.T) (string, map[string]string) {
			repo := t.TempDir()
			gitInit(t, repo)
			other := t.TempDir()
			gitInit(t, other)
			enrolledStore(t, other, []string{"KnownFunc"})
			return payload(t, "Edit", filepath.Join(repo, "main.go"), "z := Whatever()\n", "", nil),
				scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
		{"schema-newer", func(t *testing.T) (string, map[string]string) {
			repo := t.TempDir()
			gitInit(t, repo)
			enrolledStore(t, repo, []string{"KnownFunc"})
			raw, err := sql.Open("sqlite", filepath.Join(os.Getenv("RUNECHO_HOME"), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec("PRAGMA user_version = 9999"); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			return payload(t, "Edit", filepath.Join(repo, "main.go"), "z := KnownFunc()\n", "", nil),
				scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
		{"empty-input", func(t *testing.T) (string, map[string]string) {
			repo := t.TempDir()
			gitInit(t, repo)
			enrolledStore(t, repo, []string{"KnownFunc"})
			return payload(t, "Edit", filepath.Join(repo, "main.go"), "", "", nil), scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
		{"bad-path-nul", func(t *testing.T) (string, map[string]string) {
			repo := t.TempDir()
			gitInit(t, repo)
			enrolledStore(t, repo, []string{"KnownFunc"})
			return payload(t, "Edit", "/tmp/a\x00b.go", "z := KnownFunc()\n", "", nil), scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
		{"unknown-lang-markdown", func(t *testing.T) (string, map[string]string) {
			repo := t.TempDir()
			gitInit(t, repo)
			enrolledStore(t, repo, []string{"KnownFunc"})
			return payload(t, "Write", filepath.Join(repo, "NOTES.md"), "", "# notes\n", nil), scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
		{"multiedit-dropped-import", func(t *testing.T) (string, map[string]string) {
			repo := t.TempDir()
			gitInit(t, repo)
			enrolledStore(t, repo, []string{"KnownFunc"})
			py := filepath.Join(repo, "m.py")
			if err := os.WriteFile(py, []byte("from os import path\n\ndef go():\n    return path.join('a')\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RUNECHO_GUARD_DROPPED_IMPORT", "1")
			return payload(t, "Write", py, "", "def go():\n    return path.join('a')\n", nil), scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
		{"duplicate-symbol", func(t *testing.T) (string, map[string]string) {
			repo := t.TempDir()
			gitInit(t, repo)
			enrollSnapshot(t, repo, map[string][]string{"a.go": {"Helper"}}, nil)
			t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")
			return payload(t, "Write", filepath.Join(repo, "b.go"), "",
				"package main\n\nfunc Helper() {}\n", nil), scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
		{"callshape-on-unenrolled", func(t *testing.T) (string, map[string]string) {
			const decl = "def fetch(url, timeout=10):\n    return url\n"
			repo := t.TempDir()
			gitInit(t, repo)
			other := t.TempDir()
			gitInit(t, other)
			enrolledStore(t, other, []string{"KnownFunc"})
			py := filepath.Join(repo, "client.py")
			if err := os.WriteFile(py, []byte(decl), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RUNECHO_GUARD_CALLSHAPE", "1")
			return payload(t, "Write", py, "",
				decl+"\ndef go():\n    return fetch(\"u\", timeuot=5)\n", nil), scrubOf(repo, os.Getenv("RUNECHO_HOME"))
		}},
	}

	var got bytes.Buffer
	for _, tc := range cases {
		// Each case runs in its own subtest purely for t.Setenv/t.TempDir
		// scoping; the golden is the concatenation, so a reordering or a dropped
		// case is as visible as a changed byte.
		t.Run(tc.name, func(t *testing.T) {
			stdin, scrub := tc.setup(t)
			var out bytes.Buffer
			code := runHookMode(strings.NewReader(stdin), &out)

			fmt.Fprintf(&got, "### %s\nexit=%d\nstdout=%s\nrecord=%s\n\n",
				tc.name, code,
				scrubAll(strings.TrimSpace(out.String()), scrub),
				scrubAll(lastRecordJSON(t), scrub))
		})
	}

	goldenPath := filepath.Join("testdata", "characterisation.golden")
	if os.Getenv("RUNECHO_GOLDEN_UPDATE") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("golden rewritten: %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (regenerate with RUNECHO_GOLDEN_UPDATE=1): %v", err)
	}
	if got.String() != string(want) {
		t.Errorf("hook output/log drifted from the golden.\n%s", firstDiff(string(want), got.String()))
	}
}

// scrubOf builds the replacement table for one case. Longest-first replacement
// happens in scrubAll; both paths are temp dirs that would otherwise make the
// golden machine-specific.
func scrubOf(repo, home string) map[string]string {
	return map[string]string{repo: "<REPO>", home: "<HOME>"}
}

// scrubAll replaces volatile substrings, longest key first so a home nested
// inside a repo (or vice versa) cannot be half-replaced.
func scrubAll(s string, scrub map[string]string) string {
	keys := make([]string, 0, len(scrub))
	for k := range scrub {
		if k != "" {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		s = strings.ReplaceAll(s, k, scrub[k])
	}
	return s
}

// lastRecordJSON re-marshals the last decisions.jsonl record with the two
// genuinely time/build-varying fields removed. Re-marshalling from a map (rather
// than echoing the raw line) sorts keys, so a field-order change in the struct
// is not a spurious diff — while a changed VALUE, a new field or a dropped field
// all still are. "(none)" distinguishes "no record written" from an empty one,
// which is exactly the difference between the bad-path arm and a bug.
func lastRecordJSON(t *testing.T) string {
	t.Helper()
	rec := readLastDecisionLog(t)
	if rec == nil {
		return "(none)"
	}
	delete(rec, "ts")
	delete(rec, "gv")
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("re-marshal record: %v", err)
	}
	return string(b)
}

// firstDiff reports the first differing line with a little context, because a
// whole-golden dump is unreadable and the first divergence is nearly always the
// cause of every later one.
func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("first difference at line %d:\n  want: %s\n  got:  %s", i+1, wl, gl)
		}
	}
	return "(no line differs; trailing bytes only)"
}
