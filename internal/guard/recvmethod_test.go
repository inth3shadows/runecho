package guard

import "testing"

// knownSetOf builds a symbol set from qualified names as the IR records them.
func knownSetOf(names ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func recvViolations(t *testing.T, src string, known map[string]struct{}) []Violation {
	t.Helper()
	lines := TextToAddedLines(src)
	return GoReceiverMethodViolations(lines, lines, known)
}

func TestGoReceiverMethodCatchesMissingSiblingMethod(t *testing.T) {
	src := `package p

func (r *Reader) Fetch() {
	r.Parse()
}
`
	// Reader has an indexed method set, but no Parse — and Parse appears nowhere
	// else in the repo. That is the hallucination profile.
	got := recvViolations(t, src, knownSetOf("Reader.Fetch", "Reader.Close"))
	if len(got) != 1 {
		t.Fatalf("want 1 violation, got %d: %+v", len(got), got)
	}
	if got[0].Symbol != "Reader.Parse" {
		t.Errorf("Symbol = %q, want %q", got[0].Symbol, "Reader.Parse")
	}
	if got[0].Line != 4 {
		t.Errorf("Line = %d, want 4", got[0].Line)
	}
}

func TestGoReceiverMethodSuggestsFromTheReceiversOwnMethods(t *testing.T) {
	src := `package p

func (r *Reader) Fetch() {
	r.Closee()
}
`
	got := recvViolations(t, src, knownSetOf("Reader.Fetch", "Reader.Close", "Writer.Flush"))
	if len(got) != 1 {
		t.Fatalf("want 1 violation, got %d: %+v", len(got), got)
	}
	if len(got[0].Suggestions) == 0 || got[0].Suggestions[0] != "Close" {
		t.Errorf("Suggestions = %v, want Close first — the pool must be the "+
			"receiver's own methods, not every symbol in the repo", got[0].Suggestions)
	}
}

func TestGoReceiverMethodAbstentions(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		known map[string]struct{}
		why   string
	}{
		{
			name: "method exists on the type",
			src: `package p

func (r *Reader) Fetch() {
	r.Close()
}
`,
			known: knownSetOf("Reader.Fetch", "Reader.Close"),
			why:   "Reader.Close is indexed",
		},
		{
			name: "ambiguous receiver name",
			src: `package p

func (r *Reader) Fetch() {
	r.Parse()
}

func (r *Writer) Flush() {}
`,
			known: knownSetOf("Reader.Fetch", "Writer.Flush"),
			why:   "gate 1: r names two different types in one file",
		},
		{
			name: "receiver name re-bound elsewhere",
			src: `package p

func (r *Reader) Fetch() {
	r.Parse()
}

func other() {
	r := newThing()
	_ = r
}
`,
			known: knownSetOf("Reader.Fetch"),
			why:   "gate 2: r is also a short variable declaration",
		},
		{
			name: "type has no indexed methods",
			src: `package p

func (r *Reader) Fetch() {
	r.Parse()
}
`,
			known: knownSetOf("SomethingElse"),
			why:   "gate 4: no Reader.* entry, so absence proves nothing",
		},
		{
			name: "name exists elsewhere in the repo",
			src: `package p

func (r *Reader) Fetch() {
	r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch", "Writer.Parse"),
			why:   "gate 5: Parse is a method somewhere, so it may be promoted from an embedded field",
		},
		{
			name: "name exists as a plain function",
			src: `package p

func (r *Reader) Fetch() {
	r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch", "Parse"),
			why:   "gate 5: the repo knows the name in some form",
		},
		{
			name: "deeper selector, not the receiver",
			src: `package p

func (r *Reader) Fetch() {
	a.r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch"),
			why:   "left-guard: r is a field of a, not the receiver variable",
		},
		{
			name: "inside a string literal",
			src: `package p

func (r *Reader) Fetch() {
	s := "r.Parse()"
	_ = s
}
`,
			known: knownSetOf("Reader.Fetch"),
			why:   "literal masking",
		},
		{
			name: "commented out",
			src: `package p

func (r *Reader) Fetch() {
	// r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch"),
			why:   "comment lines are not code",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recvViolations(t, tc.src, tc.known); len(got) != 0 {
				t.Errorf("want no violation (%s), got %+v", tc.why, got)
			}
		})
	}
}

func TestGoReceiverMethodCatchesMissingUnexportedSiblingMethod(t *testing.T) {
	// Gate 3 used to abstain here. Unexported methods are now indexed as
	// "Reader.parse", so an unexported sibling that does not exist is exactly as
	// checkable as an exported one — this is the case that lifts the check's
	// reach from 0.5% of selector sites toward 3.2%.
	src := `package p

func (r *Reader) Fetch() {
	r.parse()
}
`
	got := recvViolations(t, src, knownSetOf("Reader.Fetch", "Reader.render"))
	if len(got) != 1 || got[0].Symbol != "Reader.parse" {
		t.Fatalf("want Reader.parse violation, got %+v", got)
	}
}

func TestGoReceiverMethodHandlesGenericReceiver(t *testing.T) {
	// parser.receiverTypeName strips the instantiation (Set[T] → "Set"), so the
	// name this check builds has to strip it the same way or it would look up
	// "Set[T].Add" and never match anything the IR recorded.
	src := `package p

func (s *Set[T]) Add() {
	s.Remove()
}
`
	got := recvViolations(t, src, knownSetOf("Set.Add"))
	if len(got) != 1 || got[0].Symbol != "Set.Remove" {
		t.Fatalf("want Set.Remove violation, got %+v", got)
	}
}

func TestGoReceiverTypesIgnoresBlankReceiver(t *testing.T) {
	// The second method is what makes this test able to fail: with only the
	// blank receiver, goReceiverTypes returns at its `len(types) == 0` exit
	// before `dropped` is ever computed, so the assertion below could not
	// observe `_` being counted (found by adversarial review of #359). `w` is
	// re-bound so it reaches the drop loop and `dropped` is non-empty for a
	// legitimate reason.
	src := `package p

func (_ *Reader) Fetch() {}

func (w *Writer) Flush() {
	w := other()
	_ = w
}
`
	got, dropped := goReceiverTypes(TextToAddedLines(src))
	if _, blank := got["_"]; blank || len(got) != 0 {
		t.Errorf("blank receiver must bind nothing, got %v", got)
	}
	// `_` is not a name a call site can use, so it is not a declined candidate
	// either — it must not show up as an abstain (#359).
	if _, blank := dropped["_"]; blank {
		t.Errorf("blank receiver must not count as a dropped receiver, got %v", dropped)
	}
	if _, ok := dropped["w"]; !ok {
		t.Fatalf("control: the re-bound receiver must be dropped, got %v", dropped)
	}
}
