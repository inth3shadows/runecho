package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The guard attaches to Claude Code through TWO hook events, and every channel
// that ships a config must wire BOTH. Nothing enforced that, and the omission
// shipped: `--outcome-mode` existed as a working, tested flag that NO shipped
// config ever invoked. Only the author's hand-edited personal settings.json
// called it, so on every external install:
//
//   - `runecho-ir fpreport` had no join key. Its approval rate is computed by
//     matching an ask to an outcome record, and outcome records are written
//     ONLY by --outcome-mode. Every ask was unrated; the report was empty.
//   - RUNECHO_GUARD_LEARN could never fire — it trains on approvals it never saw.
//   - The E6 auto-fresh reindex never ran, leaving the stale-IR false-positive
//     class live for every user.
//
// None of that surfaces as an error. The guard still asks, still defers, still
// exits 0; it just silently stops learning and stops measuring. That is the
// failure mode this test exists to make loud.
//
// This mirrors internal/parser/grammar_subset_test.go, which covers the same
// bug class for build tags: a prose comment claiming N files "must agree" is
// not synchronization — the only thing that keeps them in step is a test that
// parses all of them.
//
// Deliberately NOT derived from main.go's flag set: "which bool flags are hook
// entry points" is not recoverable from source without guessing. The event→mode
// table below IS the contract. What IS derived is the check that each mode is a
// real flag the binary defines, which is what catches a typo.
var hookContract = map[string]string{
	"PreToolUse":  "--hook-mode",
	"PostToolUse": "--outcome-mode",
}

const hookMatcher = "Edit|Write|MultiEdit"

// repoRoot walks up from this package (cmd/runecho-guard) to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %s has no go.mod — the path assumption in this test is stale: %v", root, err)
	}
	return root
}

// trackedPath returns a path AND reads it in-process, which is the point.
//
// Go's test cache keys a cached result partly on the files the test process
// opened. The two tests below run their subject (install.sh, guard.sh) through
// a SUBPROCESS, and the cache cannot see a subprocess's reads — so editing
// install.sh and re-running `go test` can serve a stale PASS. Reading the file
// here puts it in the cache key, so a change to it invalidates the result.
//
// CI never hit this (fresh checkout, cold cache); a local `go test ./...` after
// editing the shipped config would have.
func trackedPath(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if _, err := os.ReadFile(p); err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return p
}

// claudeHookFile is the shape of both hooks.json and settings.json.
type claudeHookFile struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// TestShippedConfigsWireEveryHookEvent checks the two JSON channels: the plugin
// (what `/plugin install` wires) and this repo's own settings.json (what the
// project dogfoods itself with). A guard that is not dogfooded with the same
// wiring it ships is how the PostToolUse gap survived unnoticed.
func TestShippedConfigsWireEveryHookEvent(t *testing.T) {
	root := repoRoot(t)

	channels := map[string]string{
		"plugins/runecho-guard/hooks/hooks.json": filepath.Join(root, "plugins", "runecho-guard", "hooks", "hooks.json"),
		".claude/settings.json":                  filepath.Join(root, ".claude", "settings.json"),
	}

	for name, path := range channels {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: unreadable, so this channel ships unverified: %v", name, err)
			continue
		}
		var cfg claudeHookFile
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Errorf("%s: invalid JSON — Claude Code would ignore the whole file: %v", name, err)
			continue
		}

		for event, mode := range hookContract {
			entries, ok := cfg.Hooks[event]
			if !ok || len(entries) == 0 {
				t.Errorf("%s: no %s hook. The guard degrades SILENTLY without it "+
					"(see this file's header) — it does not error, it just stops "+
					"measuring and stops learning.", name, event)
				continue
			}

			var found bool
			for _, entry := range entries {
				if entry.Matcher != hookMatcher {
					t.Errorf("%s: %s matcher is %q, want %q — a narrower matcher means "+
						"edits that the other event DOES see go unpaired",
						name, event, entry.Matcher, hookMatcher)
				}
				for _, h := range entry.Hooks {
					if strings.Contains(h.Command, mode) {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("%s: %s hook exists but never invokes %s", name, event, mode)
			}
		}
	}
}

// TestPrintHookConfigWiresEveryHookEvent covers the third channel: the manual
// fallback that `install.sh --print-hook-config` prints for hand-merging.
//
// It RUNS install.sh and parses the emitted block as JSON rather than grepping
// the heredoc. Two things only the executed form can catch: shell expansion
// inside the heredoc going wrong ($BIN_DIR/$EXE are interpolated at print time),
// and the block being malformed JSON. The second is the more dangerous — Claude
// Code ignores a settings.json it cannot parse, so a broken block here does not
// half-wire the user's hooks, it silently disables ALL of them.
//
// --print-hook-config exits before install.sh's Go-toolchain check, so this
// stays cheap and builds nothing.
func TestPrintHookConfigWiresEveryHookEvent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the POSIX install path")
	}
	root := repoRoot(t)

	cmd := exec.Command("bash", trackedPath(t, root, "install.sh"), "--print-hook-config")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("install.sh --print-hook-config: %v", err)
	}

	block, err := extractJSONBlock(string(out))
	if err != nil {
		t.Fatalf("no JSON block in --print-hook-config output: %v", err)
	}
	var cfg claudeHookFile
	if err := json.Unmarshal([]byte(block), &cfg); err != nil {
		t.Fatalf("--print-hook-config emits INVALID JSON: %v\nA user merging this "+
			"gets a settings.json Claude Code cannot parse, which disables every "+
			"hook they have, not just ours.\n%s", err, block)
	}

	for event, mode := range hookContract {
		entries, ok := cfg.Hooks[event]
		if !ok || len(entries) == 0 {
			t.Errorf("--print-hook-config omits %s — a user who follows the printed "+
				"instructions gets a half-wired install", event)
			continue
		}
		var found bool
		for _, entry := range entries {
			if entry.Matcher != hookMatcher {
				t.Errorf("--print-hook-config %s matcher is %q, want %q",
					event, entry.Matcher, hookMatcher)
			}
			for _, h := range entry.Hooks {
				if !strings.HasSuffix(h.Command, " "+mode) {
					continue
				}
				found = true
				// The path is shell-interpolated; an unexpanded variable would
				// print literally and produce a hook that can never run.
				if strings.ContainsAny(h.Command, "$") {
					t.Errorf("--print-hook-config %s command has an unexpanded variable: %q",
						event, h.Command)
				}
			}
		}
		if !found {
			t.Errorf("--print-hook-config %s never invokes %s", event, mode)
		}
	}
}

// extractJSONBlock pulls the indented JSON object out of the printed prose by
// taking the first line that is exactly "{" through the last that is exactly
// "}", then stripping the two-space display indent.
func extractJSONBlock(out string) (string, error) {
	lines := strings.Split(out, "\n")
	start, end := -1, -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "{" && start == -1 {
			start = i
		}
		if strings.TrimSpace(l) == "}" {
			end = i
		}
	}
	if start == -1 || end <= start {
		return "", errors.New("no {...} block found")
	}
	block := make([]string, 0, end-start+1)
	for _, l := range lines[start : end+1] {
		block = append(block, strings.TrimPrefix(l, "  "))
	}
	return strings.Join(block, "\n"), nil
}

// TestGuardShimSurvivesStaleBinary covers version skew between the two update
// channels. The plugin is git-sourced and moves on `/plugin update`; the binary
// comes from install.sh and is known to lag (a dogfood session once found it six
// releases behind). --outcome-mode only exists from v0.10.0, so a fresh plugin
// driving an old binary would hit flag.ContinueOnError — usage to stderr, exit
// 2 — and because the shim execs, that 2 became the hook's exit code on EVERY
// Edit/Write/MultiEdit. The mode validation cannot catch this: it checks the
// argument the shim was handed, not whether the binary understands it.
//
// --outcome-mode is observational, so it must swallow that. --hook-mode is the
// decision path and must NOT: a guard that cannot run needs to stay visible.
func TestGuardShimSurvivesStaleBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is a bash shim; the Windows install path does not use it")
	}
	root := repoRoot(t)
	shim := trackedPath(t, root, "plugins", "runecho-guard", "hooks", "guard.sh")

	// A binary that rejects every flag, the way a pre-v0.10.0 build rejects
	// --outcome-mode.
	binDir := t.TempDir()
	stale := "#!/usr/bin/env bash\necho \"flag provided but not defined: $1\" >&2\nexit 2\n"
	if err := os.WriteFile(filepath.Join(binDir, "runecho-guard"), []byte(stale), 0o755); err != nil {
		t.Fatalf("write stale binary: %v", err)
	}

	// bash's own directory has to stay on PATH. With PATH set to binDir alone
	// the stub's `#!/usr/bin/env bash` shebang cannot resolve an interpreter, so
	// it exits 127 without ever reaching its flag-rejection logic — the test
	// then still fails on a reverted fix, but for "binary is unrunnable" rather
	// than "binary rejected the flag", which is not the scenario it claims to
	// cover. Caught by reading a mutation's failure text instead of just its
	// pass/fail.
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not found: %v", err)
	}
	pathEnv := binDir + string(os.PathListSeparator) + filepath.Dir(bashPath)

	run := func(mode string) (string, int) {
		cmd := exec.Command("bash", shim, mode)
		cmd.Env = []string{"PATH=" + pathEnv, "HOME=" + binDir, "RUNECHO_BIN_DIR="}
		cmd.Stdin = strings.NewReader("{}")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		cmd.Stdout = io.Discard
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run guard.sh %s: %v", mode, err)
		}
		return stderr.String(), code
	}

	stderr, code := run("--outcome-mode")
	if code != 0 {
		t.Errorf("guard.sh --outcome-mode exited %d against a stale binary — that "+
			"becomes a hook error on every single edit, for a purely "+
			"observational step", code)
	}
	if strings.Contains(stderr, "flag provided but not defined") {
		t.Errorf("guard.sh --outcome-mode leaked a stale binary's flag-parse dump: %q",
			stderr)
	}

	// The decision path must stay loud. If this ever starts passing silently,
	// the swallow has been applied too broadly.
	if _, code := run("--hook-mode"); code == 0 {
		t.Error("guard.sh --hook-mode swallowed a stale binary's failure — the " +
			"decision path must surface it, not defer silently")
	}
}

// TestGuardShimOutcomeModeInvokesBinary pins that the --outcome-mode branch
// actually RUNS the guard.
//
// This exists because that branch had zero execution coverage: replacing its
// body with a bare `exit 0` — leaving PostToolUse completely inert — passed the
// entire suite. That is the exact bug this PR fixes (a channel that looks wired
// and does nothing), reintroduced inside the fix for it. The neighbouring tests
// could not catch it: one runs with no binary present, and the other asserts
// exit 0 plus no flag-parse dump, both of which an inert branch satisfies.
//
// A sentinel file is the assertion because it is the one thing an inert branch
// cannot fake.
func TestGuardShimOutcomeModeInvokesBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is a bash shim; the Windows install path does not use it")
	}
	root := repoRoot(t)
	shim := trackedPath(t, root, "plugins", "runecho-guard", "hooks", "guard.sh")

	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not found: %v", err)
	}
	binDir := t.TempDir()
	sentinel := filepath.Join(binDir, "invoked")
	stub := "#!/usr/bin/env bash\nprintf '%s\\n' \"$1\" > " + sentinel + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "runecho-guard"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	cmd := exec.Command("bash", shim, "--outcome-mode")
	cmd.Env = []string{
		"PATH=" + binDir + string(os.PathListSeparator) + filepath.Dir(bashPath),
		"HOME=" + binDir, "RUNECHO_BIN_DIR=",
	}
	cmd.Stdin = strings.NewReader("{}")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		t.Fatalf("guard.sh --outcome-mode: %v", err)
	}

	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("guard.sh --outcome-mode never invoked the binary — PostToolUse "+
			"is inert while every config still reports it as wired: %v", err)
	}
	if strings.TrimSpace(string(got)) != "--outcome-mode" {
		t.Errorf("binary invoked with %q, want %q", strings.TrimSpace(string(got)), "--outcome-mode")
	}
}

// TestGuardShimSurfacesRealDiagnostics is the counterpart to the stale-binary
// swallow: everything that is NOT a flag-parse dump has to reach the user.
//
// The outcome path reaches the registry, snapshot, generator and parser grammar
// loaders, all of which warn on stderr — a corrupt enrolled_at, a DB fault, a
// grammar that failed to load. An earlier revision discarded stderr wholesale on
// the false premise that runOutcomeMode never writes any, which silenced those
// for every plugin user on every edit.
func TestGuardShimSurfacesRealDiagnostics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is a bash shim; the Windows install path does not use it")
	}
	root := repoRoot(t)
	shim := trackedPath(t, root, "plugins", "runecho-guard", "hooks", "guard.sh")

	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not found: %v", err)
	}
	binDir := t.TempDir()
	const warning = "runecho: warning: repo 1 enrolled_at is corrupt"
	stub := "#!/usr/bin/env bash\necho '" + warning + "' >&2\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "runecho-guard"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	cmd := exec.Command("bash", shim, "--outcome-mode")
	cmd.Env = []string{
		"PATH=" + binDir + string(os.PathListSeparator) + filepath.Dir(bashPath),
		"HOME=" + binDir, "RUNECHO_BIN_DIR=",
	}
	cmd.Stdin = strings.NewReader("{}")
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("guard.sh --outcome-mode: %v", err)
	}

	if !strings.Contains(stderr.String(), warning) {
		t.Errorf("guard.sh swallowed a real diagnostic: got %q, want it to contain %q\n"+
			"Only the stale-binary flag-parse dump may be filtered; a DB fault or a "+
			"failed grammar load must stay debuggable.", stderr.String(), warning)
	}
}

// TestTechnicalDocDescribesEveryHookEvent covers the prose channel. install.sh
// and guard.sh both tell the reader TECHNICAL.md documents this contract, and
// TECHNICAL.md described it as "three places" with no PostToolUse and no
// --outcome-mode long after the code had two events — the same stale-contract
// defect this file exists to prevent, one file over.
//
// Docs are checked for the load-bearing TOKENS, not prose shape: asserting on
// wording would fail on every rewrite and teach the next person to delete the
// test rather than fix the doc.
func TestTechnicalDocDescribesEveryHookEvent(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "TECHNICAL.md"))
	if err != nil {
		t.Fatalf("read TECHNICAL.md: %v", err)
	}
	doc := string(raw)

	for event, mode := range hookContract {
		if !strings.Contains(doc, event) {
			t.Errorf("TECHNICAL.md never mentions %s, but install.sh and guard.sh both "+
				"point readers here for the hook contract", event)
		}
		if !strings.Contains(doc, mode) {
			t.Errorf("TECHNICAL.md never mentions %s", mode)
		}
	}
}

// TestHookModesAreRealFlags is the derived half. The contract table names two
// mode strings; if either stops being a flag main.go actually defines (renamed,
// typo'd), every config above would still "pass" while wiring a flag the binary
// rejects — and a flag-parse failure on PostToolUse is a per-edit error banner
// for a purely observational step.
func TestHookModesAreRealFlags(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	src := string(raw)

	for event, mode := range hookContract {
		flag := strings.TrimPrefix(mode, "--")
		if !strings.Contains(src, `fs.Bool("`+flag+`"`) {
			t.Errorf("%s wires %s, but main.go defines no %q flag — the binary would "+
				"reject the invocation every shipped config makes", event, mode, flag)
		}
	}
}

// TestGuardShimAcceptsEveryMode pins the plugin shim's mode validation. The
// shim rejects unknown modes rather than passing them through, so a mode added
// to the contract without a matching case arm would be silently swallowed —
// exit 0, no hook, no error, which is this bug's exact signature.
//
// This EXECUTES the shim rather than grepping it. The first version of this
// test asserted strings.Contains(shim, mode) and was vacuous: every mode name
// also appears in the file's header comment, so deleting the real `case` arm
// left the test green. Grepping a file for a token it documents proves nothing
// about what it does — which is the same mistake, one level up, as a prose
// comment claiming three configs agree.
//
// The shim is run with no runecho-guard reachable (empty PATH, HOME and
// RUNECHO_BIN_DIR pointed at an empty dir) so it always takes the
// binary-absent exit. That isolates the mode check: an accepted mode reaches
// the silent defer with no stderr, a rejected one prints first.
func TestGuardShimAcceptsEveryMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guard.sh is a bash shim; the Windows install path does not use it")
	}
	root := repoRoot(t)
	shim := trackedPath(t, root, "plugins", "runecho-guard", "hooks", "guard.sh")
	empty := t.TempDir()

	run := func(t *testing.T, mode string) (string, int) {
		t.Helper()
		cmd := exec.Command("bash", shim, mode)
		cmd.Env = []string{"PATH=" + empty, "HOME=" + empty, "RUNECHO_BIN_DIR="}
		cmd.Stdin = strings.NewReader("{}")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		cmd.Stdout = io.Discard
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run guard.sh %s: %v", mode, err)
		}
		return stderr.String(), code
	}

	for event, mode := range hookContract {
		stderr, code := run(t, mode)
		if code != 0 {
			t.Errorf("guard.sh %s exited %d — a hook shim must always exit 0", mode, code)
		}
		if strings.Contains(stderr, "unknown mode") {
			t.Errorf("guard.sh rejects %s (wired by %s) as an unknown mode: %s\n"+
				"The plugin would exit 0 without ever running the guard — "+
				"indistinguishable from a clean check.", mode, event, strings.TrimSpace(stderr))
		}
	}

	// The negative control. Without it, a shim that accepted everything
	// (validation deleted entirely) would pass every assertion above.
	stderr, code := run(t, "--not-a-real-mode")
	if code != 0 {
		t.Errorf("guard.sh exited %d on an unknown mode — it must still exit 0", code)
	}
	if !strings.Contains(stderr, "unknown mode") {
		t.Error("guard.sh accepted an unknown mode without complaint — its validation " +
			"is gone, so the positive cases above prove nothing")
	}
}
