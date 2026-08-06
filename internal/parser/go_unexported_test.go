package parser

import (
	"slices"
	"testing"
)

const goUnexportedSrc = `package p

import "fmt"

type Reader struct {
	Buf  []byte
	pos  int
	hook func(string) error
}

type cache struct {
	entries map[string]int
}

const maxRetries = 3

var defaultHandler = fmt.Println

func Exported() {}

func helper() {}

func (r *Reader) Fetch() {}

func (r *Reader) parse() {}

func (c *cache) Get() {}
`

func goStructure(t *testing.T) FileStructure {
	t.Helper()
	s, err := NewGoParser().Parse(goUnexportedSrc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

// TestGoParser_UnexportedIsSeparateFromTheExportedSurface is the contract test
// for this whole change. Functions/Classes/Exports are what `structure` and
// `map` render as a package's public API surface; indexing unexported
// declarations must not widen them by a single name, or every existing consumer
// silently starts showing internals.
func TestGoParser_UnexportedIsSeparateFromTheExportedSurface(t *testing.T) {
	s := goStructure(t)

	for _, name := range []string{"helper", "Reader.parse", "cache", "maxRetries", "defaultHandler"} {
		if slices.Contains(s.Functions, name) || slices.Contains(s.Classes, name) || slices.Contains(s.Exports, name) {
			t.Errorf("%q is unexported and must not appear in the exported-surface lists", name)
		}
	}
	for _, want := range []string{"Exported", "Reader.Fetch", "cache.Get"} {
		if !slices.Contains(s.Functions, want) {
			t.Errorf("Functions must still carry %q; got %v", want, s.Functions)
		}
	}
	if !slices.Contains(s.Classes, "Reader") {
		t.Errorf("Classes must still carry Reader; got %v", s.Classes)
	}
}

func TestGoParser_UnexportedCollectsEveryTopLevelForm(t *testing.T) {
	s := goStructure(t)
	// Funcs bare, methods receiver-qualified — the same naming the exported
	// halves use, so the guard can look either up the same way.
	for _, want := range []string{"helper", "Reader.parse", "cache", "maxRetries", "defaultHandler"} {
		if !slices.Contains(s.Unexported, want) {
			t.Errorf("Unexported must carry %q; got %v", want, s.Unexported)
		}
	}
	// `cache.Get` is an EXPORTED method on an unexported type, so it belongs to
	// the exported surface, not here. Only the name's own export status decides.
	if slices.Contains(s.Unexported, "cache.Get") {
		t.Errorf("an exported method on an unexported type is not unexported; got %v", s.Unexported)
	}
}

// TestGoParser_FieldsIncludeFuncTyped covers the false-positive class that
// forced field indexing: a func-typed struct field is called exactly like a
// method (`rb.flushF(rb)`), so without fields in the index the receiver-method
// check reported real code as a hallucination. Both x/text sites that proved it
// (`reorderBuffer.flushF`, `enum.rename`) were unexported fields.
func TestGoParser_FieldsIncludeFuncTyped(t *testing.T) {
	s := goStructure(t)
	for _, want := range []string{"Reader.Buf", "Reader.pos", "Reader.hook", "cache.entries"} {
		if !slices.Contains(s.Fields, want) {
			t.Errorf("Fields must carry %q; got %v", want, s.Fields)
		}
	}
	// Fields are not part of the API surface either.
	for _, name := range s.Fields {
		if slices.Contains(s.Functions, name) || slices.Contains(s.Classes, name) {
			t.Errorf("field %q leaked into the exported-surface lists", name)
		}
	}
}

// TestGoParser_EmbeddedFieldsOmitted pins the abstention. An embedded field has
// no name of its own and promotes names from a type this single-file parser
// cannot resolve; inventing entries would claim knowledge the parser lacks.
func TestGoParser_EmbeddedFieldsOmitted(t *testing.T) {
	src := `package p

import "sync"

type Guarded struct {
	sync.Mutex
	Inner
	count int
}
`
	s, err := NewGoParser().Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !slices.Contains(s.Fields, "Guarded.count") {
		t.Errorf("named fields must still be collected; got %v", s.Fields)
	}
	for _, name := range s.Fields {
		if name == "Guarded.Mutex" || name == "Guarded.Inner" {
			t.Errorf("embedded field %q must not be recorded as a named field", name)
		}
	}
}
