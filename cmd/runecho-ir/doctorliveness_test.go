package main

import (
	"testing"

	"github.com/inth3shadows/runecho/internal/doctor"
)

// #553: doctor's freshen-staleness threshold is "two missed ticks in a row",
// so it must move with the schedule's cadence.
func TestFreshenStaleAfterIsThreeIntervals(t *testing.T) {
	if doctor.FreshenStaleAfter != 3*freshenInterval {
		t.Errorf("doctor.FreshenStaleAfter = %v, want 3*freshenInterval = %v", doctor.FreshenStaleAfter, 3*freshenInterval)
	}
}

// doctor.FreshenLogPath re-implements the unquoting of the installer's
// generators (doctor cannot import package main). Round-trip both generated
// shapes, with paths that exercise every quoting rule, so the two cannot drift.
func TestDoctorFindsInstalledFreshenLog(t *testing.T) {
	const bin, gitDir = "/opt/run echo/runecho-ir", "/src/runecho/.git"
	cases := []struct{ name, entry, want string }{
		{"crontab",
			cronEntry(bin, "/r/reindex.log") + "\n" + freshenCronEntry(bin, "/r/o'b%x.log", gitDir),
			"/r/o'b%x.log"},
		{"launchd",
			launchdPlist(bin, "/r/reindex.log", "/r/reindex.log") + "\n" + freshenPlist(bin, "/r/a&b'<c>.log", "/r/a&b'<c>.log", gitDir),
			"/r/a&b'<c>.log"},
	}
	for _, c := range cases {
		if got := doctor.FreshenLogPath(c.entry); got != c.want {
			t.Errorf("%s: doctor.FreshenLogPath = %q, want %q\nentry:\n%s", c.name, got, c.want, c.entry)
		}
	}
}
