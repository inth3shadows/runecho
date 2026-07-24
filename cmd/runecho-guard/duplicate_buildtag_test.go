package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/ir"
)

func TestGoBuildTagged(t *testing.T) {
	for _, tt := range []struct {
		name string
		src  string
		want bool
	}{
		{"modern constraint", "//go:build unix\n\npackage store\n\nfunc F() {}\n", true},
		{"negated constraint", "//go:build !unix\n\npackage store\n", true},
		{"legacy constraint", "// +build linux\n\npackage store\n", true},
		{"license header then constraint", "// Copyright 2026\n// SPDX-License-Identifier: MIT\n\n//go:build unix\n\npackage store\n", true},
		{"unconstrained", "// Package store does things.\npackage store\n\nfunc F() {}\n", false},
		{"no package clause, no constraint", "func F() {}\n", false},
		{"empty", "", false},
		// The scan must stop at the package clause: the go tool ignores
		// constraints below it, so a test fixture that embeds one in a string
		// literal is not a constrained file.
		{"constraint below package clause is not a constraint", "package guard_test\n\nconst fixture = `//go:build unix`\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := goBuildTagged(tt.src); got != tt.want {
				t.Errorf("goBuildTagged = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGoFilenameConstrained(t *testing.T) {
	for _, tt := range []struct {
		path string
		want bool
	}{
		{"internal/store/lock_windows.go", true},
		{"internal/store/lock_linux.go", true},
		{"internal/store/lock_arm64.go", true},
		{"internal/store/lock_linux_amd64.go", true},
		{"internal/store/lock_js.go", true}, // js is a GOOS
		{"internal/store/lock_unix.go", false},
		{"internal/store/lock_other.go", false},
		{"internal/store/lock.go", false},
		// go/build does not treat a file whose entire name is the suffix as
		// constrained — there is no leading field for the suffix to qualify.
		{"internal/store/windows.go", false},
		{filepath.FromSlash("internal/store/lock_windows.go"), true},
	} {
		t.Run(tt.path, func(t *testing.T) {
			if got := goFilenameConstrained(tt.path); got != tt.want {
				t.Errorf("goFilenameConstrained(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// goBuildConstrained must consult BOTH sides of the edit. An Edit hunk carries
// no file header, so only the pre-edit text can reveal an existing constraint;
// a newly created file has no pre-edit text, so only the new text can.
func TestGoBuildConstrained_BothEditSides(t *testing.T) {
	const tagged = "//go:build !unix\n\npackage store\n"
	const plain = "package store\n"

	if !goBuildConstrained("/r/internal/store/lock_other.go", tagged, "func WithFileLock() {}") {
		t.Error("existing constraint in the pre-edit file must count (Edit hunk case)")
	}
	if !goBuildConstrained("/r/internal/store/lock_other.go", "", tagged) {
		t.Error("constraint in the new text must count (file-creation case)")
	}
	if goBuildConstrained("/r/internal/store/lock.go", plain, plain) {
		t.Error("an unconstrained file must not read as constrained")
	}
	if !goBuildConstrained("/r/internal/store/lock_windows.go", plain, plain) {
		t.Error("an implicit filename constraint must count on its own")
	}
}

// Regression for the only live Go duplicate-symbol ask on record: RunEcho's own
// internal/store/lock_unix.go (//go:build unix) and lock_other.go (//go:build
// !unix) both define WithFileLock. That is a complementary pair, not a
// collision — the compiler never sees both — yet the check flagged it twice and
// the agent approved both times.
func TestDuplicate_ComplementaryBuildTagsDefer(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, map[string]ir.FileIR{
		"internal/store/lock_unix.go":  {Hash: "h1", Symbols: funcsToSymbols([]string{"WithFileLock"})},
		"internal/store/lock_other.go": {Hash: "h2", Symbols: funcsToSymbols([]string{"Placeholder"})},
	})
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	dir := filepath.Join(top, "internal", "store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The candidate side must exist on disk — its constraint lives in the file
	// header, which no index records.
	if err := os.WriteFile(filepath.Join(dir, "lock_unix.go"),
		[]byte("//go:build unix\n\npackage store\n\nfunc WithFileLock() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "lock_other.go")
	if err := os.WriteFile(file,
		[]byte("//go:build !unix\n\npackage store\n\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	in := payloadOld(t, "Edit", file, "func Placeholder() {}",
		"func Placeholder() {}\nfunc WithFileLock() {}", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec == "ask" {
		t.Fatalf("complementary build constraints are not a collision:\n%s", d.Hook.PermissionReason)
	}
}

// Control: the suppression is both-constrained only. An UNconstrained sibling
// really does collide with a constrained file whenever its constraint holds, so
// that pair must still ask. Without this, the fix above could silently degrade
// into "any constrained file disables the check".
func TestDuplicate_ConstrainedVsUnconstrainedStillAsks(t *testing.T) {
	repoRoot := t.TempDir()
	gitInit(t, repoRoot)
	top := enrolledStoreWithFiles(t, repoRoot, map[string]ir.FileIR{
		"internal/store/lock.go":       {Hash: "h1", Symbols: funcsToSymbols([]string{"WithFileLock"})},
		"internal/store/lock_other.go": {Hash: "h2", Symbols: funcsToSymbols([]string{"Placeholder"})},
	})
	t.Setenv("RUNECHO_GUARD_DUPLICATE", "1")

	dir := filepath.Join(top, "internal", "store")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lock.go"),
		[]byte("package store\n\nfunc WithFileLock() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "lock_other.go")
	if err := os.WriteFile(file,
		[]byte("//go:build !unix\n\npackage store\n\nfunc Placeholder() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	in := payloadOld(t, "Edit", file, "func Placeholder() {}",
		"func Placeholder() {}\nfunc WithFileLock() {}", "", nil)
	_, _, d := runHook(t, in)

	if d.Hook.PermissionDec != "ask" {
		t.Fatalf("a constrained file colliding with an unconstrained one must still ask, got %q",
			d.Hook.PermissionDec)
	}
	if !strings.Contains(d.Hook.PermissionReason, "WithFileLock") {
		t.Errorf("reason should name the colliding symbol:\n%s", d.Hook.PermissionReason)
	}
}

// An unreadable candidate must not masquerade as a definite "constrained".
// The candidate path comes from the snapshot index, so it can name a file that
// has since been deleted; silently treating that as a complementary build pair
// would swallow a real duplicate warning with no trace. dropConstrained reports
// it, and checkDuplicateDefs folds it into queryErrs so the hook's degraded
// accounting sees it (#138: a suppressed check must never be indistinguishable
// from a clean pass).
func TestGoFileConstrained_UnreadableIsUnknownNotConstrained(t *testing.T) {
	top := t.TempDir()

	// Present and unconstrained.
	plain := filepath.Join(top, "lock.go")
	if err := os.WriteFile(plain, []byte("package store\n\nfunc F() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if constrained, known := goFileConstrained(top, "lock.go"); constrained || !known {
		t.Errorf("readable unconstrained file = (%v, %v), want (false, true)", constrained, known)
	}

	// Present and constrained by header.
	if err := os.WriteFile(filepath.Join(top, "lock_other.go"),
		[]byte("//go:build !unix\n\npackage store\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if constrained, known := goFileConstrained(top, "lock_other.go"); !constrained || !known {
		t.Errorf("tagged file = (%v, %v), want (true, true)", constrained, known)
	}

	// Absent: unknown, NOT constrained.
	if constrained, known := goFileConstrained(top, "vanished.go"); constrained || known {
		t.Errorf("missing file = (%v, %v), want (false, false)", constrained, known)
	}

	// A filename constraint needs no read at all, so it stays knowable even when
	// the file is gone — that is what keeps the common _windows.go case free.
	if constrained, known := goFileConstrained(top, "lock_windows.go"); !constrained || !known {
		t.Errorf("missing but name-constrained = (%v, %v), want (true, true)", constrained, known)
	}

	kept, unknown := dropConstrained(top, []string{"lock.go", "lock_other.go", "vanished.go"})
	if len(kept) != 1 || kept[0] != "lock.go" {
		t.Errorf("kept = %v, want [lock.go]", kept)
	}
	if unknown != 1 {
		t.Errorf("unknown = %d, want 1 — an unreadable candidate must be counted, not silently dropped", unknown)
	}
}
