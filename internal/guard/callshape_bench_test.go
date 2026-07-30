package guard

import (
	"fmt"
	"strings"
	"testing"
)

// Latency benchmarks for the call-shape check. They exist because the check was
// REDESIGNED twice on their evidence, not to decorate the package:
//
//   - a whole-file tree-sitter parse cost 180 ms on a module this size
//   - parsing only the signature still cost 8 ms, tree-sitter's fixed per-parse
//     overhead, so the parser was dropped for a masked scan
//   - narrowing candidates before touching the whole file took the no-candidate
//     case from 370 us to 4 us
//
// The hook's budget for everything it does is about 12 ms. Anything here that
// regresses past that is not shippable, which is why the numbers are pinned in
// comments next to the code they justify.
//
// The corpus is GENERATED rather than read from disk. An earlier version pointed
// at /usr/lib/python3.12/email/_header_value_parser.py and skipped when absent,
// which meant every latency number the design rests on was unmeasurable on CI, on
// macOS, and on this box after any Python upgrade — the evidence would have
// vanished silently. An adversarial review flagged it.

// benchModuleLines returns a synthetic Python module of roughly the size that
// forced each design decision (~3000 lines, ~200 module-level defs), with no
// `globals(`/`locals(`/`exec(`/`eval(` — any of those would abstain the whole file
// and the benchmark would measure nothing.
func benchModuleLines() []AddedLine {
	var b strings.Builder
	b.WriteString("\"\"\"Generated benchmark module.\"\"\"\nimport os\n\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "def helper_%d(value, policy=None, timeout=%d, *, strict=False):\n", i, i)
		fmt.Fprintf(&b, "    total = value + %d\n", i)
		for j := 0; j < 10; j++ {
			fmt.Fprintf(&b, "    total = total + %d\n", j)
		}
		b.WriteString("    return total\n\n")
	}
	return TextToAddedLines(b.String())
}

// The worst case: an Edit to a large file whose hunk carries a kwarg-bearing call,
// so every whole-file cost is paid.
func BenchmarkCallShapeCandidate(b *testing.B) {
	whole := benchModuleLines()
	added := TextToAddedLines("    return helper_7(value, policy=1)\n")
	fd := FileDiff{Path: "m.py", AddedLines: added}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PyCallShapeMismatches(LangPython, whole, fd, nil, false)
	}
}

// The common case: a hunk with no kwarg-bearing call. It must not pay for any
// whole-file work at all.
func BenchmarkCallShapeNoCandidate(b *testing.B) {
	whole := benchModuleLines()
	added := TextToAddedLines("    x = compute(a, b)\n")
	fd := FileDiff{Path: "m.py", AddedLines: added}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PyCallShapeMismatches(LangPython, whole, fd, nil, false)
	}
}

// The one-pass index, which replaced four separate whole-file extractor passes.
func BenchmarkCallShapeDeclIndex(b *testing.B) {
	whole := benchModuleLines()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		newPyDeclIndex(whole)
	}
}

// One signature resolution. This is the number that made dropping tree-sitter
// worth a hand-written reader: 8 ms became under a microsecond.
func BenchmarkCallShapeSignatureResolve(b *testing.B) {
	whole := benchModuleLines()
	idx := newPyDeclIndex(whole)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.cache = map[string]pyDeclShape{}
		idx.hasCache = map[string]bool{}
		idx.shapesFor("helper_7")
	}
}
