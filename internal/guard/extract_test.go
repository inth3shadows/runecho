package guard

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// helpers

func lines(strs ...string) []AddedLine {
	ls := make([]AddedLine, len(strs))
	for i, s := range strs {
		ls[i] = AddedLine{LineNo: i + 1, Text: s}
	}
	return ls
}

// TestExtractRefs_CallOnDefLineChecked pins the fix for the def-line skip: a call
// that shares a line with a definition (a one-line Go body, a Python default-arg
// factory) must still be validated, while the definition's OWN name is skipped as
// a self-match. Previously the whole line was skipped, hiding hallucinated calls.
func TestExtractRefs_CallOnDefLineChecked(t *testing.T) {
	goRefs := ExtractRefs(LangGo, lines("func Helper() int { return ComputeSomething() }"))
	if !containsAll(goRefs, "ComputeSomething") {
		t.Errorf("one-line Go body: ComputeSomething not extracted: %v", refNames(goRefs))
	}
	if !containsNone(goRefs, "Helper") {
		t.Errorf("Go def name Helper should self-skip: %v", refNames(goRefs))
	}

	pyRefs := ExtractRefs(LangPython, lines("def process(data, factory=NonExistentFactory()):"))
	if !containsAll(pyRefs, "NonExistentFactory") {
		t.Errorf("py default-arg factory not extracted: %v", refNames(pyRefs))
	}
	if !containsNone(pyRefs, "process") {
		t.Errorf("py def name process should self-skip: %v", refNames(pyRefs))
	}
}

func refNames(refs []Ref) []string {
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	return names
}

func containsAll(got []Ref, want ...string) bool {
	gotSet := make(map[string]bool)
	for _, r := range got {
		gotSet[r.Name] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			return false
		}
	}
	return true
}

func containsNone(got []Ref, nope ...string) bool {
	gotSet := make(map[string]bool)
	for _, r := range got {
		gotSet[r.Name] = true
	}
	for _, n := range nope {
		if gotSet[n] {
			return false
		}
	}
	return true
}

// --- ExtractDefs ---

func TestExtractDefs_Go(t *testing.T) {
	ls := lines(
		`func ProcessFoo(x int) error {`,
		`func (r *Receiver) Method() {`,
		`// not a def`,
		`result := SomeCall()`,
	)
	defs := ExtractDefs(LangGo, ls)
	if len(defs) != 2 {
		t.Fatalf("expected 2 defs, got %v", defs)
	}
	if defs[0] != "ProcessFoo" || defs[1] != "Method" {
		t.Errorf("defs = %v", defs)
	}
}

func TestExtractDefs_Python(t *testing.T) {
	ls := lines(
		`def process_foo(x):`,
		`    result = helper()`,
	)
	defs := ExtractDefs(LangPython, ls)
	if len(defs) != 1 || defs[0] != "process_foo" {
		t.Errorf("defs = %v", defs)
	}
}

func TestExtractDefs_PythonAsync(t *testing.T) {
	ls := lines(
		`async def search(query):`,
		`    return await run(query)`,
	)
	defs := ExtractDefs(LangPython, ls)
	if len(defs) != 1 || defs[0] != "search" {
		t.Errorf("async def not recognized as a definition: defs = %v", defs)
	}
}

// An `async def` line defines a symbol; its own name must not be counted as a
// call site (regression: the def-skip regex previously matched only plain `def`,
// so async defs leaked into refs).
func TestExtractRefs_Python_AsyncDefNotARef(t *testing.T) {
	ls := lines(
		`async def search(query):`,
		`    return await run(query)`,
	)
	refs := ExtractRefs(LangPython, ls)
	for _, r := range refs {
		if r.Name == "search" {
			t.Errorf("async def name leaked into refs: %+v", refs)
		}
	}
}

func TestExtractDefs_JS(t *testing.T) {
	ls := lines(
		`function processBar(x) {`,
		`const computeFoo = (x) => x * 2`,
		`let helperFn = function() {}`,
		`var arrowFn = async (x) => x`,
		`const notAFunc = 42`,
	)
	defs := ExtractDefs(LangJS, ls)
	if len(defs) < 2 {
		t.Errorf("expected at least processBar + computeFoo, got %v", defs)
	}
}

// --- ExtractRefs ---

// TestExtractRefs_Go_GenericInstantiatedCall pins the generic-call FN: a Go call
// written with explicit type arguments (`Foo[int](x)`) puts `[int]` between the
// name and `(`, so the bare `name(` scanner never matched it and a hallucinated
// generic call was silently never checked — a false negative, the worst class
// for the truth-oracle guard.
func TestExtractRefs_Go_GenericInstantiatedCall(t *testing.T) {
	ls := lines(
		`x := DoesNotExist[int](5)`,
		`y := Transform[string, int](items)`,
		`z := Wrap[T](v)`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "DoesNotExist", "Transform", "Wrap") {
		t.Errorf("generic-instantiated calls must be extracted, got %v", refNames(refs))
	}
}

// TestExtractRefs_Go_NestedGenericCall pins the residual FN from #124: a generic
// call whose type argument itself contains brackets (`Foo[map[K]V](x)`,
// `Foo[[]byte](x)`) was missed because the type-arg body was non-nesting. One
// level of bracket nesting is now allowed.
func TestExtractRefs_Go_NestedGenericCall(t *testing.T) {
	ls := lines(
		`x := DoesNotExist[map[K]V](5)`,
		`y := Wrap[[]byte](b)`,
		`z := Conv[map[string]int](m)`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "DoesNotExist", "Wrap", "Conv") {
		t.Errorf("nested-generic calls must be extracted, got %v", refNames(refs))
	}
}

// TestExtractRefs_Go_IndexCallResolvesViaBinding guards the FP direction for
// indexing a slice/map of funcs and calling the result — `arr[i](x)`.
//
// The protection moved. It used to come from the blanket unexported-name skip,
// which is gone; `arr` and `handlers` are now extracted as the genuine references
// they are. What keeps them from being flagged is that FoldInFileDefs folds the
// file's own bindings (GoDeclaredNames) into the known set — so the test asserts
// the outcome that actually matters, no violation, rather than the mechanism.
func TestExtractRefs_Go_IndexCallResolvesViaBinding(t *testing.T) {
	src := `package p

func run(i int, key string, x, ctx any) {
	arr := handlerList()
	handlers := handlerMap()
	arr[i](x)
	handlers[key](ctx)
}
`
	fileLines := TextToAddedLines(src)
	known := map[string]struct{}{"handlerList": {}, "handlerMap": {}}
	FoldInFileDefs(known, fileLines, LangGo)
	got := Run(known, "", []FileDiff{{Path: "p.go", AddedLines: fileLines}})
	if len(got) != 0 {
		t.Errorf("index-then-call on locally bound names must not be flagged, got %+v", got)
	}
}

// TestExtractRefs_JS_GenericInstantiatedCall pins the TS twin of the Go
// generic-call FN: a call with explicit type arguments (`Foo<T>(x)`) puts `<T>`
// between the name and `(`, so the bare `name(` scanner missed it and a
// hallucinated `DoesNotExist<T>(5)` was never checked (FN).
func TestExtractRefs_JS_GenericInstantiatedCall(t *testing.T) {
	ls := lines(
		`const x = DoesNotExist<T>(5)`,
		`const y = Transform<string, number>(items)`,
		`const z = Make<Map<string, number>>(a)`,
		`const r = Parse<Foo[]>(raw)`,
	)
	refs := ExtractRefs(LangJS, ls)
	if !containsAll(refs, "DoesNotExist", "Transform", "Make", "Parse") {
		t.Errorf("TS generic calls must be extracted, got %v", refNames(refs))
	}
}

// TestExtractRefs_JS_ComparisonNotGenericCall guards the FP direction that makes
// `<...>` hard: a conventionally-spaced comparison / shift / compound-boolean /
// JSX expression must NOT have its identifiers read as a generic call. The `<`
// being flush against the name (generic) vs spaced (comparison) is the
// discriminator. (An unspaced `a<b>(c)` comparison — convention-violating and
// nonsensical — is a known, accepted FP and is deliberately not asserted here.)
func TestExtractRefs_JS_ComparisonNotGenericCall(t *testing.T) {
	ls := lines(
		`if (a < b > (c)) {}`,
		`const t = a << b >> (c)`,
		`if (count < max && idx > (lo)) {}`,
		`return width < height > (pad)`,
		`const e = <Foo>(bar)`,
	)
	refs := ExtractRefs(LangJS, ls)
	if !containsNone(refs, "a", "b", "count", "max", "idx", "width", "height") {
		t.Errorf("spaced comparisons/shift/JSX must not be read as generic calls, got %v", refNames(refs))
	}
}

// TestExtractRefs_JS_GenericFuncDefSelfSkipped guards the def/call regex sync: a
// generic function *declaration* must be recognized as a definition so the
// call-side regex (which now bridges `<...>`) doesn't read the function's own
// name as an unresolved call. Non-generic defs were already self-skipped; adding
// a type param must not flip that into a false positive.
func TestExtractRefs_JS_GenericFuncDefSelfSkipped(t *testing.T) {
	ls := lines(
		`function transform<T>(items) {`,
		`export async function load<A, B>(url) {`,
	)
	refs := ExtractRefs(LangJS, ls)
	if !containsNone(refs, "transform", "load") {
		t.Errorf("generic function decls must self-skip, not flag their own name, got %v", refNames(refs))
	}
}

// TestExtractRefs_DedupsByName pins F4: the same call repeated across many lines
// yields exactly one Ref (first line wins), so a pathological input can't hold
// millions of Ref structs. Both consumers already collapse by name downstream,
// so this is behavior-preserving.
func TestExtractRefs_DedupsByName(t *testing.T) {
	ls := lines(
		`result := ProcessFoo(a)`,
		`other := ProcessFoo(b)`,
		`third := ProcessFoo(c)`,
	)
	refs := ExtractRefs(LangGo, ls)
	n := 0
	for _, r := range refs {
		if r.Name == "ProcessFoo" {
			n++
			if r.LineNo != 1 {
				t.Errorf("deduped ref should keep the first line (1), got %d", r.LineNo)
			}
		}
	}
	if n != 1 {
		t.Errorf("ProcessFoo should appear once after dedup, got %d: %v", n, refNames(refs))
	}
}

func TestExtractRefs_Go_BareCall(t *testing.T) {
	ls := lines(`result := ProcessFoo(ctx, bar)`)
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "ProcessFoo") {
		t.Errorf("expected ProcessFoo in refs, got %v", refNames(refs))
	}
}

func TestExtractRefs_Go_QualifiedSkipped(t *testing.T) {
	ls := lines(
		`pkg.ProcessFoo()`,
		`os.ReadFile(path)`,
		`fmt.Printf("%s", s)`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsNone(refs, "ProcessFoo", "ReadFile", "Printf") {
		t.Errorf("qualified calls should be skipped, got %v", refNames(refs))
	}
}

func TestExtractRefs_Go_BuiltinsSkipped(t *testing.T) {
	ls := lines(
		`n := len(items)`,
		`buf := make([]byte, 0)`,
		`p := new(Foo)`,
		`s := string(b)`,
		`go goroutine()`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsNone(refs, "len", "make", "new", "string", "go") {
		t.Errorf("builtins should be excluded, got %v", refNames(refs))
	}
}

// TestExtractRefs_Go_UnexportedNowExtracted pins the inversion of the old
// unexported-skip policy. The skip existed because the IR held only exported Go
// symbols, so an unexported call had nothing to validate against; the parser now
// records unexported top-level declarations under the "unexported" kind, and the
// compiler-oracle differential measured this shape at 0/29 caught before the
// change and 33/33 after, at zero proven false positives over 468k lines of
// foreign Go.
func TestExtractRefs_Go_UnexportedNowExtracted(t *testing.T) {
	ls := lines(
		`lookupSymbols()`,
		`hookApprove()`,
		`textToAddedLines("x")`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "lookupSymbols", "hookApprove", "textToAddedLines") {
		t.Errorf("unexported Go refs must now be extracted, got %v", refNames(refs))
	}
}

// TestExtractRefs_Go_BuiltinsNotFlagged pins the builtin exclusions that the
// unexported skip used to hide. `min`/`max`/`clear` arrived in Go 1.21 and were
// missing from the table; `complex64`/`complex128` were never there. Both
// surfaced as proven false positives the moment lowercase refs started being
// checked, which is precisely the class this test now protects.
func TestExtractRefs_Go_BuiltinsNotFlagged(t *testing.T) {
	ls := lines(
		`n := min(a, b)`,
		`m := max(a, b)`,
		`clear(cache)`,
		`z := complex128(v)`,
		`w := complex64(v)`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsNone(refs, "min", "max", "clear", "complex128", "complex64") {
		t.Errorf("Go builtins must not be flagged, got %v", refNames(refs))
	}
}

func TestExtractRefs_Go_ExportedStillChecked(t *testing.T) {
	ls := lines(`result := ParseStagedDiff(root)`)
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "ParseStagedDiff") {
		t.Errorf("exported Go ref should still be extracted, got %v", refNames(refs))
	}
}

// TestExtractRefs_Go_InterfaceMethodNotACall pins the interface-method FP: an
// exported method signature inside a Go `interface { ... }` body is a
// declaration, not a call. reCallIdent matches `ExecContext(` and defNamesInContext only
// recognizes `func`-prefixed lines, so before the fix these method signatures
// were extracted as unresolved calls — flagging any diff that adds an interface.
func TestExtractRefs_Go_InterfaceMethodNotACall(t *testing.T) {
	ls := lines(
		`type migExecer interface {`,
		`	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)`,
		`	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row`,
		`}`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsNone(refs, "ExecContext", "QueryRowContext") {
		t.Errorf("interface method signatures must not be extracted as calls, got %v", refNames(refs))
	}
}

// TestExtractRefs_Go_CallAfterInterfaceStillChecked guards against over-suppression:
// once the interface body closes, a genuine call on a later line must still be
// caught (the suppression is scoped to the interface braces, not the whole hunk).
func TestExtractRefs_Go_CallAfterInterfaceStillChecked(t *testing.T) {
	ls := lines(
		`type Store interface {`,
		`	Save(x int) error`,
		`}`,
		`result := ProcessFoo(ctx)`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "ProcessFoo") {
		t.Errorf("call after interface body should still be extracted, got %v", refNames(refs))
	}
	if !containsNone(refs, "Save") {
		t.Errorf("interface method Save must not be extracted, got %v", refNames(refs))
	}
}

// TestExtractRefs_Go_InterfaceLiteralNotSuppressed guards the regression the
// first cut introduced: `interface{}` also appears in the ubiquitous
// `map[string]interface{}{...}` / `[]interface{}{...}` composite literals. A
// brace tracker that treated that as an interface body suppressed real calls
// inside the literal — a false NEGATIVE (a hidden hallucinated call, the worst
// class). Calls inside such a literal must still be extracted.
func TestExtractRefs_Go_InterfaceLiteralNotSuppressed(t *testing.T) {
	ls := lines(
		`handlers := map[string]interface{}{`,
		`	"run": BuildHandler(),`,
		`}`,
		`items := []interface{}{MakeThing()}`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "BuildHandler", "MakeThing") {
		t.Errorf("calls inside interface{} composite literals must be extracted, got %v", refNames(refs))
	}
}

// TestExtractRefs_Go_GenericConstraintExit guards the net-zero close-line
// regression: a multi-line generic type set `[K interface { ... }]` closes on a
// line like `}](m map[K]V) {` that also opens the function body. A depth counter
// netted to zero and stayed "in interface", suppressing the body's calls. The
// call after the constraint must be extracted.
func TestExtractRefs_Go_GenericConstraintExit(t *testing.T) {
	ls := lines(
		`func Keys[K interface {`,
		`	comparable`,
		`}](m map[K]int) {`,
		`	DoThing()`,
		`}`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "DoThing") {
		t.Errorf("call after a multi-line generic constraint must be extracted, got %v", refNames(refs))
	}
}

func TestExtractRefs_Go_DefLineSkipped(t *testing.T) {
	ls := lines(`func HandleRequest(w http.ResponseWriter, r *http.Request) {`)
	refs := ExtractRefs(LangGo, ls)
	if !containsNone(refs, "HandleRequest") {
		t.Errorf("function definition line should not produce a ref, got %v", refNames(refs))
	}
}

func TestExtractRefs_Python_BareCall(t *testing.T) {
	ls := lines(`result = process_foo(data)`)
	refs := ExtractRefs(LangPython, ls)
	if !containsAll(refs, "process_foo") {
		t.Errorf("expected process_foo in refs, got %v", refNames(refs))
	}
}

func TestExtractRefs_Python_BuiltinsSkipped(t *testing.T) {
	ls := lines(
		`for i in range(10):`,
		`print(len(items))`,
	)
	refs := ExtractRefs(LangPython, ls)
	if !containsNone(refs, "range", "print", "len") {
		t.Errorf("Python builtins should be excluded, got %v", refNames(refs))
	}
}

func TestExtractRefs_JS_BareCall(t *testing.T) {
	ls := lines(`const result = processData(input)`)
	refs := ExtractRefs(LangJS, ls)
	if !containsAll(refs, "processData") {
		t.Errorf("expected processData in refs, got %v", refNames(refs))
	}
}

func TestExtractRefs_JS_ConsoleSkipped(t *testing.T) {
	ls := lines(`console.log("hello")`)
	refs := ExtractRefs(LangJS, ls)
	if !containsNone(refs, "console", "log") {
		t.Errorf("console.log should be entirely skipped, got %v", refNames(refs))
	}
}

// TestExtractRefs_JS_DollarLedCalls pins the `\b`-boundary fix: a `$`-led call
// like `$http(...)` (AngularJS) must resolve to `$http`, not the wrong bare name
// `http`, and a bare `$(...)` (jQuery) must be captured, not missed entirely.
// RE2's `\w` excludes `$`, so the old leading `\b` split these apart.
func TestExtractRefs_JS_DollarLedCalls(t *testing.T) {
	refs := ExtractRefs(LangJS, lines(`$http(config)`, `const el = $('#root')`))
	if !containsAll(refs, "$http", "$") {
		t.Errorf("expected $http and $ as call refs, got %v", refNames(refs))
	}
	if !containsNone(refs, "http") {
		t.Errorf("$http must resolve to $http, not the truncated name http: %v", refNames(refs))
	}
}

// TestAddedLinesWithGap_ResetsStringStateBetweenBlocks pins the MultiEdit fix:
// an unterminated string/template opened in one edit block must not leak its
// open-string state into the next block and blank real calls there. The gap
// AddedLinesWithGap inserts forces the stateful scanner to reset per block.
func TestAddedLinesWithGap_ResetsStringStateBetweenBlocks(t *testing.T) {
	block1 := "const q = `SELECT * FROM" // unterminated template literal
	block2 := "doThing(RealCall())"

	gapped := ExtractRefs(LangJS, AddedLinesWithGap([]string{block1, block2}))
	if !containsAll(gapped, "doThing", "RealCall") {
		t.Errorf("gap must reset open-string state so block 2 calls are seen; got %v", refNames(gapped))
	}

	// Contrast: the old contiguous "\n"-join leaks the open template into block 2
	// and blanks its calls — the bug this fix removes.
	leaked := ExtractRefs(LangJS, TextToAddedLines(block1+"\n"+block2))
	if !containsNone(leaked, "RealCall") {
		t.Errorf("contiguous join was expected to leak string state and miss RealCall, got %v", refNames(leaked))
	}
}

func TestExtractRefs_Unknown_ReturnsNil(t *testing.T) {
	ls := lines(`whatever`)
	refs := ExtractRefs(LangUnknown, ls)
	if refs != nil {
		t.Errorf("expected nil for unknown lang, got %v", refs)
	}
}

// TestExtractRefs_Go_CommentLinesStateDriven verifies the state-driven comment
// handling: `//` line comments and the interior of a genuine /* ... */ block are
// blanked, but a `* `/`*/`-prefixed line that is NOT inside a tracked block
// comment is now scanned as code. The latter is the deliberate FP-over-FN
// tradeoff (a stray `*/` outside a block reads as code, a noisy FP, rather than
// dropping a real `\t* Compute()` multiplication as a silent FN).
func TestExtractRefs_Go_CommentLinesStateDriven(t *testing.T) {
	// A genuine block comment: open, body line, close — all blanked.
	block := lines(
		`/* BlockOpen()`,
		`* InsideBlock()`,
		`*/`,
	)
	refs := ExtractRefs(LangGo, block)
	if !containsNone(refs, "BlockOpen", "InsideBlock") {
		t.Errorf("block-comment interior should produce no refs, got %v", refNames(refs))
	}

	// A `//` line comment is still skipped outright.
	refs = ExtractRefs(LangGo, lines(`// LineComment()`))
	if !containsNone(refs, "LineComment") {
		t.Errorf("// line comment should produce no refs, got %v", refNames(refs))
	}
}

// TestExtractRefs_Go_StarPrefixCodeNotDropped is the regression guard for the
// `* `-prefix FN: a wrapped multiplication starting a line (NOT inside a block
// comment) must be scanned as code, so a hallucinated call there is still caught.
func TestExtractRefs_Go_StarPrefixCodeNotDropped(t *testing.T) {
	ls := lines("\t* Compute()")
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "Compute") {
		t.Errorf("`* `-prefixed code (wrapped multiply) must be scanned, got %v", refNames(refs))
	}
}

// TestExtractRefs_Go_StarInsideBlockComment proves the contrast: the SAME `* X()`
// line, when actually inside a /* ... */ region, is correctly treated as comment.
func TestExtractRefs_Go_StarInsideBlockComment(t *testing.T) {
	ls := lines(
		`/* Documentation block`,
		`* Mentions Fake() here`,
		`*/`,
		`real := Real()`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsNone(refs, "Fake") {
		t.Errorf("`* `-line inside a block comment must not yield refs, got %v", refNames(refs))
	}
	if !containsAll(refs, "Real") {
		t.Errorf("code after the block-comment close must still be scanned, got %v", refNames(refs))
	}
}

// TestExtractRefs_Go_InlineBlockComment verifies a single-line /* ... */ blanks
// its interior but leaves surrounding code visible.
func TestExtractRefs_Go_InlineBlockComment(t *testing.T) {
	ls := lines(`x := /* Hidden() */ Visible()`)
	refs := ExtractRefs(LangGo, ls)
	if !containsNone(refs, "Hidden") {
		t.Errorf("inline block-comment interior should be blanked, got %v", refNames(refs))
	}
	if !containsAll(refs, "Visible") {
		t.Errorf("code after an inline block comment should be scanned, got %v", refNames(refs))
	}
}

// TestExtractRefs_BlockCommentStateResetsOnLineGap documents the conservative
// tradeoff: when a diff-hunk gap resets block-comment state, a `* `-prefixed
// continuation line reads as code (a potential FP) rather than being silently
// dropped (an FN). FP is the safe direction for a truth-oracle.
func TestExtractRefs_BlockCommentStateResetsOnLineGap(t *testing.T) {
	ls := []AddedLine{
		{LineNo: 1, Text: `/* opened here`},
		{LineNo: 80, Text: `* Stranded()`}, // far away → block state reset → scanned as code
	}
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "Stranded") {
		t.Errorf("post-gap `* ` line should be scanned as code (FP-over-FN), got %v", refNames(refs))
	}
}

// --- P1: keyword exclusion (the dominant JS/Py false-positive driver) ---

func TestExtractRefs_Python_KeywordsSkipped(t *testing.T) {
	ls := lines(
		`for x in (1, 2):`,
		`return (value)`,
		`result = a or (b)`,
		`if not (done):`,
		`assert (x > 0)`,
	)
	refs := ExtractRefs(LangPython, ls)
	if !containsNone(refs, "in", "return", "or", "not", "if", "assert") {
		t.Errorf("Python keywords should not be treated as calls, got %v", refNames(refs))
	}
}

func TestExtractRefs_Python_ExceptionsSkipped(t *testing.T) {
	ls := lines(
		`raise ValueError("bad")`,
		`raise FileNotFoundError(path)`,
		`raise RuntimeError()`,
		`x = round(y)`,
		`d = pow(a, b)`,
	)
	refs := ExtractRefs(LangPython, ls)
	if !containsNone(refs, "ValueError", "FileNotFoundError", "RuntimeError", "round", "pow") {
		t.Errorf("Python builtins/exceptions should be excluded, got %v", refNames(refs))
	}
}

func TestExtractRefs_JS_KeywordsSkipped(t *testing.T) {
	ls := lines(
		`for (const x of (items)) {}`,
		`return (value)`,
		`if (x in (obj)) {}`,
		`switch (y) {}`,
		`d = new Date()`,
	)
	refs := ExtractRefs(LangJS, ls)
	if !containsNone(refs, "of", "return", "in", "switch", "Date") {
		t.Errorf("JS keywords/globals should be excluded, got %v", refNames(refs))
	}
}

// --- P1: string-literal & inline-comment stripping ---

func TestExtractRefs_Go_SQLInStringSkipped(t *testing.T) {
	ls := lines(
		`q := "SELECT COUNT(*) FROM t WHERE id IN (1)"`,
		`raw := `+"`"+`INSERT INTO x VALUES (1)`+"`",
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsNone(refs, "COUNT", "IN", "VALUES") {
		t.Errorf("identifiers inside string literals should be skipped, got %v", refNames(refs))
	}
}

func TestExtractRefs_RealCallOutsideStringStillFound(t *testing.T) {
	// Regression guard: stripping strings must NOT suppress a genuine bare call
	// sitting next to a string literal.
	ls := lines(`x := RealCall("SELECT COUNT(*) FROM t")`)
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "RealCall") {
		t.Errorf("real call beside a string should still be found, got %v", refNames(refs))
	}
	if !containsNone(refs, "COUNT") {
		t.Errorf("COUNT inside the string arg should be skipped, got %v", refNames(refs))
	}
}

func TestExtractRefs_Python_InlineCommentSkipped(t *testing.T) {
	ls := lines(`x = realFn(1)  # call fakeFn(2) here later`)
	refs := ExtractRefs(LangPython, ls)
	if !containsAll(refs, "realFn") {
		t.Errorf("expected realFn, got %v", refNames(refs))
	}
	if !containsNone(refs, "fakeFn") {
		t.Errorf("call inside a trailing comment should be skipped, got %v", refNames(refs))
	}
}

func TestExtractRefs_HashInStringIsNotComment(t *testing.T) {
	// A '#' inside a string must NOT start a comment that swallows a real call.
	ls := lines(`url = build("http://x#frag") + realFn(1)`)
	refs := ExtractRefs(LangPython, ls)
	if !containsAll(refs, "build", "realFn") {
		t.Errorf("'#' inside a string must not be treated as a comment, got %v", refNames(refs))
	}
}

func TestStripLiterals_PreservesLength(t *testing.T) {
	cases := []struct {
		lang Lang
		in   string
	}{
		{LangGo, `q := "SELECT COUNT(*)" // trailing`},
		{LangPython, `x = f('a\'b') # note`},
		{LangJS, "const s = `tmpl ${x}` // c"},
	}
	for _, tc := range cases {
		got := stripLiterals(tc.lang, tc.in)
		if len(got) != len(tc.in) {
			t.Errorf("stripLiterals(%q) changed length: %d != %d", tc.in, len(got), len(tc.in))
		}
	}
}

func TestStripLiterals_EscapedQuote(t *testing.T) {
	// The escaped quote must not be treated as the closing delimiter, so the call
	// after the string stays visible. (Exported name so Go's unexported-skip rule
	// doesn't drop it for an unrelated reason.)
	ls := lines(`x := "a\"b" + RealFn(1)`)
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "RealFn") {
		t.Errorf("escaped quote mishandled; RealFn lost. refs=%v", refNames(refs))
	}
}

// --- P-fix: Python f-string interpolation scanning ---

// TestExtractRefs_Python_FStringInterpolation is the regression guard for the
// f-string FN: a genuine call inside f"{...}" must be found, while literal text
// (and {{ }} escapes) outside the interpolation stays blanked.
func TestExtractRefs_Python_FStringInterpolation(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string // refs that MUST appear
		nope []string // refs that must NOT appear
	}{
		{"basic", `x = f"{Build(y)}"`, []string{"Build"}, nil},
		{"prose-outside-interp", `x = f"call Foo() here {Bar(y)}"`, []string{"Bar"}, []string{"Foo"}},
		{"brace-escapes", `x = f"{{Literal()}} real {Build(y)}"`, []string{"Build"}, []string{"Literal"}},
		{"nested-calls", `x = f"{Outer(Inner(z))}"`, []string{"Outer", "Inner"}, nil},
		{"rf-prefix", `x = rf"{Build(y)}"`, []string{"Build"}, nil},
		{"fr-prefix", `x = fr"{Build(y)}"`, []string{"Build"}, nil},
		{"upper-F-prefix", `x = F"{Build(y)}"`, []string{"Build"}, nil},
		{"plain-string-unaffected", `x = "call Foo() here"`, nil, []string{"Foo"}},
		{"b-prefix-not-fstring", `x = b"{Foo()}"`, nil, []string{"Foo"}},
		{"two-interps", `x = f"{First(a)}-{Second(b)}"`, []string{"First", "Second"}, nil},
		{"dict-literal-in-interp", `x = f"{lookup[Key(k)]}"`, []string{"Key"}, nil},
		// A string literal nested INSIDE the interpolation is data, not code. Leaving
		// its bytes intact reported the parenthesised text as a bare call: an aligned
		// table header written as f"{'acc(curr)':>10}" flagged `acc` as a
		// hallucinated symbol on working code. Found by the #243 differential harness
		// against CPython's ast, on real repo source.
		{"nested-literal-in-format-spec", `x = f"{'acc(curr)':>10}"`, nil, []string{"acc"}},
		{"nested-literal-double-quoted", `x = f'{"ll(prev)":>9}'`, nil, []string{"ll"}},
		{"nested-literal-dict-key", `x = f"{d['count(x)']}"`, nil, []string{"count"}},
		// ...and the FN guard for that fix: masking the nested literal must not
		// swallow a genuine call that follows it in the same interpolation.
		{"call-after-nested-literal", `x = f"{Fmt('x(1)') + Compute(y)}"`, []string{"Compute"}, []string{"x"}},
		{"call-before-nested-literal", `x = f"{Build(y)} {'z(2)'}"`, []string{"Build"}, []string{"z"}},
		// A nested F-STRING is code, not data: blanking it would drop a real call and
		// trade the #256 false positive for a false negative, the worse direction.
		{"nested-fstring-single-in-double", `msg = f"outer {f'{Build(x)}'} tail"`, []string{"Build"}, nil},
		{"nested-fstring-double-in-single", `msg = f'outer {f"{Build(x)}"} tail'`, []string{"Build"}, nil},
		{"nested-fstring-keeps-outer-fix", `msg = f"{f'{Build(x)}'} {'z(2)'}"`, []string{"Build"}, []string{"z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs := ExtractRefs(LangPython, lines(tc.line))
			if len(tc.want) > 0 && !containsAll(refs, tc.want...) {
				t.Errorf("want %v in refs, got %v", tc.want, refNames(refs))
			}
			if len(tc.nope) > 0 && !containsNone(refs, tc.nope...) {
				t.Errorf("did not want %v, got %v", tc.nope, refNames(refs))
			}
		})
	}
}

// TestStripLiterals_FStringPreservesLength keeps the load-bearing invariant: the
// f-string scan path must still be length-preserving so LineNo/indices stay honest.
func TestStripLiterals_FStringPreservesLength(t *testing.T) {
	cases := []string{
		`x = f"{Build(y)}"`,
		`x = f"{{esc}} {Call(z)}"`,
		`x = f"prose {A(b)} more {C(d)}"`,
		`x = rf"\d+ {Match(p)}"`,
	}
	for _, in := range cases {
		got := stripLiterals(LangPython, in)
		if len(got) != len(in) {
			t.Errorf("stripLiterals(%q) changed length: %d != %d (%q)", in, len(got), len(in), got)
		}
	}
}

// --- import extraction ---

func TestExtractImports_Python(t *testing.T) {
	ls := lines(
		`from pathlib import Path`,
		`from datetime import datetime, timedelta`,
		`from x.y import (a, b as B)`,
		`import os`,
		`import numpy as np`,
		`import a.b.c`,
		`from m import *`,
		`from careers import (`,
		`    _slug,`,
		`    _normalize as norm,`,
		`)`,
	)
	got := ExtractImports(LangPython, ls)
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	for _, want := range []string{"Path", "datetime", "timedelta", "a", "B", "os", "np", "_slug", "norm"} {
		if !gotSet[want] {
			t.Errorf("expected import %q bound, got %v", want, got)
		}
	}
	if gotSet["c"] {
		t.Errorf("`import a.b.c` should bind `a`, not `c`; got %v", got)
	}
}

// TestExtractImports_PythonInlineComment pins #144: an inline `# ...` comment on a
// Python import must not drop names. Before the fix, strings.Trim(list, "()") left
// the comment attached to the last name so it failed reIdent and was dropped —
// a later bare call to it then read as a hallucination (guard false positive).
func TestExtractImports_PythonInlineComment(t *testing.T) {
	ls := lines(
		`from mod import (Foo, Bar)  # see notes`, // single-line paren + comment
		`from other import Baz  # a note`,         // single name + comment
		`import os  # stdlib`,                     // plain import + comment
		`from pkg import (  # opens the group`,    // multi-line opener, ) only in comment
		`    Alpha,`,
		`    Beta as B,  # aliased`,
		`)  # close`,
	)
	got := ExtractImports(LangPython, ls)
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	for _, want := range []string{"Foo", "Bar", "Baz", "os", "Alpha", "B"} {
		if !gotSet[want] {
			t.Errorf("expected import %q bound despite inline comment, got %v", want, got)
		}
	}
}

// A `from m import (` opens multi-line paren state; a non-contiguous line (a
// LineNo jump, i.e. a separate diff hunk) must reset it so the continuation is
// not misread as imported names. Mirrors the ExtractRefs multi-line-state reset.
func TestExtractImports_PythonParenStateResetsOnLineGap(t *testing.T) {
	ls := []AddedLine{
		{LineNo: 1, Text: `from m import (`},
		{LineNo: 50, Text: `    notAnImport,`}, // far away → paren state reset
	}
	for _, n := range ExtractImports(LangPython, ls) {
		if n == "notAnImport" {
			t.Errorf("paren state should reset across a line gap; bound %q", n)
		}
	}
}

// A single-line triple-quoted f-string interpolates: the call inside {…} is real
// and must be extracted, while a non-f triple-quoted string's prose must not be.
func TestExtractRefs_Python_TripleQuotedFString(t *testing.T) {
	hasRef := func(rs []Ref, name string) bool {
		for _, r := range rs {
			if r.Name == name {
				return true
			}
		}
		return false
	}
	if got := ExtractRefs(LangPython, lines(`x = f"""pre {Build(y)} post"""`)); !hasRef(got, "Build") {
		t.Errorf("call inside a single-line triple-quoted f-string should be extracted; got %v", got)
	}
	if got := ExtractRefs(LangPython, lines(`x = """just docs Build(y) here"""`)); hasRef(got, "Build") {
		t.Errorf("text inside a non-f triple-quoted string must be blanked; got %v", got)
	}
}

// A single-line f-string whose interpolation's own '(' runs past end of line
// (valid Python 3.12+: a call spanning lines inside f"{...}") must propagate the
// quote as the open multi-line-string delimiter so the continuation line's
// closing quote correctly ends the string, instead of being misread as opening
// a fresh one — which would blank the trailing `+ extra()` call on line 3.
func TestExtractRefs_Python_FStringInterpolationSpansLines(t *testing.T) {
	l1 := `msg = f"Total: {sum(`
	l2 := `    1, 2, 3,`
	l3 := `)}" + extra()`
	refs := ExtractRefs(LangPython, lines(l1, l2, l3))
	if !containsAll(refs, "extra") {
		t.Errorf("call after the closing f-string on the continuation line should be extracted; got %v", refNames(refs))
	}
}

func TestExtractImports_JS(t *testing.T) {
	ls := lines(
		`import fs from 'fs'`,
		`import { readFileSync, existsSync as exists } from 'fs'`,
		`import * as path from 'path'`,
		`const { join, resolve } = require('path')`,
		`const lodash = require('lodash')`,
	)
	got := ExtractImports(LangJS, ls)
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	for _, want := range []string{"fs", "readFileSync", "exists", "path", "join", "resolve", "lodash"} {
		if !gotSet[want] {
			t.Errorf("expected JS import %q bound, got %v", want, got)
		}
	}
}

// TestExtractImports_JSMultiLine pins #304: a multi-line named-import block is
// the dominant style in any real TS codebase, and ExtractImports previously
// returned nothing for it — a bare call to a multi-line-imported name then read
// as a hallucination. Repro is the exact shape from the issue.
func TestExtractImports_JSMultiLine(t *testing.T) {
	ls := lines(
		`import {`,
		`  makeSeededRandom,`,
		`  depthFor,`,
		`} from './track-b-core';`,
		``,
		`import { alpha, beta } from './x';`,
		``,
		`const r = makeSeededRandom(1);`,
	)
	got := ExtractImports(LangJS, ls)
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	for _, want := range []string{"makeSeededRandom", "depthFor", "alpha", "beta"} {
		if !gotSet[want] {
			t.Errorf("expected multi-line JS import %q bound, got %v", want, got)
		}
	}
}

// A default-plus-named multi-line import (`import Default, {\n  a,\n} from 'm'`)
// must bind both the default export and the named members.
func TestExtractImports_JSMultiLineDefaultPlusNamed(t *testing.T) {
	ls := lines(
		`import React, {`,
		`  useState,`,
		`  useEffect,`,
		`} from 'react';`,
	)
	got := ExtractImports(LangJS, ls)
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	for _, want := range []string{"React", "useState", "useEffect"} {
		if !gotSet[want] {
			t.Errorf("expected %q bound, got %v", want, got)
		}
	}
}

// TestExtractImports_JSMultiLineCommentContainingFrom pins a code-review finding
// on #319: a comment line inside the block that happens to contain the
// standalone word "from" (a realistic pattern, e.g. "pulled from utils") must
// not be mistaken for the closing `from` clause — that misread the block as
// still-open, the buffer never found a real terminator on a later masked line,
// and every name in the block was silently dropped (the exact FP class #304 was
// meant to fix).
func TestExtractImports_JSMultiLineCommentContainingFrom(t *testing.T) {
	ls := lines(
		`import {`,
		`  // pulled from utils`,
		`  foo,`,
		`} from './utils';`,
		``,
		`foo(1);`,
	)
	got := ExtractImports(LangJS, ls)
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	if !gotSet["foo"] {
		t.Errorf("expected %q bound despite a comment containing \"from\" inside the block, got %v", "foo", got)
	}
}

// TestExtractImports_JSMultiLineFromOnOwnLine pins a code-review finding on
// #319 (Bug 2): a valid style that puts `from` on its own line while the named
// list is already balanced on the `import` line (`import { a, b, c }\n  from
// './m';`) was never detected as multi-line by the old unbalanced-brace opener
// check, so the whole import was silently skipped.
func TestExtractImports_JSMultiLineFromOnOwnLine(t *testing.T) {
	ls := lines(
		`import { a, b, c }`,
		`  from './m';`,
		``,
		`const r = a(1);`,
	)
	got := ExtractImports(LangJS, ls)
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !gotSet[want] {
			t.Errorf("expected %q bound when `from` sits on its own line, got %v", want, got)
		}
	}
}

// A side-effect-only import (`import './styles.css';`) binds no names and must
// never be mistaken for an unterminated multi-line import opener — it starts
// with `import` and carries no `from` clause, same as a genuine multi-line
// opener, but is already a complete statement.
func TestExtractImports_JSSideEffectImportDoesNotOpenMultiLine(t *testing.T) {
	ls := lines(
		`import './styles.css';`,
		`import { real } from './m';`,
	)
	got := ExtractImports(LangJS, ls)
	gotSet := map[string]bool{}
	for _, n := range got {
		gotSet[n] = true
	}
	if !gotSet["real"] {
		t.Errorf("side-effect import must not swallow the next import as a continuation; got %v", got)
	}
	if len(got) != 1 {
		t.Errorf("side-effect import should bind no names of its own, got %v", got)
	}
}

// A multi-line import block split across a diff-hunk gap must not leak its
// continuation state into unrelated code (mirrors the Python paren-reset test).
func TestExtractImports_JSMultiLineStateResetsOnLineGap(t *testing.T) {
	ls := []AddedLine{
		{LineNo: 1, Text: `import {`},
		{LineNo: 50, Text: `  notAnImport,`}, // far away → multi-line state reset
	}
	for _, n := range ExtractImports(LangJS, ls) {
		if n == "notAnImport" {
			t.Errorf("multi-line import state should reset across a line gap; bound %q", n)
		}
	}
}

func TestExtractRefs_ImportedNameResolvesViaInFileContext(t *testing.T) {
	// When the import binding is in the line set (as it is when the whole file is
	// scanned), a bare call to the imported name is not flagged. Simulated here by
	// folding ExtractImports output into the diff's own definitions is done by the
	// caller; here we assert the binding name itself is extractable.
	imps := ExtractImports(LangPython, lines(`from pathlib import Path`))
	if len(imps) != 1 || imps[0] != "Path" {
		t.Fatalf("Path import not bound: %v", imps)
	}
}

// --- multi-line string stripping ---

func TestExtractRefs_Python_TripleQuotedSQLSkipped(t *testing.T) {
	ls := lines(
		`query = """`,
		`    INSERT INTO t (a, b)`,
		`    VALUES (1, 2)`,
		`"""`,
		`row = realFetch(query)`,
	)
	refs := ExtractRefs(LangPython, ls)
	if !containsNone(refs, "VALUES", "INSERT", "INTO") {
		t.Errorf("identifiers inside a triple-quoted string should be skipped, got %v", refNames(refs))
	}
	if !containsAll(refs, "realFetch") {
		t.Errorf("call after the closing triple-quote should still be found, got %v", refNames(refs))
	}
}

func TestExtractRefs_Python_DocstringProseSkipped(t *testing.T) {
	ls := lines(
		`def f():`,
		`    """`,
		`    Consume (and project) the value.`,
		`    """`,
		`    return helper()`,
	)
	refs := ExtractRefs(LangPython, ls)
	if !containsNone(refs, "Consume") {
		t.Errorf("docstring prose should not yield refs, got %v", refNames(refs))
	}
}

func TestExtractRefs_MultilineStateResetsOnLineGap(t *testing.T) {
	// Non-consecutive LineNos (a diff hunk gap) must reset string state so a stray
	// open triple-quote in one hunk does not silently blank a later hunk.
	ls := []AddedLine{
		{LineNo: 1, Text: `x = """unterminated`},
		{LineNo: 50, Text: `y = RealCall(z)`}, // far away → state reset
	}
	refs := ExtractRefs(LangGo, ls) // Go: backtick not triple, but tests the gap reset path generically
	_ = refs
	// Use JS where the open delimiter matters:
	ls2 := []AddedLine{
		{LineNo: 1, Text: "s = `unterminated template"},
		{LineNo: 99, Text: `r = RealCall(z)`},
	}
	refs2 := ExtractRefs(LangJS, ls2)
	if !containsAll(refs2, "RealCall") {
		t.Errorf("line-gap should reset multi-line string state so RealCall is seen, got %v", refNames(refs2))
	}
}

func TestLangFor(t *testing.T) {
	cases := []struct {
		path string
		lang Lang
	}{
		{"foo.go", LangGo},
		{"src/bar.ts", LangJS},
		{"baz.jsx", LangJS},
		{"qux.gs", LangJS},
		{"esm.mjs", LangJS},
		{"commonjs.cjs", LangJS},
		{"script.py", LangPython},
		{"data.json", LangUnknown},
		// Shell is intentionally parser-only: the ShellParser indexes .sh/.bash
		// functions for the IR, but the guard's hallucination check stays OUT of
		// shell (a bare command is indistinguishable from an external binary), so
		// LangFor must keep reporting Unknown for these — do NOT "fix" this to
		// match the parser.
		{"deploy.sh", LangUnknown},
		{"lib.bash", LangUnknown},
	}
	for _, tc := range cases {
		if got := LangFor(tc.path); got != tc.lang {
			t.Errorf("LangFor(%q) = %q, want %q", tc.path, got, tc.lang)
		}
	}
}

// --- #56: non-call reference extraction (const refs + type annotations) ---
// Positives prove the new catches; the negatives are the load-bearing half —
// they pin that the false-positive surface stayed flat.

func TestExtractRefs_Python_ConstRef(t *testing.T) {
	ls := lines(`            kind = TASTING_ROOM_KIND[t]`)
	refs := ExtractRefs(LangPython, ls)
	if !containsAll(refs, "TASTING_ROOM_KIND") {
		t.Errorf("expected SCREAMING_SNAKE const ref, got %v", refNames(refs))
	}
}

func TestExtractRefs_Python_ConstDefNotARef(t *testing.T) {
	// A constant definition line is a def, not a use — must not self-flag, and
	// ExtractDefs must capture it so references elsewhere resolve.
	ls := lines(`MAX_SIZE = 100`)
	if refs := ExtractRefs(LangPython, ls); !containsNone(refs, "MAX_SIZE") {
		t.Errorf("const definition should not be a ref, got %v", refNames(refs))
	}
	if defs := ExtractDefs(LangPython, ls); !containsStr(defs, "MAX_SIZE") {
		t.Errorf("ExtractDefs should capture the const def, got %v", defs)
	}
}

func TestExtractRefs_Python_ConstNegatives(t *testing.T) {
	ls := lines(
		`data[i] = row`,       // lowercase local — not a const
		`cfg.MAX_RETRIES = 3`, // qualified attribute — skip
		`HTTP = 1`,            // single segment, no underscore — not matched
	)
	refs := ExtractRefs(LangPython, ls)
	if !containsNone(refs, "MAX_RETRIES", "HTTP", "data", "row") {
		t.Errorf("const negatives leaked, got %v", refNames(refs))
	}
}

func TestExtractRefs_JS_TypeRef(t *testing.T) {
	ls := lines(`  ctx: RouteContext<"/api/x">`)
	refs := ExtractRefs(LangJS, ls)
	if !containsAll(refs, "RouteContext") {
		t.Errorf("expected type-annotation ref, got %v", refNames(refs))
	}
}

func TestExtractRefs_JS_TypeNegatives(t *testing.T) {
	ls := lines(
		`  name: string`,         // primitive type
		`  items: Array<number>`, // jsBuiltin generic
		`  cfg: Partial<Config>`, // utility type (Partial); Config not after a param colon
		`  x: T`,                 // single-char generic param
		`  cb: ns.Handler`,       // qualified type
	)
	refs := ExtractRefs(LangJS, ls)
	if !containsNone(refs, "string", "Array", "Partial", "T", "Handler") {
		t.Errorf("type negatives leaked, got %v", refNames(refs))
	}
}

func TestExtractDefs_JS_TypeDecls(t *testing.T) {
	ls := lines(
		`export interface Props {`,
		`type RouteId = string`,
		`enum Color { Red }`,
		`export class Widget {}`,
	)
	defs := ExtractDefs(LangJS, ls)
	for _, want := range []string{"Props", "RouteId", "Color", "Widget"} {
		if !containsStr(defs, want) {
			t.Errorf("ExtractDefs should capture %q, got %v", want, defs)
		}
	}
}

// End-to-end via Run: a locally-defined type/const used in annotation/subscript
// position must NOT be flagged, while an undefined one must be — the FP-safety
// proof, not just extraction.
func TestRun_NonCallRefs_ResolveAgainstLocalDefs(t *testing.T) {
	diffs := []FileDiff{{
		Path: "x.ts",
		AddedLines: lines(
			`interface LocalCfg {}`,
			`function h(c: LocalCfg, ctx: RouteContext) {}`,
		),
	}}
	v := Run(map[string]struct{}{}, "", diffs)
	if names := violationSymbols(v); containsStr(names, "LocalCfg") {
		t.Errorf("locally-defined type must not be flagged, got %v", names)
	}
	if names := violationSymbols(v); !containsStr(names, "RouteContext") {
		t.Errorf("undefined type must be flagged, got %v", names)
	}
}

func violationSymbols(vs []Violation) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Symbol
	}
	return out
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// TestIsFStringPrefix_RejectsNonFCombinations pins #274: isFStringPrefix must
// only return true for a valid f-string prefix (bare f/F, or rf/fr in any
// case), not merely for any prefix run CONTAINING an f — bf/fb/ff are real
// prefix-letter combinations but none of them are f-strings.
func TestIsFStringPrefix_RejectsNonFCombinations(t *testing.T) {
	cases := map[string]bool{
		`f"x"`: true, `F"x"`: true, `rf"x"`: true, `fr"x"`: true,
		`Rf"x"`: true, `fR"x"`: true, `RF"x"`: true, `FR"x"`: true,
		`bf"x"`: false, `fb"x"`: false, `ff"x"`: false,
		`b"x"`: false, `r"x"`: false, `br"x"`: false, `rb"x"`: false,
	}
	for s, want := range cases {
		qi := strings.IndexByte(s, '"')
		if got := isFStringPrefix([]byte(s), qi); got != want {
			t.Errorf("isFStringPrefix(%q) = %v, want %v", s, got, want)
		}
	}
}

// TestParseJSBindingTarget_ArrayDestructure pins #275: `const [a, b] =
// require('m')` must bind both a and b, not fall through to the
// bare-identifier branch and return nil.
func TestParseJSBindingTarget_ArrayDestructure(t *testing.T) {
	got := parseJSBindingTarget("[a, b]")
	want := []string{"a", "b"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("parseJSBindingTarget(%q) = %v, want %v", "[a, b]", got, want)
	}
}

// TestParseJSBindingTarget_NestedObjectDestructure pins #276: a nested object
// destructuring target (`{a: {b, c}}`) must bind both b and c. The former
// strings.Trim(s, "{}") cutset trim stripped only the outermost brace pair,
// then split on "," produced a corrupted "{b" item that silently dropped b.
func TestParseJSBindingTarget_NestedObjectDestructure(t *testing.T) {
	got := parseJSBindingTarget("{a: {b, c}}")
	found := map[string]bool{}
	for _, g := range got {
		found[g] = true
	}
	if !found["b"] || !found["c"] {
		t.Errorf("parseJSBindingTarget(%q) = %v, want b AND c present", "{a: {b, c}}", got)
	}
}

// pyCtxFor builds the per-line brace context appendConstRefs and
// defNamesInContext take, the same way extractRefs does: mask the line, then
// read it starting from base — the {}-depth a real caller's cross-line tracking
// would have carried into this line.
func pyCtxFor(text string, base int) pyLineCtx {
	_, braceScan, _ := stripLiteralsBraces(LangPython, text, "")
	return pyLineCtx{scan: braceScan, base: base}
}

// defNamesNoContext is defNamesInContext for one line with no cross-line state.
// Production callers always have a masked scan in hand from their own scanning
// pass and hand it over, so no such wrapper exists outside tests.
func defNamesNoContext(lang Lang, text string) map[string]struct{} {
	_, braceScan, _ := stripLiteralsBraces(lang, text, "")
	return defNamesInContext(lang, text, pyLineCtx{scan: braceScan})
}

// TestAppendConstRefs_DictKeyIsAReference pins #277: a SCREAMING_SNAKE name
// used as a dict key (`{MAX_VALUE: 5}`) is a genuine USE of the constant, not
// a type-annotation definition target, even though the text after it (`: 5`)
// looks identical to one. The distinguishing signal is position: MAX_VALUE is
// preceded by `{`, not by only whitespace back to the start of the line.
func TestAppendConstRefs_DictKeyIsAReference(t *testing.T) {
	refs := appendConstRefs(nil, map[string]struct{}{}, `result = {MAX_VALUE: 5}`, 1, nil, nil, pyCtxFor(`result = {MAX_VALUE: 5}`, 0))
	if len(refs) != 1 || refs[0].Name != "MAX_VALUE" {
		t.Errorf("got %+v, want a single MAX_VALUE reference", refs)
	}
}

// TestAppendConstRefs_AnnotationTargetStillSkipped guards the case
// TestAppendConstRefs_DictKeyIsAReference's fix must not break: a genuine
// statement-start type annotation (`MAX_VALUE: int = 5`) is still a
// definition, not a use.
func TestAppendConstRefs_AnnotationTargetStillSkipped(t *testing.T) {
	refs := appendConstRefs(nil, map[string]struct{}{}, `MAX_VALUE: int = 5`, 1, nil, nil, pyCtxFor(`MAX_VALUE: int = 5`, 0))
	if len(refs) != 0 {
		t.Errorf("got %+v, want none — this is a definition, not a use", refs)
	}
}

// TestAppendConstRefs_MultiLineDictKeyIsAReference pins #289: a dict key on
// its own line inside a multi-line literal (`result = {\n    MAX_VALUE: 5,\n}`)
// is preceded by only whitespace on ITS line — indistinguishable, at a single-
// line position check, from a genuine statement-start annotation. The caller
// (ExtractRefs) tracks whether a `{` opened on an earlier line is still
// unclosed and threads that in as the context's starting depth; when it is
// non-zero, a name preceded by whitespace must still be read as a dict key,
// not a definition.
func TestAppendConstRefs_MultiLineDictKeyIsAReference(t *testing.T) {
	refs := appendConstRefs(nil, map[string]struct{}{}, `    MAX_VALUE: 5,`, 2, nil, nil, pyCtxFor(`    MAX_VALUE: 5,`, 1))
	if len(refs) != 1 || refs[0].Name != "MAX_VALUE" {
		t.Errorf("got %+v, want a single MAX_VALUE reference", refs)
	}

	// Same line shape, but at depth 0 (a true statement-start) — still a
	// definition, guarding against the fix over-firing.
	notInDict := appendConstRefs(nil, map[string]struct{}{}, `    MAX_VALUE: 5,`, 2, nil, nil, pyCtxFor(`    MAX_VALUE: 5,`, 0))
	if len(notInDict) != 0 {
		t.Errorf("got %+v, want none — not inside an open brace, this is a definition", notInDict)
	}
}

// TestExtractRefs_MultiLineDictKey is the end-to-end pin for #289: a real
// multi-line dict literal fed through the full ExtractRefs pipeline (which is
// what threads the cross-line brace-depth state) must flag the key as a
// reference.
func TestExtractRefs_MultiLineDictKey(t *testing.T) {
	lines := []AddedLine{
		{LineNo: 1, Text: "result = {"},
		{LineNo: 2, Text: "    MAX_VALUE: 5,"},
		{LineNo: 3, Text: "}"},
	}
	refs := ExtractRefs(LangPython, lines)
	found := false
	for _, r := range refs {
		if r.Name == "MAX_VALUE" {
			found = true
		}
	}
	if !found {
		t.Errorf("ExtractRefs(%+v) = %+v, want MAX_VALUE reference", lines, refs)
	}
}

// TestStripLiteralsBraces_FStringBracesExcludedFromBraceScan pins #291 at the
// masking layer: an f-string interpolation's braces survive stripping on purpose
// (so the call inside `f"{Build(x)}"` is still scanned), but they are string
// syntax, not dict nesting — braceScan, the scan the {}-depth counters read,
// must not contain them. scan itself must still carry them, or the interpolated
// call stops being seen.
func TestStripLiteralsBraces_FStringBracesExcludedFromBraceScan(t *testing.T) {
	for _, tc := range []struct {
		name     string
		text     string
		open     string
		wantScan string // braces the code scan must keep
	}{
		{"unterminated interpolation", `msg = f"{compute(`, "", "{"},
		{"balanced interpolation", `msg = f"{compute(a)}"`, "", "{}"},
		{"triple-quoted f-string", `msg = f"""{compute(`, "", "{"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scan, braceScan, _ := stripLiteralsBraces(LangPython, tc.text, tc.open)
			if got := onlyBraces(scan); got != tc.wantScan {
				t.Errorf("scan braces = %q, want %q (scan=%q)", got, tc.wantScan, scan)
			}
			if got := onlyBraces(braceScan); got != "" {
				t.Errorf("braceScan braces = %q, want none (braceScan=%q)", got, braceScan)
			}
			if len(scan) != len(tc.text) || len(braceScan) != len(tc.text) {
				t.Errorf("length not preserved: scan=%d braceScan=%d, want %d", len(scan), len(braceScan), len(tc.text))
			}
		})
	}

	// A real dict literal on an f-string line keeps its braces in braceScan —
	// only the interpolation's are neutralized.
	_, braceScan, _ := stripLiteralsBraces(LangPython, `d = {"k": f"{v}"}`, "")
	if got := onlyBraces(braceScan); got != "{}" {
		t.Errorf("braceScan braces = %q, want the dict's own pair (braceScan=%q)", got, braceScan)
	}
}

func onlyBraces(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '{' || s[i] == '}' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// TestExtractDefs_FStringInterpolationDoesNotLeakBraceDepth is #291's end-to-end
// pin. A call spanning lines inside an f-string interpolation leaves its `{` on
// the opening line while the matching `}` lands on a continuation line that the
// propagated open-quote state blanks wholesale. Counting braces on the code scan
// therefore adds a `+1` that never comes back down, and every later constant in
// the run is misread as a dict key — a false positive on a real, in-hunk
// definition.
func TestExtractDefs_FStringInterpolationDoesNotLeakBraceDepth(t *testing.T) {
	lines := []AddedLine{
		{LineNo: 1, Text: `msg = f"{compute(`},
		{LineNo: 2, Text: `    a, b`},
		{LineNo: 3, Text: `)}"`},
		{LineNo: 4, Text: `MAX_RETRIES = 5`},
	}
	defs := ExtractDefs(LangPython, lines)
	if !containsName(defs, "MAX_RETRIES") {
		t.Errorf("ExtractDefs = %v, want MAX_RETRIES — the f-string's brace is not dict nesting", defs)
	}
	// The reference side must agree: MAX_RETRIES is a definition here, so it is
	// not emitted as a use.
	for _, r := range ExtractRefs(LangPython, lines) {
		if r.Name == "MAX_RETRIES" {
			t.Errorf("ExtractRefs emitted MAX_RETRIES as a reference; it is a definition")
		}
	}
}

// TestConstAfterDictCloseOnSameLine pins #292: a dict literal's closing `}` and
// a fresh top-level constant definition can share one physical line
// (`}; MAX_TIMEOUT = 30`). The brace state that matters is the one at the NAME's
// own offset — after the close — not the line-start snapshot, and rePyConstDef's
// `^` anchor has to be applied per top-level `;`-separated statement to see the
// definition at all. Both passes must agree, or a later use of the constant is
// flagged unresolved on an edit that invented nothing.
func TestConstAfterDictCloseOnSameLine(t *testing.T) {
	lines := []AddedLine{
		{LineNo: 1, Text: `cfg = {`},
		{LineNo: 2, Text: `    "a": 1,`},
		{LineNo: 3, Text: `}; MAX_TIMEOUT = 30`},
		{LineNo: 4, Text: `use(MAX_TIMEOUT)`},
	}
	defs := ExtractDefs(LangPython, lines)
	if !containsName(defs, "MAX_TIMEOUT") {
		t.Errorf("ExtractDefs = %v, want MAX_TIMEOUT — it is defined after the dict closes", defs)
	}
	// Pass 2 must not emit the definition line itself as a reference. The later
	// bare use (line 4) legitimately is one, and resolves against the defs above.
	for _, r := range ExtractRefs(LangPython, lines) {
		if r.Name == "MAX_TIMEOUT" && r.LineNo == 3 {
			t.Errorf("ExtractRefs emitted MAX_TIMEOUT at line 3 as a reference; that line defines it")
		}
	}

	// The neighbouring shape must not regress: a key inside the still-open dict
	// is a reference, and a `;` inside the literal does not start a statement.
	inDict := ExtractRefs(LangPython, []AddedLine{
		{LineNo: 1, Text: `cfg = {`},
		{LineNo: 2, Text: `    MAX_VALUE: 5,`},
		{LineNo: 3, Text: `}`},
	})
	if !containsRef(inDict, "MAX_VALUE") {
		t.Errorf("ExtractRefs = %+v, want MAX_VALUE reference — still inside the open dict", inDict)
	}
}

func containsRef(refs []Ref, want string) bool {
	for _, r := range refs {
		if r.Name == want {
			return true
		}
	}
	return false
}

// TestConstDefsAfterDictCloseMultiTarget pins the multi-target arm of #292's
// shape, found by code review on this PR. Applying the per-statement split to
// rePyConstDef alone left `}; MAX_A, MAX_B = 1, 2` and `}; MAX_A = MAX_B = 1`
// flagging names the edit plainly defines: rePyTupleAssignTargets is `^`-anchored
// and pyChainedAssignTargets read the whole line, so neither could see past the
// line's first statement either.
func TestConstDefsAfterDictCloseMultiTarget(t *testing.T) {
	for _, tc := range []struct {
		last     string
		targets  []string
		wantDefs bool // chained targets land in defs; tuple targets are only ref-suppressed
	}{
		{`}; MAX_A, MAX_B = 1, 2`, []string{"MAX_A", "MAX_B"}, false},
		{`}; MAX_A = MAX_B = 1`, []string{"MAX_A", "MAX_B"}, true},
	} {
		t.Run(tc.last, func(t *testing.T) {
			lines := []AddedLine{
				{LineNo: 1, Text: `cfg = {`},
				{LineNo: 2, Text: `    "a": 1,`},
				{LineNo: 3, Text: tc.last},
			}
			// Neither arm may report a target as an unresolved use — that is the
			// false positive. Tuple targets have never been added to defs (they are
			// suppressed at the reference site instead), so only the chained arm
			// asserts on ExtractDefs.
			for _, r := range ExtractRefs(LangPython, lines) {
				if containsStr(tc.targets, r.Name) {
					t.Errorf("ExtractRefs emitted %q as a reference; that line defines it", r.Name)
				}
			}
			if !tc.wantDefs {
				return
			}
			defs := ExtractDefs(LangPython, lines)
			for _, want := range tc.targets {
				if !containsStr(defs, want) {
					t.Errorf("ExtractDefs = %v, missing %s — it is defined after the dict closes", defs, want)
				}
			}
		})
	}
}

// TestPyConstDefNames_ComparisonIsNotADefinition pins the `==` hole code review
// found: rePyConstDef's `\s*[:=]` matches the FIRST `=` of a `==`, so a
// comparison was captured as a definition and folded into the known set — a
// hallucinated constant used only in a comparison would never be checked.
// Running the regex at every top-level `;` offset made it reachable in more
// places, which is why the guard appendConstRefs already carried belongs here.
func TestPyConstDefNames_ComparisonIsNotADefinition(t *testing.T) {
	for _, text := range []string{
		`MAX_VALUE == 2`,
		`x = 1; MAX_VALUE == 2`,
	} {
		if got := pyConstDefNames(pyCtxFor(text, 0)); len(got) != 0 {
			t.Errorf("pyConstDefNames(%q) = %v, want none — this is a comparison", text, got)
		}
	}
	// The genuine definitions must survive the guard.
	for _, tc := range []struct{ text, want string }{
		{`MAX_VALUE = 2`, "MAX_VALUE"},
		{`MAX_VALUE: int = 2`, "MAX_VALUE"},
		{`x = 1; MAX_VALUE = 2`, "MAX_VALUE"},
	} {
		if got := pyConstDefNames(pyCtxFor(tc.text, 0)); !containsStr(got, tc.want) {
			t.Errorf("pyConstDefNames(%q) = %v, missing %s", tc.text, got, tc.want)
		}
	}
}

// TestPyChainedTargets_StatementFollowedByMore pins the round-2 review finding:
// the per-statement slices ran to END OF LINE, so the split only worked for the
// LAST statement. pyChainedAssignTargets splits its whole input, so on
// `MAX_A = OTHER_B = 5; z = 1` the first slice swallowed the trailing statement,
// the segment `" 5; z "` failed its clean-name check, the whole detection bailed,
// and OTHER_B came out as an unresolved use. Bounding each span at the next
// statement is what makes the split real rather than order-dependent.
func TestPyChainedTargets_StatementFollowedByMore(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []string
	}{
		{`MAX_A = OTHER_B = 5; z = 1`, []string{"MAX_A", "OTHER_B"}},
		{`z = 1; MAX_A = OTHER_B = 5`, []string{"MAX_A", "OTHER_B"}},
		{`MAX_A = OTHER_B = 5; MAX_C = MAX_D = 6`, []string{"MAX_A", "OTHER_B", "MAX_C", "MAX_D"}},
	} {
		t.Run(tc.text, func(t *testing.T) {
			lines := []AddedLine{{LineNo: 1, Text: tc.text}}
			defs := ExtractDefs(LangPython, lines)
			for _, want := range tc.want {
				if !containsStr(defs, want) {
					t.Errorf("ExtractDefs(%q) = %v, missing %s", tc.text, defs, want)
				}
			}
			for _, r := range ExtractRefs(LangPython, lines) {
				if containsStr(tc.want, r.Name) {
					t.Errorf("ExtractRefs(%q) emitted %q as a reference; that line defines it", tc.text, r.Name)
				}
			}
		})
	}
}

// TestStripLiteralsBraces_InterpolationBodyIsNotStatementSyntax pins the round-2
// review finding that blanking only an interpolation's BRACES left the rest of
// it readable as statement structure. A `;` in a format spec or nested
// replacement field then reads as a top-level statement separator, and the name
// after it is folded into the known set as a module constant that does not
// exist — a silent false negative, the worst class for a truth oracle. braceScan
// drops the whole region; scan keeps it, so the call inside is still checked.
func TestStripLiteralsBraces_InterpolationBodyIsNotStatementSyntax(t *testing.T) {
	const text = `f"{v:{W};{MAX_A:{X}}}"`
	scan, braceScan, _ := stripLiteralsBraces(LangPython, text, "")
	if strings.ContainsAny(braceScan, "{};") {
		t.Errorf("braceScan = %q, want the interpolation region gone entirely", braceScan)
	}
	if got := pyConstDefNames(pyLineCtx{scan: braceScan}); len(got) != 0 {
		t.Errorf("pyConstDefNames = %v, want none — nothing here defines a constant", got)
	}
	// The code scan must be unchanged, or a call inside an interpolation stops
	// being validated — the false negative #291's masking exists to avoid.
	if !strings.Contains(scan, "MAX_A") {
		t.Errorf("scan = %q, want the interpolation body intact for reference extraction", scan)
	}
	if len(scan) != len(text) || len(braceScan) != len(text) {
		t.Errorf("length not preserved: scan=%d braceScan=%d, want %d", len(scan), len(braceScan), len(text))
	}
}

// TestStmtSpansAgreeWithPyBraceStateAt pins that the line's two walkers share one
// accounting rule. They do today because both step through pyBraceStep, and this
// is the test that keeps it that way: the PR's own HIGH finding was three
// hand-kept copies of the brace arithmetic where one kept the pre-fix behaviour,
// and a second walker with its own inlined clamp would reopen that class.
func TestStmtSpansAgreeWithPyBraceStateAt(t *testing.T) {
	for _, tc := range []struct {
		scan string
		base int
	}{
		{`MAX_A = 1; MAX_B = 2`, 0},
		{`}; MAX_A = 1`, 1},
		{`    "a": 1,`, 1},
		{`} {`, 0},
		{`x = {"a": 1}; MAX_A = 2`, 0},
		{`;`, 0},
		{`a; b; c`, 0},
		{``, 0},
	} {
		t.Run(tc.scan, func(t *testing.T) {
			ctx := pyLineCtx{scan: tc.scan, base: tc.base}
			for _, sp := range ctx.stmtSpans() {
				// Every span start must be a position the OTHER walker also calls a
				// statement start at depth 0 — that is the invariant every consumer
				// of stmtSpans relies on.
				if !ctx.isDefinitionPos(sp[0]) {
					depth, stmtStart := pyBraceStateAt(tc.scan, tc.base, sp[0])
					t.Errorf("stmtSpans gave a start at %d that pyBraceStateAt calls depth=%d stmtStart=%t",
						sp[0], depth, stmtStart)
				}
				if sp[0] > sp[1] || sp[1] > len(tc.scan) {
					t.Errorf("span %v out of range for scan of len %d", sp, len(tc.scan))
				}
			}
		})
	}
}

// TestPyBraceStateAt covers the positional walk both #291 and #292 rest on,
// independent of the plumbing above it: depth at an offset, the progressive
// clamp on a stray close, and where a statement is considered to start.
func TestPyBraceStateAt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scan      string
		base      int
		pos       int
		depth     int
		stmtStart bool
	}{
		{"line start", "MAX = 5", 0, 0, 0, true},
		{"leading indent keeps stmt start", "    MAX = 5", 0, 4, 0, true},
		{"inside inline dict", `x = {MAX: 5}`, 0, 5, 1, false},
		{"inside multi-line dict", `    MAX: 5,`, 1, 4, 1, true},
		{"after close on same line", `}; MAX = 30`, 1, 3, 0, true},
		{"after close, no separator", `} MAX = 30`, 1, 2, 0, false},
		{"semicolon inside dict does not restart", `{"a": 1; MAX`, 0, 9, 1, false},
		{"stray close clamps at zero", `} {`, 0, 3, 1, false},
		{"pos past end is clamped", "MAX = {", 0, 99, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			depth, stmtStart := pyBraceStateAt(tc.scan, tc.base, tc.pos)
			if depth != tc.depth || stmtStart != tc.stmtStart {
				t.Errorf("pyBraceStateAt(%q, %d, %d) = (%d, %t), want (%d, %t)",
					tc.scan, tc.base, tc.pos, depth, stmtStart, tc.depth, tc.stmtStart)
			}
		})
	}
}

// TestPyBraceDepthBefore pins the code-review finding on #290: pyBraceDepth reset
// to 0 at every contiguous-added-lines boundary with no seeding from the file's
// actual pre-hunk state, so the dominant real-world edit shape — adding a key to
// an EXISTING multi-line dict, where the opening `{` is unchanged context and
// never appears among the added lines — still misread the key as a definition.
// PyBraceDepthBefore is the seed source (OpenStateBefore's counterpart) that
// closes this; these cases pin its own arithmetic directly, independent of the
// seeding plumbing above it.
func TestPyBraceDepthBefore(t *testing.T) {
	fileLines := []AddedLine{
		{LineNo: 1, Text: "result = {"},
		{LineNo: 2, Text: `    "a": 1,`},
		{LineNo: 3, Text: "    MAX_VALUE: 5,"},
		{LineNo: 4, Text: "}"},
		{LineNo: 5, Text: "other = 1"},
	}
	cases := []struct {
		idx  int
		want int
	}{
		{0, 0}, // before any line: unopened
		{1, 1}, // start of line 2: dict opened on line 1
		{2, 1}, // start of line 3 (MAX_VALUE's line): still open
		{3, 1}, // start of line 4 ("}"): still open — line 4 itself is what closes it
		{4, 0}, // start of line 5: closed by line 4's `}`
	}
	for _, tc := range cases {
		if got := PyBraceDepthBefore(fileLines, tc.idx); got != tc.want {
			t.Errorf("PyBraceDepthBefore(fileLines, %d) = %d, want %d", tc.idx, got, tc.want)
		}
	}

	// Braces inside a string/docstring must not count — mirrors ExtractRefs'
	// own literal-stripping via stripLiteralsStateful.
	withString := []AddedLine{
		{LineNo: 1, Text: `s = "{ not a dict }"`},
		{LineNo: 2, Text: "next_line = 1"},
	}
	if got := PyBraceDepthBefore(withString, 1); got != 0 {
		t.Errorf("PyBraceDepthBefore must not count braces inside a string literal, got %d", got)
	}

	// Out-of-range idx clamps rather than panicking.
	if got := PyBraceDepthBefore(fileLines, -1); got != 0 {
		t.Errorf("negative idx should clamp to 0, got %d", got)
	}
	if got := PyBraceDepthBefore(fileLines, 999); got != 0 {
		t.Errorf("idx past the end should clamp to the full-file depth (0, dict closed), got %d", got)
	}
	if got := PyBraceDepthBefore(nil, 1); got != 0 {
		t.Errorf("nil fileLines should return 0, got %d", got)
	}
}

// TestAppendConstRefs_TupleAssignTargetsNotReferences pins #278: every name in
// a tuple/multiple-assignment LHS (`MAX_VALUE, OTHER_VALUE = 5, 10`) is being
// DEFINED, not used — neither should be added as a reference. A call argument
// list sharing the same shape (`foo(MAX_VALUE, OTHER_VALUE)`) must still be
// treated as two genuine references.
func TestAppendConstRefs_TupleAssignTargetsNotReferences(t *testing.T) {
	refs := appendConstRefs(nil, map[string]struct{}{}, `MAX_VALUE, OTHER_VALUE = 5, 10`, 1, nil, nil, pyCtxFor(`MAX_VALUE, OTHER_VALUE = 5, 10`, 0))
	if len(refs) != 0 {
		t.Errorf("got %+v, want none — both names are tuple-assignment targets", refs)
	}
	callRefs := appendConstRefs(nil, map[string]struct{}{}, `foo(MAX_VALUE, OTHER_VALUE)`, 1, nil, nil, pyCtxFor(`foo(MAX_VALUE, OTHER_VALUE)`, 0))
	if len(callRefs) != 2 {
		t.Errorf("got %+v, want 2 references (call arguments)", callRefs)
	}
}

// TestDefNames_MultipleDeclaratorsPerLine pins #279: a single statement can
// define more than one name — a JS multi-declarator `const` with a
// function/arrow value on each side of the comma, and a Python chained
// assignment — and defNamesInContext must return all of them, not only the first.
func TestDefNames_MultipleDeclaratorsPerLine(t *testing.T) {
	js := defNamesNoContext(LangJS, "const a = () => {}, b = () => {}")
	if _, ok := js["a"]; !ok {
		t.Errorf("js defNamesInContext missing a: %v", js)
	}
	if _, ok := js["b"]; !ok {
		t.Errorf("js defNamesInContext missing b: %v", js)
	}

	py := defNamesNoContext(LangPython, "MAX_A = OTHER_B = 5")
	if _, ok := py["MAX_A"]; !ok {
		t.Errorf("py defNamesInContext missing MAX_A: %v", py)
	}
	if _, ok := py["OTHER_B"]; !ok {
		t.Errorf("py defNamesInContext missing OTHER_B: %v", py)
	}

	// A comparison must not be misread as a chained assignment.
	cmp := defNamesNoContext(LangPython, "x = MAX_A == OTHER_B")
	if len(cmp) != 0 {
		t.Errorf("py defNamesInContext(%q) = %v, want none — this is a comparison", "x = MAX_A == OTHER_B", cmp)
	}
}

// TestDefNames_ChainedAssignmentAnyLength pins the code-review finding on
// #279's first fix: a regex-FindAll approach to a chained assignment's second
// target has a trailing-`=`-guard that gets consumed by its own match, so it
// can supply at most one extra name and silently drops a third or later
// target. pyChainedAssignTargets (split-based, no such limit) replaced it.
func TestDefNames_ChainedAssignmentAnyLength(t *testing.T) {
	for _, tc := range []struct {
		text  string
		names []string
	}{
		{"MAX_A = OTHER_B = THIRD_C = 5", []string{"MAX_A", "OTHER_B", "THIRD_C"}},
		{"A_A = B_B = C_C = D_D = 5", []string{"A_A", "B_B", "C_C", "D_D"}},
	} {
		got := defNamesNoContext(LangPython, tc.text)
		for _, want := range tc.names {
			if _, ok := got[want]; !ok {
				t.Errorf("defNamesInContext(%q) = %v, missing %s", tc.text, got, want)
			}
		}
	}
}

// TestAppendConstRefs_ChainedAssignmentTargetsNotReferences pins the
// code-review finding on #279's first fix: appendConstRefs never consulted
// defNamesInContext' defs map, so a chained-assignment target that defNamesInContext correctly
// recognizes (`OTHER_B` in `MAX_A = OTHER_B = 5`) was still added here as a
// plain reference — the fix in defNamesInContext never reached the function that
// actually decides ref-vs-definition for SCREAMING_SNAKE names. Mirrors how
// the real caller in ExtractRefs threads its own defNamesInContext result through.
func TestAppendConstRefs_ChainedAssignmentTargetsNotReferences(t *testing.T) {
	text := "MAX_A = OTHER_B = 5"
	defs := defNamesNoContext(LangPython, text)
	refs := appendConstRefs(nil, map[string]struct{}{}, text, 1, nil, defs, pyCtxFor(text, 0))
	if len(refs) != 0 {
		t.Errorf("got %+v, want none — both names are chained-assignment targets", refs)
	}
}

// TestIsImportLine_MinifiedJS pins #280: space-less minified/bundled import
// and export-from syntax must still be recognized as import lines, so
// references on those lines are skipped as bindings rather than checked as
// unresolved calls. A plain `export{a}` (no `from`) is a local re-export, not
// an import, and must NOT match — same distinction the spaced form already
// draws. A dynamic `import(x)` call expression must not match either. The
// partially-spaced mixes (space on only one side of `from`) pin the
// code-review finding that a literal `"}from"` substring check missed them.
func TestIsImportLine_MinifiedJS(t *testing.T) {
	cases := map[string]bool{
		`import{a}from"m"`:               true,
		`export{a}from"m"`:               true,
		`export{a} from"m"`:              true,
		`export {a}from"m"`:              true,
		`export { a } from 'm'`:          true,
		`export{a}`:                      false,
		`import(x)`:                      false,
		`export function fromThing() {}`: false,
	}
	for text, want := range cases {
		if got := isImportLine(LangJS, text); got != want {
			t.Errorf("isImportLine(%q) = %v, want %v", text, got, want)
		}
	}
}

// TestPyDefPosScannerAgreesWithIsDefinitionPos pins that the linear forward
// scanner and the re-walking isDefinitionPos give the same answer at every
// offset. The scanner exists only to make appendConstRefs linear (a 21KB
// constant tuple cost 74ms per line before it, against a ~12ms hook budget);
// it must not change a single verdict.
func TestPyDefPosScannerAgreesWithIsDefinitionPos(t *testing.T) {
	for _, tc := range []struct {
		scan string
		base int
	}{
		{`ALLOWED = (CODE_A, CODE_B, CODE_C)`, 0},
		{`}; MAX_A = 1; MAX_B = 2`, 1},
		{`    MAX_VALUE: 5,`, 1},
		{`x = {"a": MAX_A}; MAX_B = 2`, 0},
		{`} {MAX_A: 1}`, 0},
	} {
		t.Run(tc.scan, func(t *testing.T) {
			ctx := pyLineCtx{scan: tc.scan, base: tc.base}
			s := newPyDefPosScanner(ctx)
			for pos := 0; pos <= len(tc.scan); pos++ {
				if got, want := s.at(pos), ctx.isDefinitionPos(pos); got != want {
					t.Fatalf("pos %d: scanner=%t isDefinitionPos=%t", pos, got, want)
				}
			}
		})
	}
}

// TestTupleTargetsAfterDictCloseResolveLater pins the round-3 finding: the tuple
// arm of #292 suppressed the reference on the DEFINING line but never put the
// names in the known set, so a later use flagged both — the false positive was
// relocated by a line, not removed.
func TestTupleTargetsAfterDictCloseResolveLater(t *testing.T) {
	lines := []AddedLine{
		{LineNo: 1, Text: `cfg = {`},
		{LineNo: 2, Text: `    "a": 1,`},
		{LineNo: 3, Text: `}; MAX_A, MAX_B = 1, 2`},
		{LineNo: 4, Text: `use(MAX_A, MAX_B)`},
	}
	known := map[string]struct{}{"use": {}}
	for _, d := range ExtractDefs(LangPython, lines) {
		known[d] = struct{}{}
	}
	for _, v := range Run(known, "", []FileDiff{{Path: "config.py", AddedLines: lines}}) {
		t.Errorf("unexpected violation %q at line %d — it is defined on line 3", v.Symbol, v.Line)
	}
}

// BenchmarkAppendConstRefsWideLine records the shape that made the per-match
// re-walk untenable: one module-level constant tuple with many SCREAMING_SNAKE
// names on a single line.
func BenchmarkAppendConstRefsWideLine(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("ALLOWED = (")
	for i := 0; i < 2000; i++ {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "CODE_%d", i)
	}
	sb.WriteString(")")
	lines := []AddedLine{{LineNo: 1, Text: sb.String()}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ExtractRefs(LangPython, lines)
	}
}

// TestExtractDefs_PythonOrderIsDeterministic pins #296. The Python branch
// collects into a map, and Go deliberately randomizes map iteration order, so
// a line defining more than one name returned a different order across runs.
// Every in-tree caller folds the result into a set and could not see it, but
// ExtractDefs is exported with no ordering caveat: a golden-output test or any
// order-sensitive consumer would have flaked about half the time.
//
// Repeating the call is what makes this a real pin rather than a coincidence —
// a single call passes ~50% of the time on the unfixed code.
func TestExtractDefs_PythonOrderIsDeterministic(t *testing.T) {
	lines := []AddedLine{
		{LineNo: 1, Text: `MAX_A = OTHER_B = THIRD_C = 5`},
		{LineNo: 2, Text: `ZED_A, ALPHA_B = 1, 2`},
		{LineNo: 3, Text: `def handler(x):`},
	}
	first := ExtractDefs(LangPython, lines)
	for i := 0; i < 64; i++ {
		if got := ExtractDefs(LangPython, lines); !slices.Equal(got, first) {
			t.Fatalf("run %d returned %v, first run returned %v — order must not vary", i, got, first)
		}
	}

	// Line order is preserved; only names WITHIN a line are sorted, since line
	// order is the half of the ordering that carries meaning.
	want := []string{"MAX_A", "OTHER_B", "THIRD_C", "ALPHA_B", "ZED_A", "handler"}
	if !slices.Equal(first, want) {
		t.Errorf("ExtractDefs = %v, want %v", first, want)
	}
}
