package ir

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIR_MarshalJSON_Determinism(t *testing.T) {
	// Create IR with files in random insertion order
	ir := &IR{
		Version: 1,
		Files: map[string]FileIR{
			"zebra.ts": {
				Hash: "abc123",
				Symbols: []Symbol{
					{Name: "Zulu", Kind: "class"},
					{Name: "foo", Kind: "export"},
					{Name: "bar", Kind: "function"},
					{Name: "foo", Kind: "function"},
					{Name: "react", Kind: "import"},
				},
			},
			"alpha.ts": {
				Hash: "def456",
				Symbols: []Symbol{
					{Name: "baz", Kind: "export"},
					{Name: "baz", Kind: "function"},
					{Name: "lodash", Kind: "import"},
				},
			},
			"beta.js": {
				Hash: "ghi789",
				Symbols: []Symbol{
					{Name: "Test", Kind: "class"},
					{Name: "test", Kind: "function"},
				},
			},
		},
	}

	// Marshal 100 times
	results := make([][]byte, 100)
	for i := 0; i < 100; i++ {
		data, err := json.Marshal(ir)
		if err != nil {
			t.Fatalf("Marshal failed on iteration %d: %v", i, err)
		}
		results[i] = data
	}

	// Verify all results are byte-identical
	first := string(results[0])
	for i := 1; i < 100; i++ {
		current := string(results[i])
		if first != current {
			t.Errorf("Marshal result %d differs from first result", i)
			t.Logf("First:\n%s", first)
			t.Logf("Current:\n%s", current)
		}
	}
}

func TestIR_MarshalJSON_FilesAreSorted(t *testing.T) {
	ir := &IR{
		Version: 1,
		Files: map[string]FileIR{
			"z.ts": {Hash: "hash1"},
			"a.ts": {Hash: "hash2"},
			"m.ts": {Hash: "hash3"},
		},
	}

	data, err := json.Marshal(ir)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Parse JSON to check order
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	filesMap := parsed["files"].(map[string]interface{})

	// In the JSON string, "a.ts" should appear before "m.ts" and "z.ts"
	jsonStr := string(data)
	posA := indexOf(jsonStr, `"a.ts"`)
	posM := indexOf(jsonStr, `"m.ts"`)
	posZ := indexOf(jsonStr, `"z.ts"`)

	if posA == -1 || posM == -1 || posZ == -1 {
		t.Fatalf("Not all file keys found in JSON")
	}

	if !(posA < posM && posM < posZ) {
		t.Errorf("Files not in sorted order in JSON: a.ts at %d, m.ts at %d, z.ts at %d", posA, posM, posZ)
	}

	// Verify all files are present
	if len(filesMap) != 3 {
		t.Errorf("Expected 3 files, got %d", len(filesMap))
	}
}

// TestLoadCapped_RejectsOversized pins F1: Load caps the file size so a crafted
// or corrupt .ai/ir.json (which the PostToolUse guard auto-reads on every edit)
// can't OOM the process. Tested via loadCapped with a small explicit limit so it
// doesn't need a 100 MiB fixture.
func TestLoadCapped_RejectsOversized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ir.json")
	// 4 KiB of content, cap at 1 KiB → must be rejected without unmarshalling.
	if err := os.WriteFile(path, []byte("["+string(make([]byte, 4096))+"]"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCapped(path, 1024); err == nil {
		t.Fatal("expected loadCapped to reject a file over the size cap")
	}
	// A small valid IR under the cap still loads.
	small := &IR{Version: IRVersion, Files: map[string]FileIR{}}
	if err := small.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCapped(path, 1<<20); err != nil {
		t.Fatalf("loadCapped should accept a file under the cap: %v", err)
	}
}

// TestIR_Save_OwnerOnlyPerms pins B4: the IR file is 0600 and its dir 0700 (it
// holds symbol/import names), not world-readable.
func TestIR_Save_OwnerOnlyPerms(t *testing.T) {
	dir := t.TempDir()
	irPath := filepath.Join(dir, ".ai", "ir.json")
	if err := (&IR{Version: IRVersion, Files: map[string]FileIR{}}).Save(irPath); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(irPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("ir.json perm = %o, want 0600", perm)
	}
	di, err := os.Stat(filepath.Dir(irPath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0700 {
		t.Errorf(".ai dir perm = %o, want 0700", perm)
	}
}

// TestIR_Save_SelfIgnoresAIDir pins #550: saving into a .ai/ dir writes a
// .ai/.gitignore that hides runecho's own files from git regardless of the host
// repo's ignore config, without hiding anything else a repo keeps in .ai/
// (PR #551 F1); it is never overwritten, never written through a symlinked .ai
// (F3), is world-readable (F4), and a Save to a dir not named .ai writes none.
func TestIR_Save_SelfIgnoresAIDir(t *testing.T) {
	empty := &IR{Version: IRVersion, Files: map[string]FileIR{}}

	t.Run("writes runecho-only gitignore", func(t *testing.T) {
		dir := t.TempDir()
		if err := empty.Save(filepath.Join(dir, ".ai", "ir.json")); err != nil {
			t.Fatalf("Save: %v", err)
		}
		gi := filepath.Join(dir, ".ai", ".gitignore")
		got, err := os.ReadFile(gi)
		if err != nil {
			t.Fatalf(".ai/.gitignore not written: %v", err)
		}
		if string(got) != selfIgnoreContent {
			t.Errorf(".ai/.gitignore = %q, want %q", got, selfIgnoreContent)
		}
		for _, line := range strings.Split(string(got), "\n") {
			if strings.TrimSpace(line) == "*" {
				t.Errorf(".ai/.gitignore ignores everything (%q); must list runecho's files only", line)
			}
		}
		fi, err := os.Stat(gi)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0644 {
			t.Errorf(".ai/.gitignore perm = %o, want 0644 (other uids running git must read it)", perm)
		}
	})

	t.Run("keeps existing gitignore", func(t *testing.T) {
		dir := t.TempDir()
		ai := filepath.Join(dir, ".ai")
		if err := os.MkdirAll(ai, 0700); err != nil {
			t.Fatal(err)
		}
		const custom = "ir.json\n"
		if err := os.WriteFile(filepath.Join(ai, ".gitignore"), []byte(custom), 0644); err != nil {
			t.Fatal(err)
		}
		if err := empty.Save(filepath.Join(ai, "ir.json")); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(ai, ".gitignore"))
		if string(got) != custom {
			t.Errorf("existing .gitignore overwritten: %q, want %q", got, custom)
		}
	})

	t.Run("no gitignore outside .ai", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "out")
		if err := empty.Save(filepath.Join(out, "ir.json")); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := os.Stat(filepath.Join(out, ".gitignore")); !os.IsNotExist(err) {
			t.Errorf("Save outside .ai wrote a .gitignore (stat err=%v)", err)
		}
	})

	t.Run("no gitignore through symlinked .ai", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "elsewhere")
		if err := os.MkdirAll(target, 0700); err != nil {
			t.Fatal(err)
		}
		repo := filepath.Join(dir, "repo")
		if err := os.MkdirAll(repo, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(repo, ".ai")); err != nil {
			t.Skipf("symlink: %v", err)
		}
		if err := empty.Save(filepath.Join(repo, ".ai", "ir.json")); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := os.Stat(filepath.Join(target, ".gitignore")); !os.IsNotExist(err) {
			t.Errorf("Save wrote a .gitignore into a symlinked .ai's target (stat err=%v)", err)
		}
	})

	t.Run("gitignore appears only complete", func(t *testing.T) {
		// F2: a kill between creating .gitignore and writing it must not leave
		// an empty file that EEXIST then protects forever. The file may only
		// come into existence by linking an already complete temp file.
		dir := t.TempDir()
		ai := filepath.Join(dir, ".ai")
		gi := filepath.Join(ai, ".gitignore")
		called := false
		orig := linkSelfIgnore
		t.Cleanup(func() { linkSelfIgnore = orig })
		linkSelfIgnore = func(oldname, newname string) error {
			called = true
			if newname != gi {
				t.Errorf("link target = %q, want %q", newname, gi)
			}
			if _, err := os.Lstat(gi); !os.IsNotExist(err) {
				t.Errorf(".gitignore existed before the link (stat err=%v)", err)
			}
			if got, err := os.ReadFile(oldname); err != nil || string(got) != selfIgnoreContent {
				t.Errorf("temp at link time = %q (err %v), want complete content", got, err)
			}
			return orig(oldname, newname)
		}
		if err := empty.Save(filepath.Join(ai, "ir.json")); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if !called {
			t.Fatal(".gitignore was not created by linking a complete temp file")
		}
		if left, _ := filepath.Glob(filepath.Join(ai, ".gitignore.tmp-*")); len(left) != 0 {
			t.Errorf("temp files left behind: %v", left)
		}
	})

	t.Run("git hides runecho files only", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not on PATH")
		}
		dir := t.TempDir()
		// F6: drop GIT_DIR/GIT_WORK_TREE/GIT_INDEX_FILE etc. inherited from a
		// hook or `git rebase -x`, which would aim these commands at the outer repo.
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "GIT_") {
				env = append(env, kv)
			}
		}
		env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		git := func(args ...string) string {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-c", "core.excludesFile=" + os.DevNull}, args...)...)
			cmd.Dir = dir
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
			return string(out)
		}
		git("init", "-q")
		ai := filepath.Join(dir, ".ai")
		if err := empty.Save(filepath.Join(ai, "ir.json")); err != nil {
			t.Fatalf("Save: %v", err)
		}
		for _, name := range []string{"ir.json.tmp-123", ".gitignore.tmp-456", "notes.md"} {
			if err := os.WriteFile(filepath.Join(ai, name), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		out := git("ls-files", "--others", "--exclude-standard")
		if out != ".ai/notes.md\n" {
			t.Errorf("untracked = %q, want only %q (runecho files hidden, the repo's own .ai files visible)", out, ".ai/notes.md\n")
		}
	})
}

func TestIR_SaveAndLoad_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	irPath := filepath.Join(tmpDir, "ir.json")

	original := &IR{
		Version: IRVersion,
		Files: map[string]FileIR{
			"test.ts": {
				Hash: "abcdef123456",
				// Canonical order: sortSymbols orders by kind (alphabetical:
				// class, export, function, import) then name.
				Symbols: []Symbol{
					{Name: "Test", Kind: "class", Line: 1},
					{Name: "bar", Kind: "export"},
					{Name: "foo", Kind: "export"},
					{Name: "bar", Kind: "function", Line: 9, Hash: "h2"},
					{Name: "foo", Kind: "function", Line: 3, Hash: "h1"},
					{Name: "lodash", Kind: "import"},
					{Name: "react", Kind: "import"},
				},
				Refs: []string{"baz", "qux"},
			},
		},
	}

	// Save
	if err := original.Save(irPath); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load
	loaded, err := Load(irPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Verify
	if loaded.Version != original.Version {
		t.Errorf("Version mismatch: got %d, want %d", loaded.Version, original.Version)
	}

	if len(loaded.Files) != len(original.Files) {
		t.Errorf("Files count mismatch: got %d, want %d", len(loaded.Files), len(original.Files))
	}

	for path, origFile := range original.Files {
		loadedFile, exists := loaded.Files[path]
		if !exists {
			t.Errorf("File %s missing after load", path)
			continue
		}

		if !equalFileIR(origFile, loadedFile) {
			t.Errorf("File %s differs after load", path)
			t.Logf("Original: %+v", origFile)
			t.Logf("Loaded: %+v", loadedFile)
		}
	}
}

func TestIR_Save_Determinism(t *testing.T) {
	tmpDir := t.TempDir()

	ir := &IR{
		Version: 1,
		Files: map[string]FileIR{
			"test.ts": {Hash: "abc123"},
		},
	}

	// Save 10 times to different files
	paths := make([]string, 10)
	for i := 0; i < 10; i++ {
		path := filepath.Join(tmpDir, "ir_"+string(rune(i+'0'))+".json")
		paths[i] = path
		if err := ir.Save(path); err != nil {
			t.Fatalf("Save %d failed: %v", i, err)
		}
	}

	// Read all files and verify they're byte-identical
	first, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatalf("Failed to read first file: %v", err)
	}

	for i := 1; i < 10; i++ {
		current, err := os.ReadFile(paths[i])
		if err != nil {
			t.Fatalf("Failed to read file %d: %v", i, err)
		}

		if string(first) != string(current) {
			t.Errorf("File %d differs from first file", i)
			t.Logf("First:\n%s", string(first))
			t.Logf("Current:\n%s", string(current))
		}
	}
}

func TestIR_Save_DefaultPath(t *testing.T) {
	// Create temporary directory and change to it
	tmpDir := t.TempDir()
	oldDir, _ := os.Getwd()
	defer os.Chdir(oldDir)
	os.Chdir(tmpDir)

	// Create .ai directory
	os.Mkdir(".ai", 0755)

	ir := &IR{
		Version: 1,
		Files: map[string]FileIR{
			"test.ts": {Hash: "abc123"},
		},
	}

	// Save with empty string - should use DefaultIRPath
	if err := ir.Save(""); err != nil {
		t.Fatalf("Save with empty path failed: %v", err)
	}

	// Verify file exists at DefaultIRPath
	if _, err := os.Stat(DefaultIRPath); os.IsNotExist(err) {
		t.Errorf("File not created at DefaultIRPath: %s", DefaultIRPath)
	}

	// Verify we can load it back
	loaded, err := Load(DefaultIRPath)
	if err != nil {
		t.Fatalf("Load from DefaultIRPath failed: %v", err)
	}

	if loaded.Version != ir.Version {
		t.Errorf("Version mismatch after load")
	}
}

// Helper functions

func equalFileIR(a, b FileIR) bool {
	return a.Hash == b.Hash &&
		equalSymbols(a.Symbols, b.Symbols) &&
		equalStringSlices(a.Refs, b.Refs)
}

func equalSymbols(a, b []Symbol) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestFileIR_LegacyJSONUnmarshal is the IR v5 compat-shim guarantee: a pre-v5
// ir.json (parallel functions/classes/exports/imports arrays + symbol_hashes/
// symbol_lines maps, with NO `symbols` array) still loads, and its data is
// reconstructed into the canonical Symbols slice with the right kinds, lines,
// and hashes.
func TestFileIR_LegacyJSONUnmarshal(t *testing.T) {
	legacy := `{
		"hash": "h1",
		"imports": ["fmt"],
		"functions": ["DoThing"],
		"classes": ["Widget"],
		"exports": ["MAX"],
		"refs": ["println"],
		"symbol_hashes": {"function:DoThing": "abc123"},
		"symbol_lines": {"function:DoThing": 5, "class:Widget": 9, "export:MAX": 3}
	}`
	var f FileIR
	if err := json.Unmarshal([]byte(legacy), &f); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if f.Hash != "h1" || !equalStringSlices(f.Refs, []string{"println"}) {
		t.Errorf("hash/refs lost: hash=%q refs=%v", f.Hash, f.Refs)
	}
	// Sorted by (kind, name): class, export, function, import.
	want := []Symbol{
		{Name: "Widget", Kind: "class", Line: 9},
		{Name: "MAX", Kind: "export", Line: 3},
		{Name: "DoThing", Kind: "function", Line: 5, Hash: "abc123"},
		{Name: "fmt", Kind: "import"},
	}
	if !equalSymbols(f.Symbols, want) {
		t.Errorf("reconstructed Symbols = %+v, want %+v", f.Symbols, want)
	}
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// TestIR_Save_UniqueTempName pins the F61 fix: Save must not use a fixed
// "<path>.tmp" scratch name. With a fixed name, two concurrent Saves interleave
// writes into the same temp file and the loser renames a torn mix into place
// (the E6 auto-refresh hook hits exactly this). A unique per-call temp name
// makes each rename atomic with its own complete content. Pinned observably:
// a pre-existing sibling file named exactly "<path>.tmp" must survive Save
// untouched.
func TestIR_Save_UniqueTempName(t *testing.T) {
	dir := t.TempDir()
	irPath := filepath.Join(dir, "ir.json")
	sentinel := irPath + ".tmp"
	if err := os.WriteFile(sentinel, []byte("other writer's data"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := (&IR{Version: IRVersion, Files: map[string]FileIR{}}).Save(irPath); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("sentinel %s gone after Save: %v", sentinel, err)
	}
	if string(got) != "other writer's data" {
		t.Errorf("Save clobbered the fixed-name temp file: %q", got)
	}
	if _, err := Load(irPath); err != nil {
		t.Errorf("saved IR does not load back: %v", err)
	}
}
