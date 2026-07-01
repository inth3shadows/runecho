package snapshot

import "testing"

// TestSaveSnapshot_RejectsAutoLabel: the "auto" label is reserved for the E6
// rolling snapshot. A user-facing SaveSnapshot must reject it so a manual
// snapshot can't be silently deleted by the next auto-roll (or masquerade as the
// guard's freshness baseline). RollAutoSnapshot must still own the label.
func TestSaveSnapshot_RejectsAutoLabel(t *testing.T) {
	db, _ := openTemp(t)
	id, err := db.EnrollRepo("r", "/tmp/r", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SaveSnapshot(id, "sess", autoSnapshotLabel, "/tmp/r", makeIR("h1", "Foo")); err == nil {
		t.Fatalf("SaveSnapshot with reserved label %q should error, got nil", autoSnapshotLabel)
	}
	// A non-reserved label still works.
	if _, err := db.SaveSnapshot(id, "sess", "manual", "/tmp/r", makeIR("h2", "Bar")); err != nil {
		t.Fatalf("SaveSnapshot with normal label: %v", err)
	}
	// The internal roll path may still use the reserved label.
	if _, err := db.RollAutoSnapshot(id, "", "/tmp/r", makeIR("a1", "Foo")); err != nil {
		t.Fatalf("RollAutoSnapshot: %v", err)
	}
	if n := countLabel(t, db, id, autoSnapshotLabel); n != 1 {
		t.Fatalf("want exactly 1 auto snapshot, got %d", n)
	}
}
