package guard

import (
	"fmt"
	"testing"
)

// BenchmarkSuggest_LargeSet pins the allocation behaviour of the "did you mean"
// path, which runs inside the PreToolUse hook's ~12 ms budget.
//
// It exists because the cost here was invisible to every correctness test: an
// earlier levenshtein allocated one row PER CHARACTER of the probe, per
// candidate, so a single suggestion against a realistic symbol set cost ~18,000
// allocations and ~2.8 MB. Reusing a scratch buffer took that to 13 allocations
// and ~4 KB.
//
// The symbol set is deliberately adversarial: every name shares a prefix and sits
// within the ±2 length prune, so the prune filters almost nothing and the
// distance computation runs on ~1000 candidates. Real repos prune harder; this is
// the worst case, not the typical one. Watch allocs/op — a regression there is
// the signal, and it will show up long before wall-clock does on a fast machine.
func BenchmarkSuggest_LargeSet(b *testing.B) {
	for _, n := range []int{1000, 10000, 50000} {
		known := make(map[string]struct{}, n)
		for i := 0; i < n; i++ {
			known[fmt.Sprintf("someFunctionName%d", i)] = struct{}{}
		}
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				Suggest("someFunctionNam42", known)
			}
		})
	}
}
