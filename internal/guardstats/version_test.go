package guardstats

import (
	"strings"
	"testing"
)

// askGV is ask() with a guard-version stamp.
func askGV(gv, reason, lang, repo, file string, mins int, syms ...string) Decision {
	d := ask(reason, lang, repo, file, mins, syms...)
	d.GV = gv
	return d
}

func outcomeGV(gv, file string, mins int, syms ...string) Decision {
	d := outcome(file, mins, syms...)
	d.GV = gv
	return d
}

// The regression this whole feature exists for: two builds in one window, each
// with a very different approval rate, pooled into a single headline number that
// describes neither. Measured on the real log at 70% vs 19% (#207).
func TestFPReport_ByVersionSeparatesBuilds(t *testing.T) {
	decs := []Decision{
		// old build: 2 asks, both approved → 100%
		askGV("v0.6.1", "violations", "py", "r1", "a.py", 0, "foo"),
		outcomeGV("v0.6.1", "a.py", 1, "foo"),
		askGV("v0.6.1", "violations", "py", "r1", "b.py", 2, "bar"),
		outcomeGV("v0.6.1", "b.py", 3, "bar"),
		// new build: 2 asks, neither approved → 0%
		askGV("v0.12.0", "violations", "py", "r1", "c.py", 10, "baz"),
		askGV("v0.12.0", "violations", "py", "r1", "d.py", 12, "qux"),
	}
	s := FPReport(decs, ts(-1000), 10)

	if !s.MixedVersions() {
		t.Fatal("MixedVersions() = false, want true for a two-build window")
	}
	if got := s.ByVersion["v0.6.1"]; got.Asks != 2 || got.Approved != 2 {
		t.Errorf("v0.6.1 bucket = %+v, want 2 asks / 2 approved", got)
	}
	if got := s.ByVersion["v0.12.0"]; got.Asks != 2 || got.Approved != 0 {
		t.Errorf("v0.12.0 bucket = %+v, want 2 asks / 0 approved", got)
	}
	// The pooled rate (50%) describes neither build — that is the point.
	if got := s.Window.Rate(); got != 0.5 {
		t.Errorf("pooled rate = %.2f, want 0.50", got)
	}
}

func TestFPReport_UnstampedRecordsBucketAsUnknown(t *testing.T) {
	decs := []Decision{
		ask("violations", "py", "r1", "a.py", 0, "foo"), // no GV — pre-#207 record
		askGV("v0.12.0", "violations", "py", "r1", "b.py", 2, "bar"),
	}
	s := FPReport(decs, ts(-1000), 10)

	if got := s.ByVersion[UnknownVersion].Asks; got != 1 {
		t.Errorf("unknown bucket asks = %d, want 1", got)
	}
	if got := s.ByVersion["v0.12.0"].Asks; got != 1 {
		t.Errorf("v0.12.0 bucket asks = %d, want 1", got)
	}
	if !s.MixedVersions() {
		t.Error("unknown + a stamped version is still a mixed window")
	}
}

func TestFPReport_SingleVersionIsNotMixed(t *testing.T) {
	decs := []Decision{
		askGV("v0.12.0", "violations", "py", "r1", "a.py", 0, "foo"),
		askGV("v0.12.0", "violations", "py", "r1", "b.py", 2, "bar"),
	}
	s := FPReport(decs, ts(-1000), 10)
	if s.MixedVersions() {
		t.Errorf("MixedVersions() = true for a single-build window (%+v)", s.ByVersion)
	}
}

// An empty window must not be reported as mixed — len(ByVersion) is 0, not 1.
func TestFPReport_EmptyWindowIsNotMixed(t *testing.T) {
	s := FPReport(nil, ts(0), 10)
	if s.MixedVersions() {
		t.Error("MixedVersions() = true for an empty window")
	}
	if len(s.ByVersion) != 0 {
		t.Errorf("ByVersion = %+v, want empty", s.ByVersion)
	}
}

func TestFilterVersion(t *testing.T) {
	decs := []Decision{
		askGV("v0.6.1", "violations", "py", "r1", "a.py", 0, "foo"),
		askGV("v0.12.0", "violations", "py", "r1", "b.py", 2, "bar"),
		ask("violations", "py", "r1", "c.py", 4, "baz"), // unstamped
	}

	if got := len(FilterVersion(decs, "")); got != 3 {
		t.Errorf("empty gv filtered %d records, want a no-op (3)", got)
	}
	if got := FilterVersion(decs, "v0.12.0"); len(got) != 1 || got[0].File != "b.py" {
		t.Errorf("gv=v0.12.0 selected %+v, want only b.py", got)
	}
	if got := FilterVersion(decs, UnknownVersion); len(got) != 1 || got[0].File != "c.py" {
		t.Errorf("gv=unknown selected %+v, want only the unstamped c.py", got)
	}
	if got := FilterVersion(decs, "v9.9.9"); len(got) != 0 {
		t.Errorf("gv with no matches selected %+v, want none", got)
	}
}

// Filtering must drop an ask's outcome too, so a filtered report cannot join an
// ask from one build to an approval recorded by another.
func TestFilterVersion_KeepsAskOutcomePairsIntact(t *testing.T) {
	decs := []Decision{
		askGV("v0.12.0", "violations", "py", "r1", "a.py", 0, "foo"),
		outcomeGV("v0.12.0", "a.py", 1, "foo"),
		askGV("v0.6.1", "violations", "py", "r1", "b.py", 2, "bar"),
		outcomeGV("v0.6.1", "b.py", 3, "bar"),
	}
	s := FPReport(FilterVersion(decs, "v0.12.0"), ts(-1000), 10)
	if s.Window.Asks != 1 || s.Window.Approved != 1 {
		t.Fatalf("filtered report = %d asks / %d approved, want 1/1", s.Window.Asks, s.Window.Approved)
	}
	if s.MixedVersions() {
		t.Error("a version-filtered report must never be mixed")
	}
}

// The warning has to land above the breakdown tables: a reader who stops at the
// first number must still learn that the number is pooled.
func TestFormatFP_MixedVersionWarningPrecedesTables(t *testing.T) {
	decs := []Decision{
		askGV("v0.6.1", "violations", "py", "r1", "a.py", 0, "foo"),
		askGV("v0.12.0", "violations", "py", "r1", "b.py", 2, "bar"),
	}
	out := FormatFP(FPReport(decs, ts(-1000), 10))

	warn := strings.Index(out, "MIXED GUARD VERSIONS")
	if warn < 0 {
		t.Fatalf("no mixed-version warning in report:\n%s", out)
	}
	byCheck := strings.Index(out, "By check (reason):")
	if byCheck < 0 || warn > byCheck {
		t.Errorf("warning at %d must precede the by-check table at %d:\n%s", warn, byCheck, out)
	}
	if !strings.Contains(out, "--gv=") {
		t.Error("warning should name the flag that fixes it")
	}
	for _, want := range []string{"By guard version:", "v0.6.1", "v0.12.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

func TestFormatFP_NoWarningOnSingleVersion(t *testing.T) {
	decs := []Decision{askGV("v0.12.0", "violations", "py", "r1", "a.py", 0, "foo")}
	out := FormatFP(FPReport(decs, ts(-1000), 10))
	if strings.Contains(out, "MIXED GUARD VERSIONS") {
		t.Errorf("unexpected mixed-version warning:\n%s", out)
	}
	// The per-version table still renders — it is how you learn which build
	// produced the number, not just that more than one did.
	if !strings.Contains(out, "By guard version:") {
		t.Errorf("single-version report should still show the version table:\n%s", out)
	}
}

func TestPayloadFP_CarriesVersionBreakdown(t *testing.T) {
	decs := []Decision{
		askGV("v0.6.1", "violations", "py", "r1", "a.py", 0, "foo"),
		askGV("v0.12.0", "violations", "py", "r1", "b.py", 2, "bar"),
	}
	p := PayloadFP(FPReport(decs, ts(-1000), 10))

	if mixed, ok := p["mixed_versions"].(bool); !ok || !mixed {
		t.Errorf("mixed_versions = %v, want true", p["mixed_versions"])
	}
	versions, ok := p["by_version"].([]map[string]any)
	if !ok || len(versions) != 2 {
		t.Fatalf("by_version = %+v, want 2 entries", p["by_version"])
	}
}
