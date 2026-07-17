package parser

import "testing"

// FuzzShellParse hardens the hand-rolled shell masker/parser against arbitrary
// input by pinning its two load-bearing invariants — maskShell is length-preserving
// and only ever turns non-newline bytes into spaces (never inserts, deletes, or
// moves a newline), and neither maskShell nor Parse panics. Every branch in the
// scanner advances i and every pop is guarded by top(), so these must always hold;
// the fuzzer exercises the escape/quote/heredoc/nesting edges the table tests can't
// enumerate. Parity with the tree-sitter parsers' nestguard/fuzz coverage.
func FuzzShellParse(f *testing.F) {
	seeds := []string{
		"f() {\n  echo hi\n}\n",
		"function g { cat <<EOF\nbody() {\nEOF\n}\n",
		"a() { x=${y:-{p,q}}; z=$(echo \"}\"); }\n",
		"real() (\n  cd /tmp && ls\n)\n",
		"'unterminated",
		"\"a $(echo `b` ${c:-d}) e\"",
		"<<<herestring",
		"cat <<-A <<B\n\tA\nB\n",
		"echo \\{ \\} \\( \\) \\\n next",
		"${", "$(", "`", "\"", "'", "{{{{", "}}}}", "((((",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		masked := maskShell([]byte(s))
		if len(masked) != len(s) {
			t.Fatalf("maskShell changed length: %d != %d", len(masked), len(s))
		}
		for i := range masked {
			if (masked[i] == '\n') != (s[i] == '\n') {
				t.Fatalf("maskShell altered a newline at offset %d", i)
			}
		}
		// Must not panic on arbitrary input.
		_, _ = NewShellParser().Parse(s)
	})
}
