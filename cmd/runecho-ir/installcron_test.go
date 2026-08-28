package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installCron replaces the user's ENTIRE crontab (`crontab -` takes the whole
// file on stdin), so what it reads first is load-bearing. `crontab -l` exits
// non-zero both when the user has no crontab and when the read genuinely fails,
// and the original code discarded that error — making a transient failure
// indistinguishable from "empty" and deleting every entry the user had.
//
// These tests drive a fake `crontab` on PATH, so they exercise the real
// exec/stderr plumbing rather than a stubbed-out seam.

// fakeCrontab installs a `crontab` shim earlier on PATH than the real one and
// returns the path it records a write to. lsBehavior is shell run for `-l`.
func fakeCrontab(t *testing.T, lsBehavior string) string {
	t.Helper()
	dir := t.TempDir()
	wrote := filepath.Join(dir, "written")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"-l\" ]; then\n" + lsBehavior + "\nfi\n" +
		"if [ \"$1\" = \"-\" ]; then cat > " + wrote + "; exit 0; fi\n" +
		"echo unexpected args: \"$@\" >&2; exit 99\n"
	if err := os.WriteFile(filepath.Join(dir, "crontab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return wrote
}

func TestNoCrontabYet(t *testing.T) {
	// The two strings Debian/Ubuntu cron's crontab binary actually carries,
	// confirmed against the shipped binary, plus the failures that must NOT be
	// mistaken for them.
	for msg, want := range map[string]bool{
		"no crontab for ericm":                      true,
		"no crontab for ericm - using an empty one": true,
		"crontab: no crontab for ericm":             true,
		"":                                          false,
		"crontab: you are not allowed to use this program":  false,
		"/var/spool/cron/crontabs/ericm: Permission denied": false,
		"crontab: error: could not read /var/spool":         false,
	} {
		if got := noCrontabYet(msg); got != want {
			t.Errorf("noCrontabYet(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestInstallCron_RefusesToOverwriteWhenReadFails(t *testing.T) {
	wrote := fakeCrontab(t, `echo "/var/spool/cron/crontabs/u: Permission denied" >&2; exit 1`)

	err := installCron("/usr/local/bin/runecho-ir", "/tmp/reindex.log")
	if err == nil {
		t.Fatal("installCron returned nil on an unreadable crontab — it would have overwritten it")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("error = %q, want it to say why it stopped", err)
	}
	if _, statErr := os.Stat(wrote); statErr == nil {
		body, _ := os.ReadFile(wrote)
		t.Fatalf("crontab was written despite the failed read — this is the data loss:\n%s", body)
	}
}

func TestInstallCron_TreatsNoCrontabAsEmpty(t *testing.T) {
	wrote := fakeCrontab(t, `echo "no crontab for u" >&2; exit 1`)

	if err := installCron("/usr/local/bin/runecho-ir", "/tmp/reindex.log"); err != nil {
		t.Fatalf("installCron on a user with no crontab: %v", err)
	}
	got, err := os.ReadFile(wrote)
	if err != nil {
		t.Fatalf("nothing written: %v", err)
	}
	want := cronEntry("/usr/local/bin/runecho-ir", "/tmp/reindex.log") + "\n"
	if string(got) != want {
		t.Errorf("wrote %q, want %q (a leading blank line means the empty-crontab split was not skipped)", got, want)
	}
}

func TestInstallCron_PreservesExistingEntriesAndReplacesItsOwn(t *testing.T) {
	wrote := fakeCrontab(t, `printf '%s\n' "0 2 * * * /home/u/backup.sh" "# a comment" "0 * * * * /old/runecho-ir reindex # runecho"; exit 0`)

	if err := installCron("/usr/local/bin/runecho-ir", "/tmp/reindex.log"); err != nil {
		t.Fatalf("installCron: %v", err)
	}
	got, err := os.ReadFile(wrote)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"0 2 * * * /home/u/backup.sh",
		"# a comment",
		cronEntry("/usr/local/bin/runecho-ir", "/tmp/reindex.log"),
	}, "\n") + "\n"
	if string(got) != want {
		t.Errorf("wrote:\n%s\nwant:\n%s", got, want)
	}
}
