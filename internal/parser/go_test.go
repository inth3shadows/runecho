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
	wantFuncs := []string{"MyFunc", "MyMethod"}
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
