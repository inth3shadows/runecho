package parser

import (
	"reflect"
	"testing"
)

func TestShellParser_Extensions(t *testing.T) {
	p := NewShellParser()
	for _, ext := range []string{".sh", ".bash"} {
		if !p.SupportsExtension(ext) {
			t.Errorf("SupportsExtension(%q) = false, want true", ext)
		}
	}
	for _, ext := range []string{".go", ".py", ".zsh", ".txt", ""} {
		if p.SupportsExtension(ext) {
			t.Errorf("SupportsExtension(%q) = true, want false", ext)
		}
	}
}

// TestShellParser_FunctionForms covers both definition syntaxes and the extended
// name charset (bash allows -, ., :), and pins that command invocations, arrays,
// and arithmetic are NOT mistaken for definitions.
func TestShellParser_FunctionForms(t *testing.T) {
	src := `#!/usr/bin/env bash
set -euo pipefail

# a comment mentioning fake() { } must be ignored
greet() {
  echo hi
}

function deploy {
  echo deploying
}

function build() { echo built; }

my-hook.pre:run() {
  echo namespaced
}

# calls / non-defs that must NOT be extracted:
grep -q foo bar
arr=( a b c )
x=$(( 1 << 4 ))
result=$(compute_something)
`
	fs, err := NewShellParser().Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"build", "deploy", "greet", "my-hook.pre:run"}
	if !reflect.DeepEqual(fs.Functions, want) {
		t.Errorf("Functions = %v, want %v", fs.Functions, want)
	}
	// Shell has no imports/classes/exports.
	if len(fs.Imports) != 0 || len(fs.Classes) != 0 || len(fs.Exports) != 0 {
		t.Errorf("imports/classes/exports should be empty, got %v / %v / %v", fs.Imports, fs.Classes, fs.Exports)
	}
	// Each function now carries a body hash (enables modified-symbol diffing).
	for _, name := range want {
		if fs.SymbolHashes["function:"+name] == "" {
			t.Errorf("expected a body hash for %q, got none (hashes=%v)", name, fs.SymbolHashes)
		}
	}
	// Start lines power `map`; greet is on line 5.
	if got := fs.SymbolLines["function:greet"]; got != 5 {
		t.Errorf("greet start line = %d, want 5", got)
	}
}

// TestShellParser_HeredocBodySkipped pins that function-def-looking lines inside a
// heredoc body are NOT extracted — the realistic case of a script that writes out
// another script. Both `<<WORD` and the tab-stripping `<<-WORD` forms, and that a
// herestring `<<<` does NOT trigger body-skipping.
func TestShellParser_HeredocBodySkipped(t *testing.T) {
	src := "real_one() {\n" +
		"  cat > out.sh <<EOF\n" +
		"buried_in_body() {\n" +
		"  echo not a real symbol\n" +
		"}\n" +
		"EOF\n" +
		"}\n" +
		"\tcat <<-DASH\n" +
		"\talso_buried() {\n" +
		"\tDASH\n" +
		"after_heredoc() { echo real; }\n" +
		"echo <<< also_not_a_heredoc\n" +
		"real_two() { echo real; }\n"
	fs, err := NewShellParser().Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"after_heredoc", "real_one", "real_two"}
	if !reflect.DeepEqual(fs.Functions, want) {
		t.Errorf("Functions = %v, want %v (heredoc bodies must be skipped, herestring must not)", fs.Functions, want)
	}
}

// funcHash is a small helper: parse src and return the body hash of function name.
func funcHash(t *testing.T, src, name string) string {
	t.Helper()
	fs, err := NewShellParser().Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	return fs.SymbolHashes["function:"+name]
}

// TestShellParser_BodyHashing pins the #155 body-hashing follow-up: a function's
// body hash changes when its body changes and is stable when only code OUTSIDE it
// changes — this is what makes a shell function-body edit show as "modified" in
// diff/verify instead of being invisible.
func TestShellParser_BodyHashing(t *testing.T) {
	base := "f() {\n  echo one\n}\ng() {\n  echo two\n}\n"
	bodyEdit := "f() {\n  echo ONE_CHANGED\n}\ng() {\n  echo two\n}\n"
	outsideEdit := "f() {\n  echo one\n}\ng() {\n  echo two\n}\n# a new trailing comment\n"

	if funcHash(t, base, "f") == funcHash(t, bodyEdit, "f") {
		t.Error("f's body hash must change when its body changes")
	}
	if funcHash(t, base, "g") != funcHash(t, bodyEdit, "g") {
		t.Error("g's body hash must be stable when only f changed")
	}
	if funcHash(t, base, "f") != funcHash(t, outsideEdit, "f") {
		t.Error("f's body hash must be stable when only code outside it changed")
	}
	if h := funcHash(t, base, "f"); h == "" {
		t.Error("f should have a body hash")
	}
}

// TestShellParser_ParamNestedBraceBodySpan pins the masker's parameter-expansion
// nesting: a `${…{…}…}` (e.g. a brace-shaped default) must not close the param at
// the FIRST inner `}`. If it did, the leaked `}` would close the FUNCTION body early,
// truncating its hash so an edit to the body AFTER the expansion goes undetected
// (a modified-symbol diff false-negative). The hash must therefore change when the
// tail after the `${…{…}…}` changes.
func TestShellParser_ParamNestedBraceBodySpan(t *testing.T) {
	base := "real() {\n  y=${z:-{a,b}}\n  echo original_tail\n}\n"
	tailEdit := "real() {\n  y=${z:-{a,b}}\n  echo CHANGED_TAIL\n}\n"
	if funcHash(t, base, "real") == "" {
		t.Fatal("real should have a body hash")
	}
	if funcHash(t, base, "real") == funcHash(t, tailEdit, "real") {
		t.Error("editing the body AFTER a ${…{…}…} must change the hash — the nested brace leaked and truncated the span")
	}
}

// TestShellParser_MaskingKeepsBodySpanHonest pins the masker: a `}` or a
// function-def-shaped line inside a string, command substitution, parameter
// expansion, comment, or escape must NOT close a body early or be read as a def.
func TestShellParser_MaskingKeepsBodySpanHonest(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "brace in double-quoted string",
			src:  "outer() {\n  echo \"a } b\"\n  inner() { echo x; }\n}\n",
			want: []string{"inner", "outer"}, // the `}` in the string must not close outer early
		},
		{
			name: "def-shaped line in single-quoted string",
			src:  "real() {\n  x='fake() {'\n  echo ok\n}\n",
			want: []string{"real"}, // fake() inside '...' is not a definition
		},
		{
			name: "brace in parameter expansion with nested string",
			src:  "real() {\n  y=${z:-\"}\"}\n  echo ok\n}\n",
			want: []string{"real"}, // the `}` inside ${...} / its string must not close the body
		},
		{
			name: "brace in command substitution",
			src:  "real() {\n  v=$(echo '{' ; echo '}')\n  echo ok\n}\n",
			want: []string{"real"},
		},
		{
			name: "brace in comment",
			src:  "real() {\n  echo hi   # trailing } brace in a comment\n}\n",
			want: []string{"real"},
		},
		{
			name: "escaped brace",
			src:  "real() {\n  echo \\}\n}\n",
			want: []string{"real"},
		},
		{
			name: "multi-line double-quoted string spanning a fake def",
			src:  "real() {\n  echo \"line1\nfake() {\nline3\"\n}\nafter() { echo x; }\n",
			want: []string{"after", "real"}, // the string spans lines; fake() is inside it
		},
		{
			name: "subshell body",
			src:  "sub() (\n  cd /tmp && ls\n)\nafter() { echo x; }\n",
			want: []string{"after", "sub"}, // `()` body brace-matched too
		},
		{
			name: "backtick command substitution",
			src:  "real() {\n  x=`echo '{' ; echo '}'`\n  echo ok\n}\n",
			want: []string{"real"}, // backtick body blanked; the `}` inside must not close the body
		},
		{
			name: "param default with nested string and backtick",
			src:  "real() {\n  y=${z:-\"a`echo b`c\"}\n  echo ok\n}\n",
			want: []string{"real"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, err := NewShellParser().Parse(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fs.Functions, tc.want) {
				t.Errorf("Functions = %v, want %v", fs.Functions, tc.want)
			}
		})
	}
}

// TestShellParser_HeredocQuotedAndStacked pins the two heredoc edges the masker now
// handles: a quoted delimiter (`<<'EOF'`) whose body is still skipped, and STACKED
// heredocs on one line (`<<A <<B`) where BOTH bodies are skipped in order.
func TestShellParser_HeredocQuotedAndStacked(t *testing.T) {
	quoted := "real() {\n  cat <<'EOF'\nburied() {\nEOF\n}\nafter() { echo x; }\n"
	if fs, _ := NewShellParser().Parse(quoted); !reflect.DeepEqual(fs.Functions, []string{"after", "real"}) {
		t.Errorf("quoted heredoc: Functions = %v, want [after real]", fs.Functions)
	}

	stacked := "cat <<A <<B\nburied_a() {\nA\nburied_b() {\nB\nafter() { echo x; }\n"
	if fs, _ := NewShellParser().Parse(stacked); !reflect.DeepEqual(fs.Functions, []string{"after"}) {
		t.Errorf("stacked heredoc: Functions = %v, want [after] (both A and B bodies skipped)", fs.Functions)
	}
}

// TestShellParser_HeredocTerminatorExact pins that the heredoc delimiter is matched
// EXACTLY (bash semantics): a body line `EOF ` (delimiter + trailing space) is NOT a
// terminator, so it stays part of the body and a function-shaped line after it is
// still skipped. A trimmed compare would end the heredoc early and leak that line.
func TestShellParser_HeredocTerminatorExact(t *testing.T) {
	src := "real() {\n" +
		"  cat <<EOF\n" +
		"EOF \n" + // trailing space: NOT the delimiter, still body
		"leaked() {\n" + // would be extracted if `EOF ` wrongly terminated
		"EOF\n" + // the real delimiter, exact
		"}\n" +
		"after() { echo x; }\n"
	fs, err := NewShellParser().Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"after", "real"}
	if !reflect.DeepEqual(fs.Functions, want) {
		t.Errorf("Functions = %v, want %v (a trailing-space delimiter line must NOT terminate the heredoc)", fs.Functions, want)
	}
}
