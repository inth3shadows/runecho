package parser

import (
	"slices"
	"strings"
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

// Private and dunder helpers ARE captured now (AST extraction). The old regex
// pass dropped them, which broke symbol-level anchoring for downstream consumers
// that need to know when a private helper a decision depends on changed.
func TestPythonParser_Functions(t *testing.T) {
	src := "def process_data(x):\n    return x\n\ndef _private_helper():\n    pass\n\ndef __dunder__():\n    pass\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, want := range []string{"process_data", "_private_helper", "__dunder__"} {
		if !slices.Contains(fs.Functions, want) {
			t.Errorf("missing function: %s (got %v)", want, fs.Functions)
		}
	}
}

// Async defs, methods, and nested defs are all first-class function symbols.
// Methods/nested are qualified by their enclosing scope so identical leaf names
// in different classes never collide. This is the core Bug 1 regression guard.
func TestPythonParser_AsyncMethodsNested(t *testing.T) {
	src := "" +
		"async def search(q):\n" +
		"    return q\n" +
		"\n" +
		"class Reader:\n" +
		"    def __init__(self):\n" +
		"        pass\n" +
		"\n" +
		"    async def fetch(self, k):\n" +
		"        def inner(x):\n" +
		"            return x\n" +
		"        return inner(k)\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, want := range []string{"search", "Reader.__init__", "Reader.fetch", "Reader.fetch.inner"} {
		if !slices.Contains(fs.Functions, want) {
			t.Errorf("missing function: %s (got %v)", want, fs.Functions)
		}
	}
	if !slices.Contains(fs.Classes, "Reader") {
		t.Errorf("missing class Reader (got %v)", fs.Classes)
	}
	// Every function symbol must carry a body hash so in-place edits are diffable.
	for _, fn := range fs.Functions {
		if fs.SymbolHashes["function:"+fn] == "" {
			t.Errorf("function %s has no body hash", fn)
		}
	}
}

// A @property getter/setter/deleter share one qualified name and collapse to one
// symbol; editing ANY variant must change the combined body hash (last-write-wins
// would hide edits to all but the final variant). Also covers the decorator span:
// a decorator change must be detected.
func TestPythonParser_SameNameAndDecorator(t *testing.T) {
	p := NewPythonParser()
	base := "" +
		"class Foo:\n" +
		"    @property\n" +
		"    def x(self):\n" +
		"        return self._x\n" +
		"\n" +
		"    @x.setter\n" +
		"    def x(self, v):\n" +
		"        self._x = v\n"
	a, _ := p.Parse(base)
	// One symbol despite three (here two) defs of Foo.x.
	count := 0
	for _, fn := range a.Functions {
		if fn == "Foo.x" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Foo.x should collapse to one symbol, got %d in %v", count, a.Functions)
	}
	h0 := a.SymbolHashes["function:Foo.x"]
	if h0 == "" {
		t.Fatal("Foo.x has no combined body hash")
	}

	// Edit ONLY the getter body — combined hash must change.
	editGetter := strings.Replace(base, "return self._x", "return self._x * 2", 1)
	if got := mustHash(t, p, editGetter); got == h0 {
		t.Error("editing the getter did not change the combined hash (last-write-wins regression)")
	}

	// Edit ONLY a decorator — must be detected (span includes the decorator).
	editDecorator := strings.Replace(base, "@property", "@cached_property", 1)
	if got := mustHash(t, p, editDecorator); got == h0 {
		t.Error("editing a decorator did not change the body hash (decorator excluded from span)")
	}
}

func mustHash(t *testing.T, p *PythonParser, src string) string {
	t.Helper()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return fs.SymbolHashes["function:Foo.x"]
}

// A function's body hash must change when its signature or body changes, and stay
// stable otherwise — the mechanism behind modified-symbol diffing (Bug 2).
func TestPythonParser_BodyHashChangesOnEdit(t *testing.T) {
	p := NewPythonParser()
	a, _ := p.Parse("def get_scope():\n    return 1\n")
	b, _ := p.Parse("def get_scope(default=\"personal\"):\n    return 1\n")
	c, _ := p.Parse("def get_scope():\n    return 1\n")
	h := func(fs FileStructure) string { return fs.SymbolHashes["function:get_scope"] }
	if h(a) == "" {
		t.Fatal("no body hash produced")
	}
	if h(a) == h(b) {
		t.Error("signature change did not change body hash")
	}
	if h(a) != h(c) {
		t.Error("identical source produced different body hash")
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

// TestPythonParser_ClassHashChangesOnFieldEdit covers issue #53's cheaper
// alternative to per-field extraction: a class's own hash flips when an
// instance attribute is added/renamed, without a dedicated field symbol.
func TestPythonParser_ClassHashChangesOnFieldEdit(t *testing.T) {
	p := NewPythonParser()
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

	base := "class Config:\n    def __init__(self):\n        self.timeout = 1\n\nclass Other:\n    def __init__(self):\n        self.x = 1\n"
	fieldAdded := "class Config:\n    def __init__(self):\n        self.timeout = 1\n        self.retries = 3\n\nclass Other:\n    def __init__(self):\n        self.x = 1\n"
	fieldRenamed := "class Config:\n    def __init__(self):\n        self.deadline = 1\n\nclass Other:\n    def __init__(self):\n        self.x = 1\n"
	siblingChange := "class Config:\n    def __init__(self):\n        self.timeout = 1\n\nclass Other:\n    def __init__(self):\n        self.x = 1\n        self.y = 2\n"

	baseConfig := hashOf(base, "class:Config")
	if hashOf(fieldAdded, "class:Config") == baseConfig {
		t.Error("Config hash unchanged after adding a field — diff would miss the change")
	}
	if hashOf(fieldRenamed, "class:Config") == baseConfig {
		t.Error("Config hash unchanged after renaming its only field")
	}
	if hashOf(siblingChange, "class:Config") != baseConfig {
		t.Error("Config hash changed when only sibling Other changed — would over-report")
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

// A multi-line __all__ (the bracket and members spanning several lines) must be
// captured. The audit flagged this as a possible silent miss; in fact pyAllRegex's
// negated class spans newlines, so it already works — this pins that behavior.
func TestPythonParser_AllExportsMultiLine(t *testing.T) {
	src := "__all__ = [\n    \"foo\",\n    \"bar\",\n    \"baz\",\n]\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, want := range []string{"foo", "bar", "baz"} {
		if !slices.Contains(fs.Exports, want) {
			t.Errorf("missing multi-line __all__ export %q: %v", want, fs.Exports)
		}
	}
}

// When __all__ is absent, exports fall back to the no-underscore convention:
// top-level public functions/classes plus module-level UPPER_CASE constants.
// Private (_-prefixed), nested, and method symbols are excluded.
func TestPythonParser_FallbackExportsNoAll(t *testing.T) {
	src := "" +
		"MAX_SIZE = 100\n" +
		"DEFAULT_NAME: str = \"x\"\n" +
		"_PRIVATE_CONST = 1\n" +
		"lowercase_var = 2\n" +
		"\n" +
		"def public_fn():\n" +
		"    def nested():\n" +
		"        pass\n" +
		"    return nested\n" +
		"\n" +
		"def _private_fn():\n" +
		"    pass\n" +
		"\n" +
		"class PublicClass:\n" +
		"    def method(self):\n" +
		"        pass\n" +
		"\n" +
		"class _PrivateClass:\n" +
		"    pass\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, want := range []string{"MAX_SIZE", "DEFAULT_NAME", "public_fn", "PublicClass"} {
		if !slices.Contains(fs.Exports, want) {
			t.Errorf("missing fallback export %q (got %v)", want, fs.Exports)
		}
	}
	for _, notWant := range []string{"_PRIVATE_CONST", "lowercase_var", "_private_fn", "_PrivateClass", "nested", "method", "PublicClass.method", "public_fn.nested"} {
		if slices.Contains(fs.Exports, notWant) {
			t.Errorf("fallback exports should not include %q (got %v)", notWant, fs.Exports)
		}
	}
}

// An explicit __all__ is authoritative even when it lists nothing: `__all__ = []`
// means "no public API" and must suppress the no-underscore fallback rather than
// re-deriving exports from public symbols.
func TestPythonParser_EmptyAllSuppressesFallback(t *testing.T) {
	src := "" +
		"__all__ = []\n" +
		"PUBLIC_CONST = 1\n" +
		"def public_fn():\n    pass\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(fs.Exports) != 0 {
		t.Errorf("explicit empty __all__ must export nothing, got %v", fs.Exports)
	}
}

// A non-empty __all__ stays authoritative and is NOT augmented by the fallback:
// a public symbol omitted from __all__ stays unexported.
func TestPythonParser_AllSuppressesFallback(t *testing.T) {
	src := "" +
		"__all__ = [\"public_fn\"]\n" +
		"PUBLIC_CONST = 1\n" +
		"def public_fn():\n    pass\n" +
		"def other_public():\n    pass\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Contains(fs.Exports, "public_fn") {
		t.Errorf("declared export public_fn missing: %v", fs.Exports)
	}
	for _, notWant := range []string{"PUBLIC_CONST", "other_public"} {
		if slices.Contains(fs.Exports, notWant) {
			t.Errorf("non-empty __all__ must not be augmented; got %q in %v", notWant, fs.Exports)
		}
	}
}

// A conditionally-declared __all__ (indented, e.g. inside `if TYPE_CHECKING:`)
// is still authoritative: pyAllRegex extracts its names regardless of indent, so
// presence detection must too — otherwise the declared list is extracted and then
// silently overwritten by the fallback. Regression guard for the anchoring mismatch.
func TestPythonParser_IndentedAllIsAuthoritative(t *testing.T) {
	src := "" +
		"import typing\n" +
		"if typing.TYPE_CHECKING:\n" +
		"    __all__ = [\"Declared\"]\n" +
		"\n" +
		"def helper():\n    pass\n" +
		"\n" +
		"class Declared:\n    pass\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Contains(fs.Exports, "Declared") {
		t.Errorf("indented __all__ should be honored: %v", fs.Exports)
	}
	if slices.Contains(fs.Exports, "helper") {
		t.Errorf("declared __all__ must suppress the fallback; got fallback symbol helper in %v", fs.Exports)
	}
}

// A docstring (or comment) that merely mentions __all__ at column 0 must NOT be
// mistaken for a declaration: there is no assignment, so the no-underscore
// fallback still applies. Regression guard against false suppression.
func TestPythonParser_DocstringMentionDoesNotSuppressFallback(t *testing.T) {
	src := "" +
		"\"\"\"This module sets no __all__.\n" +
		"\n" +
		"__all__ would normally enumerate the public API.\n" +
		"\"\"\"\n" +
		"\n" +
		"def public_fn():\n    pass\n"
	p := NewPythonParser()
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Contains(fs.Exports, "public_fn") {
		t.Errorf("a prose mention of __all__ must not suppress the fallback: %v", fs.Exports)
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

// Regression: module constants declared via tuple assignment (`MIN, MAX = 0, 100`)
// must appear in the no-__all__ fallback export list. The single-name pyConstRegex
// cannot match a name followed by a comma, so both were previously dropped.
func TestPyFallbackExports_TupleConstants(t *testing.T) {
	got := pyFallbackExports("MIN, MAX = 0, 100\nTHRESHOLD = 5\n", nil, nil)
	for _, want := range []string{"MIN", "MAX", "THRESHOLD"} {
		if !slices.Contains(got, want) {
			t.Errorf("want constant %q in fallback exports, got %v", want, got)
		}
	}
}
