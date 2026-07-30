package guard

import (
	"os"
	"testing"
)

// Latency benchmarks for the call-shape check. They exist because the check was
// REDESIGNED twice on their evidence, not to decorate the package:
//
//   - a whole-file tree-sitter parse cost 180 ms on the file below
//   - parsing only the signature still cost 8 ms, tree-sitter's fixed per-parse
//     overhead, so the parser was dropped for a masked scan
//   - narrowing candidates before touching the whole file took the no-candidate
//     case from 370 us to 3.6 us
//
// The hook's budget for everything it does is about 12 ms. Anything here that
// regresses past that is not shippable, which is why the numbers are pinned in
// comments next to the code they justify.
//
// The corpus file is a large real module with no `globals(`/`eval(`/`exec(` in it
// (which would abstain the whole file and measure nothing). Skips when absent.
const benchPyFile = "/usr/lib/python3.12/email/_header_value_parser.py"

func benchLines(b *testing.B, path string) []AddedLine {
	data, err := os.ReadFile(path)
	if err != nil {
		b.Skipf("corpus file unavailable: %v", err)
	}
	return TextToAddedLines(string(data))
}

// The worst case: an Edit to a 3037-line file whose hunk carries a kwarg-bearing
// call, so every whole-file cost is paid.
func BenchmarkCallShapeCandidate(b *testing.B) {
	whole := benchLines(b, benchPyFile)
	added := TextToAddedLines("    return get_unstructured(value, policy=1)\n")
	fd := FileDiff{Path: "hvp.py", AddedLines: added}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PyCallShapeMismatches(LangPython, whole, fd, nil, false)
	}
}

// The common case: a hunk with no kwarg-bearing call. It must not pay for any
// whole-file work at all.
func BenchmarkCallShapeNoCandidate(b *testing.B) {
	whole := benchLines(b, benchPyFile)
	added := TextToAddedLines("    x = compute(a, b)\n")
	fd := FileDiff{Path: "hvp.py", AddedLines: added}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PyCallShapeMismatches(LangPython, whole, fd, nil, false)
	}
}

// The one-pass index, which replaced four separate whole-file extractor passes.
func BenchmarkCallShapeDeclIndex(b *testing.B) {
	whole := benchLines(b, benchPyFile)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		newPyDeclIndex(whole)
	}
}

// One signature resolution. This is the number that made dropping tree-sitter
// worth the hand-written reader: 8 ms became under a microsecond.
func BenchmarkCallShapeSignatureResolve(b *testing.B) {
	whole := benchLines(b, benchPyFile)
	idx := newPyDeclIndex(whole)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.cache = map[string]pyDeclShape{}
		idx.hasCache = map[string]bool{}
		idx.shapesFor("get_unstructured")
	}
}
