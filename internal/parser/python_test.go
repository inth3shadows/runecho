package parser

import (
	"slices"
	"testing"
)

func TestPythonParser_SupportsExtension(t *testing.T) {
	p := NewPythonParser()
	if !p.SupportsExtension(".py") {
		t.Error("should support .py")
	}
	for _, ext := range []string{".go", ".js", ".ts", ".rb", ""} {
		if p.SupportsExtension(ext) {
			t.Errorf("should not support %q", ext)
		}
	}
}

func TestPythonParser_Imports(t *testing.T) {
	src := "import os\nimport sys\nimport os.path\nfrom pathlib import Path\nfrom collections import OrderedDict\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, want := range []string{"os", "sys", "pathlib", "collections"} {
		if !slices.Contains(fs.Imports, want) {
			t.Errorf("missing import: %s", want)
		}
	}
	// os.path must not add a second "os" entry
	count := 0
	for _, imp := range fs.Imports {
		if imp == "os" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 'os' import, got %d", count)
	}
}

// Regression: when a dotted import precedes its bare parent, the parent must
// still be recorded. The old top-level-package dedup dropped it silently.
func TestPythonParser_DottedImportThenBareParent(t *testing.T) {
	src := "import os.path\nimport os\nimport xml.etree\nimport xml.dom\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"os", "os.path", "xml.dom", "xml.etree"}
	if !slices.Equal(fs.Imports, want) {
		t.Errorf("Imports = %v, want %v", fs.Imports, want)
	}
}

func TestPythonParser_Functions(t *testing.T) {
	src := "def process_data(x):\n    return x\n\ndef _private_helper():\n    pass\n\ndef __dunder__():\n    pass\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Contains(fs.Functions, "process_data") {
		t.Error("missing function: process_data")
	}
	if slices.Contains(fs.Functions, "_private_helper") {
		t.Error("private function should be excluded")
	}
	if slices.Contains(fs.Functions, "__dunder__") {
		t.Error("dunder function should be excluded")
	}
}

func TestPythonParser_Classes(t *testing.T) {
	src := "class Animal:\n    pass\n\nclass Mammal(Animal):\n    pass\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, want := range []string{"Animal", "Mammal"} {
		if !slices.Contains(fs.Classes, want) {
			t.Errorf("missing class: %s", want)
		}
	}
}

func TestPythonParser_AllExportsDoubleQuote(t *testing.T) {
	src := `__all__ = ["process_data", "Animal", "helper"]` + "\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, want := range []string{"process_data", "Animal", "helper"} {
		if !slices.Contains(fs.Exports, want) {
			t.Errorf("missing __all__ export: %s", want)
		}
	}
}

func TestPythonParser_AllExportsSingleQuote(t *testing.T) {
	src := "__all__ = ('foo', 'bar')\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Contains(fs.Exports, "foo") || !slices.Contains(fs.Exports, "bar") {
		t.Errorf("missing single-quote __all__ exports: %v", fs.Exports)
	}
}

func TestPythonParser_Sorted(t *testing.T) {
	src := "import zlib\nimport abc\ndef zoo(): pass\ndef apple(): pass\nclass Zebra: pass\nclass Aardvark: pass\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for i := 1; i < len(fs.Imports); i++ {
		if fs.Imports[i] < fs.Imports[i-1] {
			t.Errorf("imports not sorted: %v", fs.Imports)
		}
	}
	for i := 1; i < len(fs.Functions); i++ {
		if fs.Functions[i] < fs.Functions[i-1] {
			t.Errorf("functions not sorted: %v", fs.Functions)
		}
	}
	for i := 1; i < len(fs.Classes); i++ {
		if fs.Classes[i] < fs.Classes[i-1] {
			t.Errorf("classes not sorted: %v", fs.Classes)
		}
	}
}

func TestPythonParser_Empty(t *testing.T) {
	p := NewPythonParser()
	fs, err := p.Parse("")
	if err != nil {
		t.Fatalf("empty source should not error: %v", err)
	}
	if len(fs.Imports) != 0 || len(fs.Functions) != 0 || len(fs.Classes) != 0 {
		t.Errorf("empty source produced non-empty IR: %+v", fs)
	}
}

func TestPythonParser_CRLFLineEndings(t *testing.T) {
	src := "def foo():\r\n    pass\r\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Contains(fs.Functions, "foo") {
		t.Errorf("CRLF line endings should be handled; got functions: %v", fs.Functions)
	}
}
