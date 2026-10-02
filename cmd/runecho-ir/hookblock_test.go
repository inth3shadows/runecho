package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// foreignFragment has the shape of kb-mcp's kb-drift block (#443): its own
// markers, a conditional, and function cleanup. It must survive every merge
// byte for byte.
const foreignFragment = `# >>> kb-drift >>>
if command -v kb >/dev/null 2>&1; then
  __kb_run() { kb drift check || true; }
  __kb_run
  unset -f __kb_run
fi
# <<< kb-drift <<<
`

// legacyBodies are the hook bodies runecho wrote before #443, verbatim from git
// history, with the binary quoted either way it ever was.
func legacyBodies(q func(string) string) map[string]string {
	ir, guard := q("/opt/x/runecho-ir"), q("/opt/x/runecho-guard")
	return map[string]string{
		"pre-commit (exec)":               "#!/usr/bin/env bash\nexec " + guard + " \"$@\"\n",
		"reindex":                         "#!/usr/bin/env bash\n" + ir + " repo reindex . >/dev/null 2>&1 &\n",
		"post-checkout (&& gate)":         "#!/usr/bin/env bash\n[ \"$3\" = \"1\" ] && " + ir + " repo reindex . >/dev/null 2>&1 &\n",
		"post-merge (--reinstall)":        "#!/usr/bin/env bash\n" + ir + " version-check --reinstall --quiet || true\n" + ir + " repo reindex . >/dev/null 2>&1 &\n",
		"post-checkout (--reinstall)":     "#!/usr/bin/env bash\n[ \"$3\" = \"1\" ] || exit 0\n" + ir + " version-check --reinstall --quiet || true\n" + ir + " repo reindex . >/dev/null 2>&1 &\n",
		"post-merge (advisory)":           "#!/usr/bin/env bash\n" + ir + " version-check --quiet || true\n" + ir + " repo reindex . >/dev/null 2>&1 &\n",
		"post-checkout (advisory, |exit)": "#!/usr/bin/env bash\n[ \"$3\" = \"1\" ] || exit 0\n" + ir + " version-check --quiet || true\n" + ir + " repo reindex . >/dev/null 2>&1 &\n",
	}
}

func goQuote(s string) string { return `"` + s + `"` } // what %q produced for these paths

func TestMergeHookBlock(t *testing.T) {
	blocks := hookBlocks("/usr/local/bin/runecho-ir", "/usr/local/bin/runecho-guard")
	block := blocks["post-merge"]
	other := hookBlocks("/new/path/runecho-ir", "/new/path/runecho-guard")["post-merge"]
	foreign := "#!/usr/bin/env bash\nset -e\necho hello\nexit 0\n"

	type tc struct {
		name, existing string
		force          bool
		want           string // exact expected output; "" = check action/err only
		action         hookAction
		wantErr        string
		keep           string // must appear verbatim in the output
	}
	cases := []tc{
		{name: "empty", existing: "", want: newHookShebang + "\n" + block, action: hookCreated},
		{name: "whitespace only", existing: "\n \n", want: newHookShebang + "\n" + block, action: hookCreated},
		{name: "shebang only", existing: "#!/bin/sh\n", want: "#!/bin/sh\n" + block, action: hookCreated},
		{name: "foreign bash: block after shebang", existing: foreign,
			want: "#!/usr/bin/env bash\n" + block + "set -e\necho hello\nexit 0\n", action: hookCreated},
		{name: "no shebang: block at top", existing: "echo hi\n", want: block + "echo hi\n", action: hookCreated},
		{name: "env -S bash", existing: "#!/usr/bin/env -S bash -e\necho hi\n",
			want: "#!/usr/bin/env -S bash -e\n" + block + "echo hi\n", action: hookCreated},
		{name: "shebang without newline", existing: "#!/bin/sh",
			want: "#!/bin/sh\n" + block, action: hookCreated},
		{name: "python refused", existing: "#!/usr/bin/env python3\nprint(1)\n", action: hookRefused, wantErr: `"python3" script`},
		{name: "clean pair unchanged", existing: "#!/bin/sh\necho a\n" + block + "echo b\n",
			want: "#!/bin/sh\necho a\n" + block + "echo b\n", action: hookUnchanged},
		{name: "mid-file pair updated in place", existing: "#!/bin/sh\necho a\n" + other + "echo b\n",
			want: "#!/bin/sh\necho a\n" + block + "echo b\n", action: hookUpdated},
		{name: "pair at EOF without trailing newline", existing: "#!/bin/sh\n" + strings.TrimSuffix(other, "\n"),
			want: "#!/bin/sh\n" + block, action: hookUpdated},
		{name: "only open", existing: "#!/bin/sh\n" + hookOpenMarker + "\necho x\n", action: hookRefused, wantErr: "cannot safely replace"},
		{name: "only close", existing: "#!/bin/sh\necho x\n" + hookCloseMarker + "\n", action: hookRefused, wantErr: "cannot safely replace"},
		{name: "close before open", existing: "#!/bin/sh\n" + hookCloseMarker + "\n" + hookOpenMarker + "\n", action: hookRefused, wantErr: "cannot safely replace"},
		{name: "two pairs", existing: "#!/bin/sh\n" + block + block, action: hookRefused, wantErr: "cannot safely replace"},
		{name: "CRLF marker", existing: "#!/bin/sh\r\n" + strings.ReplaceAll(block, "\n", "\r\n"), action: hookRefused, wantErr: "cannot safely replace"},
		{name: "indented marker", existing: "#!/bin/sh\n  " + block, action: hookRefused, wantErr: "cannot safely replace"},
		{name: "glued marker", existing: "#!/bin/sh\necho x" + block, action: hookRefused, wantErr: "cannot safely replace"},
		{name: "hand-wired guard refused", existing: "#!/bin/sh\nif true; then /usr/bin/runecho-guard; fi\n",
			action: hookRefused, wantErr: "already invokes runecho"},
		{name: "hand-wired guard with force", existing: "#!/bin/sh\nif true; then /usr/bin/runecho-guard; fi\n", force: true,
			want: "#!/bin/sh\n" + block + "if true; then /usr/bin/runecho-guard; fi\n", action: hookCreated},
		{name: "comment-only mention is foreign", existing: "#!/bin/sh\n# see runecho-ir docs\necho x\n",
			want: "#!/bin/sh\n" + block + "# see runecho-ir docs\necho x\n", action: hookCreated},
		{name: "legacy + kb-drift fragment keeps fragment",
			existing: legacyBodies(shellQuote)["post-merge (advisory)"] + foreignFragment,
			want:     newHookShebang + "\n" + block + foreignFragment, action: hookMigrated, keep: foreignFragment},
		{name: "legacy pre-commit + appended line",
			existing: legacyBodies(shellQuote)["pre-commit (exec)"] + "echo after\n",
			want:     newHookShebang + "\n" + block + "echo after\n", action: hookMigrated},
	}
	for name, body := range legacyBodies(shellQuote) {
		cases = append(cases, tc{name: "legacy shellQuote " + name, existing: body,
			want: newHookShebang + "\n" + block, action: hookMigrated})
	}
	for name, body := range legacyBodies(goQuote) {
		cases = append(cases, tc{name: "legacy %q " + name, existing: body,
			want: newHookShebang + "\n" + block, action: hookMigrated})
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, action, _, err := mergeHookBlock(c.existing, block, c.force)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if action != c.action {
				t.Errorf("action = %s, want %s", action, c.action)
			}
			if c.want != "" && got != c.want {
				t.Errorf("output mismatch\n got: %q\nwant: %q", got, c.want)
			}
			if c.keep != "" && !strings.Contains(got, c.keep) {
				t.Errorf("foreign content lost:\n%s", got)
			}
			// Idempotent: merging the result again changes nothing.
			again, a2, _, err := mergeHookBlock(got, block, c.force)
			if err != nil || a2 != hookUnchanged || again != got {
				t.Errorf("second merge: action=%s err=%v changed=%v", a2, err, again != got)
			}
		})
	}
}

// Migration must say so when it makes previously dead foreign content run.
func TestMergeHookBlock_NotesNewlyLiveContent(t *testing.T) {
	block := hookBlocks("/b/runecho-ir", "/b/runecho-guard")["post-checkout"]
	legacy := legacyBodies(shellQuote)["post-checkout (advisory, |exit)"]
	_, _, notes, err := mergeHookBlock(legacy+"echo foreign\n", block, false)
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "runs now") {
		t.Fatalf("notes = %v, err = %v; want one 'runs now' note", notes, err)
	}
	_, _, notes, _ = mergeHookBlock(legacy, block, false)
	if len(notes) != 0 {
		t.Errorf("no foreign content, yet notes = %v", notes)
	}
}

func TestInstallHookFile(t *testing.T) {
	block := hookBlocks("/b/runecho-ir", "/b/runecho-guard")["post-commit"]
	quiet := func(fn func()) { captureOutput(fn) }

	t.Run("mode, bak, idempotent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "post-commit")
		orig := "#!/bin/sh\necho mine\n"
		if err := os.WriteFile(path, []byte(orig), 0700); err != nil {
			t.Fatal(err)
		}
		var action hookAction
		var err error
		quiet(func() { action, err = installHookFile(dir, "post-commit", block, false) })
		if err != nil || action != hookCreated {
			t.Fatalf("action=%s err=%v", action, err)
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0700 {
			t.Errorf("mode = %v, want 0700", fi.Mode().Perm())
		}
		if bak, _ := os.ReadFile(path + hookBakSuffix); string(bak) != orig {
			t.Errorf(".bak = %q, want the previous bytes", bak)
		}
		os.Remove(path + hookBakSuffix)
		before, _ := os.ReadFile(path)
		fi1, _ := os.Stat(path)
		quiet(func() { action, err = installHookFile(dir, "post-commit", block, false) })
		after, _ := os.ReadFile(path)
		fi2, _ := os.Stat(path)
		if err != nil || action != hookUnchanged || string(after) != string(before) || !fi1.ModTime().Equal(fi2.ModTime()) {
			t.Errorf("second run: action=%s err=%v changed=%v", action, err, string(after) != string(before))
		}
		if _, err := os.Stat(path + hookBakSuffix); !os.IsNotExist(err) {
			t.Errorf("an unchanged run wrote a .bak")
		}
	})

	t.Run("non-executable kept and noted", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "post-commit")
		os.WriteFile(path, []byte("#!/bin/sh\n"), 0644)
		_, stderr := captureOutput(func() { installHookFile(dir, "post-commit", block, false) })
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0644 {
			t.Errorf("mode = %v, want 0644 kept", fi.Mode().Perm())
		}
		if !strings.Contains(stderr, "not executable") {
			t.Errorf("no note about the non-executable hook: %q", stderr)
		}
	})

	t.Run("refusal leaves file untouched", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "post-commit")
		orig := "#!/bin/sh\n" + hookOpenMarker + "\n"
		os.WriteFile(path, []byte(orig), 0755)
		var action hookAction
		_, stderr := captureOutput(func() { action, _ = installHookFile(dir, "post-commit", block, false) })
		if got, _ := os.ReadFile(path); string(got) != orig || action != hookRefused {
			t.Errorf("action=%s, file changed=%v", action, string(got) != orig)
		}
		if _, err := os.Stat(path + hookBakSuffix); !os.IsNotExist(err) {
			t.Errorf("a refusal wrote a .bak")
		}
		if !strings.Contains(stderr, "Refusing post-commit") {
			t.Errorf("refusal not reported: %q", stderr)
		}
	})

	t.Run("symlink target updated, link kept", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real-hook")
		os.WriteFile(target, []byte("#!/bin/sh\necho mine\n"), 0755)
		link := filepath.Join(dir, "post-commit")
		if err := os.Symlink(target, link); err != nil {
			t.Skip("symlinks unsupported")
		}
		quiet(func() { installHookFile(dir, "post-commit", block, false) })
		if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("symlink replaced by a regular file")
		}
		if got, _ := os.ReadFile(target); !strings.Contains(string(got), hookOpenMarker) || !strings.Contains(string(got), "echo mine") {
			t.Errorf("target not merged: %q", got)
		}
	})
}

// The installed blocks must coexist with foreign content in the same hook:
// content before and after ours runs, the guard's failure still fails the
// commit with its own status, and post-checkout's branch gate no longer exits
// the whole script on a file checkout. Run with real bash against stub binaries.
func TestHookBlocks_ExecuteAlongsideForeignContent(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	stub := func(name string) string {
		p := filepath.Join(dir, name)
		body := "#!/bin/sh\necho " + name + " >> \"$LOG\"\nexit ${STUB_RC:-0}\n"
		if err := os.WriteFile(p, []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	blocks := hookBlocks(stub("runecho-ir"), stub("runecho-guard"))

	run := func(script string, rc string, args ...string) (string, int) {
		t.Helper()
		os.Remove(log)
		p := filepath.Join(dir, "hook")
		os.WriteFile(p, []byte(script), 0755)
		cmd := exec.Command(bash, append([]string{p}, args...)...)
		cmd.Env = append(os.Environ(), "LOG="+log, "STUB_RC="+rc)
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return readLog(log), code
	}
	foreignAround := func(block string) string {
		return "#!/usr/bin/env bash\nset -e\necho A >> \"$LOG\"\n" + block + "echo B >> \"$LOG\"\n"
	}

	if got, code := run(foreignAround(blocks["pre-commit"]), "0"); got != "A runecho-guard B" || code != 0 {
		t.Errorf("pre-commit guard ok: log=%q code=%d, want \"A runecho-guard B\" 0", got, code)
	}
	if got, code := run(foreignAround(blocks["pre-commit"]), "3"); got != "A runecho-guard" || code != 3 {
		t.Errorf("pre-commit guard fails: log=%q code=%d, want \"A runecho-guard\" 3", got, code)
	}
	// Merged into a foreign hook that ends in exit 0: the guard still runs.
	merged, _, _, err := mergeHookBlock("#!/usr/bin/env bash\necho A >> \"$LOG\"\nexit 0\n", blocks["pre-commit"], false)
	if err != nil {
		t.Fatal(err)
	}
	if got, code := run(merged, "3"); got != "runecho-guard" || code != 3 {
		t.Errorf("guard before a foreign exit 0: log=%q code=%d", got, code)
	}

	// post-checkout: a file checkout ($3=0) skips runecho but runs foreign content.
	if got, code := run(foreignAround(blocks["post-checkout"]), "0", "x", "y", "0"); got != "A B" || code != 0 {
		t.Errorf("post-checkout file checkout: log=%q code=%d, want \"A B\" 0", got, code)
	}
	// A branch switch runs the advisory (sync) and reindex (background), then B.
	got, _ := run(foreignAround(blocks["post-checkout"]), "0", "x", "y", "1")
	deadline := time.Now().Add(3 * time.Second)
	for strings.Count(got, "runecho-ir") < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		got = readLog(log)
	}
	if !strings.HasPrefix(got, "A runecho-ir") || strings.Count(got, "runecho-ir") != 2 || !strings.Contains(got, "B") {
		t.Errorf("post-checkout branch switch: log=%q", got)
	}

	for name, b := range blocks {
		for _, sh := range []string{"bash", "sh"} {
			if p, err := exec.LookPath(sh); err == nil {
				f := filepath.Join(dir, name+"."+sh)
				os.WriteFile(f, []byte(newHookShebang+"\n"+b), 0644)
				if out, err := exec.Command(p, "-n", f).CombinedOutput(); err != nil {
					t.Errorf("%s -n %s: %v %s", sh, name, err, out)
				}
			}
		}
	}
}

func readLog(path string) string {
	b, _ := os.ReadFile(path)
	return strings.Join(strings.Fields(string(b)), " ")
}
