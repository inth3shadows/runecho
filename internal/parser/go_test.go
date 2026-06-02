package parser

import "testing"

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
