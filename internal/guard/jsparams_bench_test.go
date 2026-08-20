package guard

import (
	"fmt"
	"strings"
	"testing"
)

// synthTSX builds a TSX-shaped corpus of roughly n components. It exists so the
// performance claims in this extractor's comments stay reproducible: an earlier
// revision of this file benchmarked two absolute paths into ephemeral claudew
// worktrees, which are removed by the worktree aftercare — so the numbers became
// uncheckable by anyone, including the next session in this repo.
//
// The shape deliberately mixes what the extractor must actually handle: the
// multi-line destructured component signature, interfaces and type aliases it
// must skip, generics with commas, async arrows, and plenty of ordinary calls
// (the case the identifier-byte rejection exists to skip cheaply).
func synthTSX(n int) []AddedLine {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `interface Props%d {
  value: string | null;
  onChange: (v: string | null) => void;
}

type Handler%d = (evt: MouseEvent) => void;

export function Component%d({
  value,
  onChange,
  disabled,
}: Props%d) {
  const rows = items.map((it) => transform(it, value));
  const send = async (payload: Map<string, Handler%d>) => post(payload);
  useEffect(() => {
    subscribe(value, onChange);
  }, [value, onChange]);
  return render(rows, send, disabled);
}

`, i, i, i, i, i)
	}
	return TextToAddedLines(b.String())
}

// BenchmarkJSParamNamesSynthetic is the reproducible headline number: ~120
// components, ~2400 lines, comparable in size to the real 400 KB TS file the
// original measurements used.
func BenchmarkJSParamNamesSynthetic(b *testing.B) {
	lines := synthTSX(120)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = JSParamNames(lines)
	}
}

// Baselines on the SAME input, so JSParamNames' cost reads as incremental
// against what the guard already pays for whole-file JS scanning rather than as
// an absolute against a budget no existing extractor meets either.
func BenchmarkJSDeclaredNamesSynthetic(b *testing.B) {
	lines := synthTSX(120)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = JSDeclaredNames(lines)
	}
}

func BenchmarkLocallyBoundNamesSynthetic(b *testing.B) {
	lines := synthTSX(120)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = LocallyBoundNames(LangJS, lines, nil)
	}
}
