package parser

import (
	"strings"
	"testing"
	"time"
)

func TestExceedsNestDepth(t *testing.T) {
	cases := []struct {
		n    int
		want bool
	}{
		{0, false},
		{10, false},
		{maxParseNestDepth, false},    // exactly at the cap is allowed
		{maxParseNestDepth + 1, true}, // one past trips it
		{maxParseNestDepth * 2, true},
	}
	for _, c := range cases {
		src := []byte(strings.Repeat("[", c.n) + strings.Repeat("]", c.n))
		if got := exceedsNestDepth(src); got != c.want {
			t.Errorf("exceedsNestDepth(depth=%d) = %v, want %v", c.n, got, c.want)
		}
	}
	// A long run of balanced-but-shallow brackets must NOT trip the guard — the
	// running depth returns to zero after each pair.
	if exceedsNestDepth([]byte(strings.Repeat("[]", 100000))) {
		t.Error("flat []-repeat (depth 1) must not be flagged as over-depth")
	}
	// Unbalanced closers must not drive the counter negative and mask real depth.
	if !exceedsNestDepth([]byte(strings.Repeat(")", 5) + strings.Repeat("[", maxParseNestDepth+1))) {
		t.Error("leading closers must not mask a subsequent over-depth run")
	}
}

// TestJSParse_NestGuard_NoHang is the regression for the tree-sitter super-linear
// parse DoS: a ~100 KB deeply-nested source file must return promptly (degraded
// to no AST symbols) instead of hanging the indexer/MCP server for minutes.
func TestJSParse_NestGuard_NoHang(t *testing.T) {
	depth := 50000 // ~100 KB; measured to hang for >115s before the guard
	src := "const x = " + strings.Repeat("[", depth) + strings.Repeat("]", depth) + ";"
	p := NewJSParser()
	done := make(chan FileStructure, 1)
	start := time.Now()
	go func() {
		fs, _ := p.ParseExt(src, ".js")
		done <- fs
	}()
	select {
	case fs := <-done:
		if len(fs.Functions) != 0 || len(fs.Classes) != 0 {
			t.Errorf("over-depth input should degrade to no AST symbols, got funcs=%v classes=%v", fs.Functions, fs.Classes)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("ParseExt hung on nested input for >10s (elapsed %v) — nest guard regressed", time.Since(start))
	}
}

// TestJSParse_NormalStillParses guards against the depth check over-rejecting:
// ordinary shallow code must still yield its symbols.
func TestJSParse_NormalStillParses(t *testing.T) {
	p := NewJSParser()
	fs, err := p.ParseExt("export function hello() { return [[1]]; }", ".js")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range fs.Functions {
		if f == "hello" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'hello' among functions, got %v", fs.Functions)
	}
}

// TestPythonParse_WalkDepth_NoCrash exercises the walk recursion cap: Python
// nests via indentation (no brackets), so exceedsNestDepth cannot catch it; the
// walk depth counter is the backstop that keeps a deeply-nested tree from
// overflowing the goroutine stack (an unrecoverable throw). This must return.
func TestPythonParse_WalkDepth_NoCrash(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		b.WriteString(strings.Repeat("    ", i))
		b.WriteString("if True:\n")
	}
	b.WriteString(strings.Repeat("    ", 3000))
	b.WriteString("pass\n")
	p := NewPythonParser()
	done := make(chan struct{}, 1)
	start := time.Now()
	go func() {
		_, _ = p.Parse(b.String())
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("Python Parse hung on deeply-indented input for >15s (elapsed %v)", time.Since(start))
	}
}
