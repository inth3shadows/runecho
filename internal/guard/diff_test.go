package guard

import (
	"testing"
)

func TestParseDiffOutput_SingleFile(t *testing.T) {
	raw := `diff --git a/foo.go b/foo.go
index abc..def 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,5 @@
 package main
+
+func Hello() {
+	fmt.Println("hello")
+}
`
	diffs, err := parseDiffOutput(raw)
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 FileDiff, got %d", len(diffs))
	}
	if diffs[0].Path != "foo.go" {
		t.Errorf("path = %q", diffs[0].Path)
	}
	if len(diffs[0].AddedLines) != 4 {
		t.Errorf("expected 4 added lines, got %d: %v", len(diffs[0].AddedLines), diffs[0].AddedLines)
	}
	// Line numbers: hunk @@ -1,3 +1,5 @@ → new starts at 1; context 'package main' advances to 2;
	// then '+' lines at 2,3,4,5.
	if diffs[0].AddedLines[0].LineNo != 2 {
		t.Errorf("first added line no = %d, want 2", diffs[0].AddedLines[0].LineNo)
	}
}

func TestParseDiffOutput_MultiFile(t *testing.T) {
	raw := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -0,0 +1,2 @@
+package a
+func A() {}
diff --git a/b.py b/b.py
--- a/b.py
+++ b/b.py
@@ -0,0 +1 @@
+def b(): pass
`
	diffs, err := parseDiffOutput(raw)
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if len(diffs) != 2 {
		t.Fatalf("expected 2 FileDiffs, got %d", len(diffs))
	}
	if diffs[0].Path != "a.go" || diffs[1].Path != "b.py" {
		t.Errorf("paths = %q %q", diffs[0].Path, diffs[1].Path)
	}
	if len(diffs[0].AddedLines) != 2 {
		t.Errorf("a.go: expected 2 added lines, got %d", len(diffs[0].AddedLines))
	}
	if len(diffs[1].AddedLines) != 1 {
		t.Errorf("b.py: expected 1 added line, got %d", len(diffs[1].AddedLines))
	}
}

func TestParseDiffOutput_Empty(t *testing.T) {
	diffs, err := parseDiffOutput("")
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if len(diffs) != 0 {
		t.Errorf("expected empty result for empty diff, got %v", diffs)
	}
}

func TestParseDiffOutput_RemovedLinesOnly(t *testing.T) {
	raw := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,3 +1,0 @@
-package main
-func Old() {}
-// comment
`
	diffs, err := parseDiffOutput(raw)
	if err != nil {
		t.Fatalf("parseDiffOutput: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("expected 1 FileDiff")
	}
	if len(diffs[0].AddedLines) != 0 {
		t.Errorf("expected 0 added lines for remove-only diff, got %d", len(diffs[0].AddedLines))
	}
}
