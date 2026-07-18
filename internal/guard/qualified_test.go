package guard

import "testing"

func linesOf(texts ...string) []AddedLine {
	out := make([]AddedLine, len(texts))
	for i, t := range texts {
		out[i] = AddedLine{Text: t, LineNo: i + 1}
	}
	return out
}

func TestParseGoModule(t *testing.T) {
	cases := map[string]string{
		"module github.com/acme/proj\n\ngo 1.24\n":  "github.com/acme/proj",
		"module   github.com/acme/proj  // comment": "github.com/acme/proj",
		"module \"github.com/acme/proj\"":           "github.com/acme/proj",
		"go 1.24\n":                                 "",
		"":                                          "",
	}
	for in, want := range cases {
		if got := parseGoModule(in); got != want {
			t.Errorf("parseGoModule(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameRepoGoAliases(t *testing.T) {
	mod := "github.com/acme/proj"
	file := linesOf(
		"package main",
		"import (",
		`	"fmt"`,                                // stdlib — external
		`	"github.com/other/dep"`,               // external
		`	"github.com/acme/proj/internal/snap"`, // same-repo, default alias snap
		`	pkg "github.com/acme/proj/internal/db"`, // same-repo, aliased pkg
		`	_ "github.com/acme/proj/internal/side"`, // blank import — excluded
		")",
	)
	got := sameRepoGoAliases(file, mod)
	for _, want := range []string{"snap", "pkg"} {
		if _, ok := got[want]; !ok {
			t.Errorf("expected alias %q in same-repo set, got %v", want, got)
		}
	}
	for _, notWant := range []string{"fmt", "dep", "side"} {
		if _, ok := got[notWant]; ok {
			t.Errorf("did not expect %q in same-repo set", notWant)
		}
	}
	if _, ok := sameRepoGoAliases(file, "")["snap"]; ok {
		t.Error("empty module path must yield no aliases (abstain)")
	}
}

func TestOnlySelectorQualifiers_DropsShadowed(t *testing.T) {
	cands := map[string]struct{}{"snap": {}, "clean": {}}
	// `snap` is shadowed by a local var (`snap :=`), `clean` only ever appears as
	// a selector.
	file := linesOf(
		"func f() {",
		"	snap := takeSnapshot()",
		"	snap.Commit()",
		"	clean.Do()",
		"}",
	)
	got := onlySelectorQualifiers(file, cands)
	if _, ok := got["snap"]; ok {
		t.Error("shadowed qualifier `snap` must be dropped (would be a FP)")
	}
	if _, ok := got["clean"]; !ok {
		t.Error("selector-only qualifier `clean` must be kept")
	}
}

func TestGoQualifiedViolations_FlagsHallucination(t *testing.T) {
	mod := "github.com/acme/proj"
	whole := linesOf(
		"package main",
		`import "github.com/acme/proj/internal/snap"`,
		"func f() { snap.NoSuchFunc() }",
	)
	added := linesOf("func f() { snap.NoSuchFunc() }")
	known := map[string]struct{}{"RealFunc": {}} // NoSuchFunc absent
	v := GoQualifiedViolations(whole, added, known, mod)
	if len(v) != 1 || v[0].Symbol != "snap.NoSuchFunc" {
		t.Fatalf("expected 1 violation snap.NoSuchFunc, got %+v", v)
	}
}

func TestGoQualifiedViolations_NoFalsePositives(t *testing.T) {
	mod := "github.com/acme/proj"
	known := map[string]struct{}{"RealFunc": {}}

	tests := []struct {
		name       string
		whole      []AddedLine
		added      []AddedLine
		modulePath string
	}{
		{
			name:       "known repo symbol is not flagged",
			whole:      linesOf(`import "github.com/acme/proj/internal/snap"`, "x := snap.RealFunc()"),
			added:      linesOf("x := snap.RealFunc()"),
			modulePath: mod,
		},
		{
			name:       "external package is never flagged",
			whole:      linesOf(`import "github.com/other/dep"`, "x := dep.Whatever()"),
			added:      linesOf("x := dep.Whatever()"),
			modulePath: mod,
		},
		{
			name:       "shadowed local var method call is not flagged",
			whole:      linesOf(`import "github.com/acme/proj/internal/snap"`, "snap := take()", "snap.Commit()"),
			added:      linesOf("snap.Commit()"),
			modulePath: mod,
		},
		{
			name:       "unexported selector is not flagged",
			whole:      linesOf(`import "github.com/acme/proj/internal/snap"`, "snap.doThing()"),
			added:      linesOf("snap.doThing()"),
			modulePath: mod,
		},
		{
			name:       "shadow introduced in the same edit is not flagged",
			whole:      linesOf(`import "github.com/acme/proj/internal/snap"`),
			added:      linesOf("snap := take()", "snap.Commit()"),
			modulePath: mod,
		},
		{
			name:       "empty module path abstains",
			whole:      linesOf(`import "github.com/acme/proj/internal/snap"`, "snap.NoSuchFunc()"),
			added:      linesOf("snap.NoSuchFunc()"),
			modulePath: "",
		},
		{
			name:       "call inside a string literal is not flagged",
			whole:      linesOf(`import "github.com/acme/proj/internal/snap"`, "s := \"snap.NoSuchFunc()\""),
			added:      linesOf("s := \"snap.NoSuchFunc()\""),
			modulePath: mod,
		},
		{
			name:       "deeper selector a.snap.NoSuchFunc is not flagged",
			whole:      linesOf(`import "github.com/acme/proj/internal/snap"`, "a.snap.NoSuchFunc()"),
			added:      linesOf("a.snap.NoSuchFunc()"),
			modulePath: mod,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if v := GoQualifiedViolations(tc.whole, tc.added, known, tc.modulePath); len(v) != 0 {
				t.Errorf("expected 0 violations, got %+v", v)
			}
		})
	}
}
