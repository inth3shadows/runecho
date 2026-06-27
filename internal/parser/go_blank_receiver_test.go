package parser

import "testing"

// TestGoParser_BlankReceiver verifies that a blank identifier receiver
// (func (_ Foo) Method() {}) is still qualified as "Foo.Method" in the
// functions list. The receiver name is irrelevant for qualification; only the
// receiver type matters. Blank receivers are valid Go and must not cause the
// method to be dropped or recorded under its bare name "Method".
func TestGoParser_BlankReceiver(t *testing.T) {
	p := NewGoParser()
	src := `package foo

type Foo struct{}

func (_ Foo) Method() {}
`
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatal(err)
	}

	// Method is exported and its receiver type is Foo, so the qualified name
	// must be "Foo.Method" regardless of the receiver identifier being "_".
	for _, fn := range fs.Functions {
		if fn == "Foo.Method" {
			return // found — pass
		}
	}
	t.Errorf("blank-receiver method not recorded as \"Foo.Method\"; got functions: %v", fs.Functions)
}

// TestGoParser_BlankReceiverPointer verifies the pointer-receiver variant
// (func (_ *Foo) PtrMethod() {}) is also qualified as "Foo.PtrMethod". The
// receiverTypeName helper unwraps the StarExpr before reading the Ident, so
// a blank pointer receiver must behave identically to a blank value receiver.
func TestGoParser_BlankReceiverPointer(t *testing.T) {
	p := NewGoParser()
	src := `package foo

type Foo struct{}

func (_ *Foo) PtrMethod() {}
`
	fs, err := p.Parse(src)
	if err != nil {
		t.Fatal(err)
	}

	for _, fn := range fs.Functions {
		if fn == "Foo.PtrMethod" {
			return
		}
	}
	t.Errorf("blank pointer-receiver method not recorded as \"Foo.PtrMethod\"; got functions: %v", fs.Functions)
}
