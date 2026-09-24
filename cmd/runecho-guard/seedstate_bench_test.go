package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/guard"
)

// benchPySource returns an ~n-line Python file built from repeated units that
// exercise every seed tracker (docstrings, dicts, multi-line defs with bracket
// defaults, f-strings). Each unit carries a unique marker so an Edit's
// old_string matches exactly once, as blockStartLine requires.
func benchPySource(n int) (string, []string) {
	var b strings.Builder
	var markers []string
	lines := 0
	for i := 0; lines < n; i++ {
		m := fmt.Sprintf("marker_%d = %d", i, i)
		markers = append(markers, m)
		unit := fmt.Sprintf(`%s
"""Doc for unit %d, with a { and a def x( inside."""
CFG_%d = {
    "a": [1, 2],
    "b": f"{value(%d)!r}",
}

def fn_%d(a,
          b=[
              1,
          ],
          *rest):
    return call({"k": a}, b)

`, m, i, i, i, i)
		b.WriteString(unit)
		lines += strings.Count(unit, "\n")
	}
	return b.String(), markers
}

// BenchmarkHookSeedBuild times the hook path's per-edit seed construction —
// block resolution plus every per-block seed — on a ~3,000-line file. It is
// the #295 measurement: the old builders each re-walked fileLines[:idx] once
// per tracker, so a block near the end of the file paid five O(file) walks.
func BenchmarkHookSeedBuild(b *testing.B) {
	src, markers := benchPySource(3000)
	fileLines := guard.TextToAddedLines(src)
	early, late := markers[3], markers[len(markers)-2]
	cases := []struct {
		name  string
		tool  string
		old   string
		edits []editOp
	}{
		{"Edit/early", "Edit", early, nil},
		{"Edit/late", "Edit", late, nil},
		{"MultiEdit/3", "MultiEdit", "", []editOp{
			{OldString: early, NewString: early + "\nx = 1"},
			{OldString: markers[len(markers)/2], NewString: "y = 2"},
			{OldString: late, NewString: late + "\nz = 3"},
		}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			indices := hookBlockIndices(tc.tool, tc.old, tc.edits, fileLines)
			if len(indices) == 0 {
				b.Fatal("no block resolved; the benchmark would time nothing")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var fd guard.FileDiff
				hookSeedMaps(hookBlockIndices(tc.tool, tc.old, tc.edits, fileLines), fileLines, guard.LangPython).applyTo(&fd)
			}
		})
	}
}
