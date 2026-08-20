package guard

import (
	"os"
	"testing"
)

// benchJSFile measures JSParamNames over a real-world file. The hook budget is
// ~12 ms for the whole guard run, and this extractor now runs on every JS/TS
// edit via foldInFileSymbols, so its cost is paid unconditionally.
func benchJSFile(b *testing.B, path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		b.Skipf("corpus file not present: %v", err)
	}
	lines := TextToAddedLines(string(src))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = JSParamNames(lines)
	}
}

func BenchmarkJSParamNamesSoloPage(b *testing.B) {
	benchJSFile(b, "/home/ericm/personal_projects/coriolis-local/claude-20260815-150632/frontend/src/pages/SoloPage.tsx")
}

func BenchmarkJSParamNamesTrackBDB(b *testing.B) {
	benchJSFile(b, "/home/ericm/personal_projects/frostline/claude-20260819-220901-829/template/src/lib/track-b-db.ts")
}

// Baselines on the same file: what the guard ALREADY pays for whole-file JS
// scanning, so JSParamNames' cost can be read as incremental rather than absolute.
func BenchmarkJSDeclaredNamesTrackBDB(b *testing.B) {
	src, err := os.ReadFile("/home/ericm/personal_projects/frostline/claude-20260819-220901-829/template/src/lib/track-b-db.ts")
	if err != nil {
		b.Skip(err)
	}
	lines := TextToAddedLines(string(src))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = JSDeclaredNames(lines)
	}
}

func BenchmarkLocallyBoundNamesTrackBDB(b *testing.B) {
	src, err := os.ReadFile("/home/ericm/personal_projects/frostline/claude-20260819-220901-829/template/src/lib/track-b-db.ts")
	if err != nil {
		b.Skip(err)
	}
	lines := TextToAddedLines(string(src))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = LocallyBoundNames(LangJS, lines, nil)
	}
}
