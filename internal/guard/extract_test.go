package guard

import (
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

func TestExtractRefs_Go_UnexportedSkipped(t *testing.T) {
	ls := lines(
		`lookupSymbols()`,
		`hookApprove()`,
		`textToAddedLines("x")`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsNone(refs, "lookupSymbols", "hookApprove", "textToAddedLines") {
		t.Errorf("unexported Go refs should be skipped, got %v", refNames(refs))
	}
}

func TestExtractRefs_Go_ExportedStillChecked(t *testing.T) {
	ls := lines(`result := ParseStagedDiff(root)`)
	refs := ExtractRefs(LangGo, ls)
	if !containsAll(refs, "ParseStagedDiff") {
		t.Errorf("exported Go ref should still be extracted, got %v", refNames(refs))
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
