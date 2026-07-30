package parser

import "testing"

// TestGoSymbolDocs pins the four Go attachment rules that are easy to get wrong:
// a doc comment reduces to its first line, an unparenthesized decl's comment
// reaches its spec, a *parenthesized* group's comment does NOT leak onto every
// member, and an interface method carries its own.
func TestGoSymbolDocs(t *testing.T) {
	const src = `package p

// Alpha does a thing.
// More detail that must not appear.
func Alpha() {}

// Shape is a type.
type Shape struct{}

// Group doc that documents the block, not its members.
const (
	A = 1
	// B is documented.
	B = 2
)

type I interface {
	// Do runs it.
	Do()
}

func Undocumented() {}
`
	fs, err := NewGoParser().Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, tc := range []struct{ key, want string }{
		{"function:Alpha", "Alpha does a thing."},
		{"class:Shape", "Shape is a type."},
		{"export:B", "B is documented."},
		{"function:I.Do", "Do runs it."},
	} {
		if got := fs.SymbolDocs[tc.key]; got != tc.want {
			t.Errorf("%s doc = %q, want %q", tc.key, got, tc.want)
		}
	}
	for _, key := range []string{"export:A", "function:Undocumented"} {
		if got, ok := fs.SymbolDocs[key]; ok {
			t.Errorf("%s should carry no doc, got %q", key, got)
		}
	}
}

// TestPythonSymbolDocs pins docstring extraction, including the two cases a
// naive implementation gets wrong: a DECORATED def (whose span node is the
// decorator wrapper, not the suite holding the docstring), and a string
// appearing mid-body, which Python discards and which is not documentation.
func TestPythonSymbolDocs(t *testing.T) {
	const src = `import functools

def alpha():
    """Alpha does a thing.

    More detail that must not appear.
    """
    return 1

@functools.cache
def beta():
    'Beta summary.'
    return 2

class C:
    """C holds things."""

    def m(self):
        x = 1
        "not a docstring"
        return x

def gamma():
    return 3
`
	fs, err := NewPythonParser().Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(fs.SymbolDocs) == 0 {
		t.Skip("Python grammar unavailable in this build (grammar_subset)")
	}
	for _, tc := range []struct{ key, want string }{
		{"function:alpha", "Alpha does a thing."},
		{"function:beta", "Beta summary."},
		{"class:C", "C holds things."},
	} {
		if got := fs.SymbolDocs[tc.key]; got != tc.want {
			t.Errorf("%s doc = %q, want %q", tc.key, got, tc.want)
		}
	}
	for _, key := range []string{"function:C.m", "function:gamma"} {
		if got, ok := fs.SymbolDocs[key]; ok {
			t.Errorf("%s should carry no doc, got %q", key, got)
		}
	}
}

// TestPythonDocstringPrefixes pins which string PREFIXES make a first-statement
// literal a docstring. CPython sets __doc__ only from a plain str constant, so
// an f-string or a bytes literal must yield nothing — the f-string case doubly
// so, because concatenating only its literal segments would silently drop each
// {interpolation} and report text the author never wrote. r/u prefixes are
// ordinary str literals and must still document.
func TestPythonDocstringPrefixes(t *testing.T) {
	const src = `def fstr():
    f"Hello {name} world."
    return 1

def bytesdoc():
    b"bytes are not docs."
    return 2

def rawdoc():
    r"Raw doc.\n"
    return 3

def udoc():
    u"Unicode doc."
    return 4

def concat():
    "one " "two"
    return 5
`
	fs, err := NewPythonParser().Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(fs.SymbolDocs) == 0 {
		t.Skip("Python grammar unavailable in this build (grammar_subset)")
	}
	for _, tc := range []struct{ key, want string }{
		{"function:rawdoc", `Raw doc.\n`},
		{"function:udoc", "Unicode doc."},
	} {
		if got := fs.SymbolDocs[tc.key]; got != tc.want {
			t.Errorf("%s doc = %q, want %q", tc.key, got, tc.want)
		}
	}
	// concat is an implicitly-concatenated literal: the grammar emits
	// concatenated_string, not string, so it records nothing. Pinned as known
	// behaviour — a missing doc is safe, an invented one is not.
	for _, key := range []string{"function:fstr", "function:bytesdoc", "function:concat"} {
		if got, ok := fs.SymbolDocs[key]; ok {
			t.Errorf("%s must carry no doc, got %q", key, got)
		}
	}
}

// TestFirstDocLineTruncates pins the length cap: a long first line is truncated,
// never dropped, so the field still orients.
func TestFirstDocLineTruncates(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "x"
	}
	got := firstDocLine("\n\n  " + long + "  \nsecond line\n")
	if len([]rune(got)) != maxDocLen {
		t.Errorf("len = %d, want %d", len([]rune(got)), maxDocLen)
	}
	if firstDocLine("   \n\n  ") != "" {
		t.Errorf("blank-only comment must yield no doc")
	}
}
