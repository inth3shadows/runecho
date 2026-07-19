package guard

import (
	"testing"

	"github.com/inth3shadows/runecho/internal/depindex"
)

// httpStub mirrors net/http's shape closely enough for the gate tests: Get and
// Post exist, Gett does not.
var httpStub = stubIndex{
	"net/http":                    resolvedPkg("Get", "Post", "Client", "NewRequest", "StatusOK"),
	"github.com/google/uuid":      resolvedPkg("New", "NewString", "Parse"),
	"github.com/vendor/lazything": {Res: depindex.Partial, Reason: "not scannable"},
}

const goMod = "github.com/inth3shadows/runecho"

func goDepViolations(t *testing.T, src string, idx depindex.Index) []Violation {
	t.Helper()
	return GoDepQualifiedViolations(nil, TextToAddedLines(src), goMod, idx)
}

func TestGoDepQualified_FlagsAbsentSymbol(t *testing.T) {
	src := "package main\n\nimport \"net/http\"\n\nfunc run() {\n\thttp.Gett(\"http://x\")\n}\n"
	got := goDepViolations(t, src, httpStub)
	if len(got) != 1 || got[0].Symbol != "http.Gett" {
		t.Fatalf("violations = %v, want [http.Gett]", symbolList(got))
	}
	if got[0].Lang != LangGo {
		t.Errorf("Lang = %q, want %q", got[0].Lang, LangGo)
	}
	if got[0].Suggestion != "Get" {
		t.Errorf("Suggestion = %q, want \"Get\" from the dependency's own names", got[0].Suggestion)
	}
}

func TestGoDepQualified_AliasedImport(t *testing.T) {
	src := "package main\n\nimport gu \"github.com/google/uuid\"\n\nfunc f() { _ = gu.NewStringz() }\n"
	got := goDepViolations(t, src, httpStub)
	if len(got) != 1 || got[0].Symbol != "gu.NewStringz" {
		t.Fatalf("violations = %v, want [gu.NewStringz]", symbolList(got))
	}
}

// The false-positive suite: every case is valid code and must produce nothing.
func TestGoDepQualified_NeverFlags(t *testing.T) {
	tests := []struct {
		name string
		src  string
		idx  depindex.Index
	}{
		{
			"symbol exists",
			"package main\n\nimport \"net/http\"\n\nfunc f() { http.Get(\"u\") }\n",
			httpStub,
		},
		{
			// Gate 1: same-repo imports belong to GoQualifiedViolations, which
			// validates them against the repo IR. Double-checking here would
			// flag every internal symbol, since no dep index contains them.
			"same-repo import",
			"package main\n\nimport \"github.com/inth3shadows/runecho/internal/guard\"\n\nfunc f() { guard.Nope() }\n",
			httpStub,
		},
		{
			// Gate 4: Partial resolution proves nothing by absence.
			"partial resolution",
			"package main\n\nimport \"github.com/vendor/lazything\"\n\nfunc f() { lazything.Whatever() }\n",
			httpStub,
		},
		{
			// Gate 4: package not in the index at all.
			"unknown package",
			"package main\n\nimport \"github.com/nobody/nothing\"\n\nfunc f() { nothing.Whatever() }\n",
			httpStub,
		},
		{
			// Gate 2: a local named `http` could shadow the import, making
			// `http.Gett()` a method call on a value.
			"alias used bare",
			"package main\n\nimport \"net/http\"\n\nfunc f(http *Server) { use(http); http.Gett() }\n",
			httpStub,
		},
		{
			// Gate 3: an unexported selector cannot compile as a package call, so
			// seeing one means the qualifier is not a package.
			"unexported selector",
			"package main\n\nimport \"net/http\"\n\nfunc f() { http.gett() }\n",
			httpStub,
		},
		{
			"blank and dot imports bind no qualifier",
			"package main\n\nimport (\n\t_ \"net/http\"\n\t. \"github.com/google/uuid\"\n)\n\nfunc f() { http.Gett() }\n",
			httpStub,
		},
		{
			"deeper selector",
			"package main\n\nimport \"net/http\"\n\nfunc f() { x.http.Gett() }\n",
			httpStub,
		},
		{
			"inside a string literal",
			"package main\n\nimport \"net/http\"\n\nfunc f() { s := \"http.Gett()\"; _ = s }\n",
			httpStub,
		},
		{
			"inside a comment",
			"package main\n\nimport \"net/http\"\n\n// http.Gett() is not a thing\nfunc f() {}\n",
			httpStub,
		},
		{
			"inside a raw string",
			"package main\n\nimport \"net/http\"\n\nvar doc = `\nhttp.Gett()\n`\n",
			httpStub,
		},
		{
			"nil index",
			"package main\n\nimport \"net/http\"\n\nfunc f() { http.Gett() }\n",
			nil,
		},
		{
			"no imports",
			"package main\n\nfunc f() { http.Gett() }\n",
			httpStub,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := goDepViolations(t, tt.src, tt.idx); len(got) != 0 {
				t.Fatalf("violations = %v, want none", symbolList(got))
			}
		})
	}
}

func TestGoDepQualified_BlockImportsAndDedupe(t *testing.T) {
	src := "package main\n\nimport (\n\t\"net/http\"\n\t\"github.com/google/uuid\"\n)\n\n" +
		"func f() {\n\thttp.Gett()\n\thttp.Gett()\n\tuuid.NewString()\n}\n"
	got := goDepViolations(t, src, httpStub)
	if len(got) != 1 || got[0].Symbol != "http.Gett" {
		t.Fatalf("violations = %v, want exactly one [http.Gett]", symbolList(got))
	}
}

// TestGoDepQualified_EndToEndAgainstRealModuleCache wires the real resolver to
// the real guard against this repo's OWN go.mod and the machine's module cache —
// the check that the two halves agree on real input rather than on stubs.
func TestGoDepQualified_EndToEndAgainstRealModuleCache(t *testing.T) {
	idx := depindex.NewGoIndex("..")
	if probe := idx.Lookup("strings"); probe.Res != depindex.Resolved {
		t.Skipf("stdlib not resolvable here (%s); needs GOROOT", probe.Reason)
	}

	bad := "package main\n\nimport \"strings\"\n\nfunc f() { strings.Containz(\"a\", \"b\") }\n"
	got := GoDepQualifiedViolations(nil, TextToAddedLines(bad), goMod, idx)
	if len(got) != 1 || got[0].Symbol != "strings.Containz" {
		t.Fatalf("violations = %v, want [strings.Containz]", symbolList(got))
	}
	if got[0].Suggestion != "Contains" {
		t.Errorf("Suggestion = %q, want \"Contains\"", got[0].Suggestion)
	}

	good := "package main\n\nimport \"strings\"\n\nfunc f() { strings.Contains(\"a\", \"b\") }\n"
	if got := GoDepQualifiedViolations(nil, TextToAddedLines(good), goMod, idx); len(got) != 0 {
		t.Fatalf("violations = %v on valid code, want none", symbolList(got))
	}
}
