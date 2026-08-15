package main

import (
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if code, ok := parseSub(fs, args); !ok {
		return code
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
		if err := installPeriodic(); err != nil {
			return printErr(err)
		}
	}
	return 0
}

// installHooks installs pre-commit (guard), post-commit (background reindex), and
// post-merge/post-checkout (freshness auto-reinstall + background reindex) hooks
// into the git repo containing root.
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
	// freshness: on the two moments a worktree picks up newer master (a merge, a
	// branch switch), rebuild the installed binaries if they're behind the tag,
	// THEN reindex — so the background reindex runs the just-built binary. The
	// version-check exits 0 on every path (it must never fail the git op); `|| true`
	// is belt-and-braces against a shell that treats its output oddly. It only ever
	// acts inside the runecho source tree and honours RUNECHO_NO_AUTO_INSTALL=1.
	// This folds #228 into the hooks installHooks already owns rather than adding a
	// third installer that would collide with these reindex hooks.
	freshen := fmt.Sprintf("%s version-check --reinstall --quiet || true", shellQuote(irBin))
	postMerge := fmt.Sprintf("#!/usr/bin/env bash\n%s\n%s repo reindex . >/dev/null 2>&1 &\n", freshen, shellQuote(irBin))
	// post-checkout: only act on branch switches ($3 == 1), not file checkouts.
	postCheckout := fmt.Sprintf("#!/usr/bin/env bash\n[ \"$3\" = \"1\" ] || exit 0\n%s\n%s repo reindex . >/dev/null 2>&1 &\n", freshen, shellQuote(irBin))

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

// installPeriodic installs an hourly reindex job via launchd (macOS) or cron (Linux).
func installPeriodic() error {
	irBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve binary path: %w", err)
	}
	logPath, err := reindexLogPath()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(irBin, logPath)
	default:
		return installCron(irBin, logPath)
	}
}

// installLaunchd writes a launchd plist and loads it (macOS).
func installLaunchd(irBin, logPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	plistPath := filepath.Join(agentsDir, "com.runecho.reindex.plist")
	plist := launchdPlist(irBin, logPath, logPath)
	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	// Unload first (idempotent — ignore error if not loaded), then load.
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if err := exec.Command("launchctl", "load", plistPath).Run(); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}
	fmt.Printf("Periodic reindex installed (hourly): %s\n", plistPath)
	return nil
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

// installCron adds an hourly crontab entry on Linux/other.
func installCron(irBin, logPath string) error {
	entry := cronEntry(irBin, logPath)
	// Read existing crontab, strip any prior runecho entry, append new one.
	existing, _ := exec.Command("crontab", "-l").Output()
	lines := strings.Split(strings.TrimRight(string(existing), "\n"), "\n")
	filtered := lines[:0]
	for _, l := range lines {
		if !strings.Contains(l, "# runecho") {
			filtered = append(filtered, l)
		}
	}
	filtered = append(filtered, entry)
	input := strings.Join(filtered, "\n") + "\n"
	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(input)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install crontab: %w", err)
	}
	fmt.Println("Periodic reindex installed (hourly via cron)")
	return nil
}
