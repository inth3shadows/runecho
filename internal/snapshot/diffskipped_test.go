package snapshot

import (
	"encoding/json"
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
			{Path: "vendor", Reason: ir.SkipIgnoredDir},
		},
		SkippedTruncated: true,
	})
	arr, ok := got["skipped"].([]any)
	if !ok || len(arr) != 2 {
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
		many[i] = ir.SkippedFile{Path: "f.java", Reason: ir.SkipUnsupportedLanguage}
	}
	out := FormatFull(DiffResult{
		SnapshotA:        SnapshotMeta{RootHash: "aaaaaaaaaaaa"},
		SnapshotB:        SnapshotMeta{RootHash: "bbbbbbbbbbbb"},
		SkippedKnown:     true,
		Skipped:          many,
		SkippedTruncated: true,
	})
	if !strings.Contains(out, "and 5 mores") && !strings.Contains(out, "and 5 more") {
		t.Errorf("expected an elision line for the paths beyond %d:\n%s", maxShownSkips, out)
	}
	if !strings.Contains(out, "WARNING") {
		t.Errorf("a truncated skip list must warn, got:\n%s", out)
	}
}
