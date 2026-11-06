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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
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
	"ENROLL_NOTICE", "FILESCOPE", "LEARN", "LEARN_N",
	"LEARN_TTL_DAYS", "LINT", "MAX_AGE", "POSTURE", "QUALIFIED", "RECVMETHOD", "SKIP",
	"STRICT", "VARTYPE",
}

// retiredGateFlags are RUNECHO_GUARD_* flags whose check was removed, mapped
// to the issue that removed it. They gate nothing, so checkGates warns rather
// than reporting one as part of the posture: a user who still exports one
// believes a check is running that no longer exists.
var retiredGateFlags = map[string]string{"DUPLICATE": "#414"}

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
	out = append(out, checkPeriodic()...)
	out = append(out, checkGates()...)
	return out
}

// periodicPruneArg is the flag the installed hourly job must carry for
// retention to actually run. Kept next to the check that looks for it so the
// two cannot drift.
const periodicPruneArg = "--prune"

// hasFreshenEntry reports whether the installed schedule includes the hourly
// `runecho-ir freshen <dir>` entry (#375) — a second crontab line or a second
// LaunchAgent. Its absence is reported, never warned about: it only applies to
// a from-source install, and a Homebrew/goreleaser user with --periodic cannot
// and should not satisfy it.
//
// It matches the freshen argv, not the word: the cron line's quoted binary
// followed by `freshen '<dir>'` (the same anchor as the installer's
// cronFreshenArg), or the plist's `<string>freshen</string>` element. A bare
// " freshen " would also match a path such as `/opt/my freshen tools/...` on a
// reindex-only install, and since #553 this gate opens Warn and Fail checks.
func hasFreshenEntry(entry string) bool {
	return strings.Contains(entry, cronFreshenMarker) || strings.Contains(entry, "<string>freshen</string>")
}

// cronFreshenMarker is how a freshen crontab line reads after cronQuote: the
// closing quote of the binary, the command, the opening quote of the dir.
const cronFreshenMarker = "' freshen '"

// checkPeriodic reports whether the installed hourly reindex job also prunes.
//
// This check exists because upgrading the binary does NOT rewrite the schedule.
// The cron line and the LaunchAgent plist are written once, by
// `runecho-ir install --periodic`; install.sh does not touch them, and neither
// does a rebuild (`version-check --reinstall` or the job's own --freshen,
// #375). So a machine that installed the periodic job before retention shipped
// keeps running the old reindex-only command forever, and its store keeps
// growing — on exactly the long-lived installs #351 was filed about.
//
// Asserting the GENERATED text (as the installer tests do) cannot catch that:
// the generator is correct and the installed artifact is stale. Only reading
// what is actually scheduled can, which is what this does.
func checkPeriodic() []Result {
	entry, where, found := installedPeriodicJob()
	// No `found` guard: with nothing found entry and where are both empty, so
	// neither liveness gate opens.
	return append(classifyPeriodic(entry, where, found), periodicLiveness(entry, where, livenessProbe{
		goos:       runtime.GOOS,
		now:        time.Now(),
		defaultLog: defaultReindexLog(),
		readTail:   func(p string) ([]byte, bool, error) { return readLogTail(p, freshenLogTail) },
		launchctl:  launchctlPrint,
	})...)
}

// classifyPeriodic is the verdict, split from the I/O so it can be tested
// against every shape of installed job without a real crontab or LaunchAgent
// on the machine running the tests.
func classifyPeriodic(entry, where string, found bool) []Result {
	const name = "periodic reindex"

	switch {
	case !found:
		// Not an error: the periodic job is opt-in, and the git hooks keep the
		// IR fresh without it. Say so rather than implying something is broken.
		return []Result{{
			Check: name, Status: OK,
			Detail: "not installed (optional; git hooks keep the IR fresh)",
		}}
	case !strings.Contains(entry, periodicPruneArg):
		return []Result{{
			Check: name, Status: Warn,
			Detail: "installed in " + where + " but does NOT prune — snapshot history grows without bound (#351)",
			Remedy: "runecho-ir install --periodic   # rewrites the schedule to `repo reindex --all --prune`",
		}}
	case !hasFreshenEntry(entry):
		return []Result{{
			Check: name, Status: OK,
			Detail: "installed in " + where + " and prunes; does not keep the binaries fresh — for a from-source install, re-run `runecho-ir install --periodic` from inside the runecho checkout (#375)",
		}}
	default:
		return []Result{{
			Check: name, Status: OK,
			Detail: "installed in " + where + ", prunes, and keeps the binaries at origin's newest release",
		}}
	}
}

// installedPeriodicJob returns the scheduled command line RunEcho installed,
// where it lives, and whether one was found. Reads the real artifacts — the
// user's crontab on Linux, the LaunchAgent plist on macOS — because the point
// is to catch an installed job that no longer matches what this binary would
// write today.
func installedPeriodicJob() (entry, where string, found bool) {
	if home, err := os.UserHomeDir(); err == nil {
		plist := filepath.Join(home, "Library", "LaunchAgents", "com.runecho.reindex.plist")
		if b, err := os.ReadFile(plist); err == nil {
			// The freshen agent is a separate plist (#375); fold it in so the
			// verdict can see it.
			fp := filepath.Join(home, "Library", "LaunchAgents", "com.runecho.freshen.plist")
			if fb, err := os.ReadFile(fp); err == nil {
				b = append(append(b, '\n'), fb...)
			}
			return string(b), plist, true
		}
	}
	// `crontab -l` exits non-zero when no crontab exists, which is a legitimate
	// "not installed", not a failure worth reporting.
	out, err := exec.Command("crontab", "-l").Output()
	if err != nil {
		return "", "", false
	}
	var reindex string
	var others []string
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case !strings.Contains(line, "# runecho"):
		case reindex == "" && strings.Contains(line, "repo reindex"):
			reindex = line
		default:
			others = append(others, line) // e.g. the freshen line (#375)
		}
	}
	if reindex == "" {
		return "", "", false
	}
	return strings.Join(append([]string{reindex}, others...), "\n"), "crontab", true
}

// The checks below answer "the periodic job is installed — is it actually
// running?" (#553). classifyPeriodic judges the installed schedule's text, which
// stays correct while the scheduler never starts the job: on a Mac both
// LaunchAgents sat in launchd "spawn failed" (last exit code 78: EX_CONFIG) for
// nine days while every runecho signal, this one included, read healthy.

const (
	// FreshenStaleAfter is how old the newest freshen line may be before the
	// job reads as not running: two missed hourly ticks in a row. A single
	// miss (a sleeping laptop, a stopped WSL VM) is ~2h and must not alarm.
	// Exported only so cmd/runecho-ir can pin it to 3*freshenInterval.
	FreshenStaleAfter = 3 * time.Hour
	// freshenLogTail bounds what doctor reads: the reindex job rotates the log at
	// 10 MiB since #554, but older installs never did (a 43 MB one was
	// measured). An hour of output is ~10-20 KB, so 1 MiB is ~50+ hours. A
	// freshen line older than the whole tail is stale evidence in itself.
	freshenLogTail = 1 << 20
	// freshenClockSkew is how far in the future a line may be dated before it
	// reads as a moved clock rather than an NTP step.
	freshenClockSkew = 5 * time.Minute
	launchctlTimeout = 5 * time.Second
)

var (
	// cronLogRedirect finds the `>>LOG` word cronEntry/freshenCronEntry write:
	// a cronQuote'd single-quoted word, or a bare one.
	cronLogRedirect = regexp.MustCompile(`>>\s*('(?:[^']|'\\'')*'|[^\s']+)`)
	plistStdoutPath = regexp.MustCompile(`<key>StandardOutPath</key>\s*<string>([^<]*)</string>`)
)

// RotatedLogSuffix is the one older generation the reindex job keeps (#554).
// Exported so cmd/runecho-ir rotates to exactly the name doctor falls back to.
const RotatedLogSuffix = ".1"

// livenessProbe is checkPeriodic's I/O, injected so periodicLiveness is testable
// without launchd, cron or a real log.
type livenessProbe struct {
	goos       string
	now        time.Time
	defaultLog string
	readTail   func(path string) (tail []byte, truncated bool, err error)
	launchctl  func(label string) (out string, err error)
}

// periodicLiveness reports whether the installed periodic job is running: on
// macOS, launchd's own state for each runecho LaunchAgent; everywhere, how long
// ago the freshen job last wrote its once-per-tick line (freshen.go writes one
// on every path precisely so that silence means a dead job).
func periodicLiveness(entry, where string, p livenessProbe) []Result {
	var out []Result
	// Both gates: a stray plist on Linux still yields a plist `where`, and a
	// crontab install on a Mac has no LaunchAgent to ask about.
	if p.goos == "darwin" && strings.HasSuffix(where, ".plist") {
		for _, label := range launchAgentLabels(entry) {
			printed, err := p.launchctl(label)
			out = append(out, classifyLaunchAgent(label, printed, err))
		}
	}
	if hasFreshenEntry(entry) {
		path := FreshenLogPath(entry)
		if path == "" {
			path = p.defaultLog
		}
		if path == "" {
			return append(out, Result{Check: "periodic freshen", Status: Warn,
				Detail: "cannot locate the freshen log: the installed entry names none and $RUNECHO_HOME does not resolve",
				Remedy: "runecho-ir install --periodic   # rewrites the schedule with its log redirect"})
		}
		used, tail, truncated, err := readFreshenEvidence(path, p.readTail)
		out = append(out, classifyFreshenLog(used, tail, truncated, err, p.now))
	}
	return out
}

// readFreshenEvidence returns the tail to judge and the path it came from:
// path's own tail, unless path does not exist or holds no freshen line, in which
// case path+RotatedLogSuffix's tail if THAT holds one. The :00 reindex rotates
// the log (#554), and until the :30 freshen spawn reopens it there is no active
// log at all, then one without a freshen line. Any other read error on path is
// reported as it is.
func readFreshenEvidence(path string, readTail func(string) ([]byte, bool, error)) (used string, tail []byte, truncated bool, err error) {
	tail, truncated, err = readTail(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return path, tail, truncated, err
	}
	if err == nil {
		if _, _, ok := newestFreshenLine(tail, truncated); ok {
			return path, tail, truncated, nil
		}
	}
	rotated := path + RotatedLogSuffix
	if rTail, rTrunc, rErr := readTail(rotated); rErr == nil {
		if _, _, ok := newestFreshenLine(rTail, rTrunc); ok {
			return rotated, rTail, rTrunc, nil
		}
	}
	return path, tail, truncated, err
}

// FreshenLogPath returns the log the installed freshen entry writes to — the
// cron line's `>>` target or the freshen plist's StandardOutPath — or "" if the
// entry names none. The installed entry, not this process's $RUNECHO_HOME, is
// the truth about where the job writes. The unquoting mirrors
// existingFreshenSource (cmd/runecho-ir/install.go), which doctor cannot import;
// TestDoctorFindsInstalledFreshenLog round-trips the generators to catch drift.
func FreshenLogPath(entry string) string {
	if i := strings.Index(entry, "<string>freshen</string>"); i >= 0 {
		// Search after the freshen argv element: the reindex plist, folded in
		// ahead of it, has its own StandardOutPath.
		if m := plistStdoutPath.FindStringSubmatch(entry[i:]); m != nil {
			return html.UnescapeString(m[1])
		}
		return ""
	}
	for _, line := range strings.Split(entry, "\n") {
		if !strings.Contains(line, cronFreshenMarker) {
			continue
		}
		// The redirect is the LAST `>>` on the line: a quoted binary or dir
		// can contain `>>` too, and it comes first.
		if all := cronLogRedirect.FindAllStringSubmatch(line, -1); len(all) > 0 {
			return cronUnquote(all[len(all)-1][1])
		}
		return ""
	}
	return ""
}

// cronUnquote reverses cronQuote for one word: `\%` back to `%`, then the
// shell single quotes, with `'\”` standing for a literal quote.
func cronUnquote(word string) string {
	v := strings.ReplaceAll(word, `\%`, "%")
	if len(v) >= 2 && strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
		v = strings.ReplaceAll(v[1:len(v)-1], `'\''`, "'")
	}
	return v
}

// defaultReindexLog is where `install --periodic` points the jobs
// ($RUNECHO_HOME/logs/reindex.log), used only when the entry names no log.
func defaultReindexLog() string {
	dir, err := store.RunechoDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "logs", "reindex.log")
}

// readLogTail returns at most max bytes from the end of path, and whether
// anything before them was cut off. Only a regular file is read: opening a FIFO
// with no writer blocks forever, and doctor must never hang. The Stat before the
// Open is what keeps the open from blocking; a path swapped for a FIFO between
// the two is not a case doctor defends.
func readLogTail(path string, max int64) (tail []byte, truncated bool, err error) {
	if fi, err := os.Stat(path); err != nil {
		return nil, false, err
	} else if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is not a regular file (%s)", path, fi.Mode().Type())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if fi.Size() > max {
		if _, err := f.Seek(fi.Size()-max, io.SeekStart); err != nil {
			return nil, false, err
		}
		truncated = true
	}
	tail, err = io.ReadAll(io.LimitReader(f, max))
	return tail, truncated, err
}

// newestFreshenLine returns the timestamp and message of the last line, by
// position, shaped `<RFC3339> freshen: <msg>`. A line whose first word does not
// parse is skipped, not fatal: a reindex error that mentions "freshen:", or the
// partial line a truncated tail starts with, says nothing about liveness.
func newestFreshenLine(tail []byte, truncated bool) (ts time.Time, msg string, ok bool) {
	if truncated {
		i := bytes.IndexByte(tail, '\n')
		if i < 0 {
			return time.Time{}, "", false
		}
		tail = tail[i+1:]
	}
	lines := strings.Split(string(tail), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		first, rest, found := strings.Cut(strings.TrimSuffix(lines[i], "\r"), " ")
		if !found || !strings.HasPrefix(rest, "freshen: ") {
			continue
		}
		t, err := time.Parse(time.RFC3339, first)
		if err != nil {
			continue
		}
		return t, strings.TrimPrefix(rest, "freshen: "), true
	}
	return time.Time{}, "", false
}

// freshenRemedy is shared by every "installed but not running" verdict.
const freshenRemedy = "the freshen schedule is installed but not running — macOS: see the launchd checks above; " +
	"Linux: confirm cron is running. Meanwhile 'runecho-ir version-check --reinstall' catches the binaries up"

// classifyFreshenLog is the freshen-liveness verdict. Every non-OK case is a
// Warn, not a Fail: it is an inference (a laptop asleep for a weekend looks the
// same), matching doctor's other "installed but quietly not doing its job"
// verdicts (stale enrollment, no prune, zero asks).
func classifyFreshenLog(path string, tail []byte, truncated bool, readErr error, now time.Time) Result {
	const name = "periodic freshen"
	switch {
	case errors.Is(readErr, fs.ErrNotExist):
		return Result{Check: name, Status: Warn,
			Detail: path + " does not exist — the freshen job has never written to it",
			Remedy: freshenRemedy}
	case readErr != nil:
		return Result{Check: name, Status: Warn,
			Detail: fmt.Sprintf("cannot read %s: %v", path, readErr),
			Remedy: freshenRemedy}
	}
	ts, msg, ok := newestFreshenLine(tail, truncated)
	if !ok {
		if truncated {
			return Result{Check: name, Status: Warn,
				Detail: "no freshen line in the last 1 MiB of " + path + " — expected one every hour",
				Remedy: freshenRemedy}
		}
		return Result{Check: name, Status: Warn,
			Detail: "no freshen line in " + path + " — the job has never run (the first run lands up to an hour after install)",
			Remedy: freshenRemedy}
	}
	age := now.Sub(ts)
	switch {
	case age < -freshenClockSkew:
		return Result{Check: name, Status: Warn,
			Detail: fmt.Sprintf("newest freshen line is dated %s, after now (%s) — the system clock moved; cannot judge",
				ts.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)),
			Remedy: "check the system clock (date -u)"}
	case age > FreshenStaleAfter:
		return Result{Check: name, Status: Warn,
			Detail: fmt.Sprintf("no freshen run for %s (last %s) — expected hourly; log %s",
				roughAge(age), ts.UTC().Format(time.RFC3339), path),
			Remedy: freshenRemedy}
	}
	if age < 0 {
		age = 0
	}
	if r := []rune(msg); len(r) > 160 {
		msg = string(r[:160]) + "…"
	}
	return Result{Check: name, Status: OK,
		Detail: fmt.Sprintf("last ran %s ago: %s (%s)", roughAge(age), msg, path)}
}

// roughAge renders d to the minute without Duration's trailing "0s" ("23m",
// "216h30m"), and anything under a minute as "<1m".
func roughAge(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
}

// launchAgentLabels lists the LaunchAgents the installed entry implies: the
// reindex agent always (its plist is what made `where` a plist), the freshen
// agent only when its plist was folded in.
func launchAgentLabels(entry string) []string {
	labels := []string{"com.runecho.reindex"}
	if hasFreshenEntry(entry) {
		labels = append(labels, "com.runecho.freshen")
	}
	return labels
}

// launchctlPrint returns `launchctl print` for label in the user's launchd
// domain. A read, so doctor stays read-only.
func launchctlPrint(label string) (string, error) {
	return launchctlPrintWith(runLaunchctlPrint, os.Getuid(), label)
}

// launchctlPrintWith asks gui/<uid> first, where an agent loaded from a login
// session lives, then user/<uid>, where legacy `launchctl load` (the installer's
// writeLaunchAgent) puts it when run from a non-GUI session such as ssh. Only
// when neither knows the label does the gui answer stand.
func launchctlPrintWith(run func(target string) (string, error), uid int, label string) (string, error) {
	out, err := run(fmt.Sprintf("gui/%d/%s", uid, label))
	if err != nil && launchdNotFound(out) {
		if uOut, uErr := run(fmt.Sprintf("user/%d/%s", uid, label)); uErr == nil {
			return uOut, nil
		}
	}
	return out, err
}

// runLaunchctlPrint runs `launchctl print <target>`, stdout and stderr together
// (a missing service explains itself on stderr), bounded by launchctlTimeout.
// WaitDelay bounds the wait for the output pipes too, should a child of
// launchctl hold them open past the kill.
func runLaunchctlPrint(target string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), launchctlTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "launchctl", "print", target)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// launchdNotFound reports whether launchctl's output says it knows no such
// service ("Could not find service ... in domain ...").
func launchdNotFound(out string) bool {
	return strings.Contains(strings.ToLower(out), "could not find service")
}

// launchdField returns the value of the first top-level `key = value` line in
// `launchctl print` output whose key is one of keys. Top level is exactly one
// tab deep: nested blocks (resource coalition, ...) carry their own
// `state = active` two tabs deep, which must not be read as the job's state.
// Trimming exactly one tab enforces that: a nested line keeps a leading tab,
// so its key never equals a bare one.
func launchdField(printed string, keys ...string) (string, bool) {
	for _, line := range strings.Split(printed, "\n") {
		rest, ok := strings.CutPrefix(line, "\t")
		if !ok {
			continue
		}
		k, v, found := strings.Cut(rest, " = ")
		if found && slices.Contains(keys, k) {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// runechoExitCode reports whether code is one runecho-ir itself can return
// (cmd/runecho-ir/main.go: 0 ok, 1 findings, 2 error). Anything else — 78
// EX_CONFIG above all — came from launchd failing to run the binary. Exit 2
// alone is NOT a dead reindex job: `repo reindex --all` returns it on any tick
// where one repo's reindex fails (a walk timeout on a large tree, an unreadable
// root), and binaries before #554 also returned it for every deleted
// enrollment.
func runechoExitCode(code int) bool { return code >= 0 && code <= 2 }

// freshenAgentLabel's job is fail-open: freshen returns ExitOK on every path
// (freshen.go), so a nonzero exit means the binary could not run the command at
// all — one too old to have it exits 2, "unknown command", on every tick.
const freshenAgentLabel = "com.runecho.freshen"

// classifyLaunchAgent is launchd's verdict on one runecho LaunchAgent.
func classifyLaunchAgent(label, printed string, err error) Result {
	name := "launchd " + label
	remedy := fmt.Sprintf("launchctl bootout gui/$(id -u)/%s; launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/%s.plist", label, label)
	if err != nil {
		if launchdNotFound(printed) {
			return Result{Check: name, Status: Fail,
				Detail: "plist installed but not loaded in launchd — it never runs", Remedy: remedy}
		}
		return Result{Check: name, Status: Warn, Detail: fmt.Sprintf("could not query launchd: %v", err)}
	}
	exit, haveExit := launchdField(printed, "last exit code")
	// Observed during the #553 outage as a top-level `job state = spawn
	// failed`; `state` is checked too in case launchd reports it there.
	for _, key := range []string{"job state", "state"} {
		if v, ok := launchdField(printed, key); ok && strings.Contains(v, "spawn failed") {
			detail := fmt.Sprintf("launchd cannot start it (%s = %s", key, v)
			if haveExit {
				detail += "; last exit code = " + exit
			}
			return Result{Check: name, Status: Fail, Detail: detail + ")", Remedy: remedy}
		}
	}
	if haveExit {
		num, _, _ := strings.Cut(exit, ":")
		if code, err := strconv.Atoi(strings.TrimSpace(num)); err == nil {
			switch {
			case !runechoExitCode(code):
				return Result{Check: name, Status: Fail,
					Detail: "last exit code = " + exit + " — not a code runecho-ir returns; launchd failed to run it", Remedy: remedy}
			case label == freshenAgentLabel && code != 0:
				return Result{Check: name, Status: Fail,
					Detail: "last exit code = " + exit + " — freshen always exits 0, so the installed binary cannot run it (too old to have the command?)",
					Remedy: "bash install.sh   # from the runecho checkout, to rebuild the binary; then runecho-ir install --periodic"}
			}
		}
	}
	st, haveState := launchdField(printed, "state")
	if !haveExit && !haveState {
		return Result{Check: name, Status: OK, Detail: "loaded (launchctl output not recognised; not judged)"}
	}
	return Result{Check: name, Status: OK, Detail: fmt.Sprintf("loaded; state = %s, last exit code = %s", st, exit)}
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
				// Two remedies because this check compares against the tag reachable
				// from the checkout, while --reinstall installs ORIGIN's newest release
				// (#375) — they differ when a local tag is unpushed.
				Remedy: fmt.Sprintf("run 'bash %s/install.sh' (builds this checkout), or 'runecho-ir version-check --reinstall' (installs origin's newest release)", root),
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
	for _, name := range slices.Sorted(maps.Keys(channels)) { // fixed output order
		path := channels[name]
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
				Remedy: "run 'runecho-ir install' (it updates runecho's marked block in place; if it refuses the hook, follow its message)",
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

// storeHealthResult turns a live Health result and any recorded check failure
// into doctor's verdict. Pure, so every branch is unit-testable.
//
// Remedies name 'repo reindex --all', which always runs the full check (and so
// records or clears the finding); 'repo list' would skip it while the last
// pass is fresh and nothing is recorded — doctor itself only reads.
func storeHealthResult(h snapshot.HealthInfo, healthErr error, storePath, finding string, recorded bool) Result {
	const recheck = "'runecho-ir repo reindex --all'"
	ierr := snapshot.ClassifyIntegrity(h.Integrity)
	if healthErr != nil {
		// Health itself failed (the check or a count could not run): no new
		// verdict on damage, handled like an unreadable check below.
		h.Integrity, ierr = healthErr.Error(), healthErr
	}
	switch {
	case errors.Is(ierr, snapshot.ErrIntegrityFailed):
		return Result{
			Check: "store health", Status: Fail,
			Detail: "sqlite integrity check reports: " + h.Integrity,
			// Not 'runecho-ir backup': it runs the same check and refuses a
			// corrupt store, so the way back is an existing backup.
			Remedy: "restore " + storePath + " from " + snapshot.DefaultBackupPath(storePath) + " (or another backup), then run " + recheck + " to re-check",
		}
	case ierr != nil:
		// The check could not read the store (I/O, memory) — that says nothing
		// new about damage, so it must not send anyone to a restore on its own;
		// but an earlier recorded finding still stands and must be shown.
		r := Result{
			Check: "store health", Status: Warn,
			Detail: "the integrity check could not run: " + h.Integrity,
			Remedy: "re-run 'runecho-ir doctor'; if it persists, check the disk",
		}
		if recorded {
			r.Status = Fail
			r.Detail += "; an earlier check recorded damage (" + finding + ")"
			r.Remedy = "check the disk, then restore " + storePath + " from " + snapshot.DefaultBackupPath(storePath) + " and run " + recheck
		}
		return r
	case recorded:
		// A live pass with an old finding still on disk: the guard and
		// runecho-mcp keep warning until a checked open clears it.
		return Result{
			Check: "store health", Status: Warn,
			Detail: "integrity ok now, but an earlier check recorded damage (" + finding + ")",
			Remedy: "run " + recheck + " — it re-checks the store and clears the record",
		}
	}
	return Result{
		Check: "store health", Status: OK,
		Detail: fmt.Sprintf("schema v%d, %d repo(s) enrolled, integrity ok", h.SchemaVersion, h.RepoCount),
	}
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
	storePath := filepath.Join(dir, "history.db")

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
		finding, recorded := snapshot.CorruptFinding(storePath)
		out = append(out, storeHealthResult(h, err, storePath, finding, recorded))
		if repos, err := db.ListRepos(); err == nil {
			out = append(out, checkRefreshLocks(dir, repos))
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
	out := []Result{{Check: "gates", Status: OK, Detail: detail}}
	for _, name := range slices.Sorted(maps.Keys(retiredGateFlags)) {
		if v := os.Getenv("RUNECHO_GUARD_" + name); v != "" {
			out = append(out, Result{
				Check: "gates", Status: Warn,
				Detail: fmt.Sprintf("RUNECHO_GUARD_%s=%s is set, but its check was retired in %s; the flag does nothing", name, v, retiredGateFlags[name]),
				Remedy: "unset RUNECHO_GUARD_" + name,
			})
		}
	}
	return out
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
		case "ask", "deny": // a deny is RUNECHO_GUARD_POSTURE=deny's ask (#528)
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

// checkRefreshLocks reports E6 refresh locks in the store dir whose repo id has
// no enrolment row (#384).
//
// Reported rather than swept, and Warn rather than Fail: a lock is advisory
// scratch state, not data, so an orphan costs readability and — eventually — a
// blocked writer, never correctness. Sweeping lives on `repo prune-missing`,
// which is human-triggered and already the command that deletes. doctor is
// read-only by contract (#331), so it must not be the thing that removes them.
//
// It exists because #382's fix stopped the leak without clearing the backlog,
// and invisible rot is what let the backlog grow in the first place — the same
// argument #382 itself made for surfacing enrolment rot in `repo list`.
func checkRefreshLocks(dir string, repos []snapshot.Repo) Result {
	live := make(map[int64]struct{}, len(repos))
	for i := range repos {
		live[repos[i].ID] = struct{}{}
	}
	matches, err := filepath.Glob(store.RefreshLockGlob(dir))
	if err != nil {
		return Result{Check: "refresh locks", Status: Warn, Detail: err.Error()}
	}
	orphans := 0
	for _, p := range matches {
		// A name RefreshLockID refuses is not ours; it is not counted, exactly
		// as the sweeper would not delete it.
		if id, ok := store.RefreshLockID(filepath.Base(p)); ok {
			if _, live := live[id]; !live {
				orphans++
			}
		}
	}
	if orphans == 0 {
		return Result{
			Check: "refresh locks", Status: OK,
			Detail: fmt.Sprintf("%d lock(s), none orphaned", len(matches)),
		}
	}
	return Result{
		Check: "refresh locks", Status: Warn,
		Detail: fmt.Sprintf("%d of %d E6 refresh lock(s) have no enrolled repo", orphans, len(matches)),
		Remedy: "runecho-ir repo prune-missing --yes  (sweeps them)",
	}
}
