package main

import (
	"fmt"
	"strings"
	"testing"
)

// askLine builds one hook-mode ask record, optionally version-stamped.
func askLine(gv, file, sym string, minsAgo int) string {
	stamp := ""
	if gv != "" {
		stamp = fmt.Sprintf(`"gv":%q,`, gv)
	}
	return fmt.Sprintf(`{"v":1,%s"ts":%q,"mode":"hook","repo":"r","file":%q,"lang":"py","decision":"ask","reason":"violations","symbols":[%q]}`,
		stamp, minAgo(minsAgo), file, sym)
}

func approveLine(gv, file, sym string, minsAgo int) string {
	stamp := ""
	if gv != "" {
		stamp = fmt.Sprintf(`"gv":%q,`, gv)
	}
	return fmt.Sprintf(`{"v":1,%s"ts":%q,"mode":"hook","file":%q,"decision":"outcome","reason":"approved","symbols":[%q]}`,
		stamp, minAgo(minsAgo), file, sym)
}

// mixedLog is two builds' worth of asks in one window: the old one approves
// every ask, the new one approves none. Pooled that reads as 50% — a rate
// neither build ever had. Each build carries gateMinAsks asks on its own, so a
// --gv-scoped run stays gate-eligible and the skip under test is attributable to
// version mixing rather than to volume.
func mixedLog(t *testing.T, dir string) {
	t.Helper()
	var lines []string
	for i := 0; i < gateMinAsks; i++ {
		f := fmt.Sprintf("old%d.py", i)
		lines = append(lines, askLine("v0.6.1", f, "foo", 20), approveLine("v0.6.1", f, "foo", 19))
	}
	for i := 0; i < gateMinAsks; i++ {
		lines = append(lines, askLine("v0.12.0", fmt.Sprintf("new%d.py", i), "bar", 10))
	}
	writeDecisions(t, dir, lines...)
}

// The gate must not pass or fail on a pooled average — it must refuse to run,
// the same way it refuses a below-threshold ask count.
func TestFPReport_GateSkippedOnMixedVersions(t *testing.T) {
	dir := t.TempDir()
	mixedLog(t, dir)

	var code int
	var stderr string
	withHome(dir, func() {
		_, stderr = captureOutput(func() { code = runFPReport([]string{"--days=1", "--max-rate=0.1"}) })
	})

	if code != ExitOK {
		t.Errorf("exit = %d, want ExitOK — a skipped gate must not fail CI", code)
	}
	if !strings.Contains(stderr, "gate skipped") || !strings.Contains(stderr, "guard versions") {
		t.Errorf("stderr should say the gate was skipped for mixed versions, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--gv=") {
		t.Errorf("skip note should name the flag that fixes it, got:\n%s", stderr)
	}
}

// With --gv the window is a single build, so the gate evaluates again — and the
// new build's real 0% rate passes a gate the pooled 50% would have tripped.
func TestFPReport_GateEvaluatesWhenScopedToOneVersion(t *testing.T) {
	dir := t.TempDir()
	mixedLog(t, dir)

	var code int
	var stderr string
	withHome(dir, func() {
		_, stderr = captureOutput(func() {
			code = runFPReport([]string{"--days=1", "--max-rate=0.1", "--gv=v0.12.0"})
		})
	})

	if code != ExitOK {
		t.Errorf("exit = %d, want ExitOK — v0.12.0 approved nothing", code)
	}
	if strings.Contains(stderr, "gate skipped") {
		t.Errorf("gate should have evaluated for a single version, got:\n%s", stderr)
	}
}

// The complement: scoping to the bad build trips the gate that the pooled
// average was hiding.
func TestFPReport_GateTripsOnTheBadVersion(t *testing.T) {
	dir := t.TempDir()
	mixedLog(t, dir)

	var code int
	withHome(dir, func() {
		captureOutput(func() {
			code = runFPReport([]string{"--days=1", "--max-rate=0.1", "--gv=v0.6.1"})
		})
	})

	if code != ExitError {
		t.Errorf("exit = %d, want ExitError — v0.6.1 approved 100%% of asks", code)
	}
}

func TestFPReport_GVSelectsUnstampedRecords(t *testing.T) {
	dir := t.TempDir()
	writeDecisions(t, dir,
		askLine("", "legacy.py", "foo", 5),
		askLine("v0.12.0", "current.py", "bar", 5),
	)

	var out string
	withHome(dir, func() {
		out, _ = captureOutput(func() { runFPReport([]string{"--days=1", "--gv=unknown"}) })
	})

	if !strings.Contains(out, "1 ask(s)") {
		t.Errorf("--gv=unknown should select only the unstamped record, got:\n%s", out)
	}
	if strings.Contains(out, "MIXED GUARD VERSIONS") {
		t.Errorf("a version-scoped report must never warn about mixing:\n%s", out)
	}
}
