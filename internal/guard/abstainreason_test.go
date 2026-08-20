package guard

import "testing"

// #359: the four checks that could never report WHY they found nothing now
// return an abstain reason alongside their findings. These tests pin two things
// per check: that each reason is reachable, and — at least as important — that
// the shapes which were never candidates keep reporting NO reason. A reason that
// fires on every edit is worse than none: it would make every var-type or
// qualified verdict read as "unknown" and the measurement #359 exists for would
// be no better than the "ok" it replaced.

func TestGoQualifiedAbstainReasons(t *testing.T) {
	const modulePath = "example.com/m"
	cases := []struct {
		name  string
		file  string
		added string
		known map[string]struct{}
		want  string
	}{
		{
			name: "shadowed qualifier",
			// snap is imported from this repo AND used bare (`snap := ...`), so
			// the shadow gate cannot tell the package from the local.
			file: `package p

import "example.com/m/internal/snap"

func other() {
	snap := build()
	_ = snap
}
`,
			added: `	snap.Missing()`,
			known: knownSetOf("build"),
			want:  "shadowed-qualifier",
		},
		{
			name: "unexported selector",
			file: `package p

import "example.com/m/internal/snap"
`,
			added: `	snap.missing()`,
			known: knownSetOf(),
			want:  "unexported-selector",
		},
		{
			name: "resolving call is not an abstain",
			file: `package p

import "example.com/m/internal/snap"
`,
			added: `	snap.Build()`,
			known: knownSetOf("Build"),
			want:  "",
		},
		{
			name: "violation is not an abstain",
			file: `package p

import "example.com/m/internal/snap"
`,
			added: `	snap.Missing()`,
			known: knownSetOf("Build"),
			want:  "",
		},
		{
			// The single most important negative: stdlib and external calls are
			// most of every Go diff, and they were never this check's shape.
			name: "foreign qualifier is not an abstain",
			file: `package p

import (
	"fmt"

	"example.com/m/internal/snap"
)
`,
			added: `	fmt.Println(snap.Build())`,
			known: knownSetOf("Build"),
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := GoQualifiedViolationsWithReason(
				TextToAddedLines(tc.file), TextToAddedLines(tc.added), tc.known, modulePath)
			if got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGoQualifiedShadowedQualifierStillReportsRealViolations(t *testing.T) {
	// Two aliases, one shadowed and one clean: the abstain on the shadowed one
	// must not suppress the finding on the other.
	file := `package p

import (
	"example.com/m/internal/snap"
	"example.com/m/internal/keep"
)

func other() {
	snap := build()
	_ = snap
}
`
	vs, reason := GoQualifiedViolationsWithReason(
		TextToAddedLines(file),
		TextToAddedLines("\tsnap.Missing()\n\tkeep.AlsoMissing()"),
		knownSetOf("build"), "example.com/m")
	if len(vs) != 1 || vs[0].Symbol != "keep.AlsoMissing" {
		t.Fatalf("want the unshadowed alias still flagged, got %+v", vs)
	}
	if reason != "shadowed-qualifier" {
		t.Errorf("reason = %q, want shadowed-qualifier", reason)
	}
}

func TestGoReceiverMethodAbstainReasons(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		known map[string]struct{}
		want  string
	}{
		{
			name: "ambiguous receiver",
			src: `package p

func (r *Reader) Fetch() {
	r.Parse()
}

func (r *Writer) Flush() {}
`,
			known: knownSetOf("Reader.Fetch", "Writer.Flush"),
			want:  "ambiguous-receiver",
		},
		{
			name: "receiver type has no indexed members",
			src: `package p

func (r *Reader) Fetch() {
	r.Parse()
}
`,
			known: knownSetOf("someFunc"),
			want:  "no-indexed-members",
		},
		{
			name: "name known elsewhere as a plain symbol",
			src: `package p

func (r *Reader) Fetch() {
	r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch", "Parse"),
			want:  "name-known-elsewhere",
		},
		{
			name: "name known elsewhere as another type's method",
			src: `package p

func (r *Reader) Fetch() {
	r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch", "Writer.Parse"),
			want:  "name-known-elsewhere",
		},
		{
			name: "violation is not an abstain",
			src: `package p

func (r *Reader) Fetch() {
	r.Parse()
}
`,
			known: knownSetOf("Reader.Fetch", "Reader.Close"),
			want:  "",
		},
		{
			name: "resolving call is not an abstain",
			src: `package p

func (r *Reader) Fetch() {
	r.Close()
}
`,
			known: knownSetOf("Reader.Fetch", "Reader.Close"),
			want:  "",
		},
		{
			// A selector on anything that is not a receiver variable — the
			// overwhelming majority of Go call sites.
			name: "non-receiver selector is not an abstain",
			src: `package p

import "fmt"

func (r *Reader) Fetch() {
	fmt.Println(r.Close())
}
`,
			known: knownSetOf("Reader.Fetch", "Reader.Close"),
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := TextToAddedLines(tc.src)
			_, got := GoReceiverMethodViolationsWithReason(lines, lines, tc.known)
			if got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGoVarTypeAbstainReasons(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		known map[string]struct{}
		want  string
	}{
		{
			name: "ambiguous local: two distinct bindings",
			src: `package p

func a() {
	var v *Reader
	v.Parse()
}

func b() {
	v := Reader{}
	_ = v
}
`,
			known: knownSetOf("Reader.Fetch"),
			want:  "ambiguous-local",
		},
		{
			name: "local type has no indexed members",
			src: `package p

func a() {
	var v *Reader
	v.Parse()
}
`,
			known: knownSetOf("someFunc"),
			want:  "no-indexed-members",
		},
		{
			name: "name known elsewhere",
			src: `package p

func a() {
	var v *Reader
	v.Parse()
}
`,
			known: knownSetOf("Reader.Fetch", "Parse"),
			want:  "name-known-elsewhere",
		},
		{
			name: "violation is not an abstain",
			src: `package p

func a() {
	var v *Reader
	v.Parse()
}
`,
			known: knownSetOf("Reader.Fetch"),
			want:  "",
		},
		{
			// A local bound by a form this check cannot read was never a
			// candidate — it must not be reported as a declined one.
			name: "unreadable binding form is not an abstain",
			src: `package p

func a() {
	v := open()
	v.Parse()
}
`,
			known: knownSetOf("Reader.Fetch"),
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := TextToAddedLines(tc.src)
			_, got := GoVarTypeMethodViolationsWithReason(lines, lines, tc.known)
			if got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPyCallShapeAbstainReasons(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		added   string
		removed string
		write   bool
		want    string
	}{
		{
			name: "shadowed callee",
			file: `def foo(a=1):
    pass

foo = wrapper(foo)
`,
			added: `foo(bad=2)`,
			want:  "shadowed-callee",
		},
		{
			name: "nested def shadow",
			file: `def foo(a=1):
    pass

def outer():
    def foo(b=2):
        pass
`,
			added: `foo(bad=2)`,
			want:  "nested-def-shadow",
		},
		{
			name: "ambiguous decl shapes",
			file: `def foo(a=1):
    pass


def foo(b=2):
    pass
`,
			added: `foo(bad=3)`,
			want:  "ambiguous-decl-shapes",
		},
		{
			name: "unusable decl shape: **kwargs",
			file: `def foo(**kwargs):
    pass
`,
			added: `foo(bad=2)`,
			want:  "unusable-decl-shape",
		},
		{
			name: "unusable decl shape: decorated",
			file: `@wraps
def foo(a=1):
    pass
`,
			added: `foo(bad=2)`,
			want:  "unusable-decl-shape",
		},
		{
			name: "declaration edited by this hunk",
			file: `def foo(a=1):
    pass
`,
			added:   `foo(b=2)`,
			removed: `def foo(a=1):`,
			want:    "decl-edited-in-hunk",
		},
		{
			name: "dynamic binding",
			file: `def foo(a=1):
    pass

globals()["foo"] = other
`,
			added: `foo(bad=2)`,
			want:  "dynamic-binding",
		},
		{
			// The unreadable call must be to a callee THIS FILE declares, and
			// the edit must also carry a readable candidate — the reason is a
			// fallback that never buys itself a whole-file scan. Both halves
			// are pinned by the two negatives that follow.
			name: "unreliable call shape",
			file: `def foo(a=1):
    pass


def bar(b=2):
    pass
`,
			added: `bar(b=3)
foo(key=lambda x, y: x)`,
			want: "unreliable-call-shape",
		},
		{
			// An f-string argument sets Unreliable too, and an imported callee
			// was never this check's candidate — reporting it would fire on
			// ordinary Python (found by adversarial review).
			name: "unreliable call to an imported callee is not an abstain",
			file: `from lib import notify


def bar(b=2):
    pass
`,
			added: `bar(b=3)
notify(text=f"hello {name}")`,
			want: "",
		},
		{
			name: "unreliable call with no readable candidate records nothing",
			file: `def foo(a=1):
    pass
`,
			added: `foo(key=lambda x, y: x)`,
			want:  "",
		},
		{
			name: "violation is not an abstain",
			file: `def foo(a=1):
    pass
`,
			added: `foo(bad=2)`,
			want:  "",
		},
		{
			name: "accepted keyword is not an abstain",
			file: `def foo(a=1):
    pass
`,
			added: `foo(a=2)`,
			want:  "",
		},
		{
			// The common case by far: a kwarg call to something this file does
			// not declare. Owned by other checks, never this one's candidate.
			name: "cross-file callee is not an abstain",
			file: `from lib import bar
`,
			added: `bar(bad=2)`,
			want:  "",
		},
		{
			name:  "no kwargs at all is not an abstain",
			file:  `x = 1`,
			added: `print(x)`,
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fd := FileDiff{Path: "m.py", AddedLines: TextToAddedLines(tc.added)}
			var whole []AddedLine
			if !tc.write {
				whole = TextToAddedLines(tc.file)
			}
			var removed []AddedLine
			if tc.removed != "" {
				removed = TextToAddedLines(tc.removed)
			}
			_, got := PyCallShapeMismatchesWithReason(LangPython, whole, fd, removed, tc.write)
			if got != tc.want {
				t.Errorf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPyCallShapeOversizedPreEditFileOnlyWithACandidate(t *testing.T) {
	// nil wholeFile is how the hook reports an unreadable/oversized pre-edit
	// file. It is degraded coverage only if this edit actually had something to
	// check — a kwarg-free edit is out of shape, not uncovered.
	withKwarg := FileDiff{Path: "m.py", AddedLines: TextToAddedLines(`foo(bad=2)`)}
	if _, got := PyCallShapeMismatchesWithReason(LangPython, nil, withKwarg, nil, false); got != "oversized-pre-edit-file" {
		t.Errorf("reason = %q, want oversized-pre-edit-file", got)
	}
	noKwarg := FileDiff{Path: "m.py", AddedLines: TextToAddedLines(`foo(2)`)}
	if _, got := PyCallShapeMismatchesWithReason(LangPython, nil, noKwarg, nil, false); got != "" {
		t.Errorf("reason = %q, want empty — no candidate means no coverage was lost", got)
	}
}

// TestPyCallShapeForeignUnreliableIsSilent reproduces the exact inputs
// adversarial review used to demonstrate the pre-fix regression: an f-string or
// lambda keyword argument to an IMPORTED callee flipped this check from "ok" to
// "unknown" on ordinary Python, and — with no readable pre-edit file — produced
// a degraded reason that raises the strict advisory.
func TestPyCallShapeForeignUnreliableIsSilent(t *testing.T) {
	const file = `from lib import notify, register
`
	for _, added := range []string{
		`notify(text=f"hello {name}")`,
		`register(cb=lambda a, b: a)`,
		`notify(text=f"{a}", level="warn")`,
	} {
		fd := FileDiff{Path: "m.py", AddedLines: TextToAddedLines(added)}
		if _, got := PyCallShapeMismatchesWithReason(LangPython, TextToAddedLines(file), fd, nil, false); got != "" {
			t.Errorf("%s: reason = %q, want empty — the callee is imported", added, got)
		}
		// The same call with no readable pre-edit file must not manufacture a
		// degraded reason either: there was still no candidate of ours.
		if _, got := PyCallShapeMismatchesWithReason(LangPython, nil, fd, nil, false); got != "" {
			t.Errorf("%s (no pre-edit file): reason = %q, want empty", added, got)
		}
	}
}
