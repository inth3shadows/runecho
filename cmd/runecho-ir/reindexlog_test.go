package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReindexLogPath_NotInSharedTmp pins the symlink-attack fix. The periodic
// reindex job used to append to the fixed path /tmp/runecho-reindex.log; on a
// shared host another local user can pre-create that as a symlink to the
// operator's ~/.bashrc, and the job writes through it. The log embeds file paths
// from indexed repos, so the content is partially attacker-chosen. macOS (where
// the launchd variant runs) has no fs.protected_symlinks equivalent.
func TestReindexLogPath_NotInSharedTmp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	got, err := reindexLogPath()
	if err != nil {
		t.Fatalf("reindexLogPath: %v", err)
	}

	want := filepath.Join(home, "logs", "reindex.log")
	if got != want {
		t.Errorf("log path = %q, want %q", got, want)
	}
	// The defect was a FIXED, world-predictable path, not "/tmp" as such — the
	// test's own RUNECHO_HOME is a t.TempDir() under /tmp and is perfectly safe
	// because it is unguessable and 0700. Assert the old literal is gone.
	if got == "/tmp/runecho-reindex.log" {
		t.Errorf("log path is still the fixed shared-tmp literal: %q", got)
	}
	if !strings.HasPrefix(got, home+string(filepath.Separator)) {
		t.Errorf("log path %q is outside RUNECHO_HOME %q", got, home)
	}
}

// TestReindexLogPath_DirIsOwnerOnly: living under RUNECHO_HOME only helps if the
// directory it creates is actually owner-only, matching the rest of the store.
func TestReindexLogPath_DirIsOwnerOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	got, err := reindexLogPath()
	if err != nil {
		t.Fatalf("reindexLogPath: %v", err)
	}

	info, err := os.Stat(filepath.Dir(got))
	if err != nil {
		t.Fatalf("stat log dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("log dir mode = %04o, want 0700", perm)
	}
}

// TestCronEntry_QuotesBothPaths: the log path is interpolated into a crontab
// command field, so it must be quoted like the binary path already was. A bare
// `%` there is turned into a newline by cron itself — splitting the command and
// feeding the remainder as stdin — before any shell parsing happens.
func TestCronEntry_QuotesBothPaths(t *testing.T) {
	entry := cronEntry("/opt/bin/runecho-ir", "/home/u/.runecho 100%/logs/reindex.log")

	if strings.Contains(entry, "%") && !strings.Contains(entry, `\%`) {
		t.Errorf("cron entry leaves a bare %%: %q", entry)
	}
	if !strings.Contains(entry, `>>'/home/u/.runecho 100\%/logs/reindex.log'`) {
		t.Errorf("log path not single-quoted into the redirect: %q", entry)
	}
	if !strings.Contains(entry, "'/opt/bin/runecho-ir'") {
		t.Errorf("binary path lost its quoting: %q", entry)
	}
	if !strings.HasSuffix(entry, "# runecho") {
		t.Errorf("entry lost its # runecho marker (install would stop replacing it): %q", entry)
	}
}
