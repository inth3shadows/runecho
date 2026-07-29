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
`)), "fetch_rows")

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
`)), "build_client")
	if got.Pos != 0 || len(got.Kwargs) != 0 {
		t.Errorf("want empty shape, got %+v", got)
	}
}

func TestExtractCallShapes_TrailingCommaIsNotAnArgument(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
render(ctx, name="x",)
`)), "render")
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
`)), "run")
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
			got := find(t, ExtractCallShapes(LangPython, csLines("\n"+src)), "check")
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
`)), "outer")
	if !reflect.DeepEqual(got.Kwargs, []string{"flag"}) {
		t.Errorf("Kwargs = %v, want [flag] — inner's keyword is not outer's", got.Kwargs)
	}
	if got.Pos != 1 {
		t.Errorf("Pos = %d, want 1", got.Pos)
	}
	inner := find(t, ExtractCallShapes(LangPython, csLines("\nouter(inner(deep=1), flag=True)")), "inner")
	if !reflect.DeepEqual(inner.Kwargs, []string{"deep"}) {
		t.Errorf("inner Kwargs = %v, want [deep]", inner.Kwargs)
	}
}

// A non-identifier left of '=' is not a keyword binding.
func TestExtractCallShapes_NonIdentifierLHSIsPositional(t *testing.T) {
	for _, src := range []string{`emit(d["k"] == 1)`, `emit(obj.attr == 2)`} {
		got := find(t, ExtractCallShapes(LangPython, csLines("\n"+src)), "emit")
		if len(got.Kwargs) != 0 {
			t.Errorf("%s: Kwargs = %v, want none", src, got.Kwargs)
		}
	}
}

func TestExtractCallShapes_StarUnpackingSetsFlagsAndIsNotCounted(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
dispatch(first, *rest, mode="x", **extra)
`)), "dispatch")
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
`)), "create_user")
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
`)), "compare")
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
`))
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
	for _, s := range ExtractCallShapes(LangPython, lines) {
		if s.Name == "create_user" {
			t.Fatalf("followed an argument list across a hunk gap: %+v", s)
		}
	}
}

// A def line declares; its `a=1` is a default, not a caller's keyword.
func TestExtractCallShapes_DefLineIsNotACall(t *testing.T) {
	for _, s := range ExtractCallShapes(LangPython, csLines("\ndef fetch_rows(conn, limit=5):")) {
		if s.Name == "fetch_rows" {
			t.Fatalf("read a declaration as a call: %+v", s)
		}
	}
}

// ...but a genuine call sharing the def line is still reported. Skipping the whole
// line would lose it.
func TestExtractCallShapes_CallOnDefLineIsStillFound(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines("\ndef handler(cfg=load_config(path=\"p\")):")), "load_config")
	if !reflect.DeepEqual(got.Kwargs, []string{"path"}) {
		t.Errorf("Kwargs = %v, want [path]", got.Kwargs)
	}
}

func TestExtractCallShapes_QualifiedCallsSkipped(t *testing.T) {
	for _, s := range ExtractCallShapes(LangPython, csLines("\nclient.fetch(limit=5)")) {
		if s.Name == "fetch" {
			t.Fatalf("emitted a qualified call: %+v", s)
		}
	}
}

// Parens and '=' inside a string cannot alter the parse; the scan is masked first.
func TestExtractCallShapes_StringLiteralsCannotSkewTheParse(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
log("oops) name=", level=2)
`)), "log")
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
`)), "create_user")
	if got.Pos != 1 || !reflect.DeepEqual(got.Kwargs, []string{"email"}) {
		t.Errorf("got %+v, want 1 positional and [email]", got)
	}
}

// A top-level lambda's parameter commas are indistinguishable from argument
// commas, so the shape is flagged unreliable rather than reported as fact.
func TestExtractCallShapes_TopLevelLambdaFlagged(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
apply(key=lambda a, b: a)
`)), "apply")
	if !got.HasLambda {
		t.Error("HasLambda not set — the comma split is unreliable here")
	}
}

// A lambda nested inside brackets keeps its commas off depth zero, so the split is
// sound and the flag must stay clear — otherwise the check abstains needlessly.
func TestExtractCallShapes_NestedLambdaNotFlagged(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
apply(rows=sorted(xs, key=lambda a, b: a), n=1)
`)), "apply")
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
`)), "apply")
	if got.HasLambda {
		t.Error("HasLambda set by an identifier that merely contains the letters")
	}
}

// Python only. A shared path that half-worked on another language would be worse
// than an explicit gap, because its output would look authoritative.
func TestExtractCallShapes_NonPythonReturnsNil(t *testing.T) {
	for _, lang := range []Lang{LangGo, LangJS, LangUnknown} {
		if got := ExtractCallShapes(lang, csLines("\nfoo(a=1)")); got != nil {
			t.Errorf("lang %v returned %+v, want nil", lang, got)
		}
	}
}

func TestExtractCallShapes_BuiltinsSkipped(t *testing.T) {
	for _, s := range ExtractCallShapes(LangPython, csLines(`
print(sep="")
`)) {
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
	for _, s := range ExtractCallShapes(LangPython, lines) {
		if s.Name == "spread" {
			t.Fatalf("followed an argument list past the lookahead ceiling: %+v", s)
		}
	}
}

func TestExtractCallShapes_ArgByteCeilingAbstains(t *testing.T) {
	huge := strings.Repeat("a", callShapeMaxArgBytes+64)
	for _, s := range ExtractCallShapes(LangPython, csLines("\nspread("+huge+")")) {
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
	if got := len(ExtractCallShapes(LangPython, lines)); got > callShapeMaxSites {
		t.Errorf("returned %d sites, ceiling is %d", got, callShapeMaxSites)
	}
}

// Repeated keywords are a defect in their own right, so the extractor must not
// collapse them — the consumer needs to see both.
func TestExtractCallShapes_DuplicateKeywordPreserved(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
run(mode="a", mode="b")
`)), "run")
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
			shapes := ExtractCallShapes(LangPython, csLines("\n"+tc.src))
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
	got := find(t, ExtractCallShapes(LangPython, csLines("\nrender(ctx,)")), "render")
	if got.Pos != 1 || got.Unreliable {
		t.Errorf("got %+v, want Pos=1 and Unreliable=false", got)
	}
}

// A comment interleaved with the arguments cannot be told apart from a value on the
// following line once the list is joined into one string, so the shape is marked
// unreliable rather than guessed at. Undercounting silently would surface as an
// arity false positive later.
func TestExtractCallShapes_InterleavedCommentMarksUnreliable(t *testing.T) {
	got := find(t, ExtractCallShapes(LangPython, csLines(`
build(
    rows=[1, 2],  # one graded, one tie
)
`)), "build")
	if !got.Unreliable {
		t.Error("Unreliable not set — an interleaved comment is not classifiable")
	}
	if !reflect.DeepEqual(got.Kwargs, []string{"rows"}) {
		t.Errorf("Kwargs = %v, want [rows] — keyword names come from masked text and stay sound", got.Kwargs)
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
`)), "build")
	if got.Unreliable {
		t.Errorf("Unreliable set though the value is visible in masked text: %+v", got)
	}
	if !reflect.DeepEqual(got.Kwargs, []string{"rows"}) {
		t.Errorf("Kwargs = %v, want [rows]", got.Kwargs)
	}
}

func TestExtractCallShapes_EmptyInputIsNil(t *testing.T) {
	if got := ExtractCallShapes(LangPython, nil); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}
