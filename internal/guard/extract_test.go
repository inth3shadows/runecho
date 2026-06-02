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
