package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Liveness tests for #553: the periodic job is installed but is it running?

var livenessNow = time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC)

const reindexLine = "Unchanged r: snapshot id=1 files=3 root_hash=abc (touched)"

func freshenAt(t time.Time, msg string) string {
	return t.UTC().Format(time.RFC3339) + " freshen: " + msg
}

// --- newestFreshenLine ---

func TestNewestFreshenLine_PicksLastByPosition(t *testing.T) {
	tail := "2026-11-06T10:30:01Z freshen: a\n" + reindexLine + "\n2026-11-06T11:30:02Z freshen: b\n" + reindexLine + "\n"
	ts, msg, ok := newestFreshenLine([]byte(tail), false)
	if !ok || !ts.Equal(time.Date(2026, 11, 6, 11, 30, 2, 0, time.UTC)) || msg != "b" {
		t.Errorf("got (%v, %q, %v), want the 11:30:02 line with msg b", ts, msg, ok)
	}
}

func TestNewestFreshenLine_SkipsUnparseableTimestamp(t *testing.T) {
	tail := "2026-11-06T10:30:01Z freshen: good\nnot-a-time freshen: bad\n"
	ts, msg, ok := newestFreshenLine([]byte(tail), false)
	if !ok || !ts.Equal(time.Date(2026, 11, 6, 10, 30, 1, 0, time.UTC)) || msg != "good" {
		t.Errorf("got (%v, %q, %v), want the parseable 10:30:01 line — an unparseable line must be skipped, not end the scan", ts, msg, ok)
	}
}

func TestNewestFreshenLine_IgnoresMidLineMention(t *testing.T) {
	for _, line := range []string{
		"Error: x freshen: y",
		// Timestamped but not a freshen tick: only the "freshen: " prefix
		// right after the timestamp marks one.
		"2026-11-06T13:00:00Z reindex: failed while freshen: y ran",
	} {
		if _, _, ok := newestFreshenLine([]byte(line+"\n"), false); ok {
			t.Errorf("%q only mentions freshen: mid-line but was read as a freshen tick", line)
		}
	}
}

func TestNewestFreshenLine_DropsPartialFirstLineWhenTruncated(t *testing.T) {
	tail := []byte("2026-11-06T13:00:00Z freshen: cut\n" + reindexLine + "\n")
	if _, _, ok := newestFreshenLine(tail, true); ok {
		t.Error("truncated tail: the first (possibly partial) line must be dropped")
	}
	if _, _, ok := newestFreshenLine(tail, false); !ok {
		t.Error("untruncated tail: the first line is whole and must be read")
	}
}

func TestNewestFreshenLine_CRLF(t *testing.T) {
	_, msg, ok := newestFreshenLine([]byte("2026-11-06T13:30:02Z freshen: hi\r\n"), false)
	if !ok || msg != "hi" {
		t.Errorf("CRLF line: got (%q, %v), want (%q, true)", msg, ok, "hi")
	}
}

func TestNewestFreshenLine_LastLineWithoutNewline(t *testing.T) {
	if _, _, ok := newestFreshenLine([]byte(reindexLine+"\n2026-11-06T13:30:02Z freshen: x"), false); !ok {
		t.Error("a final line with no trailing newline must still be read")
	}
}

// --- classifyFreshenLog ---

const logPath = "/home/u/.runecho/logs/reindex.log"

func classify(tail string, truncated bool, err error) Result {
	return classifyFreshenLog(logPath, []byte(tail), truncated, err, livenessNow)
}

func TestClassifyFreshenLog_RecentIsOK(t *testing.T) {
	r := classify(reindexLine+"\n2026-11-06T13:32:00Z freshen: installed v0.88.1 is up to date\n", false, nil)
	if r.Status != OK {
		t.Fatalf("status = %q, want ok: %s", r.Status, r.Detail)
	}
	if !strings.Contains(r.Detail, "28m") || !strings.Contains(r.Detail, logPath) || !strings.Contains(r.Detail, "up to date") {
		t.Errorf("detail %q should give the age, the tick's message and the log path", r.Detail)
	}
}

// The #553 incident: the last tick was 2026-10-28T09:30Z, nine days earlier.
func TestClassifyFreshenLog_StaleIsWarn(t *testing.T) {
	r := classify("2026-10-28T09:30:00Z freshen: reinstalled, now v0.85.4\n"+reindexLine+"\n", false, nil)
	if r.Status != Warn {
		t.Fatalf("status = %q, want warn for a 9-day-old tick", r.Status)
	}
	if !strings.Contains(r.Detail, logPath) {
		t.Errorf("detail %q should name the log", r.Detail)
	}
	if !strings.Contains(r.Remedy, "launchd") || !strings.Contains(r.Remedy, "cron") {
		t.Errorf("remedy %q should point at both schedulers", r.Remedy)
	}
}

func TestClassifyFreshenLog_ThresholdBoundary(t *testing.T) {
	if r := classify(freshenAt(livenessNow.Add(-2*time.Hour-59*time.Minute), "x")+"\n", false, nil); r.Status != OK {
		t.Errorf("2h59m old: status = %q, want ok (one missed tick must not alarm)", r.Status)
	}
	if r := classify(freshenAt(livenessNow.Add(-3*time.Hour-1*time.Minute), "x")+"\n", false, nil); r.Status != Warn {
		t.Errorf("3h01m old: status = %q, want warn", r.Status)
	}
}

func TestClassifyFreshenLog_MissingLogIsWarn(t *testing.T) {
	r := classify("", false, &fs.PathError{Op: "open", Path: logPath, Err: fs.ErrNotExist})
	if r.Status != Warn || !strings.Contains(r.Detail, "does not exist") || !strings.Contains(r.Detail, logPath) {
		t.Errorf("got %q %q, want warn naming the missing log", r.Status, r.Detail)
	}
}

func TestClassifyFreshenLog_UnreadableLogIsWarn(t *testing.T) {
	r := classify("", false, errors.New("permission denied"))
	if r.Status != Warn || !strings.Contains(r.Detail, "cannot read") {
		t.Errorf("got %q %q, want warn 'cannot read'", r.Status, r.Detail)
	}
}

func TestClassifyFreshenLog_NoLineEverWrittenIsWarn(t *testing.T) {
	r := classify(reindexLine+"\n"+reindexLine+"\n", false, nil)
	if r.Status != Warn || !strings.Contains(r.Detail, "never") {
		t.Errorf("got %q %q, want warn saying the job has never run", r.Status, r.Detail)
	}
}

func TestClassifyFreshenLog_NoLineInTailIsWarn(t *testing.T) {
	r := classify(reindexLine+"\n"+reindexLine+"\n", true, nil)
	if r.Status != Warn || !strings.Contains(r.Detail, "last 1 MiB") {
		t.Errorf("got %q %q, want warn about the bounded tail", r.Status, r.Detail)
	}
}

func TestClassifyFreshenLog_FutureTimestampIsWarn(t *testing.T) {
	r := classify(freshenAt(livenessNow.Add(2*time.Hour), "x")+"\n", false, nil)
	if r.Status != Warn || !strings.Contains(r.Detail, "clock") {
		t.Errorf("got %q %q, want warn about a moved clock", r.Status, r.Detail)
	}
}

func TestClassifyFreshenLog_SmallSkewIsOK(t *testing.T) {
	if r := classify(freshenAt(livenessNow.Add(30*time.Second), "x")+"\n", false, nil); r.Status != OK {
		t.Errorf("30s in the future: status = %q, want ok (an NTP step must not alarm)", r.Status)
	}
}

func TestClassifyFreshenLog_OnlyUnparseableLineIsWarn(t *testing.T) {
	if r := classify("garbage freshen: x\n", false, nil); r.Status != Warn {
		t.Errorf("status = %q, want warn: an unparseable line is not a tick", r.Status)
	}
}

// --- FreshenLogPath ---

func TestFreshenLogPath(t *testing.T) {
	reindexPlist := `<plist><dict><key>ProgramArguments</key><array><string>/b/runecho-ir</string><string>repo</string><string>reindex</string></array>
	<key>StandardOutPath</key>
	<string>/a/r.log</string></dict></plist>`
	freshenPlistText := `<plist><dict><key>ProgramArguments</key><array><string>/b/runecho-ir</string><string>freshen</string><string>/g/.git</string></array>
	<key>StandardOutPath</key>
	<string>/b/a&amp;b.log</string></dict></plist>`
	cases := []struct{ name, entry, want string }{
		{"cron freshen line wins over reindex line",
			"0 * * * * '/b/runecho-ir' repo reindex --all --prune >>'/a/r.log' 2>&1 # runecho\n" +
				"30 * * * * '/b/runecho-ir' freshen '/g/.git' >>'/b/f.log' 2>&1 # runecho", "/b/f.log"},
		{"cron quoting undone",
			`30 * * * * '/b/runecho-ir' freshen '/g/.git' >>'/h/o'\''b\%x/f.log' 2>&1 # runecho`, "/h/o'b%x/f.log"},
		{"bare redirect word", "30 * * * * '/b/runecho-ir' freshen '/g/.git' >>/tmp/r.log 2>&1 # runecho", "/tmp/r.log"},
		{"freshen plist after reindex plist", reindexPlist + "\n" + freshenPlistText, "/b/a&b.log"},
		{"no redirect", "30 * * * * '/b/runecho-ir' freshen '/g/.git' # runecho", ""},
		// F4: a `>>` inside the quoted gitDir or binary is not the redirect.
		{">> in gitDir", `30 * * * * '/b/runecho-ir' freshen '/g>>x/.git' >>'/h/r.log' 2>&1 # runecho`, "/h/r.log"},
		{">> in binary", `30 * * * * '/b>>/ir' freshen '/g/.git' >>'/h/r.log' 2>&1 # runecho`, "/h/r.log"},
		// F5: " freshen " inside the reindex line's quoted binary path does not
		// make it the freshen line.
		{"freshen in a path", "0 * * * * '/opt/my freshen tools/runecho-ir' repo reindex --all --prune >>'/a/r.log' 2>&1 # runecho\n" +
			"30 * * * * '/opt/my freshen tools/runecho-ir' freshen '/g/.git' >>'/b/f.log' 2>&1 # runecho", "/b/f.log"},
	}
	for _, c := range cases {
		if got := FreshenLogPath(c.entry); got != c.want {
			t.Errorf("%s: FreshenLogPath = %q, want %q", c.name, got, c.want)
		}
	}
}

// --- readLogTail ---

func TestReadLogTail(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.log")
	// Non-periodic, so the first and last 1000 bytes differ.
	var cb strings.Builder
	for i := 0; cb.Len() < 3000; i++ {
		fmt.Fprintf(&cb, "%d,", i)
	}
	content := cb.String()[:3000]
	if err := os.WriteFile(big, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	tail, truncated, err := readLogTail(big, 1000)
	if err != nil || !truncated || string(tail) != content[2000:] {
		t.Errorf("3000-byte file, max 1000: got len=%d truncated=%v err=%v, want the last 1000 bytes, truncated", len(tail), truncated, err)
	}
	small := filepath.Join(dir, "small.log")
	if err := os.WriteFile(small, []byte(content[:500]), 0600); err != nil {
		t.Fatal(err)
	}
	tail, truncated, err = readLogTail(small, 1000)
	if err != nil || truncated || string(tail) != content[:500] {
		t.Errorf("500-byte file: got len=%d truncated=%v err=%v, want the whole file, not truncated", len(tail), truncated, err)
	}
	if _, _, err := readLogTail(filepath.Join(dir, "missing.log"), 1000); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: err = %v, want fs.ErrNotExist", err)
	}
}

func TestLaunchdField_TopLevelOnly(t *testing.T) {
	printed := "gui/502/x = {\n\tresource coalition = {\n\t\tstate = active\n\t\tlast exit code = 78\n\t}\n\tstate = not running\n\tlast exit code = 0\n}\n"
	if v, _ := launchdField(printed, "state"); v != "not running" {
		t.Errorf("state = %q, want the top-level %q, not a nested block's", v, "not running")
	}
	if v, _ := launchdField(printed, "last exit code"); v != "0" {
		t.Errorf("last exit code = %q, want the top-level %q", v, "0")
	}
	if r := classifyLaunchAgent("com.runecho.freshen", printed, nil); r.Status != OK {
		t.Errorf("a nested block's exit 78 was read as the job's: %q %s", r.Status, r.Detail)
	}
}

// --- classifyLaunchAgent ---

// launchctlFixture is shaped on real `launchctl print gui/<uid>/<label>` output
// captured from the Mac (healthy, 2026-11-06), including a nested block whose
// two-tab `state = active` must not be read as the job's state. topLevel lines
// are inserted one tab deep.
func launchctlFixture(label string, topLevel ...string) string {
	var b strings.Builder
	b.WriteString("gui/502/" + label + " = {\n")
	b.WriteString("\tactive count = 0\n")
	b.WriteString("\tpath = /Users/u/Library/LaunchAgents/" + label + ".plist\n")
	b.WriteString("\ttype = LaunchAgent\n")
	for _, l := range topLevel {
		b.WriteString("\t" + l + "\n")
	}
	b.WriteString("\n\tprogram = /Users/u/.local/bin/runecho-ir\n")
	b.WriteString("\truns = 2\n")
	b.WriteString("\n\tresource coalition = {\n\t\tID = 444030\n\t\ttype = resource\n\t\tstate = active\n\t}\n")
	b.WriteString("\tproperties = inferred program | needs LWCR update | managed LWCR | has LWCR\n")
	b.WriteString("}\n")
	return b.String()
}

const (
	freshenLabel = "com.runecho.freshen"
	reindexLabel = "com.runecho.reindex"
)

// The #553 incident, as `launchctl print` reported it during the outage.
func TestClassifyLaunchAgent_SpawnFailedIsFail(t *testing.T) {
	out := launchctlFixture(freshenLabel, "state = not running", "last exit code = 78: EX_CONFIG", "job state = spawn failed")
	r := classifyLaunchAgent(freshenLabel, out, nil)
	if r.Status != Fail || !strings.Contains(r.Detail, "spawn failed") {
		t.Fatalf("got %q %q, want fail naming spawn failed", r.Status, r.Detail)
	}
	for _, want := range []string{
		"launchctl bootout gui/$(id -u)/com.runecho.freshen",
		"launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.runecho.freshen.plist",
	} {
		if !strings.Contains(r.Remedy, want) {
			t.Errorf("remedy %q missing %q", r.Remedy, want)
		}
	}
}

func TestClassifyLaunchAgent_LaunchdExitCodeIsFail(t *testing.T) {
	for _, label := range []string{reindexLabel, freshenLabel} {
		out := launchctlFixture(label, "state = not running", "last exit code = 78: EX_CONFIG")
		r := classifyLaunchAgent(label, out, nil)
		if r.Status != Fail {
			t.Errorf("%s exit 78 without a spawn-failed state: status = %q, want fail", label, r.Status)
		}
		if !strings.Contains(r.Remedy, "bootstrap") {
			t.Errorf("%s exit 78: remedy %q should reload the agent", label, r.Remedy)
		}
	}
}

// Exit 1 and 2 are codes runecho-ir returns itself: `repo reindex --all` exits 2
// on every healthy tick that meets an enrolled repo whose root is gone
// (cmd/runecho-ir/repo.go runRepoReindexAll), so for the reindex agent they
// must not read as dead. freshen is fail-open and always exits 0.
func TestClassifyLaunchAgent_HealthyIsOK(t *testing.T) {
	for label, codes := range map[string][]string{
		reindexLabel: {"0", "(never exited)", "1", "2"},
		freshenLabel: {"0", "(never exited)"},
	} {
		for _, code := range codes {
			out := launchctlFixture(label, "state = not running", "last exit code = "+code)
			if r := classifyLaunchAgent(label, out, nil); r.Status != OK {
				t.Errorf("%s last exit code = %s: status = %q (%s), want ok", label, code, r.Status, r.Detail)
			}
		}
	}
}

// F1 (#555 review): freshen returns ExitOK on every path, so any nonzero exit
// means the binary could not run it — e.g. one too old to have the freshen
// command exits 2 (unknown command) on every tick. bootout/bootstrap cannot
// fix that, so the remedy must point at rebuilding the binary.
func TestClassifyLaunchAgent_FreshenNonzeroExitIsFail(t *testing.T) {
	for _, code := range []string{"1", "2"} {
		out := launchctlFixture(freshenLabel, "state = not running", "last exit code = "+code)
		r := classifyLaunchAgent(freshenLabel, out, nil)
		if r.Status != Fail {
			t.Errorf("freshen last exit code = %s: status = %q, want fail", code, r.Status)
		}
		if !strings.Contains(r.Remedy, "install.sh") {
			t.Errorf("freshen exit %s: remedy %q should say to rebuild the binary", code, r.Remedy)
		}
	}
}

func TestClassifyLaunchAgent_SpawnFailedWithoutExitLine(t *testing.T) {
	out := launchctlFixture(freshenLabel, "job state = spawn failed")
	r := classifyLaunchAgent(freshenLabel, out, nil)
	if r.Status != Fail || strings.Contains(r.Detail, "last exit code") {
		t.Errorf("got %q %q, want fail that names no absent exit code", r.Status, r.Detail)
	}
}

// Real output for a label launchd does not know (captured 2026-11-06, exit 113).
func TestClassifyLaunchAgent_NotLoadedIsFail(t *testing.T) {
	out := "Bad request.\nCould not find service \"com.runecho.freshen\" in domain for user gui: 502\n"
	r := classifyLaunchAgent(freshenLabel, out, errors.New("exit status 113"))
	if r.Status != Fail || !strings.Contains(r.Remedy, "bootstrap") {
		t.Errorf("got %q remedy %q, want fail with the bootstrap remedy", r.Status, r.Remedy)
	}
}

func TestClassifyLaunchAgent_QueryErrorIsWarn(t *testing.T) {
	r := classifyLaunchAgent(freshenLabel, "", &exec.Error{Name: "launchctl", Err: exec.ErrNotFound})
	if r.Status != Warn {
		t.Errorf("launchctl missing: status = %q, want warn", r.Status)
	}
}

func TestClassifyLaunchAgent_UnrecognisedOutputIsOK(t *testing.T) {
	r := classifyLaunchAgent(freshenLabel, "something else\n", nil)
	if r.Status != OK || !strings.Contains(r.Detail, "not recognised") {
		t.Errorf("got %q %q, want ok 'not recognised' — a macOS format change must not alarm forever", r.Status, r.Detail)
	}
}

// --- periodicLiveness ---

type fakeProbe struct {
	t           *testing.T
	labels      []string
	tailPaths   []string
	noLaunchctl bool
	noTail      bool
}

func (f *fakeProbe) probe(goos string) livenessProbe {
	return livenessProbe{
		goos: goos, now: livenessNow, defaultLog: "/default/reindex.log",
		readTail: func(p string) ([]byte, bool, error) {
			if f.noTail {
				f.t.Errorf("readTail(%q) called; no log should be read", p)
			}
			f.tailPaths = append(f.tailPaths, p)
			return []byte(freshenAt(livenessNow.Add(-10*time.Minute), "ok") + "\n"), false, nil
		},
		launchctl: func(label string) (string, error) {
			if f.noLaunchctl {
				f.t.Errorf("launchctl(%q) called; it must not run here", label)
			}
			f.labels = append(f.labels, label)
			return launchctlFixture(label, "state = not running", "last exit code = 0"), nil
		},
	}
}

func checks(rs []Result) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Check)
	}
	return out
}

const cronBoth = "0 * * * * '/b/runecho-ir' repo reindex --all --prune >>'/a/r.log' 2>&1 # runecho\n" +
	"30 * * * * '/b/runecho-ir' freshen '/g/.git' >>'/a/r.log' 2>&1 # runecho"

func TestPeriodicLiveness_LinuxSkipsLaunchctl(t *testing.T) {
	f := &fakeProbe{t: t, noLaunchctl: true}
	rs := periodicLiveness(cronBoth, "/x/Library/LaunchAgents/com.runecho.reindex.plist", f.probe("linux"))
	if got := checks(rs); !reflect.DeepEqual(got, []string{"periodic freshen"}) {
		t.Errorf("checks = %v, want only periodic freshen on linux", got)
	}
}

func TestPeriodicLiveness_DarwinChecksEachAgent(t *testing.T) {
	f := &fakeProbe{t: t}
	entry := launchdPlistFixture() + "\n" + `<plist><dict><key>ProgramArguments</key><array><string>/b/runecho-ir</string><string>freshen</string><string>/g/.git</string></array>
	<key>StandardOutPath</key><string>/f/freshen.log</string></dict></plist>`
	rs := periodicLiveness(entry, "/Users/u/Library/LaunchAgents/com.runecho.reindex.plist", f.probe("darwin"))
	if want := []string{"com.runecho.reindex", "com.runecho.freshen"}; !reflect.DeepEqual(f.labels, want) {
		t.Errorf("launchctl labels = %v, want %v", f.labels, want)
	}
	if want := []string{"launchd com.runecho.reindex", "launchd com.runecho.freshen", "periodic freshen"}; !reflect.DeepEqual(checks(rs), want) {
		t.Errorf("checks = %v, want %v", checks(rs), want)
	}
	if want := []string{"/f/freshen.log"}; !reflect.DeepEqual(f.tailPaths, want) {
		t.Errorf("read log %v, want the freshen plist's StandardOutPath %v", f.tailPaths, want)
	}
}

func TestPeriodicLiveness_DarwinCrontabSkipsLaunchctl(t *testing.T) {
	f := &fakeProbe{t: t, noLaunchctl: true}
	periodicLiveness(cronBoth, "crontab", f.probe("darwin"))
}

func TestPeriodicLiveness_NoFreshenEntryReadsNoLog(t *testing.T) {
	reindexOnly := "0 * * * * '/b/runecho-ir' repo reindex --all --prune >>'/a/r.log' 2>&1 # runecho"
	f := &fakeProbe{t: t, noTail: true}
	if rs := periodicLiveness(reindexOnly, "crontab", f.probe("linux")); len(rs) != 0 {
		t.Errorf("reindex-only cron: got %v, want no liveness results", checks(rs))
	}
	f = &fakeProbe{t: t, noTail: true}
	rs := periodicLiveness(launchdPlistFixture(), "/Users/u/Library/LaunchAgents/com.runecho.reindex.plist", f.probe("darwin"))
	if want := []string{"launchd com.runecho.reindex"}; !reflect.DeepEqual(checks(rs), want) {
		t.Errorf("reindex-only plist: checks = %v, want %v", checks(rs), want)
	}
}

func TestPeriodicLiveness_FallsBackToDefaultLog(t *testing.T) {
	f := &fakeProbe{t: t}
	periodicLiveness("30 * * * * '/b/runecho-ir' freshen '/g/.git' # runecho", "crontab", f.probe("linux"))
	if want := []string{"/default/reindex.log"}; !reflect.DeepEqual(f.tailPaths, want) {
		t.Errorf("read %v, want the default log %v when the entry names none", f.tailPaths, want)
	}
}

func TestRoughAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                              "<1m",
		20 * time.Second:               "<1m",
		28 * time.Minute:               "28m",
		90 * time.Minute:               "1h30m",
		216*time.Hour + 30*time.Minute: "216h30m",
	} {
		if got := roughAge(d); got != want {
			t.Errorf("roughAge(%v) = %q, want %q", d, got, want)
		}
	}
}

// F8: with no redirect in the entry and no resolvable $RUNECHO_HOME there is no
// log to read; say so rather than warning about a file named "".
func TestPeriodicLiveness_NoLocatableLogIsWarn(t *testing.T) {
	f := &fakeProbe{t: t, noTail: true}
	p := f.probe("linux")
	p.defaultLog = ""
	rs := periodicLiveness("30 * * * * '/b/runecho-ir' freshen '/g/.git' # runecho", "crontab", p)
	r := find(rs, "periodic freshen")
	if r == nil || r.Status != Warn || !strings.Contains(r.Detail, "cannot locate") {
		t.Errorf("got %+v, want a warn saying the log cannot be located", r)
	}
}

// F5: a reindex-only install whose paths merely contain " freshen " has no
// freshen job, so it must get neither a freshen launchd check nor a log check.
func TestPeriodicLiveness_FreshenInPathIsNotAFreshenEntry(t *testing.T) {
	plist := `<plist><dict><key>ProgramArguments</key><array><string>/Users/me/my freshen dir/runecho-ir</string><string>repo</string><string>reindex</string><string>--prune</string></array></dict></plist>`
	f := &fakeProbe{t: t, noTail: true}
	rs := periodicLiveness(plist, "/Users/me/Library/LaunchAgents/com.runecho.reindex.plist", f.probe("darwin"))
	if want := []string{"launchd com.runecho.reindex"}; !reflect.DeepEqual(checks(rs), want) {
		t.Errorf("plist: checks = %v, want %v", checks(rs), want)
	}
	cron := "0 * * * * '/opt/my freshen tools/runecho-ir' repo reindex --all --prune >>'/a/r.log' 2>&1 # runecho"
	f = &fakeProbe{t: t, noTail: true}
	if rs := periodicLiveness(cron, "crontab", f.probe("linux")); len(rs) != 0 {
		t.Errorf("cron: got %v, want no liveness results", checks(rs))
	}
}

// F3: an agent loaded with legacy `launchctl load` from a non-GUI (ssh)
// session lives in user/<uid>, not gui/<uid>. Not found in gui must fall back
// to user before reading as "not loaded".
func TestLaunchctlPrintWith_FallsBackToUserDomain(t *testing.T) {
	notFound := "Bad request.\nCould not find service \"com.runecho.freshen\" in domain for user gui: 502\n"
	healthy := launchctlFixture(freshenLabel, "state = not running", "last exit code = 0")
	var asked []string
	run := func(target string) (string, error) {
		asked = append(asked, target)
		if strings.HasPrefix(target, "gui/") {
			return notFound, errors.New("exit status 113")
		}
		return healthy, nil
	}
	out, err := launchctlPrintWith(run, 502, freshenLabel)
	if err != nil || out != healthy {
		t.Errorf("got (%q, %v), want the user-domain output", out, err)
	}
	if want := []string{"gui/502/com.runecho.freshen", "user/502/com.runecho.freshen"}; !reflect.DeepEqual(asked, want) {
		t.Errorf("asked %v, want %v", asked, want)
	}

	// Found in neither domain: the gui answer stands, so the verdict is "not loaded".
	run = func(target string) (string, error) { return notFound, errors.New("exit status 113") }
	out, err = launchctlPrintWith(run, 502, freshenLabel)
	if r := classifyLaunchAgent(freshenLabel, out, err); r.Status != Fail {
		t.Errorf("not found in either domain: status = %q, want fail", r.Status)
	}

	// A healthy gui answer never asks the user domain.
	asked = nil
	run = func(target string) (string, error) { asked = append(asked, target); return healthy, nil }
	if _, _ = launchctlPrintWith(run, 502, freshenLabel); len(asked) != 1 {
		t.Errorf("asked %v, want only the gui domain", asked)
	}
}

// --- #554: the rotated-log fallback ---

// mapProbe serves fixed tails per path and records what was read.
type mapProbe struct {
	files map[string]string // path -> tail; absent = ErrNotExist
	errs  map[string]error
	read  []string
}

func (m *mapProbe) readTail(p string) ([]byte, bool, error) {
	m.read = append(m.read, p)
	if err, ok := m.errs[p]; ok {
		return nil, false, err
	}
	s, ok := m.files[p]
	if !ok {
		return nil, false, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return []byte(s), false, nil
}

const rotCron = "30 * * * * '/b/runecho-ir' freshen '/g/.git' >>'/a/r.log' 2>&1 # runecho"

func rotLiveness(t *testing.T, m *mapProbe) Result {
	t.Helper()
	p := livenessProbe{goos: "linux", now: livenessNow, readTail: m.readTail,
		launchctl: func(string) (string, error) { t.Error("launchctl called on linux"); return "", nil }}
	r := find(periodicLiveness(rotCron, "crontab", p), "periodic freshen")
	if r == nil {
		t.Fatal("no periodic freshen result")
	}
	return *r
}

func recent(ago time.Duration) string { return freshenAt(livenessNow.Add(-ago), "ok") + "\n" }

// Between the :00 rotation and the :30 freshen spawn there is no active log.
func TestPeriodicLiveness_RotatedFallback_ActiveMissing(t *testing.T) {
	m := &mapProbe{files: map[string]string{"/a/r.log.1": reindexLine + "\n" + recent(40*time.Minute)}}
	r := rotLiveness(t, m)
	if r.Status != OK || !strings.Contains(r.Detail, "/a/r.log.1") {
		t.Errorf("got %q %q, want ok naming the rotated log", r.Status, r.Detail)
	}
	if want := []string{"/a/r.log", "/a/r.log.1"}; !reflect.DeepEqual(m.read, want) {
		t.Errorf("read %v, want %v", m.read, want)
	}
}

func TestPeriodicLiveness_RotatedFallback_ActiveWithoutFreshenLine(t *testing.T) {
	m := &mapProbe{files: map[string]string{
		"/a/r.log":   reindexLine + "\n",
		"/a/r.log.1": recent(50 * time.Minute),
	}}
	if r := rotLiveness(t, m); r.Status != OK || !strings.Contains(r.Detail, "/a/r.log.1") {
		t.Errorf("got %q %q, want ok naming the rotated log", r.Status, r.Detail)
	}
}

func TestPeriodicLiveness_RotatedFallback_NeitherHasLine(t *testing.T) {
	for name, files := range map[string]map[string]string{
		".1 has no line": {"/a/r.log": reindexLine + "\n", "/a/r.log.1": reindexLine + "\n"},
		".1 absent":      {"/a/r.log": reindexLine + "\n"},
	} {
		r := rotLiveness(t, &mapProbe{files: files})
		if r.Status != Warn || !strings.Contains(r.Detail, "no freshen line in /a/r.log —") || strings.Contains(r.Detail, ".1") {
			t.Errorf("%s: got %q %q, want the active log's own warn, not naming .1", name, r.Status, r.Detail)
		}
	}
}

func TestPeriodicLiveness_RotatedFallback_ActiveHasLineSkips1(t *testing.T) {
	m := &mapProbe{files: map[string]string{"/a/r.log": recent(10 * time.Minute), "/a/r.log.1": recent(70 * time.Minute)}}
	r := rotLiveness(t, m)
	if r.Status != OK || strings.Contains(r.Detail, ".1") {
		t.Errorf("got %q %q, want ok naming the active log", r.Status, r.Detail)
	}
	if want := []string{"/a/r.log"}; !reflect.DeepEqual(m.read, want) {
		t.Errorf("read %v, want only the active log", m.read)
	}
}

func TestPeriodicLiveness_RotatedFallback_UnreadableActiveNoFallback(t *testing.T) {
	m := &mapProbe{
		files: map[string]string{"/a/r.log.1": recent(10 * time.Minute)},
		errs:  map[string]error{"/a/r.log": errors.New("permission denied")},
	}
	r := rotLiveness(t, m)
	if r.Status != Warn || !strings.Contains(r.Detail, "cannot read /a/r.log") {
		t.Errorf("got %q %q, want the active log's read error", r.Status, r.Detail)
	}
	if want := []string{"/a/r.log"}; !reflect.DeepEqual(m.read, want) {
		t.Errorf("read %v; an unreadable active log must not fall back", m.read)
	}
}

func TestPeriodicLiveness_RotatedFallback_StaleIn1IsWarn(t *testing.T) {
	m := &mapProbe{files: map[string]string{"/a/r.log.1": recent(5 * time.Hour)}}
	r := rotLiveness(t, m)
	if r.Status != Warn || !strings.Contains(r.Detail, "no freshen run for 5h") || !strings.Contains(r.Detail, "/a/r.log.1") {
		t.Errorf("got %q %q, want a stale warn naming the rotated log", r.Status, r.Detail)
	}
}
