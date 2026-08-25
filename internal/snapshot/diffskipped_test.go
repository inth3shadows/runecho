package snapshot

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/ir"
)

func payloadOf(t *testing.T, d DiffResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(DiffPayload(d))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return got
}

// TestDiffPayload_SkippedOmittedWhenUnmeasured is the load-bearing half of the
// contract. A snapshot-to-snapshot diff never walks the filesystem, so it cannot
// know what the indexer declined. Emitting `"skipped": []` there would tell a
// consumer "nothing was skipped" purely on the strength of never having looked —
// the same fail-open shape as an empty `removed` list standing in for a guard
// that never ran. The key must be ABSENT, so absent can mean "unknown".
func TestDiffPayload_SkippedOmittedWhenUnmeasured(t *testing.T) {
	got := payloadOf(t, DiffResult{
		SnapshotA: SnapshotMeta{RootHash: "aaaaaaaaaaaa"},
		SnapshotB: SnapshotMeta{RootHash: "bbbbbbbbbbbb"},
		// SkippedKnown deliberately false: this is the two-ID mode.
	})
	if _, present := got["skipped"]; present {
		t.Error("`skipped` present on a diff that never walked the filesystem — " +
			"absence is the only honest answer for 'we could not measure it'")
	}
	if _, present := got["skipped_truncated"]; present {
		t.Error("`skipped_truncated` present without `skipped`")
	}
	if _, present := got["ignored_paths"]; present {
		t.Error("`ignored_paths` present on a diff that never walked the filesystem")
	}
}

// TestDiffPayload_SkippedEmptyWhenMeasuredAndClean: measured-and-none is a
// different fact from not-measured, and must serialize differently. An empty
// array, never null — a machine consumer must not have to null-guard.
func TestDiffPayload_SkippedEmptyWhenMeasuredAndClean(t *testing.T) {
	got := payloadOf(t, DiffResult{
		SnapshotA:    SnapshotMeta{RootHash: "aaaaaaaaaaaa"},
		SnapshotB:    SnapshotMeta{RootHash: "bbbbbbbbbbbb"},
		SkippedKnown: true,
	})
	arr, ok := got["skipped"].([]any)
	if !ok {
		t.Fatalf("`skipped` should be an array, got %T (%v)", got["skipped"], got["skipped"])
	}
	if len(arr) != 0 {
		t.Errorf("expected an empty array, got %v", arr)
	}
	if got["skipped_truncated"] != false {
		t.Errorf("skipped_truncated = %v, want false", got["skipped_truncated"])
	}
}

// TestDiffPayload_SkippedEntryKeys pins the key casing a consumer parses.
// Untagged Go field names, matching the sibling `files` array in the same
// payload — a lone snake_case array beside Go-cased ones is a trap.
func TestDiffPayload_SkippedEntryKeys(t *testing.T) {
	got := payloadOf(t, DiffResult{
		SnapshotA:    SnapshotMeta{RootHash: "aaaaaaaaaaaa"},
		SnapshotB:    SnapshotMeta{RootHash: "bbbbbbbbbbbb"},
		SkippedKnown: true,
		Skipped: []ir.SkippedFile{
			{Path: "Widget.java", Reason: ir.SkipUnsupportedLanguage},
		},
		SkippedTruncated: true,
	})
	arr, ok := got["skipped"].([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("skipped shape wrong: %v", got["skipped"])
	}
	first := arr[0].(map[string]any)
	for _, key := range []string{"Path", "Reason"} {
		if _, ok := first[key]; !ok {
			t.Errorf("missing key %q in a skipped entry: %v", key, first)
		}
	}
	if first["Path"] != "Widget.java" || first["Reason"] != ir.SkipUnsupportedLanguage {
		t.Errorf("entry contents wrong: %v", first)
	}
	if got["skipped_truncated"] != true {
		t.Errorf("skipped_truncated = %v, want true", got["skipped_truncated"])
	}
}

// TestDiffPayload_PolicySkipsGoToTheirOwnArray is the regression test for the
// defect review found in the first cut.
//
// `.git` is in DefaultIgnoredPaths, so a merged list was non-empty in EVERY
// repo. A consumer following the documented prefix rule then blocked an edit to
// `testdata/README.md` — a documentation-only false block, which is exactly what
// the language table was built to prevent, arriving through the other reason
// code.
//
// The split is by "did the operator ask for this", NOT by file-versus-directory.
// An earlier cut used the latter, which filed `symlink_dir` and `unreadable_dir`
// as informational — so a directory the walk could not read, whose whole subtree
// went unrecorded because nothing ever saw it, was reported in the array
// consumers are told to ignore.
func TestDiffPayload_PolicySkipsGoToTheirOwnArray(t *testing.T) {
	got := payloadOf(t, DiffResult{
		SnapshotA:    SnapshotMeta{RootHash: "aaaaaaaaaaaa"},
		SnapshotB:    SnapshotMeta{RootHash: "bbbbbbbbbbbb"},
		SkippedKnown: true,
		Skipped: []ir.SkippedFile{
			{Path: ".git", Reason: ir.SkipIgnoredDir},
			{Path: "Widget.java", Reason: ir.SkipUnsupportedLanguage},
			{Path: "locked", Reason: ir.SkipUnreadableDir},
			{Path: "shared", Reason: ir.SkipSymlinkDir},
			{Path: "testdata", Reason: ir.SkipIgnoredDir},
		},
	})
	skipped, _ := got["skipped"].([]any)
	wantCapability := map[string]bool{"Widget.java": true, "locked": true, "shared": true}
	if len(skipped) != len(wantCapability) {
		t.Fatalf("`skipped` must carry every blind spot, including unreadable and "+
			"unfollowable DIRECTORIES; got %v", skipped)
	}
	for _, e := range skipped {
		if !wantCapability[e.(map[string]any)["Path"].(string)] {
			t.Errorf("unexpected entry in `skipped`: %v", e)
		}
	}
	ignored, _ := got["ignored_paths"].([]any)
	if len(ignored) != 2 {
		t.Errorf("`ignored_paths` must carry ONLY the operator's configured ignores, got %v", ignored)
	}
}

// TestDiffPayload_BothArraysAreEmptyNotNull: a machine consumer must never have
// to null-guard either one.
func TestDiffPayload_BothArraysAreEmptyNotNull(t *testing.T) {
	got := payloadOf(t, DiffResult{
		SnapshotA:    SnapshotMeta{RootHash: "aaaaaaaaaaaa"},
		SnapshotB:    SnapshotMeta{RootHash: "bbbbbbbbbbbb"},
		SkippedKnown: true,
	})
	for _, key := range []string{"skipped", "ignored_paths"} {
		arr, ok := got[key].([]any)
		if !ok {
			t.Errorf("%s should be an array, got %T (%v)", key, got[key], got[key])
			continue
		}
		if len(arr) != 0 {
			t.Errorf("%s should be empty, got %v", key, arr)
		}
	}
}

// TestTruncateSkips_CopiesAndFlags. TruncateSkips must not hand back a re-slice
// of the recorder's backing array presented as complete — "absence means
// indexed" is the fail-open this whole feature closes.
func TestTruncateSkips_CopiesAndFlags(t *testing.T) {
	original := []ir.SkippedFile{
		{Path: "a.java", Reason: ir.SkipUnsupportedLanguage},
		{Path: "b.java", Reason: ir.SkipUnsupportedLanguage},
		{Path: "c.java", Reason: ir.SkipUnsupportedLanguage},
	}
	d := TruncateSkips(DiffResult{SkippedKnown: true, Skipped: original}, 2)
	if len(d.Skipped) != 2 {
		t.Fatalf("len = %d, want 2", len(d.Skipped))
	}
	if !d.SkippedTruncated {
		t.Error("truncation must be flagged, or the clip silently asserts completeness")
	}
	d.Skipped[0].Path = "mutated"
	if original[0].Path != "a.java" {
		t.Error("TruncateSkips aliased the caller's slice")
	}
	// Under the limit: untouched, and NOT flagged.
	same := TruncateSkips(DiffResult{SkippedKnown: true, Skipped: original}, 10)
	if same.SkippedTruncated || len(same.Skipped) != 3 {
		t.Errorf("a list under the limit must pass through unflagged, got %+v", same)
	}
	// Not measured: nothing to clip, and the flag must stay off.
	unknown := TruncateSkips(DiffResult{Skipped: original}, 1)
	if unknown.SkippedTruncated {
		t.Error("an unmeasured diff must not be flagged as truncated")
	}
}

// TestFormatFull_ZeroDriftStillNamesSkips is the human-output half of the same
// point. "No structural changes." is the exact sentence a Java repo gets today
// while nothing in it was ever indexed; unqualified, it is the failure this
// feature exists to end.
func TestFormatFull_ZeroDriftStillNamesSkips(t *testing.T) {
	out := FormatFull(DiffResult{
		SnapshotA:    SnapshotMeta{RootHash: "aaaaaaaaaaaa"},
		SnapshotB:    SnapshotMeta{RootHash: "bbbbbbbbbbbb"},
		SkippedKnown: true,
		Skipped:      []ir.SkippedFile{{Path: "Widget.java", Reason: ir.SkipUnsupportedLanguage}},
	})
	if !strings.Contains(out, "No structural changes.") {
		t.Fatalf("expected the zero-drift branch, got:\n%s", out)
	}
	if !strings.Contains(out, "NOT EXAMINED") || !strings.Contains(out, "Widget.java") {
		t.Errorf("zero-drift output must still name what went unexamined, got:\n%s", out)
	}
}

func TestFormatFull_SaysNothingWhenNothingSkipped(t *testing.T) {
	out := FormatFull(DiffResult{
		SnapshotA:    SnapshotMeta{RootHash: "aaaaaaaaaaaa"},
		SnapshotB:    SnapshotMeta{RootHash: "bbbbbbbbbbbb"},
		SkippedKnown: true,
	})
	if strings.Contains(out, "NOT EXAMINED") {
		t.Errorf("no noise on the clean path — the note must mean something when it appears:\n%s", out)
	}
}

// TestFormatFull_TruncationIsShouted: a capped list read as a complete one is
// the fail-open; the human surface must say so too.
func TestFormatFull_TruncationIsShouted(t *testing.T) {
	many := make([]ir.SkippedFile, maxShownSkips+5)
	for i := range many {
		many[i] = ir.SkippedFile{Path: fmt.Sprintf("f%d.java", i), Reason: ir.SkipUnsupportedLanguage}
	}
	out := FormatFull(DiffResult{
		SnapshotA:        SnapshotMeta{RootHash: "aaaaaaaaaaaa"},
		SnapshotB:        SnapshotMeta{RootHash: "bbbbbbbbbbbb"},
		SkippedKnown:     true,
		Skipped:          many,
		SkippedTruncated: true,
	})
	// Exact, not a Contains pair whose second clause subsumes the first — the
	// earlier version of this assertion passed for "and 5 mores" and pinned
	// neither spelling.
	if !strings.Contains(out, "... and 5 more (use --json for the full list)") {
		t.Errorf("expected an elision line for the paths beyond %d:\n%s", maxShownSkips, out)
	}
	if strings.Contains(out, "mores") {
		t.Errorf(`plural() appended an "s" to "more":\n%s`, out)
	}
	// A truncated list is a floor, not a total; "25 paths" would contradict the
	// warning two lines down.
	if !strings.Contains(out, "(25+ paths)") {
		t.Errorf("a truncated count must read as a floor:\n%s", out)
	}
	if !strings.Contains(out, "WARNING") {
		t.Errorf("a truncated skip list must warn, got:\n%s", out)
	}
}

// TestFormatFull_ConfiguredIgnoresAreNotHumanNoise. `.git` is in
// DefaultIgnoredPaths, so echoing configured ignores into the text output would
// print a block on every diff of every repo — and a note that fires every time
// stops being read. Everything the operator did NOT configure is a capability
// skip now, so it shows in NOT EXAMINED rather than needing a second block.
func TestFormatFull_ConfiguredIgnoresAreNotHumanNoise(t *testing.T) {
	base := DiffResult{
		SnapshotA:    SnapshotMeta{RootHash: "aaaaaaaaaaaa"},
		SnapshotB:    SnapshotMeta{RootHash: "bbbbbbbbbbbb"},
		SkippedKnown: true,
	}
	base.Skipped = []ir.SkippedFile{{Path: ".git", Reason: ir.SkipIgnoredDir}}
	if out := FormatFull(base); strings.Contains(out, "NOT EXAMINED") {
		t.Errorf("a configured ignore must not print:\n%s", out)
	}
	for _, reason := range []string{ir.SkipSymlinkDir, ir.SkipUnreadableDir} {
		base.Skipped = []ir.SkippedFile{{Path: "shared", Reason: reason}}
		out := FormatFull(base)
		if !strings.Contains(out, "NOT EXAMINED") || !strings.Contains(out, "shared") {
			t.Errorf("an unconfigured prune (%s) must print:\n%s", reason, out)
		}
	}
}

// TestFormatTrail_NamesWhatWentUnexamined.
//
// 97643cf added an ir.Stats parameter to TruthTrail and updated four test call
// sites to pass it, with a doc comment claiming the receipt "can name what the
// indexer never read". FormatTrail never referenced Diff.Skipped, so the
// plumbing was inert: `truth-trail` on a Java-only repo printed "No structural
// changes since <label>." at exit 0 while `verify` on the same tree named
// Widget.java. A receipt is read as a settled account, which makes it the worst
// surface to leave unqualified.
func TestFormatTrail_NamesWhatWentUnexamined(t *testing.T) {
	skipped := []ir.SkippedFile{{Path: "Widget.java", Reason: ir.SkipUnsupportedLanguage}}

	// The zero-drift branch: the one that returns early.
	quiet := FormatTrail(TrailResult{
		SnapshotRef: SnapshotMeta{Label: "session-start"},
		Diff:        DiffResult{SkippedKnown: true, Skipped: skipped},
	})
	if !strings.Contains(quiet, "No structural changes") {
		t.Fatalf("expected the zero-drift branch, got:\n%s", quiet)
	}
	if !strings.Contains(quiet, "NOT EXAMINED") || !strings.Contains(quiet, "Widget.java") {
		t.Errorf("a receipt reporting no changes must say what it did not read:\n%s", quiet)
	}

	// And the branch that does report drift.
	noisy := FormatTrail(TrailResult{
		SnapshotRef: SnapshotMeta{Label: "session-start"},
		Diff: DiffResult{
			SkippedKnown: true,
			Skipped:      skipped,
			Files:        []FileDiff{{Path: "main.go", Status: "modified"}},
		},
	})
	if !strings.Contains(noisy, "NOT EXAMINED") || !strings.Contains(noisy, "Widget.java") {
		t.Errorf("the drift branch must name it too:\n%s", noisy)
	}
}
