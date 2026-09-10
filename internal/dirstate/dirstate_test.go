package dirstate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestStatClassifies pins the four cases the three old predicates were
// collapsing (#386). The divergence table in the package doc is only true if
// these are actually distinguishable.
func TestStatClassifies(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		want State
	}{
		{"a directory", dir, Present},
		{"nothing there", filepath.Join(dir, "nope"), Absent},
		{"a regular file", file, NotADirectory},
		{"a path under a file", filepath.Join(file, "child"), Unreadable},
	} {
		if got := Stat(tc.path); got != tc.want {
			t.Errorf("%s: Stat = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestPosturesDisagreeAsDocumented is the point of the package: the three
// postures must NOT agree, and this pins exactly where they part company. If a
// future change makes two of these columns identical, one of the three callers
// has silently changed behaviour.
func TestPosturesDisagreeAsDocumented(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(file, "child") // stat fails with ENOTDIR, not ENOENT

	for _, tc := range []struct {
		name                   string
		path                   string
		gone, usable, mayExist bool
	}{
		//                              IsGone  IsUsable  MayExist
		{"directory", dir, false, true, true},
		{"absent", filepath.Join(dir, "nope"), true, false, false},
		{"regular file", file, false, false, false},
		{"unreadable", unreadable, false, false, true},
	} {
		if got := IsGone(tc.path); got != tc.gone {
			t.Errorf("%s: IsGone = %v, want %v", tc.name, got, tc.gone)
		}
		if got := IsUsable(tc.path); got != tc.usable {
			t.Errorf("%s: IsUsable = %v, want %v", tc.name, got, tc.usable)
		}
		if got := MayExist(tc.path); got != tc.mayExist {
			t.Errorf("%s: MayExist = %v, want %v", tc.name, got, tc.mayExist)
		}
	}
}

// TestUnreadableIsNeverGone is the property that keeps `prune-missing --yes`
// from deleting history for an unmounted drive. Only ENOENT is evidence of
// deletion; an EACCES directory must read as not-gone AND as possibly-live.
func TestUnreadableIsNeverGone(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("chmod-based permission denial does not hold for root or on windows")
	}
	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	if Stat(child) != Unreadable {
		t.Fatalf("Stat = %v, want unreadable", Stat(child))
	}
	if IsGone(child) {
		t.Error("IsGone = true on an unreadable path — prune-missing --yes would delete real history")
	}
	if !MayExist(child) {
		t.Error("MayExist = false on an unreadable path — the resolver would skip a live sibling")
	}
	if IsUsable(child) {
		t.Error("IsUsable = true on an unreadable path — the guard would validate against a root it cannot read")
	}
}
