// Package doctor answers "is this install actually wired and answering?" —
// the question #331 filed because every existing signal (version-check,
// hookwiring_test.go, MCP status/health, degraded-path logging) answers only
// a slice of it, and every slice fails silently: the guard still asks, still
// defers, still exits 0, while quietly not doing its job.
//
// Doctor is read-only. It fixes nothing and installs nothing — purely a
// report, one line per check, so a maintainer (or a subagent, via --json)
// can tell "not wired" from "wired and quiet" without already knowing where
// to look.
package doctor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/guard"
	"github.com/inth3shadows/runecho/internal/hookwiring"
	"github.com/inth3shadows/runecho/internal/snapshot"
	"github.com/inth3shadows/runecho/internal/store"
	"github.com/inth3shadows/runecho/internal/version"
)

// Status is one check's verdict.
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Result is one line of the report.
type Result struct {
	Check  string `json:"check"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy,omitempty"`
}

// knownGateFlags is the current RUNECHO_GUARD_* set. No central registry of
// these exists — each check owns its own os.Getenv call in its own file
// (cmd/runecho-guard/{callshape,contract,dangling,...}.go) — so this list is
// hand-kept and WILL drift when a new check ships. Accepted as a known
// limitation rather than refactoring 11 files into a registry for this issue.
var knownGateFlags = []string{
	"CALLSHAPE", "CONTRACT", "DANGLING", "DEPS_GO", "DROPPED_IMPORT",
	"DUPLICATE", "FILESCOPE", "LEARN", "LEARN_N", "LEARN_TTL_DAYS", "LINT",
	"MAX_AGE", "QUALIFIED", "RECVMETHOD", "SKIP", "STRICT", "VARTYPE",
}

// hookFiles maps each installed git hook to the binary installHooks (cmd/
// runecho-ir/install.go) wrote its body to invoke.
var hookFiles = []string{"pre-commit", "post-commit", "post-merge", "post-checkout"}

// Run executes all six checks against the repo containing root and returns
// one or more Results per check, in a fixed order.
func Run(root string) []Result {
	var out []Result
	out = append(out, checkBinaries(root)...)
	out = append(out, checkWiring(root)...)
	out = append(out, checkGitHooks(root)...)
	out = append(out, checkEnrollment(root)...)
	out = append(out, checkStore(root)...)
	out = append(out, checkGates()...)
	return out
}

// resolvedBin returns the absolute, symlink-resolved path exec.LookPath finds
// for name, or "" if it is not on PATH.
func resolvedBin(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// checkBinaries confirms runecho-ir and runecho-guard resolve on PATH and
// are not behind the newest tag reachable from root (this is version-check's
// standing form, generalized to report status rather than auto-reinstall).
func checkBinaries(root string) []Result {
	var out []Result
	newest, tagErr := gitutil.DescribeTag(root)
	newestCore := version.SemverCore(newest)

	for _, name := range []string{"runecho-ir", "runecho-guard"} {
		path, err := exec.LookPath(name)
		if err != nil {
			out = append(out, Result{
				Check: name, Status: Fail,
				Detail: "not found on PATH",
				Remedy: "run install.sh, or add its install dir to PATH",
			})
			continue
		}
		verOut, _ := exec.Command(path, "--version").Output()
		installed := version.SemverCore(string(verOut))

		if tagErr != nil || newestCore == "" {
			out = append(out, Result{
				Check: name, Status: OK,
				Detail: fmt.Sprintf("%s (%s) — no source tag reachable from %s to compare against", path, dispVer(installed), root),
			})
			continue
		}
		if version.SemverLess(installed, newestCore) {
			out = append(out, Result{
				Check: name, Status: Fail,
				Detail: fmt.Sprintf("%s is %s, behind source tag %s", path, dispVer(installed), newestCore),
				Remedy: "run 'runecho-ir version-check --reinstall'",
			})
			continue
		}
		out = append(out, Result{
			Check: name, Status: OK,
			Detail: fmt.Sprintf("%s (%s), at or ahead of source tag %s", path, dispVer(installed), newestCore),
		})
	}
	return out
}

func dispVer(v string) string {
	if v == "" {
		return "unknown version"
	}
	return v
}

// checkWiring parses whichever of the two on-disk Claude Code hook-config
// channels (.claude/settings.json, the plugin's hooks.json) are present and
// validates each against hookwiring.Contract.
//
// The third channel install.sh prints (--print-hook-config) is deliberately
// NOT checked here: it is a documentation generator for hand-merging, not a
// config that persists on an installed user's machine, and it requires a
// runecho source checkout to even invoke. It stays covered by
// cmd/runecho-guard/hookwiring_test.go, which verifies the source tree; this
// check verifies an install.
func checkWiring(root string) []Result {
	channels := map[string]string{
		".claude/settings.json":                  filepath.Join(root, ".claude", "settings.json"),
		"plugins/runecho-guard/hooks/hooks.json": filepath.Join(root, "plugins", "runecho-guard", "hooks", "hooks.json"),
	}

	var out []Result
	present := 0
	for name, path := range channels {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // not present — not applicable, not a failure on its own
		}
		present++
		violations, err := hookwiring.CheckChannel(raw)
		if err != nil {
			out = append(out, Result{
				Check: name, Status: Fail,
				Detail: "invalid JSON: " + err.Error(),
				Remedy: "fix the file — Claude Code silently ignores an unparseable settings.json, disabling every hook, not just this one",
			})
			continue
		}
		if len(violations) > 0 {
			out = append(out, Result{
				Check: name, Status: Fail,
				Detail: strings.Join(violations, "; "),
				Remedy: "run 'runecho-ir install --force', or re-run /plugin install",
			})
			continue
		}
		out = append(out, Result{Check: name, Status: OK, Detail: "PreToolUse and PostToolUse both wired"})
	}
	if present == 0 {
		out = append(out, Result{
			Check: "claude code wiring", Status: Fail,
			Detail: "no hook config found (.claude/settings.json or plugins/runecho-guard/hooks/hooks.json)",
			Remedy: "run 'runecho-ir install' or '/plugin install'",
		})
	}
	return out
}

// checkGitHooks reads the git-common-dir hook files installHooks wrote and
// flags any whose body does not point at the binary currently resolved on
// PATH. This is #301's detection half — the mitigation (--no-hooks) already
// shipped; nothing before this reported a mispointed hook.
func checkGitHooks(root string) []Result {
	commonDir, err := gitutil.AbsGitDir(root)
	if err != nil {
		return []Result{{
			Check: "git hooks", Status: Fail,
			Detail: "not a git working tree: " + err.Error(),
		}}
	}
	hooksDir := filepath.Join(commonDir, "hooks")

	expect := map[string]string{
		"pre-commit":    resolvedBin("runecho-guard"),
		"post-commit":   resolvedBin("runecho-ir"),
		"post-merge":    resolvedBin("runecho-ir"),
		"post-checkout": resolvedBin("runecho-ir"),
	}

	var out []Result
	for _, name := range hookFiles {
		wantBin := expect[name]
		path := filepath.Join(hooksDir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			out = append(out, Result{
				Check: "git hook " + name, Status: Warn,
				Detail: "not installed",
				Remedy: "run 'runecho-ir install'",
			})
			continue
		}
		if wantBin == "" {
			// The binary itself didn't resolve on PATH — already reported by
			// checkBinaries; nothing new to say about which one this hook targets.
			out = append(out, Result{
				Check: "git hook " + name, Status: Warn,
				Detail: "installed, but the binary it should invoke is not on PATH",
			})
			continue
		}
		if !strings.Contains(string(body), wantBin) {
			out = append(out, Result{
				Check: "git hook " + name, Status: Fail,
				Detail: fmt.Sprintf("does not invoke %s — points at a different (likely stale) binary", wantBin),
				Remedy: "run 'runecho-ir install --force'",
			})
			continue
		}
		out = append(out, Result{Check: "git hook " + name, Status: OK, Detail: "invokes " + wantBin})
	}
	if hp := gitutil.HooksPath(root); hp != "" {
		out = append(out, Result{
			Check: "git hooks", Status: Fail,
			Detail: fmt.Sprintf("core.hooksPath is set to %q — git will NOT run the hooks in %s", hp, hooksDir),
			Remedy: "unset core.hooksPath, or reinstall pointed at that directory",
		})
	}
	return out
}

// checkEnrollment reports whether root is enrolled and how stale its latest
// snapshot is against RUNECHO_GUARD_MAX_AGE (default 24h) — the same
// threshold the guard itself uses to decide a snapshot is too old to trust.
func checkEnrollment(root string) []Result {
	// Any failure to open the store — including "it doesn't exist yet",
	// the common fresh-install case — reads as "not enrolled" here. A real
	// store problem (corruption, a bad schema) is reported at Fail severity
	// by checkStore's own explicit integrity check; this check would only
	// ever duplicate that at a lower-information Warn, so it doesn't try.
	db, err := openStore()
	if err != nil {
		return []Result{{
			Check: "enrollment", Status: Warn,
			Detail: "not enrolled (" + err.Error() + ")",
			Remedy: "run 'runecho-ir repo add', or let it auto-enroll on the first tracked edit",
		}}
	}
	defer db.Close()

	repo, _, ok := db.ResolveRepo(root)
	if !ok {
		return []Result{{
			Check: "enrollment", Status: Warn,
			Detail: "not enrolled",
			Remedy: "run 'runecho-ir repo add', or let it auto-enroll on the first tracked edit",
		}}
	}

	if repo.LastIndexed.IsZero() {
		return []Result{{
			Check: "enrollment", Status: Fail,
			Detail: fmt.Sprintf("%s is enrolled but has never been indexed", repo.Name),
			Remedy: "run 'runecho-ir repo reindex " + root + "'",
		}}
	}

	age := time.Since(repo.LastIndexed)
	maxAge, err := guard.ParseMaxAge()
	if err != nil {
		maxAge = 24 * time.Hour
	}
	status := OK
	var remedy string
	if age > maxAge {
		status = Warn
		remedy = "run 'runecho-ir repo reindex " + root + "', or confirm the post-commit/post-merge hooks are installed (see git hook checks above)"
	}
	return []Result{{
		Check: "enrollment", Status: status,
		Detail: fmt.Sprintf("%s last indexed %s ago (max age %s)", repo.Name, age.Round(time.Second), maxAge),
		Remedy: remedy,
	}}
}

// checkStore reports the central store's health and recent activity. It
// flags the exact signature of the incident hookwiring_test.go exists to
// prevent: an enrolled repo with zero asks AND zero outcomes in the last 7
// days, which is what --outcome-mode being unwired looks like from the
// store's side.
func checkStore(root string) []Result {
	var out []Result

	dir, err := store.RunechoDir()
	if err != nil {
		return []Result{{Check: "store", Status: Fail, Detail: "cannot resolve RUNECHO_HOME: " + err.Error()}}
	}
	out = append(out, Result{Check: "store", Status: OK, Detail: "RUNECHO_HOME = " + dir})

	db, err := openStore()
	if err != nil {
		// A store that doesn't exist yet is a fresh install, not a health
		// problem — same reasoning checkEnrollment applies to the identical
		// condition. A real open failure (permissions, corruption) still
		// surfaces below via db.Health()'s integrity check when a store
		// DOES exist, which is the case this Fail-vs-Warn split protects.
		status := Warn
		if !errors.Is(err, os.ErrNotExist) {
			status = Fail
		}
		out = append(out, Result{Check: "store health", Status: status, Detail: err.Error()})
	} else {
		defer db.Close()
		h, err := db.Health()
		if err != nil {
			out = append(out, Result{Check: "store health", Status: Fail, Detail: err.Error()})
		} else if h.Integrity != "ok" {
			out = append(out, Result{
				Check: "store health", Status: Fail,
				Detail: "sqlite integrity check reports: " + h.Integrity,
				Remedy: "restore from ~/.runecho/backups/, or 'runecho-ir backup' before further writes",
			})
		} else {
			out = append(out, Result{
				Check: "store health", Status: OK,
				Detail: fmt.Sprintf("schema v%d, %d repo(s) enrolled, integrity ok", h.SchemaVersion, h.RepoCount),
			})
		}
	}

	decisionsPath := filepath.Join(dir, "decisions.jsonl")
	asks, outcomes, err := countRecentDecisions(decisionsPath, 7*24*time.Hour)
	switch {
	case err != nil && os.IsNotExist(err):
		out = append(out, Result{
			Check: "decision log", Status: Warn,
			Detail: "no decisions.jsonl yet — no guard activity has been recorded",
		})
	case err != nil:
		out = append(out, Result{Check: "decision log", Status: Fail, Detail: err.Error()})
	default:
		enrolled := false
		if db != nil {
			if _, _, ok := db.ResolveRepo(root); ok {
				enrolled = true
			}
		}
		if enrolled && asks == 0 && outcomes == 0 {
			out = append(out, Result{
				Check: "decision log", Status: Warn,
				Detail: "0 asks and 0 outcomes in the last 7 days on an enrolled repo",
				Remedy: "check that BOTH PreToolUse and PostToolUse are wired (see claude code wiring checks above) — this is the exact signature of --outcome-mode shipping unwired",
			})
		} else {
			out = append(out, Result{
				Check: "decision log", Status: OK,
				Detail: fmt.Sprintf("%d ask(s), %d outcome(s) in the last 7 days", asks, outcomes),
			})
		}
	}
	return out
}

// checkGates reports which RUNECHO_GUARD_* flags are set in the current
// environment, so a dogfood number can be attributed to a posture. Always OK
// — this is a report of the current configuration, not a pass/fail check.
func checkGates() []Result {
	var set []string
	for _, name := range knownGateFlags {
		if v := os.Getenv("RUNECHO_GUARD_" + name); v != "" {
			set = append(set, name+"="+v)
		}
	}
	detail := "no RUNECHO_GUARD_* flags set (default posture for every check)"
	if len(set) > 0 {
		detail = strings.Join(set, " ")
	}
	return []Result{{Check: "gates", Status: OK, Detail: detail}}
}

// countRecentDecisions scans path (decisions.jsonl) and counts "ask" and
// "outcome" records with a timestamp within window of now. Malformed lines
// are skipped, not fatal — the log is append-only, best-effort text, and one
// bad line must not blind the count to every good one around it.
func countRecentDecisions(path string, window time.Duration) (asks, outcomes int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	cutoff := time.Now().Add(-window)
	type record struct {
		TS       string `json:"ts"`
		Decision string `json:"decision"`
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var r record
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, r.TS)
		if err != nil || ts.Before(cutoff) {
			continue
		}
		switch r.Decision {
		case "ask":
			asks++
		case "outcome":
			outcomes++
		}
	}
	return asks, outcomes, sc.Err()
}

// openStore opens the central snapshot store for a fast read. Mirrors
// cmd/runecho-ir/helpers.go's enrolledRepoID: OpenFast skips the on-open
// PRAGMA quick_check integrity scan (checkStore runs its own explicit
// integrity check via Health, so paying for it twice is pure latency).
func openStore() (*snapshot.DB, error) {
	dir, err := store.RunechoDir()
	if err != nil {
		return nil, fmt.Errorf("resolve RUNECHO_HOME: %w", err)
	}
	dbPath := filepath.Join(dir, "history.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no store at %s yet — nothing has been indexed: %w", dbPath, err)
	}
	db, err := snapshot.OpenFast(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	return db, nil
}
