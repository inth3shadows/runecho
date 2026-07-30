package guard

import (
	"strings"
	"testing"
)

// mismatchCase drives PyCallShapeMismatches through the hook's two shapes: a
// Write (added text IS the whole post-edit file) and an Edit (a hunk plus the
// pre-edit whole file).
type mismatchCase struct {
	name string
	// file is the pre-edit whole file. For write:true cases it is ignored and
	// added is treated as the whole post-edit file.
	file    string
	added   string
	removed string
	write   bool
	// want is the (callee, keyword) pairs expected, in order, as "callee:keyword".
	want []string
}

func runMismatch(t *testing.T, tc mismatchCase) []CallShapeMismatch {
	t.Helper()
	fd := FileDiff{Path: "m.py", AddedLines: TextToAddedLines(tc.added)}
	var whole []AddedLine
	if !tc.write {
		whole = TextToAddedLines(tc.file)
	}
	var removed []AddedLine
	if tc.removed != "" {
		removed = TextToAddedLines(tc.removed)
	}
	return PyCallShapeMismatches(LangPython, whole, fd, removed, tc.write)
}

func assertMismatches(t *testing.T, got []CallShapeMismatch, want []string) {
	t.Helper()
	var gotKeys []string
	for _, m := range got {
		gotKeys = append(gotKeys, m.Callee+":"+m.Keyword)
	}
	if strings.Join(gotKeys, " ") != strings.Join(want, " ") {
		t.Errorf("mismatches = %v, want %v", gotKeys, want)
	}
}

func TestPyCallShapeMismatchesFires(t *testing.T) {
	// The headline case: a typo'd keyword against a same-file declaration. A Write
	// carries the whole post-edit file, so the declaration and the call are both in
	// the added text.
	got := runMismatch(t, mismatchCase{
		added: `def fetch(url, timeout=10):
    return url

def go():
    return fetch("u", timeuot=5)
`,
		write: true,
		want:  []string{"fetch:timeuot"},
	})
	assertMismatches(t, got, []string{"fetch:timeuot"})
	if len(got) != 1 {
		t.Fatalf("want 1 mismatch, got %d", len(got))
	}
	m := got[0]
	if m.LineNo != 5 {
		t.Errorf("LineNo = %d, want 5 (the call line)", m.LineNo)
	}
	if m.DeclLine != 1 {
		t.Errorf("DeclLine = %d, want 1 (the def line)", m.DeclLine)
	}
	if strings.Join(m.Accepted, ",") != "url,timeout" {
		t.Errorf("Accepted = %v, want [url timeout]", m.Accepted)
	}
	// The suggester is what turns "wrong keyword" into "you meant timeout".
	if len(m.Suggestions) == 0 || m.Suggestions[0] != "timeout" {
		t.Errorf("Suggestions = %v, want timeout first", m.Suggestions)
	}
}

func TestPyCallShapeMismatchesQuietOnValidCode(t *testing.T) {
	// The anti-vacuity pairing for the case above: the same file with the keyword
	// spelled correctly must be silent. A check that fires on both is worthless.
	got := runMismatch(t, mismatchCase{
		added: `def fetch(url, timeout=10):
    return url

def go():
    return fetch("u", timeout=5)
`,
		write: true,
	})
	assertMismatches(t, got, nil)
}

func TestPyCallShapeMismatchesAbstains(t *testing.T) {
	for _, tc := range []mismatchCase{
		{
			name: "declaration takes **kwargs — accepts every keyword",
			added: `def fetch(url, **opts):
    return url

fetch("u", anything=1)
`,
			write: true,
		},
		{
			name: "call passes **kwargs — may supply any keyword at runtime",
			added: `def fetch(url, timeout=1):
    return url

fetch("u", **cfg)
`,
			write: true,
		},
		{
			name: "declaration is decorated — a wrapper may accept a different set",
			added: `@retry
def fetch(url, timeout=1):
    return url

fetch("u", attempts=3)
`,
			write: true,
		},
		{
			name: "callee is also imported — cannot tell which fetch is called",
			added: `from httpx import fetch

def fetch(url, timeout=1):
    return url

fetch("u", verify=False)
`,
			write: true,
		},
		{
			name: "callee is reassigned — the def may not be what runs",
			added: `def fetch(url, timeout=1):
    return url

fetch = memoize(fetch)

fetch("u", ttl=5)
`,
			write: true,
		},
		{
			name: "callee shadowed by an enclosing parameter",
			added: `def fetch(url, timeout=1):
    return url

def run(fetch):
    return fetch("u", retries=2)
`,
			write: true,
		},
		{
			name: "star-import makes the namespace unknowable",
			added: `from os.path import *

def fetch(url, timeout=1):
    return url

fetch("u", bogus=1)
`,
			write: true,
		},
		{
			name: "no same-file declaration — the existence check's business",
			added: `import requests

def go():
    return requests_get("u", bogus=1)
`,
			write: true,
		},
		{
			name: "conditional branches accept different keywords",
			added: `try:
    def fetch(url, fast=True):
        return url
except ImportError:
    def fetch(url, slow=True):
        return url

fetch("u", fast=True)
`,
			write: true,
		},
		{
			name: "a `/` does not make the parameters after it unacceptable",
			// The FP direction of positional-only handling: dropping the whole
			// accepted list on `/` (rather than only its prefix) would ask here.
			// TestPyCallShapeMismatchesPositionalOnlyByKeyword covers the other half.
			added: `def fetch(url, /, timeout=1):
    return url

fetch("u", timeout=2)
`,
			write: true,
		},
		{
			name: "lambda at argument depth zero makes the shape unreliable",
			added: `def each(items, key=None):
    return items

each([1], key=lambda a, b: a)
`,
			write: true,
		},
		{
			name:  "no whole-file context — declaration invisible",
			file:  "",
			added: "fetch(\"u\", bogus=1)\n",
		},
		{
			name: "a dynamic-binding marker anywhere in the file",
			added: `def fetch(url, timeout=1):
    return url

globals()["fetch"] = other
fetch("u", bogus=1)
`,
			write: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertMismatches(t, runMismatch(t, tc), nil)
		})
	}
}

func TestPyCallShapeMismatchesEditUsesFreshestSignature(t *testing.T) {
	// The recurring-FP case this exists for: the edit ADDS a parameter and uses it.
	// The pre-edit file's signature lacks it, so comparing against the on-disk copy
	// would flag a valid call on every "add a param and pass it" edit.
	file := `def fetch(url):
    return url

def go():
    return fetch("u")
`
	added := `def fetch(url, timeout=10):
    return url

def go():
    return fetch("u", timeout=5)
`
	got := PyCallShapeMismatches(LangPython, TextToAddedLines(file),
		FileDiff{Path: "m.py", AddedLines: TextToAddedLines(added)},
		TextToAddedLines(file), false)
	assertMismatches(t, got, nil)
}

func TestPyCallShapeMismatchesEditFiresAgainstNewSignature(t *testing.T) {
	// The other half: the edit rewrites the signature AND typos the keyword. The
	// new signature is the right comparison basis, and it still disagrees.
	file := "def fetch(url):\n    return url\n"
	added := `def fetch(url, timeout=10):
    return url

def go():
    return fetch("u", timeuot=5)
`
	got := PyCallShapeMismatches(LangPython, TextToAddedLines(file),
		FileDiff{Path: "m.py", AddedLines: TextToAddedLines(added)},
		TextToAddedLines(file), false)
	assertMismatches(t, got, []string{"fetch:timeuot"})
}

func TestPyCallShapeMismatchesAbstainsWhenSignatureLineRemoved(t *testing.T) {
	// A MultiEdit that changes ONE line of a multi-line parameter list. The hunk
	// contains no whole `def` line, so the added-text path does not trigger and the
	// pre-edit declaration is stale by exactly this edit. Comparing against it
	// would be a false positive.
	file := `def fetch(
    url,
    timeout=1,
):
    return url

def go():
    return fetch("u")
`
	// The edit replaces the `timeout=1,` line with `deadline=1,` and updates the
	// call. Only the call line and the new param line are added.
	added := "    deadline=1,\n    return fetch(\"u\", deadline=5)\n"
	removed := "    timeout=1,\n"
	got := PyCallShapeMismatches(LangPython, TextToAddedLines(file),
		FileDiff{Path: "m.py", AddedLines: TextToAddedLines(added)},
		TextToAddedLines(removed), false)
	assertMismatches(t, got, nil)
}

func TestPyCallShapeMismatchesEditFiresWithUntouchedSignature(t *testing.T) {
	// The reach case for Edit: the declaration is untouched by the edit, which only
	// adds a call. Nothing is stale, so the pre-edit signature is authoritative.
	file := `def fetch(url, timeout=1):
    return url
`
	added := "    return fetch(\"u\", timeuot=5)\n"
	got := PyCallShapeMismatches(LangPython, TextToAddedLines(file),
		FileDiff{Path: "m.py", AddedLines: TextToAddedLines(added)},
		TextToAddedLines("    return fetch(\"u\")\n"), false)
	assertMismatches(t, got, []string{"fetch:timeuot"})
}

func TestPyCallShapeMismatchesPositionalOnlyByKeyword(t *testing.T) {
	// `url` is a real parameter name, and `fetch(url=...)` is still a TypeError
	// because `/` makes it positional-only. Reporting it is correct: the keyword is
	// not one the declaration accepts.
	got := runMismatch(t, mismatchCase{
		added: `def fetch(url, /, timeout=1):
    return url

fetch(url="u")
`,
		write: true,
	})
	assertMismatches(t, got, []string{"fetch:url"})
}

func TestPyCallShapeMismatchesKeywordOnlyAccepted(t *testing.T) {
	// Keyword-only parameters (after `*` or `*args`) are keyword-callable and must
	// not be reported. Getting this backwards would false-positive on every
	// keyword-only API.
	got := runMismatch(t, mismatchCase{
		added: `def fetch(url, *, timeout=1, retries=0):
    return url

fetch("u", timeout=2, retries=1)
`,
		write: true,
	})
	assertMismatches(t, got, nil)
}

func TestPyCallShapeMismatchesDedupesRepeatedKeyword(t *testing.T) {
	// `foo(bad=1, bad=2)` is one problem, reported once. (The repetition is itself a
	// Python error, but naming it is not this slice's job.)
	got := runMismatch(t, mismatchCase{
		added: `def fetch(url):
    return url

fetch("u", bad=1, bad=2)
`,
		write: true,
	})
	assertMismatches(t, got, []string{"fetch:bad"})
}

func TestPyCallShapeMismatchesCapsFindings(t *testing.T) {
	var b strings.Builder
	b.WriteString("def fetch(url):\n    return url\n\n")
	for i := 0; i < callShapeMaxMismatches+4; i++ {
		b.WriteString("fetch(\"u\", bad")
		b.WriteByte(byte('a' + i))
		b.WriteString("=1)\n")
	}
	got := runMismatch(t, mismatchCase{added: b.String(), write: true})
	if len(got) != callShapeMaxMismatches {
		t.Errorf("got %d mismatches, want the cap of %d", len(got), callShapeMaxMismatches)
	}
}

func TestPyCallShapeMismatchesNonPythonIsInert(t *testing.T) {
	src := "func fetch(url string) {}\nfetch(url: 1)\n"
	for _, lang := range []Lang{LangGo, LangJS, LangUnknown} {
		got := PyCallShapeMismatches(lang, TextToAddedLines(src),
			FileDiff{Path: "m.go", AddedLines: TextToAddedLines(src)}, nil, true)
		if got != nil {
			t.Errorf("lang %s: got %v, want nil", lang, got)
		}
	}
}

func TestPyCallShapeMismatchesAcceptedListCapped(t *testing.T) {
	var params []string
	for i := 0; i < callShapeMaxAccepted+3; i++ {
		params = append(params, string(rune('a'+i))+"=1")
	}
	src := "def wide(" + strings.Join(params, ", ") + "):\n    return 1\n\nwide(zzz=1)\n"
	got := runMismatch(t, mismatchCase{added: src, write: true})
	if len(got) != 1 {
		t.Fatalf("want 1 mismatch, got %d", len(got))
	}
	if n := len(got[0].Accepted); n != callShapeMaxAccepted+1 {
		t.Errorf("Accepted has %d entries, want %d (cap plus the truncation marker)", n, callShapeMaxAccepted+1)
	}
	if last := got[0].Accepted[len(got[0].Accepted)-1]; last != "…" {
		t.Errorf("truncated Accepted must end with the ellipsis marker, got %q", last)
	}
}

func TestPySignatureLinesSpan(t *testing.T) {
	lines := TextToAddedLines("x = 1\ndef f(\n    a,\n    b=2,\n):\n    pass\n")
	got := pySignatureLines(lines, 2)
	want := []string{"def f(", "a,", "b=2,", "):"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("pySignatureLines = %v, want %v", got, want)
	}
	if got := pySignatureLines(lines, 99); got != nil {
		t.Errorf("out-of-range declLine: got %v, want nil", got)
	}
	// Never balances: abstain rather than returning a partial span that a caller
	// would treat as complete.
	if got := pySignatureLines(TextToAddedLines("def f(\n    a,\n"), 1); got != nil {
		t.Errorf("unbalanced signature: got %v, want nil", got)
	}
}

func TestMatchesPyDefRegexMetacharactersAreLiteral(t *testing.T) {
	// The callee name comes from repo text, which #212 established is
	// attacker-influenced. A name with regex metacharacters must not compile into a
	// pattern that matches something else.
	if matchesPyDef("def foo(a):\n    pass\n", "f.o") {
		t.Errorf("`f.o` must not match `def foo(` — the name is a literal, not a pattern")
	}
	if !matchesPyDef("def foo(a):\n    pass\n", "foo") {
		t.Errorf("`foo` must match `def foo(`")
	}
	if !matchesPyDef("    async def foo(a):\n        pass\n", "foo") {
		t.Errorf("indented `async def` must match")
	}
	if matchesPyDef("# def foo(a)\nfoo = 1\n", "bar") {
		t.Errorf("absent name must not match")
	}
}
