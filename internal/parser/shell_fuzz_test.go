package parser

import "testing"

// FuzzShellParse asserts the shell parser never panics on arbitrary input and
// keeps the sorted/deduplicated invariants the IR relies on. The AST walk's own
// recover() (shellSymbolsFromAST) is what should make this hold — the fuzzer
// exercises the escape/quote/heredoc/nesting edges the table tests can't
// enumerate, parity with the other tree-sitter parsers' fuzz coverage.
// Run: go test -run=x -fuzz=FuzzShellParse ./internal/parser
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
		"f() {\n  ((x << y))\n}\n",
		"f() {\n  x=$(cat <<EOF\n)\nEOF\n)\n}\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	p := NewShellParser()
	f.Fuzz(func(t *testing.T, s string) {
		fs, err := p.Parse(s) // must never panic
		if err != nil {
			return
		}
		assertParserInvariants(t, fs)
	})
}
