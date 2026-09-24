package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkRunPrecommitSeeded times the pre-commit path's work on one staged
// Python file: Run plus the file-scope check, both seeded from the file's
// AbsPath, exactly as runPreCommit drives them. It is the #295 measurement for
// pre-commit: before PrepareSeeds each seed provider read and walked the file
// independently, so one staged .py cost nine reads.
func BenchmarkRunPrecommitSeeded(b *testing.B) {
	var src strings.Builder
	for i := 0; src.Len() < 120_000; i++ {
		fmt.Fprintf(&src, "CFG_%d = {\n    \"a\": [1, 2],\n}\n\ndef fn_%d(a,\n          b=[1],\n):\n    return a\n\n", i, i)
	}
	path := filepath.Join(b.TempDir(), "mod.py")
	if err := os.WriteFile(path, []byte(src.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	wholeFile := TextToAddedLines(src.String())
	n := len(wholeFile)
	fd := FileDiff{
		Path:    "mod.py",
		AbsPath: path,
		AddedLines: []AddedLine{
			{LineNo: n - 3, Text: "    x = fn_0(1)"},
			{LineNo: n - 2, Text: "    return helper(x)"},
		},
	}
	symbols := map[string]struct{}{"helper": {}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		diffs := []FileDiff{fd}
		PrepareSeeds(diffs) // as runPreCommit does, once per commit
		Run(symbols, "", diffs)
		FileScopeViolationsWithReason(LangPython, wholeFile, diffs[0], symbols)
	}
}
