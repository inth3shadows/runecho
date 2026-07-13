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

// installHooks installs pre-commit (guard) and post-commit/post-merge/post-checkout
// (background reindex) hooks into the git repo containing root.
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

	preCommit := fmt.Sprintf("#!/usr/bin/env bash\nexec %s \"$@\"\n", shellQuote(guardBin))
	reindex := fmt.Sprintf("#!/usr/bin/env bash\n%s repo reindex . >/dev/null 2>&1 &\n", shellQuote(irBin))
	// post-checkout: only reindex on branch switches ($3 == 1), not file checkouts.
	postCheckout := fmt.Sprintf("#!/usr/bin/env bash\n[ \"$3\" = \"1\" ] && %s repo reindex . >/dev/null 2>&1 &\n", shellQuote(irBin))

	hooks := map[string]string{
		"pre-commit":    preCommit,
		"post-commit":   reindex,
		"post-merge":    reindex,
		"post-checkout": postCheckout,
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

// installPeriodic installs an hourly reindex job via launchd (macOS) or cron (Linux).
func installPeriodic() error {
	irBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve binary path: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(irBin)
	default:
		return installCron(irBin)
	}
}

// installLaunchd writes a launchd plist and loads it (macOS).
func installLaunchd(irBin string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	agentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	plistPath := filepath.Join(agentsDir, "com.runecho.reindex.plist")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
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
	</array>
	<key>StartInterval</key>
	<integer>3600</integer>
	<key>StandardOutPath</key>
	<string>/tmp/runecho-reindex.log</string>
	<key>StandardErrorPath</key>
	<string>/tmp/runecho-reindex.log</string>
</dict>
</plist>
`, xmlEscape(irBin))
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

// installCron adds an hourly crontab entry on Linux/other.
func installCron(irBin string) error {
	entry := fmt.Sprintf("0 * * * * %s repo reindex --all >>/tmp/runecho-reindex.log 2>&1 # runecho", cronQuote(irBin))
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
