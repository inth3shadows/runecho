package main

import (
	"flag"
	"fmt"
	"html"
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
		if err := installHooks(root, *force); err != nil {
			if !*periodic {
				return printErr(err)
			}
			fmt.Fprintf(os.Stderr, "Warning: could not install hooks: %v\n", err)
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
func installHooks(root string, force bool) error {
	gitDir, err := gitutil.AbsGitDir(root)
	if err != nil {
		return fmt.Errorf("find git dir: %w", err)
	}
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}

	irBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve binary path: %w", err)
	}
	guardBin := filepath.Join(filepath.Dir(irBin), "runecho-guard")

	preCommit := fmt.Sprintf("#!/usr/bin/env bash\nexec %q \"$@\"\n", guardBin)
	reindex := fmt.Sprintf("#!/usr/bin/env bash\n%q repo reindex . >/dev/null 2>&1 &\n", irBin)
	// post-checkout: only reindex on branch switches ($3 == 1), not file checkouts.
	postCheckout := fmt.Sprintf("#!/usr/bin/env bash\n[ \"$3\" = \"1\" ] && %q repo reindex . >/dev/null 2>&1 &\n", irBin)

	hooks := map[string]string{
		"pre-commit":    preCommit,
		"post-commit":   reindex,
		"post-merge":    reindex,
		"post-checkout": postCheckout,
	}
	for name, content := range hooks {
		if err := installHookFile(hooksDir, name, content, force); err != nil {
			return err
		}
	}
	fmt.Printf("Hooks installed in %s\n", hooksDir)
	return nil
}

// installHookFile writes a single hook script. Skips if an existing hook is not
// a runecho hook (unless force). Overwrites existing runecho hooks always.
func installHookFile(hooksDir, name, content string, force bool) error {
	path := filepath.Join(hooksDir, name)
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), "runecho") && !force {
			fmt.Fprintf(os.Stderr, "  Skipping %s: existing hook (use --force to overwrite)\n", name)
			return nil
		}
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		return fmt.Errorf("write %s hook: %w", name, err)
	}
	fmt.Printf("  Installed %s\n", name)
	return nil
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
`, html.EscapeString(irBin))
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

// installCron adds an hourly crontab entry on Linux/other.
func installCron(irBin string) error {
	entry := fmt.Sprintf("0 * * * * %q repo reindex --all >>/tmp/runecho-reindex.log 2>&1 # runecho", irBin)
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
