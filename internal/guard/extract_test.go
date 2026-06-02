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

func TestExtractRefs_Unknown_ReturnsNil(t *testing.T) {
	ls := lines(`whatever`)
	refs := ExtractRefs(LangUnknown, ls)
	if refs != nil {
		t.Errorf("expected nil for unknown lang, got %v", refs)
	}
}

// TestExtractRefs_Go_ClosingCommentLineSkipped verifies that a line starting
// with */ (block-comment close) is treated as a comment and produces no refs,
// even when followed by text that looks like a call.
func TestExtractRefs_Go_ClosingCommentLineSkipped(t *testing.T) {
	ls := lines(
		`*/ SomeCall()`,
		`* StarPrefixCall()`,
		`// LineComment()`,
		`/* BlockOpen()`,
	)
	refs := ExtractRefs(LangGo, ls)
	if !containsNone(refs, "SomeCall", "StarPrefixCall", "LineComment", "BlockOpen") {
		t.Errorf("comment lines should produce no refs, got %v", refNames(refs))
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
		{"script.py", LangPython},
		{"data.json", LangUnknown},
	}
	for _, tc := range cases {
		if got := LangFor(tc.path); got != tc.lang {
			t.Errorf("LangFor(%q) = %q, want %q", tc.path, got, tc.lang)
		}
	}
}
