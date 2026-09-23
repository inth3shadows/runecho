package main

import (
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/inth3shadows/runecho/internal/gitutil"
)

// runInstall installs git hooks in the current (or given) repo and optionally
// a periodic reindex job (launchd on macOS, cron on Linux).
// --periodic alone (no root) installs only the periodic job without touching hooks.
func runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	periodic := fs.Bool("periodic", false, "also install an hourly reindex job (launchd on macOS, cron on Linux)")
	force := fs.Bool("force", false, "overwrite existing hooks not created by runecho")
	source := fs.String("source", "", "with --periodic: the runecho checkout whose origin the job keeps the binaries fresh from (default: the current directory, #375)")
	if code, ok := parseSub(fs, args); !ok {
		return code
	}
	if *source != "" && !*periodic {
		fmt.Fprintln(os.Stderr, "runecho-ir install: --source only applies with --periodic (it chooses where the periodic job keeps the binaries fresh from)")
		return ExitError
	}

	// If a root path was given (or we're inside a git repo), install hooks.
	if len(fs.Args()) > 0 || !*periodic {
		root, code := resolveRoot(fs.Args())
		if code != 0 {
			return code
		}
		installed, err := installHooks(root, *force)
		if err != nil {
			if !*periodic {
				return printErr(err)
			}
			fmt.Fprintf(os.Stderr, "Warning: could not install hooks: %v\n", err)
		} else if installed == 0 && !*periodic {
			// Every hook was skipped (existing non-runecho hooks): an explicit
			// `install` that changed nothing must not exit 0 claiming success —
			// scripts read the code, and the guard is NOT active (F30/F33/F34).
			return ExitNoData
		}
	}

	if *periodic {
		if err := installPeriodic(*source); err != nil {
			return printErr(err)
		}
	}
	return 0
}

// installHooks installs pre-commit (guard), post-commit (background reindex), and
// post-merge/post-checkout (freshness advisory + background reindex) hooks into
// the git repo containing root.
func installHooks(root string, force bool) (installed int, err error) {
	gitDir, err := gitutil.AbsGitDir(root)
	if err != nil {
		return 0, fmt.Errorf("find git dir: %w", err)
	}
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return 0, fmt.Errorf("create hooks dir: %w", err)
	}

	irBin, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("resolve binary path: %w", err)
	}
	guardBin := filepath.Join(filepath.Dir(irBin), "runecho-guard")
	warnIfNotInstalledBinary(irBin)

	preCommit := fmt.Sprintf("#!/usr/bin/env bash\nexec %s \"$@\"\n", shellQuote(guardBin))
	reindex := fmt.Sprintf("#!/usr/bin/env bash\n%s repo reindex . >/dev/null 2>&1 &\n", shellQuote(irBin))
	// freshness ADVISORY: on the two moments a worktree picks up newer master (a
	// merge, a branch switch), say so if the installed binaries are behind the
	// nearest tag — one line, offline, and it executes nothing (#375). Rebuilding
	// moved to the periodic job and an explicit `version-check --reinstall`
	// (freshen.go): a checkout is not an act of trust, so the hook path must not
	// run the checked-out tree, and #373's attempt to gate that made the rebuild
	// inert on nearly every branch. The version-check exits 0 on every path;
	// `|| true` is belt-and-braces. It stays folded into these hooks (#228) rather
	// than a third installer that would collide with the reindex hooks.
	advise := fmt.Sprintf("%s version-check --quiet || true", shellQuote(irBin))
	postMerge := fmt.Sprintf("#!/usr/bin/env bash\n%s\n%s repo reindex . >/dev/null 2>&1 &\n", advise, shellQuote(irBin))
	// post-checkout: only act on branch switches ($3 == 1), not file checkouts.
	postCheckout := fmt.Sprintf("#!/usr/bin/env bash\n[ \"$3\" = \"1\" ] || exit 0\n%s\n%s repo reindex . >/dev/null 2>&1 &\n", advise, shellQuote(irBin))

	hooks := map[string]string{
		"pre-commit":    preCommit,
		"post-commit":   reindex,
		"post-merge":    postMerge,
		"post-checkout": postCheckout,
	}

	// core.hooksPath redirects git to run hooks from THERE, not the common-dir we
	// just wrote to — so a "success" message would be a lie. Warn instead. Empty
	// (the common case) means git reads hooks from the dir we installed into.
	if hp := gitutil.HooksPath(root); hp != "" {
		fmt.Fprintf(os.Stderr, "  Warning: core.hooksPath is set to %q — git will NOT run the hooks just installed in %s.\n", hp, hooksDir)
	}
	for name, content := range hooks {
		ok, hErr := installHookFile(hooksDir, name, content, force)
		if hErr != nil {
			return installed, hErr
		}
		if ok {
			installed++
		}
	}
	// Honest summary: "Hooks installed" used to print unconditionally, even
	// when every hook was skipped — reading as success while the guard is
	// not actually active.
	if installed == 0 {
		fmt.Printf("No hooks installed in %s (all %d skipped; use --force to overwrite existing hooks)\n", hooksDir, len(hooks))
	} else {
		fmt.Printf("Hooks installed in %s (%d/%d)\n", hooksDir, installed, len(hooks))
	}
	return installed, nil
}

// installLookupInstalledBin resolves the `runecho-ir` a normal invocation —
// a hook firing, or a user typing the bare command — would run. Seam
// overridden in tests: exec.LookPath depends on the real PATH, which a test
// must not rely on.
var installLookupInstalledBin = func() (string, error) { return exec.LookPath("runecho-ir") }

// warnIfNotInstalledBinary compares the currently-running binary against the
// one `runecho-ir` resolves to on PATH. installHooks writes hook bodies that
// invoke THIS process's own path (irBin) — shared by every worktree of the
// repo via the common git dir. A mismatch means those hooks are about to
// point at something other than the operator's normal install (a scratch
// `go build .` from a dev checkout is the case that bit #301: RUNECHO_HOME
// read as isolating the sandbox, but the hooks it silently repointed are
// shared state, not sandboxed).
//
// Advisory only, not a block: some workflows (packaging, an intentionally
// pinned build, CI) legitimately want a non-PATH binary's hooks installed.
// The bug #301 documents was that this happened with no signal at all — the
// hooks kept working, just against the wrong binary — so silence is the
// thing being fixed here, not the ability to do it deliberately.
func warnIfNotInstalledBinary(irBin string) {
	installed, err := installLookupInstalledBin()
	if err != nil {
		return // nothing on PATH to compare against; nothing to warn about
	}
	// Resolve symlinks on both sides so a symlinked install (e.g. a Homebrew
	// Cellar layout) doesn't read as a mismatch against itself.
	self, err1 := filepath.EvalSymlinks(irBin)
	other, err2 := filepath.EvalSymlinks(installed)
	if err1 != nil || err2 != nil {
		self, other = irBin, installed
	}
	if self == other {
		return
	}
	fmt.Fprintf(os.Stderr,
		"  Warning: this binary (%s) is not the one on PATH (%s) — the hooks\n"+
			"  about to be written will point at THIS binary for every worktree of\n"+
			"  this repo. If unintentional, re-run from the installed binary, or\n"+
			"  pass --no-hooks.\n",
		irBin, installed)
}

// installHookFile writes a single hook script. Skips if an existing hook is not
// a runecho hook (unless force). Overwrites existing runecho hooks always.
func installHookFile(hooksDir, name, content string, force bool) (installed bool, err error) {
	path := filepath.Join(hooksDir, name)
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), "runecho") && !force {
			fmt.Fprintf(os.Stderr, "  Skipping %s: existing hook (use --force to overwrite)\n", name)
			return false, nil
		}
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		return false, fmt.Errorf("write %s hook: %w", name, err)
	}
	fmt.Printf("  Installed %s\n", name)
	return true, nil
}

// reindexLogPath returns the file the periodic reindex job writes its output to,
// creating the parent directory 0700.
//
// It deliberately does NOT use /tmp. A fixed, world-predictable path in a shared
// directory is a symlink target: another local user pre-creates
// /tmp/runecho-reindex.log as a link to the operator's ~/.bashrc, and the hourly
// job's append-redirect then writes through it. The output embeds file paths from
// indexed repos — partially attacker-chosen text — so that chains toward planting
// shell in a startup file. Linux's fs.protected_symlinks blocks the trick by
// default; macOS, where the launchd variant below is the one in use, does not.
//
// $RUNECHO_HOME (default ~/.runecho) is already the owner-only 0700 home for
// every other thing runecho writes, so the log belongs there and inherits the
// same protection.
func reindexLogPath() (string, error) {
	dir, err := runechoDir()
	if err != nil {
		return "", err
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return "", fmt.Errorf("create log dir: %w", err)
	}
	return filepath.Join(logDir, "reindex.log"), nil
}

// installPeriodic installs an hourly reindex job via launchd (macOS) or cron
// (Linux). When source resolves to a runecho checkout it also installs a second
// hourly entry, `runecho-ir freshen <git-common-dir>`, that keeps the installed
// binaries at origin's newest release (#375) — separate so a binary without the
// freshen command can never break the reindex entry.
func installPeriodic(source string) error {
	irBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve binary path: %w", err)
	}
	logPath, err := reindexLogPath()
	if err != nil {
		return err
	}
	gitDir, note := detectFreshenSource(source)
	// Re-running `install --periodic` from somewhere else (another repo is the
	// natural place to run `install`) must not silently drop a freshen entry the
	// user set up earlier: the entries are REPLACED wholesale, and doctor only
	// reports a missing freshen entry, never warns. So with no source here, carry
	// the existing entry's value forward. An explicit --source still wins.
	if gitDir == "" && source == "" {
		if prev := existingFreshenSource(currentPeriodicJob()); prev != "" {
			if fi, err := os.Stat(prev); err == nil && fi.IsDir() {
				gitDir, note = prev, "Kept the existing freshen entry for "+prev+" (pass --source=<checkout> to change it)."
			}
		}
	}
	if note != "" {
		fmt.Println(note)
	}
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(irBin, logPath, gitDir)
	default:
		return installCron(irBin, logPath, gitDir)
	}
}

// detectFreshenSource resolves the git common dir the periodic job should
// freshen from: source if given, else the current directory, and only if it is
// inside the runecho source tree. Recording it at install time is the
// deliberate trust act — the user points at their checkout — and the value is
// visible verbatim in `crontab -l` / the plist, with no config file to migrate.
// A common dir, not a worktree: it survives worktree churn. ("", note) when
// nothing qualifies; the job is then written without --freshen.
func detectFreshenSource(source string) (gitDir, note string) {
	start := source
	if start == "" {
		start = "."
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Sprintf("Note: cannot resolve %q, so the job will not keep the binaries fresh: %v", start, err)
	}
	top, err := gitutil.TopLevel(abs)
	if err != nil || !isRunechoTree(top) {
		if source != "" {
			return "", fmt.Sprintf("Note: --source=%s is not a runecho checkout, so the job will not keep the binaries fresh.", source)
		}
		return "", "Note: not run from a runecho checkout, so the job will not keep the binaries fresh. " +
			"Re-run `runecho-ir install --periodic` from inside it (or pass --source=<checkout>) to enable that (#375)."
	}
	gitDir, err = gitutil.CommonDir(top)
	if err != nil {
		return "", fmt.Sprintf("Note: cannot resolve the git dir of %s, so the job will not keep the binaries fresh: %v", top, err)
	}
	return gitDir, ""
}

// Where each scheduler carries the freshen source: cron as the cronQuote'd word
// after `freshen`, launchd as the XML-escaped <string> after <string>freshen.
var (
	cronFreshenArg    = regexp.MustCompile(`' freshen ('(?:[^']|'\\'')*')`)
	launchdFreshenArg = regexp.MustCompile(`<string>freshen</string>\s*<string>([^<]*)</string>`)
)

// existingFreshenSource extracts the freshen source from installed schedule
// text (crontab lines or plists), undoing the quoting freshenCronEntry /
// freshenPlist applied. "" when there is none.
func existingFreshenSource(job string) string {
	if m := launchdFreshenArg.FindStringSubmatch(job); m != nil {
		return html.UnescapeString(m[1])
	}
	if m := cronFreshenArg.FindStringSubmatch(job); m != nil {
		v := strings.ReplaceAll(m[1], `\%`, "%")
		v = strings.TrimSuffix(strings.TrimPrefix(v, "'"), "'")
		return strings.ReplaceAll(v, `'\''`, "'")
	}
	return ""
}

// currentPeriodicJob returns the freshen schedule runecho installed, or "": the
// freshen LaunchAgent plist on macOS, else the `# runecho` crontab lines.
func currentPeriodicJob() string {
	if runtime.GOOS == "darwin" {
		if dir, err := launchAgentsDir(); err == nil {
			if b, err := os.ReadFile(filepath.Join(dir, freshenAgentPlist)); err == nil {
				return string(b)
			}
		}
		return ""
	}
	out, err := exec.Command("crontab", "-l").Output()
	if err != nil {
		return ""
	}
	var mine []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "# runecho") {
			mine = append(mine, line)
		}
	}
	return strings.Join(mine, "\n")
}

// installLaunchd writes a launchd plist and loads it (macOS).
func installLaunchd(irBin, logPath, gitDir string) error {
	if err := writeLaunchAgent("com.runecho.reindex.plist", launchdPlist(irBin, logPath, logPath)); err != nil {
		return err
	}
	fmt.Println("Periodic reindex installed (hourly via launchd)")
	// The freshen agent is written, or removed, to match gitDir — so re-running
	// without a source (and none to carry forward) leaves no stale agent behind.
	if gitDir == "" {
		removeLaunchAgent(freshenAgentPlist)
		return nil
	}
	if err := writeLaunchAgent(freshenAgentPlist, freshenPlist(irBin, logPath, logPath, gitDir)); err != nil {
		return err
	}
	fmt.Printf("Binary freshness installed (hourly via launchd, from %s)\n", gitDir)
	return nil
}

// freshenAgentPlist is the freshen LaunchAgent's file name (label
// com.runecho.freshen).
const freshenAgentPlist = "com.runecho.freshen.plist"

func launchAgentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents"), nil
}

// writeLaunchAgent writes a plist into ~/Library/LaunchAgents and (re)loads it.
func writeLaunchAgent(name, plist string) error {
	agentsDir, err := launchAgentsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	plistPath := filepath.Join(agentsDir, name)
	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	// Unload first (idempotent — ignore error if not loaded), then load.
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}
	return nil
}

// removeLaunchAgent unloads and deletes a plist if present; best-effort.
func removeLaunchAgent(name string) {
	agentsDir, err := launchAgentsDir()
	if err != nil {
		return
	}
	plistPath := filepath.Join(agentsDir, name)
	if _, err := os.Stat(plistPath); err != nil {
		return
	}
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	_ = os.Remove(plistPath)
}

// launchdPlist renders the hourly-reindex LaunchAgent. Split out of
// installLaunchd for the same reason cronEntry is split out of installCron: the
// generated text can then be asserted directly, on any platform, without a
// macOS-only side effect. Every value that reaches the XML goes through
// xmlEscape.
//
// ProgramArguments is an argv array executed with no shell, which is why
// retention is a `--prune` FLAG rather than a chained `&& repo prune` — a
// chained command is expressible in the crontab line and not here, and one
// scheduler quietly not pruning is exactly the asymmetry #351 is about.
func launchdPlist(irBin, outLog, errLog string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.runecho.reindex</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>repo</string>
		<string>reindex</string>
		<string>--all</string>
		<string>--prune</string>
	</array>
	<key>StartInterval</key>
	<integer>3600</integer>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, xmlEscape(irBin), xmlEscape(outLog), xmlEscape(errLog))
}

// freshenPlist renders the hourly freshen LaunchAgent (#375): its own agent so
// a binary lacking the freshen command cannot break the reindex agent.
func freshenPlist(irBin, outLog, errLog, gitDir string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.runecho.freshen</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>freshen</string>
		<string>%s</string>
	</array>
	<key>StartInterval</key>
	<integer>3600</integer>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, xmlEscape(irBin), xmlEscape(gitDir), xmlEscape(outLog), xmlEscape(errLog))
}

// xmlEscape escapes s for inclusion in an XML text node, using encoding/xml so
// the escaper matches the output format (the launchd file is a plist = XML).
// Replaces an earlier html.EscapeString whose output was valid XML only by
// coincidence and whose import misrepresented intent.
func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// shellQuote wraps s in single quotes for safe embedding in a POSIX shell
// command, escaping an embedded single quote with the standard close-escape-
// reopen sequence. Unlike Go's %q (which produces a Go string literal),
// single-quoting neutralizes $, backticks, and double quotes that the shell
// would otherwise expand. The interpolated value is os.Executable() (the
// operator's own install path, not attacker-controlled), so this is
// robustness hardening, not a reachable vulnerability: it makes a binary path
// containing shell metacharacters install a correct hook/cron line instead of
// a broken or surprising one.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cronQuote shell-quotes s for a crontab command field and additionally escapes
// `%`, which cron itself converts to a newline (splitting the command and feeding
// the remainder as stdin) BEFORE any shell parsing — single-quoting alone cannot
// prevent that, so the `%` must be backslash-escaped in the raw crontab line.
func cronQuote(s string) string {
	return strings.ReplaceAll(shellQuote(s), "%", `\%`)
}

// cronEntry builds the hourly crontab line. Split out from installCron so the
// quoting can be asserted without shelling out to `crontab`.
//
// logPath goes through cronQuote for the same reason irBin does: it is a
// filesystem path that may contain shell metacharacters — or a `%`, which cron
// itself converts to a newline before the shell ever parses the line, splitting
// the command and feeding the remainder as stdin.
// --prune keeps the store bounded on the same schedule that fills it, without a
// second crontab line to install, quote and keep in sync. It never vacuums: a
// full rewrite of a multi-gigabyte file has no business on an hourly timer.
func cronEntry(irBin, logPath string) string {
	return fmt.Sprintf("0 * * * * %s repo reindex --all --prune >>%s 2>&1 # runecho", cronQuote(irBin), cronQuote(logPath))
}

// freshenCronEntry is the second hourly line (#375), at :30 so it never starts
// alongside the reindex. Its own line, not a reindex flag: a binary that lacks
// the freshen command then fails only this line, and the reindex keeps running.
func freshenCronEntry(irBin, logPath, gitDir string) string {
	return fmt.Sprintf("30 * * * * %s freshen %s >>%s 2>&1 # runecho", cronQuote(irBin), cronQuote(gitDir), cronQuote(logPath))
}

// noCrontabYet reports whether crontab -l's stderr means "this user has no
// crontab yet" rather than "the read failed".
//
// The distinction is the difference between installing and destroying: both
// cases exit non-zero, and installCron writes the whole crontab back, so
// treating a failed read as an empty one deletes every entry the user has.
//
// Matched on the message, not the exit status, because implementations agree on
// the wording and disagree on the code. Verified against Debian/Ubuntu cron
// (vixie-cron), whose crontab binary carries exactly two such strings —
// "no crontab for %s" and "no crontab for %s - using an empty one" — both of
// which contain this substring. An implementation that words it differently
// falls through to the error path and aborts the install: a loud failure the
// user can retry, rather than a silent one they cannot undo.
func noCrontabYet(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "no crontab for")
}

// installCron adds an hourly crontab entry on Linux/other.
func installCron(irBin, logPath, gitDir string) error {
	entries := []string{cronEntry(irBin, logPath)}
	if gitDir != "" {
		entries = append(entries, freshenCronEntry(irBin, logPath, gitDir))
	}
	// Read existing crontab, strip any prior runecho entry, append new one.
	lsCmd := exec.Command("crontab", "-l")
	var lsErr bytes.Buffer
	lsCmd.Stderr = &lsErr
	existing, err := lsCmd.Output()
	if err != nil && !noCrontabYet(lsErr.String()) {
		// Anything other than "you have no crontab" — cron not installed, a
		// permission error, an unreadable spool — must not reach the write below.
		return fmt.Errorf("read existing crontab (refusing to overwrite it): %w: %s",
			err, strings.TrimSpace(lsErr.String()))
	}
	var lines []string
	// TrimRight then Split on an empty crontab yields [""], which would prepend a
	// blank line on every install; skip the split entirely in that case.
	if trimmed := strings.TrimRight(string(existing), "\n"); trimmed != "" {
		lines = strings.Split(trimmed, "\n")
	}
	filtered := lines[:0]
	for _, l := range lines {
		if !strings.Contains(l, "# runecho") {
			filtered = append(filtered, l)
		}
	}
	filtered = append(filtered, entries...)
	input := strings.Join(filtered, "\n") + "\n"
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(input)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install crontab: %w", err)
	}
	fmt.Println("Periodic reindex installed (hourly via cron)")
	if gitDir != "" {
		fmt.Printf("Binary freshness installed (hourly via cron, from %s)\n", gitDir)
	}
	return nil
}
