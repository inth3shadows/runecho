package guard

import "testing"

// Unlike recvViolations (recvmethod_test.go), this passes the snippet ONLY as
// addedLines, not duplicated into wholeFile too. A receiver's type comes from
// a func-signature line the rebinding regexes never match, so recvViolations'
// wholeFile==addedLines duplication is harmless there. Here the type-
// establishing line IS itself a var/:= binding, so duplicating it into both
// slices would double-count it as two independent bindings of the same name
// and the ambiguity gate would purge every valid case — this mirrors a real
// Write-posture edit (whole snippet is new content, nothing pre-existing).
func varTypeViolationsForTest(t *testing.T, src string, known map[string]struct{}) []Violation {
	t.Helper()
	lines := TextToAddedLines(src)
	return GoVarTypeMethodViolations(nil, lines, known)
}

func TestGoVarTypeCatchesMissingMethodOnExplicitVarDecl(t *testing.T) {
	src := `package p

func run() {
	var r *Reader
	r.Parse()
}
`
	got := varTypeViolationsForTest(t, src, knownSetOf("Reader.Fetch", "Reader.Close"))
	if len(got) != 1 {
		t.Fatalf("want 1 violation, got %d: %+v", len(got), got)
	}
	if got[0].Symbol != "Reader.Parse" {
		t.Errorf("Symbol = %q, want %q", got[0].Symbol, "Reader.Parse")
	}
}

func TestGoVarTypeCatchesMissingMethodOnCompositeLiteral(t *testing.T) {
	src := `package p

func run() {
	r := &Reader{}
	r.Parse()
}
`
	got := varTypeViolationsForTest(t, src, knownSetOf("Reader.Fetch", "Reader.Close"))
	if len(got) != 1 || got[0].Symbol != "Reader.Parse" {
		t.Fatalf("want Reader.Parse violation, got %+v", got)
	}
}

func TestGoVarTypeCatchesMissingMethodOnValueCompositeLiteral(t *testing.T) {
	src := `package p

func run() {
	r := Reader{}
	r.Parse()
}
`
	got := varTypeViolationsForTest(t, src, knownSetOf("Reader.Fetch"))
	if len(got) != 1 || got[0].Symbol != "Reader.Parse" {
		t.Fatalf("want Reader.Parse violation, got %+v", got)
	}
}

func TestGoVarTypeSuggestsFromTheVariablesOwnMethods(t *testing.T) {
	src := `package p

func run() {
	var r *Reader
	r.Closee()
}
`
	got := varTypeViolationsForTest(t, src, knownSetOf("Reader.Fetch", "Reader.Close", "Writer.Flush"))
	if len(got) != 1 {
		t.Fatalf("want 1 violation, got %d: %+v", len(got), got)
	}
	if len(got[0].Suggestions) == 0 || got[0].Suggestions[0] != "Close" {
		t.Errorf("Suggestions = %v, want Close first — the pool must be the "+
			"variable's own type's methods, not every symbol in the repo", got[0].Suggestions)
	}
}

func TestGoVarTypeAbstentions(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		known map[string]struct{}
		why   string
	}{
		{
			name: "method exists on the type",
			src: `package p

func run() {
	var r *Reader
	r.Close()
}
`,
			known: knownSetOf("Reader.Close"),
			why:   "Reader.Close is indexed",
		},
		{
			name: "variable name re-bound with a different type",
			src: `package p

func run() {
	var r *Reader
	r.Parse()
}

func other() {
	var r *Writer
	_ = r
}
`,
			known: knownSetOf("Reader.Fetch", "Writer.Flush"),
			why:   "bound twice, file-wide — ambiguous, same as receivers gate 1",
		},
		{
			name: "variable rebound via short declaration elsewhere",
			src: `package p

func run() {
	r := &Reader{}
	r.Parse()
}

func other() {
	r := newThing()
	_ = r
}
`,
			known: knownSetOf("Reader.Fetch"),
			why:   "r is bound on two separate lines, ambiguous regardless of RHS shape",
		},
		{
			name: "type has no indexed members",
			src: `package p

func run() {
	var r *Reader
	r.Parse()
}
`,
			known: knownSetOf("SomethingElse"),
			why:   "gate 4 equivalent: no Reader.* entry, absence proves nothing",
		},
		{
			name: "name exists elsewhere in the repo",
			src: `package p

func run() {
	var r *Reader
	r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch", "Writer.Parse"),
			why:   "gate 5: Parse is a method somewhere, may be promoted from an embedded field",
		},
		{
			// known deliberately holds a pkg.* member, not a Reader.* one: if the
			// dot-guard ever regressed and let "pkg" through as the resolved type,
			// this would give gate 3 (member-indexed) a real "pkg" entry to pass
			// and gate 5 would then fire on the globally-unknown "Parse" — i.e. a
			// wrong-fix here would produce a violation, not silently abstain for
			// an unrelated reason. Using Reader.* here would make this pass
			// whether or not the dot-guard works, since "pkg" would fail gate 3
			// regardless.
			name: "package-qualified var-decl type is out of scope",
			src: `package p

func run() {
	var r pkg.Reader
	r.Parse()
}
`,
			known: knownSetOf("pkg.Fetch"),
			why:   "pkg.Reader must not be misread as type pkg — out of scope for slice 1",
		},
		{
			// Same isolation reasoning as above, for the composite-literal form.
			name: "package-qualified composite literal is out of scope",
			src: `package p

func run() {
	r := &pkg.Reader{}
	r.Parse()
}
`,
			known: knownSetOf("pkg.Fetch"),
			why:   "&pkg.Reader{} must not be misread as type pkg — out of scope for slice 1",
		},
		{
			name: "tuple short declaration is not a composite literal",
			src: `package p

func run() {
	r, err := open()
	_ = err
	r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch"),
			why:   "r's type cannot be read from f()'s call text — must abstain, not guess",
		},
		{
			name: "map literal is not a struct type",
			src: `package p

func run() {
	r := map[string]int{}
	r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch"),
			why:   "map[string]int{} must not be misread as a type named int",
		},
		{
			name: "deeper selector, not the local variable",
			src: `package p

func run() {
	var r *Reader
	a.r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch"),
			why:   "left-guard: r is a field of a, not the local variable",
		},
		{
			name: "inside a string literal",
			src: `package p

func run() {
	var r *Reader
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

func run() {
	var r *Reader
	// r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch"),
			why:   "comment lines are not code",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := varTypeViolationsForTest(t, tc.src, tc.known); len(got) != 0 {
				t.Errorf("want no violation (%s), got %+v", tc.why, got)
			}
		})
	}
}

func TestGoVarTypeHandlesGenericCompositeLiteral(t *testing.T) {
	// Mirrors TestGoReceiverMethodHandlesGenericReceiver: the type capture must
	// stop at the first non-word byte so "Set[int]{}" resolves to "Set", matching
	// what the parser indexed.
	src := `package p

func run() {
	s := &Set[int]{}
	s.Remove()
}
`
	got := varTypeViolationsForTest(t, src, knownSetOf("Set.Add"))
	if len(got) != 1 || got[0].Symbol != "Set.Remove" {
		t.Fatalf("want Set.Remove violation, got %+v", got)
	}
}

func TestGoVarTypeSurvivesWholeFileDuplicatedIntoAddedLines(t *testing.T) {
	// A Write of a file whose content is unchanged (or a whole-file Write in
	// general, per resolve_differential_test.go's "write-whole" posture) passes
	// the SAME text as both the pre-edit file and the proposed content — the
	// one real-world way ctx can contain a byte-identical binding line twice.
	// goVarTypes must recognise that as ONE binding, not two independent ones
	// that make the name ambiguous — text-based dedup, not occurrence counting.
	src := `package p

func run() {
	var r *Reader
	r.Parse()
}
`
	lines := TextToAddedLines(src)
	got := GoVarTypeMethodViolations(lines, lines, knownSetOf("Reader.Fetch"))
	if len(got) != 1 || got[0].Symbol != "Reader.Parse" {
		t.Fatalf("want Reader.Parse violation even with wholeFile==addedLines, got %+v", got)
	}
}

func TestGoVarTypeAbstainsWhenNameCollidesWithAParameterElsewhere(t *testing.T) {
	// Pins the exact false positive the compiler-oracle differential's
	// external-corpus sweep found in golang.org/x/text's
	// unicode/cldr/resolve.go: "parent" is a reflect.Value PARAMETER in one
	// function and a genuinely *LDML-typed local in another. Parameters are
	// invisible to the var/:= rebinding gate, so without goFuncSignatureIdents
	// this reads as a safe, unambiguous, file-wide binding — and isn't.
	src := `package p

func run(parent *Other) {
	_ = parent
}

func other() {
	var parent *LDML
	parent.Parse()
}
`
	got := varTypeViolationsForTest(t, src, knownSetOf("LDML.Fetch"))
	if len(got) != 0 {
		t.Fatalf("want no violation — parent collides with a parameter name in another function, got %+v", got)
	}
}

func TestGoVarTypeAbstainsOnMultiLineSignatureWithBareInterfaceReturnParam(t *testing.T) {
	// Pins the adversarial-review finding: a multi-line signature carrying a
	// map[string]interface{} parameter on an earlier line was closing the
	// signature scan at THAT self-contained brace pair instead of the real
	// body brace, silently failing to reserve "parent" on the following
	// parameter line — reopening the exact collision bug #2 above was meant
	// to close, via the most ordinary of Go idioms rather than an exotic one.
	src := `package p

func handler(
	opts map[string]interface{},
	parent *Other,
) {
	parent.IsNil()
}

func other() {
	var parent *LDML
	_ = parent
}
`
	got := varTypeViolationsForTest(t, src, knownSetOf("LDML.Fetch"))
	if len(got) != 0 {
		t.Fatalf("want no violation — parent collides with a parameter name on a later signature line, got %+v", got)
	}
}

func TestGoVarTypeAbstainsOnCompositeLiteralWithChainedCall(t *testing.T) {
	// Pins the adversarial-review finding: v := T{}.Clone() binds v to
	// whatever Clone() returns, not to T — the composite literal is the
	// receiver of a chained call, not the RHS itself.
	src := `package p

func run() {
	v := T{}.Clone()
	v.Flush()
}
`
	got := varTypeViolationsForTest(t, src, knownSetOf("T.Fetch"))
	if len(got) != 0 {
		t.Fatalf("want no violation — v's real type is Clone()'s return type, not T, got %+v", got)
	}
}

func TestGoVarTypeAbstainsOnUnicodeSuffixedType(t *testing.T) {
	// Pins the adversarial-review finding: RE2's \w/\b are ASCII-only, so
	// "var v *Readerテスト" truncates the type capture to "Reader" instead of
	// failing to match — a real type in the repo whose name is coincidentally
	// an ASCII prefix of the unicode one would then be bound wrongly.
	src := `package p

func run() {
	var v *Readerテスト
	v.Zap()
}
`
	got := varTypeViolationsForTest(t, src, knownSetOf("Reader.Fetch"))
	if len(got) != 0 {
		t.Fatalf("want no violation — v's real type is Readerテスト, not Reader, got %+v", got)
	}
}

func TestGoVarTypeAbstainsOnMultiLineCompositeLiteralWithChainedCall(t *testing.T) {
	// Pins the /code-review finding: the single-line chained-call fix
	// (TestGoVarTypeAbstainsOnCompositeLiteralWithChainedCall) checked
	// matchingBraceClose's result only when it found a same-line close
	// (close >= 0); when the literal spans multiple lines, close is -1 and
	// the code silently proceeded to bind anyway — contradicting
	// matchingBraceClose's own documented contract that -1 means "unknown",
	// not "does not close".
	src := `package p

func run() {
	v := &T{
		Field: 1,
	}.Clone()
	v.Flush()
}
`
	got := varTypeViolationsForTest(t, src, knownSetOf("T.Fetch"))
	if len(got) != 0 {
		t.Fatalf("want no violation — v's real type is Clone()'s return type, not T, got %+v", got)
	}
}

func TestGoVarTypesIgnoresUntypedForm(t *testing.T) {
	src := `package p

func run() {
	r := open()
	_ = r
}
`
	got, _ := goVarTypes(TextToAddedLines(src))
	if len(got) != 0 {
		t.Errorf("a call-result assignment binds no type this function can read, got %v", got)
	}

	// A binding form this pass cannot read was never a candidate, so it must not
	// be reported as a DECLINED one either (#359). Asserting that on the source
	// above alone is inert: with `types` empty, goVarTypes returns at its early
	// exit before `dropped` is computed, so nothing could ever appear there
	// (found by adversarial review). The mixed source below is what makes the
	// claim observable — `v2` is bound twice, so the drop loop really runs, and
	// `r` must still be absent from its result.
	mixed := `package p

func a() {
	r := open()
	_ = r
	var v2 *Reader
	_ = v2
}

func b() {
	v2 := Reader{}
	_ = v2
}
`
	_, dropped := goVarTypes(TextToAddedLines(mixed))
	if _, ok := dropped["v2"]; !ok {
		t.Fatalf("control: a twice-bound local must be dropped, got %v", dropped)
	}
	if _, bad := dropped["r"]; bad {
		t.Errorf("an unreadable binding form must not count as a dropped local, got %v", dropped)
	}
}
