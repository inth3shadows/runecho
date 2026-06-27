package parser

import (
	"reflect"
	"sort"
	"testing"
)

// TestGoParser_CommentStrippingStringLiteral verifies that // inside a string
// literal does not truncate the line before the real // comment.
// Without string-literal awareness (naive strings.Index), "https://" in the
// const value would be truncated, mangling the source before the parser sees it.
func TestGoParser_CommentStrippingStringLiteral(t *testing.T) {
	p := NewGoParser()
	// Source has a const whose value contains //, followed by a real // comment.
	src := "package foo\n\nconst u = \"https://example.com\" // inline comment\n\nfunc PublicFn() {}\n"
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.Functions) != 1 || fs.Functions[0] != "PublicFn" {
		t.Errorf("functions: got %v, want [PublicFn]", fs.Functions)
	}
	if len(fs.Imports) != 0 {
		t.Errorf("imports: got %v, want []", fs.Imports)
	}
}

// TestGoParser_ExportedSymbols covers exported vs unexported filtering for
// functions and types, plus import block extraction.
func TestGoParser_ExportedSymbols(t *testing.T) {
	p := NewGoParser()
	src := `package foo

import "fmt"
import alias "path/filepath"

func MyFunc() {}
func (r *Receiver) MyMethod() {}
func unexported() {}

type MyStruct struct{}
type myPrivate struct{}
`
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	wantImports := []string{"fmt", "path/filepath"}
	if len(fs.Imports) != len(wantImports) {
		t.Fatalf("imports: got %v, want %v", fs.Imports, wantImports)
	}
	for i, w := range wantImports {
		if fs.Imports[i] != w {
			t.Errorf("import[%d] = %q, want %q", i, fs.Imports[i], w)
		}
	}
	// Methods are qualified by receiver type (Reader.Fetch style), matching the
	// Python parser's scope-qualified names — see go.go Parse doc.
	wantFuncs := []string{"MyFunc", "Receiver.MyMethod"}
	if len(fs.Functions) != len(wantFuncs) {
		t.Fatalf("functions: got %v, want %v", fs.Functions, wantFuncs)
	}
	for i, w := range wantFuncs {
		if fs.Functions[i] != w {
			t.Errorf("function[%d] = %q, want %q", i, fs.Functions[i], w)
		}
	}
	if len(fs.Classes) != 1 || fs.Classes[0] != "MyStruct" {
		t.Errorf("classes: got %v, want [MyStruct]", fs.Classes)
	}
}

// TestGoParser_InterfaceMethods covers extraction of exported interface method
// signatures into Functions (qualified by interface type, parity with JS/TS
// method_signature and Python class methods): the interface itself stays in
// Classes, exported methods are located + hashed, and unexported methods,
// embedded interfaces, and type-set constraints are skipped.
func TestGoParser_InterfaceMethods(t *testing.T) {
	p := NewGoParser()
	src := `package foo

type Reader interface {
	Read(p []byte) (n int, err error)
	Close() error
	hidden() bool
}

type ReadWriter interface {
	Reader
	Write(p []byte) (int, error)
}

type Number interface {
	~int | ~float64
}
`
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	// The interfaces themselves remain in Classes (located, not hashed).
	wantClasses := []string{"Number", "ReadWriter", "Reader"}
	if len(fs.Classes) != len(wantClasses) {
		t.Fatalf("classes: got %v, want %v", fs.Classes, wantClasses)
	}
	for i, w := range wantClasses {
		if fs.Classes[i] != w {
			t.Errorf("class[%d] = %q, want %q", i, fs.Classes[i], w)
		}
	}
	// Exported method signatures land in Functions qualified by interface type;
	// hidden() (unexported), the embedded Reader, and the ~int|~float64 type-set
	// constraint contribute nothing.
	wantFuncs := []string{"ReadWriter.Write", "Reader.Close", "Reader.Read"}
	if len(fs.Functions) != len(wantFuncs) {
		t.Fatalf("functions: got %v, want %v", fs.Functions, wantFuncs)
	}
	for i, w := range wantFuncs {
		if fs.Functions[i] != w {
			t.Errorf("function[%d] = %q, want %q", i, fs.Functions[i], w)
		}
	}
	// Read is on line 4 (1-based; package=1, blank=2, type=3, Read=4).
	if got := fs.SymbolLines["function:Reader.Read"]; got != 4 {
		t.Errorf("SymbolLines[function:Reader.Read] = %d, want 4", got)
	}
	// Signature span is hashed so a contract change surfaces.
	if fs.SymbolHashes["function:Reader.Read"] == "" {
		t.Error("function:Reader.Read has no signature hash")
	}
}

// TestGoParser_InterfaceMethodSignatureHash proves a method's hash tracks its
// signature: changing a parameter type flips the hash, an unrelated sibling does
// not. This is what lets `diff` report an interface contract change.
func TestGoParser_InterfaceMethodSignatureHash(t *testing.T) {
	p := NewGoParser()
	hashOf := func(src, key string) string {
		t.Helper()
		fs, err := p.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		h := fs.SymbolHashes[key]
		if h == "" {
			t.Fatalf("no hash for %q in:\n%s", key, src)
		}
		return h
	}
	base := "package foo\n\ntype Store interface {\n\tGet(id int) string\n\tPut(v string) error\n}\n"
	sigChange := "package foo\n\ntype Store interface {\n\tGet(id string) string\n\tPut(v string) error\n}\n"

	baseGet := hashOf(base, "function:Store.Get")
	if hashOf(sigChange, "function:Store.Get") == baseGet {
		t.Error("Store.Get hash unchanged after parameter type change — diff would miss it")
	}
	if hashOf(sigChange, "function:Store.Put") != hashOf(base, "function:Store.Put") {
		t.Error("Store.Put hash changed when only sibling Get changed — would over-report")
	}
}

// TestGoParser_ExportedVarConst covers capture of top-level exported var/const
// declarations into Exports: single-line, typed, grouped, iota groups, and the
// filtering of unexported names and in-function declarations.
func TestGoParser_ExportedVarConst(t *testing.T) {
	tests := []struct {
		name        string
		src         string
		wantExports []string
	}{
		{
			name:        "single-line exported var",
			src:         "package foo\n\nvar ExportedVar = 42\n",
			wantExports: []string{"ExportedVar"},
		},
		{
			name:        "single-line exported const",
			src:         "package foo\n\nconst ExportedConst = \"x\"\n",
			wantExports: []string{"ExportedConst"},
		},
		{
			name:        "typed var and const",
			src:         "package foo\n\nvar Timeout int = 30\nconst Pi float64 = 3.14\n",
			wantExports: []string{"Pi", "Timeout"},
		},
		{
			name:        "var declaration without initializer",
			src:         "package foo\n\nvar Buffer []byte\n",
			wantExports: []string{"Buffer"},
		},
		{
			name:        "unexported filtered",
			src:         "package foo\n\nvar private = 1\nconst secret = 2\nvar Public = 3\n",
			wantExports: []string{"Public"},
		},
		{
			name:        "grouped var block",
			src:         "package foo\n\nvar (\n\tAlpha = 1\n\tbeta  = 2\n\tGamma = 3\n)\n",
			wantExports: []string{"Alpha", "Gamma"},
		},
		{
			name:        "grouped const block typed",
			src:         "package foo\n\nconst (\n\tMaxSize int = 100\n\tminSize int = 1\n)\n",
			wantExports: []string{"MaxSize"},
		},
		{
			name:        "iota group",
			src:         "package foo\n\nconst (\n\tStateA = iota\n\tStateB\n\tprivate\n\tStateC\n)\n",
			wantExports: []string{"StateA", "StateB", "StateC"},
		},
		{
			name:        "var inside func body not captured (top-level only)",
			src:         "package foo\n\nfunc Run() {\n\tvar Local = 1\n\tconst Inner = 2\n}\n",
			wantExports: []string{},
		},
		{
			name:        "deduplicated and sorted",
			src:         "package foo\n\nvar Zebra = 1\nvar Apple = 2\nvar Apple = 3\n",
			wantExports: []string{"Apple", "Zebra"},
		},
		{
			// Regex-era bug: `var X, Y = 1, 2` captured only X. The AST's
			// ValueSpec.Names carries both, so this is fixed for free.
			name:        "multi-name single-line var (regex bug fixed)",
			src:         "package foo\n\nvar X, Y = 1, 2\n",
			wantExports: []string{"X", "Y"},
		},
		{
			// Regex-era bug: a `var (...)` block closed early on a nested `)`
			// (e.g. a call initializer), dropping every name after it. The AST
			// owns the block boundary, so B is no longer lost.
			name:        "var block with call initializer (nested-paren bug fixed)",
			src:         "package foo\n\nvar (\n\tAlpha = compute()\n\tBeta  = 1\n)\n",
			wantExports: []string{"Alpha", "Beta"},
		},
	}

	p := NewGoParser()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs, err := p.Parse(tt.src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fs.Exports, tt.wantExports) {
				t.Errorf("exports: got %v, want %v", fs.Exports, tt.wantExports)
			}
			if !sort.StringsAreSorted(fs.Exports) {
				t.Errorf("exports not sorted: %v", fs.Exports)
			}
		})
	}
}

// TestGoParser_SymbolLines verifies the per-symbol start lines that #19 adds —
// the data behind `runecho-ir map` / `locate` showing real file:line for Go
// (functions, qualified methods, types, and exported var/const).
func TestGoParser_SymbolLines(t *testing.T) {
	p := NewGoParser()
	// Line numbers are 1-based; the layout below is hand-counted.
	src := `package foo

import "fmt"

const Answer = 42

type Widget struct{}

func Top() {}

func (w *Widget) Do() {}
`
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"export:Answer":      5,
		"class:Widget":       7,
		"function:Top":       9,
		"function:Widget.Do": 11,
	}
	for key, wantLine := range want {
		if got := fs.SymbolLines[key]; got != wantLine {
			t.Errorf("SymbolLines[%q] = %d, want %d", key, got, wantLine)
		}
	}
	// Functions only (not classes) carry a body hash.
	if fs.SymbolHashes["function:Top"] == "" {
		t.Error("function:Top has no body hash")
	}
	if fs.SymbolHashes["function:Widget.Do"] == "" {
		t.Error("function:Widget.Do has no body hash")
	}
	if _, ok := fs.SymbolHashes["class:Widget"]; ok {
		t.Error("class:Widget should not be hashed (parity with Python classes)")
	}
}

// TestGoParser_BodyHashChangesOnRewrite is the proof behind the `diff ~ modified`
// acceptance criterion: a function's body hash changes when its body or signature
// changes, and stays stable when a SIBLING function changes.
func TestGoParser_BodyHashChangesOnRewrite(t *testing.T) {
	p := NewGoParser()
	hashOf := func(src, key string) string {
		t.Helper()
		fs, err := p.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		h := fs.SymbolHashes[key]
		if h == "" {
			t.Fatalf("no hash for %q in:\n%s", key, src)
		}
		return h
	}

	base := "package foo\n\nfunc F() int { return 1 }\nfunc G() int { return 10 }\n"
	bodyChange := "package foo\n\nfunc F() int { return 2 }\nfunc G() int { return 10 }\n"
	sigChange := "package foo\n\nfunc F(x int) int { return 1 }\nfunc G() int { return 10 }\n"
	siblingChange := "package foo\n\nfunc F() int { return 1 }\nfunc G() int { return 20 }\n"

	baseF := hashOf(base, "function:F")
	if hashOf(bodyChange, "function:F") == baseF {
		t.Error("F body hash unchanged after body rewrite — diff would miss the change")
	}
	if hashOf(sigChange, "function:F") == baseF {
		t.Error("F body hash unchanged after signature change")
	}
	if hashOf(siblingChange, "function:F") != baseF {
		t.Error("F body hash changed when only sibling G changed — would over-report")
	}
	if hashOf(siblingChange, "function:G") == hashOf(base, "function:G") {
		t.Error("G body hash unchanged after G was rewritten")
	}
}

// TestGoParser_Determinism runs the parser many times over a mixed source and
// asserts every output list is identical across runs (the package relies on
// deterministic IR; there are 100-iteration determinism gates in this style).
func TestGoParser_Determinism(t *testing.T) {
	p := NewGoParser()
	src := `package foo

import (
	"fmt"
	"os"
)

var (
	MaxRetries = 3
	hidden     = 0
	Timeout    int = 30
)

const Version = "1.0"

func Exported() {}
func unexported() {}

type Config struct{}
`
	first, err := p.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		got, err := p.Parse(src)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("iteration %d: non-deterministic output\n got: %#v\nwant: %#v", i, got, first)
		}
	}
	wantExports := []string{"MaxRetries", "Timeout", "Version"}
	if !reflect.DeepEqual(first.Exports, wantExports) {
		t.Errorf("exports: got %v, want %v", first.Exports, wantExports)
	}
}

// TestGoParser_ImportBlock verifies multi-import block parsing with inline comments.
func TestGoParser_ImportBlock(t *testing.T) {
	p := NewGoParser()
	src := `package foo

import (
	"net/http"  // HTTP client
	"fmt"       // formatting
)
`
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"fmt", "net/http"}
	if len(fs.Imports) != len(want) {
		t.Fatalf("imports: got %v, want %v", fs.Imports, want)
	}
	for i, w := range want {
		if fs.Imports[i] != w {
			t.Errorf("import[%d] = %q, want %q", i, fs.Imports[i], w)
		}
	}
}
