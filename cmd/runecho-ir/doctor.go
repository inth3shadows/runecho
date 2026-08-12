package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/store"
	"github.com/inth3shadows/runecho/internal/version"
	"github.com/inth3shadows/runecho/internal/wiring"
)

// `runecho-ir doctor` answers the one question none of the existing commands
// does: is this install actually wired and answering? (#331)
//
// The guard's whole contract is fail-open, so every way it can be inert is also
// a way it looks fine from outside — a stale binary, an unwired PostToolUse
// event, git hooks pointing at a scratch build, an unenrolled tree, a store with
// no snapshot. None of those error. The guard still asks, still defers, still
// exits 0. Four half-answers already exist across `version-check`, `repo list`,
// a test, and strict-mode logging; this is the command that reads all of them at
// once and says which one is lying.
//
// Read-only by construction: it fixes nothing and installs nothing. That is a
// non-goal from the issue, not an omission — a doctor that repairs is a doctor
// nobody can safely run to find out what is wrong.

// doctorStatus is the verdict for one check. Ordered by severity so a report can
// be reduced to its worst line.
type doctorStatus string

const (
	statusOK   doctorStatus = "ok"
	statusWarn doctorStatus = "warn"
	statusFail doctorStatus = "fail"
)

// severity ranks a status for worst-of reduction. Kept as a function rather
// than an ordered constant block so adding a status is a compile error here
// instead of a silently-wrong comparison.
func severity(s doctorStatus) int {
	switch s {
	case statusFail:
		return 2
	case statusWarn:
		return 1
	default:
		return 0
	}
}

// doctorCheck is one line of the report. Remedy is mandatory for anything that
// is not ok: a diagnostic a user cannot act on is a diagnostic that gets
// ignored, which is how the wiring gap survived in the first place.
type doctorCheck struct {
	Name    string       `json:"name"`
	Status  doctorStatus `json:"status"`
	Detail  string       `json:"detail"`
	Remedy  string       `json:"remedy,omitempty"`
	Details []string     `json:"details,omitempty"`
}

type doctorReport struct {
	Root    string        `json:"root"`
	Version string        `json:"version"`
	Worst   doctorStatus  `json:"worst"`
	Checks  []doctorCheck `json:"checks"`
}

func runDoctor(args []string) int {
	asJSON, strict := false, false
	root := "."
	for _, a := range args {
		switch {
		case a == "--json":
			asJSON = true
		case a == "--strict":
			strict = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(os.Stderr, "runecho-ir doctor: unknown flag %q\n", a)
			return ExitError
		default:
			root = a
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return printErr(err)
	}

	rep := doctorReport{Root: abs, Version: version.Version}
	rep.Checks = append(rep.Checks,
		checkBinaries(abs),
		checkClaudeWiring(abs),
		checkGitHooks(abs),
		checkEnrollment(abs),
		checkStore(),
		checkGates(),
	)
	rep.Worst = statusOK
	for _, c := range rep.Checks {
		if severity(c.Status) > severity(rep.Worst) {
			rep.Worst = c.Status
		}
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return printErr(err)
		}
	} else {
		printDoctor(rep)
	}
	// Exit 0 unless --strict, so `doctor` is safe to run anywhere (a shell
	// prompt, a subagent) without a nonzero exit being read as a broken command
	// rather than a broken install.
	if strict && rep.Worst == statusFail {
		return ExitError
	}
	return 0
}

func printDoctor(rep doctorReport) {
	fmt.Printf("runecho doctor — %s (runecho-ir %s)\n", rep.Root, rep.Version)
	for _, c := range rep.Checks {
		fmt.Printf("  [%-4s] %-16s %s\n", c.Status, c.Name, c.Detail)
		for _, d := range c.Details {
			fmt.Printf("           %s\n", d)
		}
		if c.Remedy != "" {
			fmt.Printf("           -> %s\n", c.Remedy)
		}
	}
	switch rep.Worst {
	case statusOK:
		fmt.Println("all checks ok")
	default:
		fmt.Printf("worst: %s\n", rep.Worst)
	}
}

// binVersion runs `<bin> --version` and returns the trailing token, which is the
// stamped version for both binaries. Shelling out rather than reading
// version.Version is the point: version.Version is THIS process's version, and
// the whole failure mode being diagnosed is a shipped binary that differs from
// the source you are standing in.
func binVersion(path string) string {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "?"
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return "?"
	}
	return fields[len(fields)-1]
}

func checkBinaries(root string) doctorCheck {
	c := doctorCheck{Name: "binaries"}
	var missing []string
	found := map[string]string{}
	for _, name := range []string{"runecho-ir", "runecho-guard"} {
		p, err := exec.LookPath(name)
		if err != nil {
			missing = append(missing, name)
			continue
		}
		v := binVersion(p)
		found[name] = v
		c.Details = append(c.Details, fmt.Sprintf("%s %s (%s)", name, v, p))
	}
	if len(missing) > 0 {
		c.Status = statusFail
		c.Detail = fmt.Sprintf("not on PATH: %s", strings.Join(missing, ", "))
		c.Remedy = "run `bash install.sh` from a runecho checkout, then re-open the shell"
		return c
	}

	// A guard that disagrees with the indexer is the #207 shape: the two halves
	// were installed at different times, so the ask side and the reporting side
	// are running different code. Measured consequence, not theory — a 30-day
	// window reported 70% approval while its trailing 2 days reported 19%.
	if found["runecho-ir"] != found["runecho-guard"] {
		c.Status = statusWarn
		c.Detail = fmt.Sprintf("runecho-ir %s and runecho-guard %s disagree", found["runecho-ir"], found["runecho-guard"])
		c.Remedy = "reinstall both from one checkout: `bash install.sh`"
		return c
	}

	// Only compare against source when standing in a runecho tree; anywhere else
	// there is no tag to compare to and silence is correct.
	if isRunechoTree(root) {
		if tag, err := gitutil.DescribeTag(root); err == nil && tag != "" {
			if versionBehind(found["runecho-ir"], tag) {
				c.Status = statusWarn
				c.Detail = fmt.Sprintf("installed %s is behind source %s", found["runecho-ir"], tag)
				c.Remedy = "run `bash install.sh` — a stale binary reports measurements from code you are not reading"
				return c
			}
		}
	}
	c.Status = statusOK
	c.Detail = fmt.Sprintf("both on PATH at %s", found["runecho-ir"])
	return c
}

// claudeWiringChannels are the config files that can attach the guard, in the
// order a user is likely to have them. Absent files are not failures — a user
// wires ONE channel — but zero present channels is.
func claudeWiringChannels(root string) map[string]string {
	home, _ := os.UserHomeDir()
	ch := map[string]string{
		"repo .claude/settings.json": filepath.Join(root, ".claude", "settings.json"),
		"plugin hooks.json":          filepath.Join(root, "plugins", "runecho-guard", "hooks", "hooks.json"),
	}
	if home != "" {
		ch["~/.claude/settings.json"] = filepath.Join(home, ".claude", "settings.json")
	}
	return ch
}

func checkClaudeWiring(root string) doctorCheck {
	c := doctorCheck{Name: "claude-wiring"}
	channels := claudeWiringChannels(root)
	names := make([]string, 0, len(channels))
	for n := range channels {
		names = append(names, n)
	}
	sort.Strings(names)

	present, wired := 0, 0
	for _, name := range names {
		hf, err := wiring.ParseFile(channels[name])
		if os.IsNotExist(err) {
			continue
		}
		present++
		if err != nil {
			c.Details = append(c.Details, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		problems := wiring.Check(hf)
		if len(problems) == 0 {
			wired++
			c.Details = append(c.Details, fmt.Sprintf("%s: %s wired", name, strings.Join(wiring.Events(), " + ")))
			continue
		}
		for _, p := range problems {
			c.Details = append(c.Details, fmt.Sprintf("%s: %s", name, p.Detail))
		}
	}

	switch {
	case present == 0:
		c.Status = statusFail
		c.Detail = "no Claude Code hook config found in any channel"
		c.Remedy = "install the plugin, or merge `bash install.sh --print-hook-config` into ~/.claude/settings.json"
	case wired == 0:
		c.Status = statusFail
		c.Detail = fmt.Sprintf("%d config(s) present, none wires the full contract", present)
		// Named explicitly because this is the exact gap that shipped: an
		// install with PreToolUse but no PostToolUse asks normally and measures
		// nothing, and no error is ever printed.
		c.Remedy = "a PreToolUse-only install still asks but records no outcomes — fpreport stays empty and learned-allow never fires"
	default:
		c.Status = statusOK
		c.Detail = fmt.Sprintf("%d of %d present config(s) wire the full contract", wired, present)
	}
	return c
}

func checkGitHooks(root string) doctorCheck {
	c := doctorCheck{Name: "git-hooks"}
	// The hooks directory is $(git rev-parse --git-common-dir)/hooks, NOT
	// gitutil.HooksPath — that returns the core.hooksPath CONFIG, which is
	// normally unset, so using it reported "not a git repository" for every
	// ordinary repo. Honour the override when it is set, since a repo that
	// redirects core.hooksPath genuinely keeps its hooks elsewhere.
	//
	// git-common-dir matters more than it looks: it is SHARED across every
	// worktree, which is precisely why one worktree's `repo add` can repoint the
	// hooks for all of them (#301).
	hooksDir := gitutil.HooksPath(root)
	if hooksDir == "" {
		common, err := gitutil.CommonDir(root)
		if err != nil {
			c.Status = statusWarn
			c.Detail = "not a git repository, so no git hooks to check"
			return c
		}
		hooksDir = filepath.Join(common, "hooks")
	} else if !filepath.IsAbs(hooksDir) {
		hooksDir = filepath.Join(root, hooksDir)
	}
	// The five hooks and their owning installer are documented in CLAUDE.md;
	// doctor reports what it finds rather than asserting a set, because a repo
	// that installs only some of them is a legitimate posture.
	want := []string{"pre-commit", "post-commit", "post-merge", "post-checkout"}
	resolved, _ := exec.LookPath("runecho-ir")
	resolvedGuard, _ := exec.LookPath("runecho-guard")

	var missing, foreign []string
	for _, h := range want {
		body, err := os.ReadFile(filepath.Join(hooksDir, h))
		if err != nil {
			missing = append(missing, h)
			continue
		}
		text := string(body)
		if !strings.Contains(text, "runecho") {
			continue // someone else's hook; not ours to judge
		}
		// #301's detection half: a hook body naming an absolute path that is not
		// the resolved install means `repo add` (or a scratch build) repointed
		// the SHARED .bare/hooks for every worktree at a binary you are not
		// running. Silent, and it makes every guard answer come from other code.
		if p := absBinPathIn(text); p != "" && p != resolved && p != resolvedGuard {
			foreign = append(foreign, fmt.Sprintf("%s -> %s", h, p))
		}
	}
	switch {
	case len(foreign) > 0:
		c.Status = statusFail
		c.Detail = fmt.Sprintf("%d hook(s) point at a binary that is not the resolved install", len(foreign))
		c.Details = foreign
		c.Remedy = "re-run `runecho-ir install` from the intended checkout; hooks live in the SHARED git-common-dir, so one worktree repoints them for all"
	case len(missing) == len(want):
		c.Status = statusWarn
		c.Detail = "no runecho git hooks installed — the IR index will not auto-refresh"
		c.Remedy = "run `runecho-ir install` in this repo (stale IR is a known false-positive source)"
	case len(missing) > 0:
		c.Status = statusWarn
		c.Detail = fmt.Sprintf("missing hook(s): %s", strings.Join(missing, ", "))
		c.Remedy = "run `runecho-ir install` to restore the auto-reindex hooks"
	default:
		c.Status = statusOK
		c.Detail = fmt.Sprintf("%d hook(s) present in %s", len(want), hooksDir)
	}
	return c
}

// absBinPathIn returns the first absolute path in the hook body that names a
// runecho binary, or "". Deliberately conservative: it matches only whitespace-
// delimited tokens that start with "/" and end in a known binary name, so a
// comment mentioning runecho does not read as an invocation.
func absBinPathIn(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") {
			continue
		}
		for _, tok := range strings.Fields(line) {
			tok = strings.Trim(tok, "\"'`")
			if !strings.HasPrefix(tok, "/") {
				continue
			}
			base := filepath.Base(tok)
			if base == "runecho-ir" || base == "runecho-guard" {
				return tok
			}
		}
	}
	return ""
}

func checkEnrollment(root string) doctorCheck {
	c := doctorCheck{Name: "enrollment"}
	db, code := mustOpenDB()
	if code != 0 {
		c.Status = statusFail
		c.Detail = "cannot open the store, so enrollment is unknowable"
		c.Remedy = "check $RUNECHO_HOME and that history.db is readable"
		return c
	}
	defer db.Close()

	repo, _, ok := db.ResolveRepo(root)
	if !ok {
		c.Status = statusWarn
		c.Detail = "this tree is not enrolled — most checks abstain silently here"
		c.Remedy = fmt.Sprintf("run `runecho-ir repo add %s`", root)
		return c
	}
	c.Details = append(c.Details, fmt.Sprintf("enrolled as %q (id %d)", repo.Name, repo.ID))

	snaps, err := db.List(repo.ID, 1)
	if err != nil || len(snaps) == 0 {
		c.Status = statusFail
		c.Detail = "enrolled but has no snapshot — every symbol reads as unknown"
		c.Remedy = fmt.Sprintf("run `runecho-ir repo reindex %s`", repo.Name)
		return c
	}
	age := time.Since(repo.LastIndexed)
	if repo.LastIndexed.IsZero() {
		age = time.Since(snaps[0].Timestamp)
	}
	c.Details = append(c.Details, fmt.Sprintf("latest snapshot %s old", age.Round(time.Minute)))
	if age > 24*time.Hour {
		c.Status = statusWarn
		c.Detail = fmt.Sprintf("index is %s old", age.Round(time.Hour))
		c.Remedy = "run `runecho-ir repo reindex .` — a stale index is a documented false-positive source"
		return c
	}
	c.Status = statusOK
	c.Detail = fmt.Sprintf("enrolled, index %s old", age.Round(time.Minute))
	return c
}

func checkStore() doctorCheck {
	c := doctorCheck{Name: "store"}
	dir, err := store.RunechoDir()
	if err != nil {
		c.Status = statusFail
		c.Detail = fmt.Sprintf("cannot resolve the store directory: %v", err)
		c.Remedy = "unset or fix $RUNECHO_HOME"
		return c
	}
	c.Details = append(c.Details, "dir "+dir)

	db, code := mustOpenDB()
	if code == 0 {
		if h, err := db.Health(); err == nil {
			c.Details = append(c.Details, fmt.Sprintf("schema v%d (binary understands v%d)", h.SchemaVersion, snapshot.SchemaVersion))
			if h.SchemaVersion > snapshot.SchemaVersion {
				db.Close()
				c.Status = statusFail
				c.Detail = fmt.Sprintf("store schema v%d is NEWER than this binary understands (v%d)", h.SchemaVersion, snapshot.SchemaVersion)
				c.Remedy = "upgrade the binaries: `bash install.sh` from the newest checkout"
				return c
			}
		}
		db.Close()
	}

	log := filepath.Join(dir, "decisions.jsonl")
	info, err := os.Stat(log)
	if err != nil {
		c.Status = statusWarn
		c.Detail = "no decisions.jsonl — the guard has never recorded a decision here"
		c.Remedy = "expected on a fresh install; if the guard has been running, check the PostToolUse wiring above"
		return c
	}
	c.Details = append(c.Details, fmt.Sprintf("decisions.jsonl %d bytes, modified %s ago",
		info.Size(), time.Since(info.ModTime()).Round(time.Minute)))
	c.Status = statusOK
	c.Detail = "store readable, decision log present"
	return c
}

// guardGates are the environment flags that change what the guard does, so a
// dogfood number can be attributed to a posture. Without this, two runs that
// disagree are indistinguishable from a flaky check — the "a harness measures
// the posture you gave it" failure.
var guardGates = []string{
	"RUNECHO_GUARD_SKIP", "RUNECHO_GUARD_STRICT", "RUNECHO_GUARD_DANGLING",
	"RUNECHO_GUARD_DROPPED_IMPORT", "RUNECHO_GUARD_DUPLICATE", "RUNECHO_GUARD_CALLSHAPE",
	"RUNECHO_GUARD_RECVMETHOD", "RUNECHO_GUARD_VARTYPE", "RUNECHO_GUARD_FILESCOPE",
	"RUNECHO_GUARD_DEPS_GO", "RUNECHO_GUARD_QUALIFIED", "RUNECHO_GUARD_CONTRACT",
	"RUNECHO_GUARD_LEARN", "RUNECHO_GUARD_LINT", "RUNECHO_HOME",
}

func checkGates() doctorCheck {
	c := doctorCheck{Name: "gates", Status: statusOK}
	var set []string
	for _, g := range guardGates {
		if v, ok := os.LookupEnv(g); ok {
			set = append(set, fmt.Sprintf("%s=%s", g, v))
		}
	}
	if len(set) == 0 {
		c.Detail = "no RUNECHO_* overrides in this environment (all defaults)"
		return c
	}
	c.Detail = fmt.Sprintf("%d override(s) active — attribute any measurement to this posture", len(set))
	c.Details = set
	// Reported, never judged. A set flag is a posture, not a fault; calling it
	// a warning would train users to ignore the line that explains their numbers.
	if v := os.Getenv("RUNECHO_GUARD_SKIP"); v == "1" {
		c.Status = statusWarn
		c.Detail = "RUNECHO_GUARD_SKIP=1 — the guard is disabled entirely in this environment"
		c.Remedy = "unset RUNECHO_GUARD_SKIP to re-enable"
	}
	return c
}
