package depindex

import (
	"path/filepath"
	"testing"
)

// BenchmarkGoDepLookup measures the WARM-cache cost of Lookup — the number
// #314 needs to judge whether GoDepQualified fits the guard's ~12ms hook
// budget. It is warm because that is the realistic per-edit cost:
// GoDepQualifiedViolations calls Lookup only for a package that has a
// qualified call in the ADDED lines (see depqualified_go.go), so a typical
// edit does 1-3 lookups against packages this same process (or an earlier
// hook fire, since the index is process-lifetime cached) has already primed —
// not a fresh cold parse of every stdlib package on every keystroke.
func BenchmarkGoDepLookup(b *testing.B) {
	root, err := filepath.Abs("../..")
	if err != nil {
		b.Fatal(err)
	}
	idx := NewGoIndex(root)
	pkgs := []string{"fmt", "os", "net/http", "strings"}
	for _, p := range pkgs {
		if ps := idx.Lookup(p); ps.Res != Resolved {
			b.Fatalf("warm-up lookup of %q did not resolve: %s (%s)", p, ps.Res, ps.Reason)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Lookup(pkgs[i%len(pkgs)])
	}
}
