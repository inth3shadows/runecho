package guard

import (
	"reflect"
	"strings"
	"testing"
)

// csLines turns a Python source block into contiguous added lines starting at
// line 1. It differs from pyLines (pyparam_test.go) only in trimming a leading
// newline, so a raw-string literal's first real line is line 1 and the LineNo
// assertions below read the same as the source.
func csLines(src string) []AddedLine {
	var out []AddedLine
	for i, t := range strings.Split(strings.TrimPrefix(src, "\n"), "\n") {
		out = append(out, AddedLine{LineNo: i + 1, Text: t})
	}
	return out
}

// find returns the single shape for callee name, failing if absent or ambiguous.
func find(t *testing.T, shapes []CallShape, name string) CallShape {
	t.Helper()
	var hits []CallShape
	for _, s := range shapes {
		if s.Name == name {
			hits = append(hits, s)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly 1 shape for %q, got %d (%+v)", name, len(hits), shapes)
	}
	return hits[0]
}

func TestExtractCallShapes_PositionalAndKeyword(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
fetch_rows(conn, 10, limit=5, offset=0)
`), nil), "fetch_rows")

	if got.Pos != 2 {
		t.Errorf("Pos = %d, want 2", got.Pos)
	}
	if !reflect.DeepEqual(got.Kwargs, []string{"limit", "offset"}) {
		t.Errorf("Kwargs = %v, want [limit offset]", got.Kwargs)
	}
	if got.LineNo != 1 {
		t.Errorf("LineNo = %d, want 1", got.LineNo)
	}
	if got.HasStar || got.HasKwStar || got.HasLambda {
		t.Errorf("no flag should be set: %+v", got)
	}
}

func TestExtractCallShapes_NoArgs(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
build_client()
`), nil), "build_client")
	if got.Pos != 0 || len(got.Kwargs) != 0 {
		t.Errorf("want empty shape, got %+v", got)
	}
}

func TestExtractCallShapes_TrailingCommaIsNotAnArgument(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
render(ctx, name="x",)
`), nil), "render")
	if got.Pos != 1 {
		t.Errorf("Pos = %d, want 1 (trailing comma is punctuation)", got.Pos)
	}
	if !reflect.DeepEqual(got.Kwargs, []string{"name"}) {
		t.Errorf("Kwargs = %v, want [name]", got.Kwargs)
	}
}

// A keyword name must survive being spelled with spaces around the '='; PEP 8
// forbids it but the parser is not a linter.
func TestExtractCallShapes_SpacedEqualsIsStillKeyword(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
run(timeout = 30)
`), nil), "run")
	if !reflect.DeepEqual(got.Kwargs, []string{"timeout"}) {
		t.Errorf("Kwargs = %v, want [timeout]", got.Kwargs)
	}
	if got.Pos != 0 {
		t.Errorf("Pos = %d, want 0", got.Pos)
	}
}

// Every operator that merely CONTAINS '=' is positional. A misread here is a
// false positive on ordinary working code, so each gets its own assertion.
func TestExtractCallShapes_ComparisonsAreNotKeywords(t *testing.T) {
	cases := map[string]string{
		"eq":     `check(a == b)`,
		"ne":     `check(a != b)`,
		"le":     `check(a <= b)`,
		"ge":     `check(a >= b)`,
		"walrus": `check(n := len(xs))`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			got := find(t, ExtractCallShapes(LangPython, csLines("\n"+src), nil), "check")
			if len(got.Kwargs) != 0 {
				t.Errorf("Kwargs = %v, want none (%s)", got.Kwargs, src)
			}
			if got.Pos != 1 {
				t.Errorf("Pos = %d, want 1 (%s)", got.Pos, src)
			}
		})
	}
}

// An '=' inside brackets belongs to a nested expression, not to this call.
func TestExtractCallShapes_NestedEqualsIsNotThisCallsKeyword(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
outer(inner(deep=1), flag=True)
`), nil), "outer")
	if !reflect.DeepEqual(got.Kwargs, []string{"flag"}) {
		t.Errorf("Kwargs = %v, want [flag] — inner's keyword is not outer's", got.Kwargs)
	}
	if got.Pos != 1 {
		t.Errorf("Pos = %d, want 1", got.Pos)
	}
	inner := find(t, ExtractCallShapes(LangPython, csLines("\nouter(inner(deep=1), flag=True)"), nil), "inner")
	if !reflect.DeepEqual(inner.Kwargs, []string{"deep"}) {
		t.Errorf("inner Kwargs = %v, want [deep]", inner.Kwargs)
	}
}

// A non-identifier left of '=' is not a keyword binding.
func TestExtractCallShapes_NonIdentifierLHSIsPositional(t *testing.T) {
	for _, src := range []string{`emit(d["k"] == 1)`, `emit(obj.attr == 2)`} {
		got := find(t, ExtractCallShapes(LangPython, csLines("\n"+src), nil), "emit")
		if len(got.Kwargs) != 0 {
			t.Errorf("%s: Kwargs = %v, want none", src, got.Kwargs)
		}
	}
}

func TestExtractCallShapes_StarUnpackingSetsFlagsAndIsNotCounted(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
dispatch(first, *rest, mode="x", **extra)
`), nil), "dispatch")
	if got.Pos != 1 {
		t.Errorf("Pos = %d, want 1 — a *unpacking has unknowable width", got.Pos)
	}
	if !got.HasStar {
		t.Error("HasStar not set")
	}
	if !got.HasKwStar {
		t.Error("HasKwStar not set")
	}
	if !reflect.DeepEqual(got.Kwargs, []string{"mode"}) {
		t.Errorf("Kwargs = %v, want [mode]", got.Kwargs)
	}
}

func TestExtractCallShapes_MultiLineArgsAreJoined(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
create_user(
    "ada",
    email="ada@example.com",
    active=True,
)
`), nil), "create_user")
	if got.Pos != 1 {
		t.Errorf("Pos = %d, want 1", got.Pos)
	}
	if !reflect.DeepEqual(got.Kwargs, []string{"email", "active"}) {
		t.Errorf("Kwargs = %v, want [email active]", got.Kwargs)
	}
	if got.LineNo != 1 {
		t.Errorf("LineNo = %d, want 1 — the callee's line, not the closing paren's", got.LineNo)
	}
}

// Bare newlines inside parens are whitespace in Python; joining lines without a
// separator would fuse two tokens into one.
func TestExtractCallShapes_LineJoinInsertsSeparator(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
compare(a
== b)
`), nil), "compare")
	if got.Pos != 1 || len(got.Kwargs) != 0 {
		t.Errorf("got %+v, want one positional and no keywords", got)
	}
}

// The central abstention: a call list that does not close inside the added lines
// is unknowable from a diff, and no shape may be reported for it.
func TestExtractCallShapes_UnbalancedListAbstains(t *testing.T) {
	shapes := ExtractCallShapes(LangPython, csLines(`
create_user(
    "ada",
    email="ada@example.com",
`), nil)
	for _, s := range shapes {
		if s.Name == "create_user" {
			t.Fatalf("emitted a shape for an unclosed call: %+v", s)
		}
	}
}

// A hunk gap breaks bracket continuity: the missing lines could contain anything.
func TestExtractCallShapes_HunkGapAbstains(t *testing.T) {
	lines := []AddedLine{
		{LineNo: 10, Text: `create_user(`},
		{LineNo: 40, Text: `    email="x")`}, // non-contiguous
	}
	for _, s := range ExtractCallShapes(LangPython, lines, nil) {
		if s.Name == "create_user" {
			t.Fatalf("followed an argument list across a hunk gap: %+v", s)
		}
	}
}

// A def line declares; its `a=1` is a default, not a caller's keyword.
func TestExtractCallShapes_DefLineIsNotACall(t *testing.T) {
	for _, s := range ExtractCallShapes(LangPython, csLines("\ndef fetch_rows(conn, limit=5):"), nil) {
		if s.Name == "fetch_rows" {
			t.Fatalf("read a declaration as a call: %+v", s)
		}
	}
}

// ...but a genuine call sharing the def line is still reported. Skipping the whole
// line would lose it.
func TestExtractCallShapes_CallOnDefLineIsStillFound(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines("\ndef handler(cfg=load_config(path=\"p\")):"), nil), "load_config")
	if !reflect.DeepEqual(got.Kwargs, []string{"path"}) {
		t.Errorf("Kwargs = %v, want [path]", got.Kwargs)
	}
}

func TestExtractCallShapes_QualifiedCallsSkipped(t *testing.T) {
	for _, s := range ExtractCallShapes(LangPython, csLines("\nclient.fetch(limit=5)"), nil) {
		if s.Name == "fetch" {
			t.Fatalf("emitted a qualified call: %+v", s)
		}
	}
}

// Parens and '=' inside a string cannot alter the parse; the scan is masked first.
func TestExtractCallShapes_StringLiteralsCannotSkewTheParse(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
log("oops) name=", level=2)
`), nil), "log")
	if got.Pos != 1 {
		t.Errorf("Pos = %d, want 1", got.Pos)
	}
	if !reflect.DeepEqual(got.Kwargs, []string{"level"}) {
		t.Errorf("Kwargs = %v, want [level] — the string's `name=` is not a keyword", got.Kwargs)
	}
}

func TestExtractCallShapes_CommentInsideArgListDoesNotClose(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
create_user(
    "ada",  # the first user
    email="a@b.c",
)
`), nil), "create_user")
	if got.Pos != 1 || !reflect.DeepEqual(got.Kwargs, []string{"email"}) {
		t.Errorf("got %+v, want 1 positional and [email]", got)
	}
}

// A top-level lambda's parameter commas are indistinguishable from argument
// commas, so the shape is flagged unreliable rather than reported as fact.
func TestExtractCallShapes_TopLevelLambdaFlagged(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
apply(key=lambda a, b: a)
`), nil), "apply")
	if !got.HasLambda {
		t.Error("HasLambda not set — the comma split is unreliable here")
	}
}

// A lambda nested inside brackets keeps its commas off depth zero, so the split is
// sound and the flag must stay clear — otherwise the check abstains needlessly.
func TestExtractCallShapes_NestedLambdaNotFlagged(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
apply(rows=sorted(xs, key=lambda a, b: a), n=1)
`), nil), "apply")
	if got.HasLambda {
		t.Error("HasLambda set for a bracket-nested lambda")
	}
	if !reflect.DeepEqual(got.Kwargs, []string{"rows", "n"}) {
		t.Errorf("Kwargs = %v, want [rows n]", got.Kwargs)
	}
}

func TestExtractCallShapes_IdentifierContainingLambdaNotFlagged(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
apply(lambda_fn=f, my_lambda=g)
`), nil), "apply")
	if got.HasLambda {
		t.Error("HasLambda set by an identifier that merely contains the letters")
	}
}

// Python only. A shared path that half-worked on another language would be worse
// than an explicit gap, because its output would look authoritative.
func TestExtractCallShapes_NonPythonReturnsNil(t *testing.T) {
	for _, lang := range []Lang{LangGo, LangJS, LangUnknown} {
		if got := ExtractCallShapes(lang, csLines("\nfoo(a=1)"), nil); got != nil {
			t.Errorf("lang %v returned %+v, want nil", lang, got)
		}
	}
}

func TestExtractCallShapes_BuiltinsSkipped(t *testing.T) {
	for _, s := range ExtractCallShapes(LangPython, csLines(`
print(sep="")
`), nil) {
		if s.Name == "print" {
			t.Fatalf("emitted a builtin: %+v", s)
		}
	}
}

// Ceilings are load-bearing: the guard scans attacker-influenced repo text inside
// an edit loop, so an unbounded scan is a denial-of-service surface (#212).
func TestExtractCallShapes_LookaheadCeilingAbstains(t *testing.T) {
	lines := []AddedLine{{LineNo: 1, Text: "spread("}}
	for i := 0; i < callShapeMaxLookahead+5; i++ {
		lines = append(lines, AddedLine{LineNo: i + 2, Text: "  x,"})
	}
	lines = append(lines, AddedLine{LineNo: len(lines) + 1, Text: ")"})
	for _, s := range ExtractCallShapes(LangPython, lines, nil) {
		if s.Name == "spread" {
			t.Fatalf("followed an argument list past the lookahead ceiling: %+v", s)
		}
	}
}

func TestExtractCallShapes_ArgByteCeilingAbstains(t *testing.T) {
	huge := strings.Repeat("a", callShapeMaxArgBytes+64)
	for _, s := range ExtractCallShapes(LangPython, csLines("\nspread("+huge+")"), nil) {
		if s.Name == "spread" {
			t.Fatalf("exceeded the argument-byte ceiling: %+v", s)
		}
	}
}

func TestExtractCallShapes_SiteCeilingBoundsOutput(t *testing.T) {
	var lines []AddedLine
	for i := 0; i < callShapeMaxSites+50; i++ {
		lines = append(lines, AddedLine{LineNo: i + 1, Text: "f(x=1)"})
	}
	if got := len(ExtractCallShapes(LangPython, lines, nil)); got > callShapeMaxSites {
		t.Errorf("returned %d sites, ceiling is %d", got, callShapeMaxSites)
	}
}

// Repeated keywords are a defect in their own right, so the extractor must not
// collapse them — the consumer needs to see both.
func TestExtractCallShapes_DuplicateKeywordPreserved(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
run(mode="a", mode="b")
`), nil), "run")
	if !reflect.DeepEqual(got.Kwargs, []string{"mode", "mode"}) {
		t.Errorf("Kwargs = %v, want [mode mode]", got.Kwargs)
	}
}

// A string-literal argument masks to pure whitespace, which is byte-identical to
// the nothing following a trailing comma. Counting on masked text alone read
// `_fmt(gap, '+.4f')` as ONE argument — found by the differential harness against
// CPython, on real repo source.
func TestExtractCallShapes_StringLiteralArgumentIsCounted(t *testing.T) {
	cases := map[string]struct {
		src string
		pos int
	}{
		"literal-second":  {`_fmt(gap, '+.4f')`, 2},
		"literal-only":    {`emit("payload")`, 1},
		"literal-both":    {`join("a", "b")`, 2},
		"literal-first":   {`_fmt("+.4f", gap)`, 2},
		"literal-keyword": {`run(fmt="+.4f")`, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			shapes := ExtractCallShapes(LangPython, csLines("\n"+tc.src), nil)
			got := shapes[0]
			if got.Pos != tc.pos {
				t.Errorf("Pos = %d, want %d for %s", got.Pos, tc.pos, tc.src)
			}
			if got.Unreliable {
				t.Errorf("Unreliable set for %s — the shape is knowable", tc.src)
			}
		})
	}
}

// The trailing comma of a single-line call is punctuation and must not become an
// argument, nor make the shape unreliable.
func TestExtractCallShapes_TrailingCommaIsNotUnreliable(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines("\nrender(ctx,)"), nil), "render")
	if got.Pos != 1 || got.Unreliable {
		t.Errorf("got %+v, want Pos=1 and Unreliable=false", got)
	}
}

// Per-argument trailing comments are ordinary formatted Python — a magic trailing
// comma plus a comment on each line — and must classify exactly, not abstain. The
// comment cannot be shadowing a value: masking blanks only the comment text, so a
// value on the following line would still appear in the masked scan (which is what
// TestExtractCallShapes_CommentLineBeforeValueIsReliable pins).
func TestExtractCallShapes_InterleavedCommentsClassifyExactly(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
compute(
    alpha,   # the first
    beta,    # the second
)
`), nil), "compute")
	if got.Pos != 2 {
		t.Errorf("Pos = %d, want 2", got.Pos)
	}
	if got.Unreliable {
		t.Error("Unreliable set on ordinary commented Python — a pure comment is punctuation")
	}
}

func TestExtractCallShapes_TrailingCommentAfterKeywordArg(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
build(
    rows=[1, 2],  # one graded, one tie
)
`), nil), "build")
	if got.Unreliable {
		t.Error("Unreliable set — the segment is a pure comment")
	}
	if got.Pos != 0 {
		t.Errorf("Pos = %d, want 0", got.Pos)
	}
	if !reflect.DeepEqual(got.Kwargs, []string{"rows"}) {
		t.Errorf("Kwargs = %v, want [rows]", got.Kwargs)
	}
}

// The quote branch in parseArgShape is reachable ONLY inside an f-string
// interpolation, where maskNestedLiteral blanks the quote bytes as well. In plain
// context stripLiteralsStateful keeps the quotes, so the segment is non-empty in
// masked and classifies through the ordinary path. Without this case the branch has
// no fixture at all — a mutation removing it leaves the rest of the suite green.
func TestExtractCallShapes_InterpolatedLiteralArgumentIsCounted(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
print(f"| Proportional | {_fmt(gap, '+.4f')} |")
`), nil), "_fmt")
	if got.Pos != 2 {
		t.Errorf("Pos = %d, want 2 — the interpolated literal's quotes are blanked too", got.Pos)
	}
	if got.Unreliable {
		t.Errorf("Unreliable set: %+v", got)
	}
}

// An added line landing inside a PRE-EXISTING docstring is prose, not code. Without
// the openSeed the run starts outside any string and the prose scans as calls — the
// #145 class the existence check already fixed.
func TestExtractCallShapes_DocstringSeedSuppressesProse(t *testing.T) {
	lines := []AddedLine{
		{LineNo: 10, Text: `    Runs COUNT(x) over the table and calls helper(a, b).`},
		{LineNo: 11, Text: `    """`},
	}
	seed := func(int) string { return `"""` }
	if got := ExtractCallShapes(LangPython, lines, seed); len(got) != 0 {
		t.Errorf("seeded inside a docstring, still emitted %+v", got)
	}
	// The companion assertion: without a seed the same lines DO scan as code, so the
	// test above is pinning the seed rather than something else.
	if got := ExtractCallShapes(LangPython, lines, nil); len(got) == 0 {
		t.Error("unseeded scan emitted nothing — this test would pass vacuously")
	}
}

// The per-attempt ceilings bound one argument list; an ABANDONED attempt still costs
// a full lookahead and yields no site, so neither of them bounds a file of
// unbalanced parens. Only the whole-input budget does.
//
// Asserted behaviourally, not on wall-clock: unbalanced lines burn the budget, and a
// genuine balanced call placed AFTER them must therefore never be reached. A timing
// assertion would be environment-dependent — the first version of this test failed
// in CI under `-race`, which is exactly the flakiness a budget test must not have.
func TestExtractCallShapes_TotalScanBudgetStopsTheScan(t *testing.T) {
	var lines []AddedLine
	junk := strings.Repeat("abcdef(", 60) // 60 candidates/line, none ever closing
	for i := 0; i < 40; i++ {
		lines = append(lines, AddedLine{LineNo: i + 1, Text: junk})
	}
	lines = append(lines, AddedLine{LineNo: len(lines) + 1, Text: `target(x=1)`})

	for _, s := range ExtractCallShapes(LangPython, lines, nil) {
		if s.Name == "target" {
			t.Fatalf("reached a call past the total-scan budget: %+v", s)
		}
	}

	// Companion assertion: the same trailing call IS found without the junk, so the
	// test pins the budget rather than a parse failure.
	tail := []AddedLine{{LineNo: 1, Text: `target(x=1)`}}
	if got := ExtractCallShapes(LangPython, tail, nil); len(got) != 1 || got[0].Name != "target" {
		t.Fatalf("trailing call not extractable on its own: %+v", got)
	}
}

// A comment that precedes a value on its own line is NOT unreliable: the masked
// text still carries the value, so the segment classifies normally.
func TestExtractCallShapes_CommentLineBeforeValueIsReliable(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
build(
    # the first row
    rows=[1, 2],
)
`), nil), "build")
	if got.Unreliable {
		t.Errorf("Unreliable set though the value is visible in masked text: %+v", got)
	}
	if !reflect.DeepEqual(got.Kwargs, []string{"rows"}) {
		t.Errorf("Kwargs = %v, want [rows]", got.Kwargs)
	}
}

func TestExtractCallShapes_EmptyInputIsNil(t *testing.T) {
	if got := ExtractCallShapes(LangPython, nil, nil); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}
