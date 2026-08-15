package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inth3shadows/runecho/internal/snapshot"
)

// gitInit creates a minimal git repo in dir, skipping the test if git is
// unavailable rather than failing it — matches internal/snapshot's own
// resolveGitInit pattern.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

// writeStubBin writes an executable shell stub at dir/name that prints ver
// on `--version` and exits 0 otherwise — a real, runnable binary, not a mock,
// so checkBinaries/checkGitHooks exercise their actual exec.LookPath and
// exec.Command paths.
func writeStubBin(t *testing.T, dir, name, ver string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/usr/bin/env bash\nif [ \"$1\" = \"--version\" ]; then echo " + ver + "; fi\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
	return path
}

// find returns the first Result with the given Check name, or nil.
func find(results []Result, check string) *Result {
	for i := range results {
		if results[i].Check == check {
			return &results[i]
		}
	}
	return nil
}

const validSettingsJSON = `{
  "hooks": {
    "PreToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [
      {"type": "command", "command": "guard.sh --hook-mode", "timeout": 5}
    ]}],
    "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [
      {"type": "command", "command": "guard.sh --outcome-mode", "timeout": 5}
    ]}]
  }
}`

const missingOutcomeSettingsJSON = `{
  "hooks": {
    "PreToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [
      {"type": "command", "command": "guard.sh --hook-mode", "timeout": 5}
    ]}]
  }
}`

func TestCheckWiring_NoChannelsPresent(t *testing.T) {
	root := t.TempDir()
	results := checkWiring(root)
	r := find(results, "claude code wiring")
	if r == nil || r.Status != Fail {
		t.Fatalf("checkWiring with no config files = %+v, want a Fail \"claude code wiring\" result", results)
	}
}

func TestCheckWiring_ValidChannelIsOK(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(validSettingsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	results := checkWiring(root)
	r := find(results, ".claude/settings.json")
	if r == nil || r.Status != OK {
		t.Fatalf("checkWiring with a valid settings.json = %+v, want OK", results)
	}
}

func TestCheckWiring_MissingEventIsFail(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "settings.json"), []byte(missingOutcomeSettingsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	results := checkWiring(root)
	r := find(results, ".claude/settings.json")
	if r == nil || r.Status != Fail {
		t.Fatalf("checkWiring with no PostToolUse hook = %+v, want Fail", results)
	}
}

// gitInitTagged creates a git repo in dir with one commit tagged tag, so
// gitutil.DescribeTag(dir) has something reachable to compare against.
func gitInitTagged(t *testing.T, dir, tag string) {
	t.Helper()
	gitInit(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "init"},
		{"tag", tag},
	} {
		full := append([]string{"-C", dir}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestCheckBinaries_NotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	results := checkBinaries(t.TempDir())
	for _, name := range []string{"runecho-ir", "runecho-guard"} {
		r := find(results, name)
		if r == nil || r.Status != Fail {
			t.Errorf("checkBinaries for missing %q = %+v, want Fail", name, r)
		}
	}
}

func TestCheckBinaries_AtOrAheadOfTagIsOK(t *testing.T) {
	root := t.TempDir()
	gitInitTagged(t, root, "v1.0.0")
	binDir := t.TempDir()
	writeStubBin(t, binDir, "runecho-ir", "v1.0.0")
	writeStubBin(t, binDir, "runecho-guard", "v1.0.0")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	results := checkBinaries(root)
	for _, name := range []string{"runecho-ir", "runecho-guard"} {
		r := find(results, name)
		if r == nil || r.Status != OK {
			t.Errorf("checkBinaries for %q at the tag = %+v, want OK", name, r)
		}
	}
}

func TestCheckBinaries_BehindTagIsFail(t *testing.T) {
	root := t.TempDir()
	gitInitTagged(t, root, "v2.0.0")
	binDir := t.TempDir()
	writeStubBin(t, binDir, "runecho-ir", "v1.0.0")
	writeStubBin(t, binDir, "runecho-guard", "v1.0.0")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	results := checkBinaries(root)
	for _, name := range []string{"runecho-ir", "runecho-guard"} {
		r := find(results, name)
		if r == nil || r.Status != Fail {
			t.Errorf("checkBinaries for %q behind v2.0.0 = %+v, want Fail", name, r)
		}
	}
}

func TestCheckGitHooks_NotAGitRepo(t *testing.T) {
	root := t.TempDir()
	results := checkGitHooks(root)
	r := find(results, "git hooks")
	if r == nil || r.Status != Fail {
		t.Fatalf("checkGitHooks outside a git tree = %+v, want a Fail \"git hooks\" result", results)
	}
}

func TestCheckGitHooks_NotInstalledIsWarn(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	results := checkGitHooks(root)
	for _, name := range hookFiles {
		r := find(results, "git hook "+name)
		if r == nil || r.Status != Warn {
			t.Errorf("checkGitHooks for uninstalled %q = %+v, want Warn", name, r)
		}
	}
}

func TestCheckGitHooks_StaleBinaryIsFail(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)

	binDir := t.TempDir()
	guardBin := writeStubBin(t, binDir, "runecho-guard", "v0.1.0")
	writeStubBin(t, binDir, "runecho-ir", "v0.1.0")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	hooksDir := filepath.Join(root, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := "#!/usr/bin/env bash\nexec /some/other/stale/runecho-guard \"$@\"\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(stale), 0o755); err != nil {
		t.Fatal(err)
	}

	results := checkGitHooks(root)
	r := find(results, "git hook pre-commit")
	if r == nil || r.Status != Fail {
		t.Fatalf("checkGitHooks for a pre-commit pointing at a different binary = %+v, want Fail", r)
	}

	// Positive control: a hook body that DOES invoke the resolved binary is OK.
	fresh := "#!/usr/bin/env bash\nexec " + guardBin + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(fresh), 0o755); err != nil {
		t.Fatal(err)
	}
	results = checkGitHooks(root)
	r = find(results, "git hook pre-commit")
	if r == nil || r.Status != OK {
		t.Fatalf("checkGitHooks for a pre-commit pointing at the resolved binary = %+v, want OK", r)
	}
}

func TestCheckEnrollment_NoStoreIsWarn(t *testing.T) {
	t.Setenv("RUNECHO_HOME", t.TempDir())
	root := t.TempDir()
	gitInit(t, root)

	results := checkEnrollment(root)
	r := find(results, "enrollment")
	if r == nil || r.Status != Warn {
		t.Fatalf("checkEnrollment with no store yet = %+v, want Warn", results)
	}
}

func TestCheckEnrollment_FreshIndexIsOK(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	root := t.TempDir()
	gitInit(t, root)

	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	id, err := db.EnrollRepo("r", root, root, 0)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := db.TouchRepo(id, time.Now(), 0, 1); err != nil {
		t.Fatalf("touch: %v", err)
	}
	db.Close()

	results := checkEnrollment(root)
	r := find(results, "enrollment")
	if r == nil || r.Status != OK {
		t.Fatalf("checkEnrollment for a freshly-indexed repo = %+v, want OK", results)
	}
}

func TestCheckEnrollment_StaleIndexIsWarn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	t.Setenv("RUNECHO_GUARD_MAX_AGE", "1h")
	root := t.TempDir()
	gitInit(t, root)

	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	id, err := db.EnrollRepo("r", root, root, 0)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if err := db.TouchRepo(id, time.Now().Add(-2*time.Hour), 0, 1); err != nil {
		t.Fatalf("touch: %v", err)
	}
	db.Close()

	results := checkEnrollment(root)
	r := find(results, "enrollment")
	if r == nil || r.Status != Warn {
		t.Fatalf("checkEnrollment for a repo indexed 2h ago against a 1h max age = %+v, want Warn", results)
	}
}

// TestCheckStore_FreshHomeIsWarnNotFail pins a fix found by live smoke
// testing: a genuinely fresh RUNECHO_HOME (nothing has ever been indexed
// anywhere) is a normal fresh-install state, not a store health problem — it
// must read the same severity (Warn) as checkEnrollment already gives the
// identical condition, not Fail.
func TestCheckStore_FreshHomeIsWarnNotFail(t *testing.T) {
	t.Setenv("RUNECHO_HOME", t.TempDir())
	results := checkStore(t.TempDir())
	r := find(results, "store health")
	if r == nil || r.Status != Warn {
		t.Fatalf("checkStore store health on a fresh RUNECHO_HOME = %+v, want Warn", r)
	}
}

func TestCheckStore_NoDecisionsYetIsWarn(t *testing.T) {
	t.Setenv("RUNECHO_HOME", t.TempDir())
	results := checkStore(t.TempDir())
	r := find(results, "decision log")
	if r == nil || r.Status != Warn {
		t.Fatalf("checkStore with no decisions.jsonl = %+v, want Warn \"decision log\"", results)
	}
}

func TestCheckStore_EnrolledZeroActivityIsWarn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)
	root := t.TempDir()
	gitInit(t, root)

	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := db.EnrollRepo("r", root, root, 0); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	db.Close()

	// An empty decisions.jsonl exists but records nothing in the window —
	// this is the exact "--outcome-mode never wired" signature.
	if err := os.WriteFile(filepath.Join(home, "decisions.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	results := checkStore(root)
	r := find(results, "decision log")
	if r == nil || r.Status != Warn {
		t.Fatalf("checkStore for an enrolled repo with zero asks/outcomes = %+v, want Warn", results)
	}
}

func TestCheckGates_ReportsSetFlags(t *testing.T) {
	t.Setenv("RUNECHO_GUARD_LINT", "1")
	for _, f := range knownGateFlags {
		if f != "LINT" {
			t.Setenv("RUNECHO_GUARD_"+f, "")
		}
	}
	results := checkGates()
	if len(results) != 1 || results[0].Status != OK {
		t.Fatalf("checkGates = %+v, want a single OK result", results)
	}
	if got := results[0].Detail; got != "LINT=1" {
		t.Errorf("checkGates detail = %q, want %q", got, "LINT=1")
	}
}

func TestCheckGates_NoneSetReportsDefaultPosture(t *testing.T) {
	for _, f := range knownGateFlags {
		t.Setenv("RUNECHO_GUARD_"+f, "")
	}
	results := checkGates()
	if len(results) != 1 || results[0].Status != OK {
		t.Fatalf("checkGates = %+v, want a single OK result", results)
	}
	if results[0].Detail == "" {
		t.Error("checkGates with nothing set returned an empty detail instead of naming the default posture")
	}
}

// The periodic-job check exists because upgrading the binary does not rewrite
// the schedule: the cron line and LaunchAgent plist are written once by
// `install --periodic`, and neither install.sh nor version-check --reinstall
// touches them. A machine that installed the job before retention shipped keeps
// running the old reindex-only command and keeps growing (#351). Asserting the
// GENERATED text cannot catch that — the generator is right and the installed
// artifact is stale — so these assert the classification of what was actually
// found on disk.

func TestCheckPeriodic_StaleJobWithoutPruneIsWarn(t *testing.T) {
	stale := `0 * * * * '/home/u/bin/runecho-ir' repo reindex --all >>/tmp/r.log 2>&1 # runecho`
	r := find(classifyPeriodic(stale, "crontab", true), "periodic reindex")
	if r == nil {
		t.Fatal("no periodic reindex result")
	}
	if r.Status != Warn {
		t.Errorf("status = %q, want %q — a pre-retention job must be flagged, or the #351 fix is invisible on exactly the installs it was written for", r.Status, Warn)
	}
	if r.Remedy == "" {
		t.Error("a warn with no remedy leaves the user with a problem and no next step")
	}
	if !strings.Contains(r.Remedy, "install --periodic") {
		t.Errorf("remedy %q should name the command that rewrites the schedule", r.Remedy)
	}
}

func TestCheckPeriodic_CurrentJobIsOK(t *testing.T) {
	current := `0 * * * * '/home/u/bin/runecho-ir' repo reindex --all --prune >>/tmp/r.log 2>&1 # runecho`
	r := find(classifyPeriodic(current, "crontab", true), "periodic reindex")
	if r == nil {
		t.Fatal("no periodic reindex result")
	}
	if r.Status != OK {
		t.Errorf("status = %q, want %q for a job that already prunes", r.Status, OK)
	}
}

// A LaunchAgent passes --prune as its own argv element rather than inline, so
// the check must match the plist shape too — not just the crontab one.
func TestCheckPeriodic_LaunchAgentPlistIsRecognised(t *testing.T) {
	plist := launchdPlistFixture()
	r := find(classifyPeriodic(plist, "/Users/u/Library/LaunchAgents/com.runecho.reindex.plist", true), "periodic reindex")
	if r == nil {
		t.Fatal("no periodic reindex result")
	}
	if r.Status != OK {
		t.Errorf("status = %q, want %q — the plist passes --prune as an argv element and must be recognised", r.Status, OK)
	}
}

// Not installed is a legitimate, common state (the job is opt-in and the git
// hooks keep the IR fresh), so it must not read as a problem.
func TestCheckPeriodic_NotInstalledIsOKNotWarn(t *testing.T) {
	r := find(classifyPeriodic("", "", false), "periodic reindex")
	if r == nil {
		t.Fatal("no periodic reindex result")
	}
	if r.Status != OK {
		t.Errorf("status = %q, want %q — the periodic job is optional; flagging its absence is noise", r.Status, OK)
	}
	if !strings.Contains(r.Detail, "not installed") {
		t.Errorf("detail %q should say plainly that no job is installed", r.Detail)
	}
}

func launchdPlistFixture() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/runecho-ir</string>
		<string>repo</string>
		<string>reindex</string>
		<string>--all</string>
		<string>--prune</string>
	</array>
</dict>
</plist>`
}
