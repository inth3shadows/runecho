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

// TestShellParser_HeredocEligibility pins the two mirror-image defects #281/#282,
// both caused by `blanking` being asked a question it does not encode. It counts
// the frames whose BYTES are literal, which is right for masking and wrong for
// "can a heredoc start here": frameSubshell is excluded (so `<<` inside an
// arithmetic `((…))` read as a redirect — #281) while frameCmdSub is included (so a
// legal heredoc inside `$(…)` was never recognised — #282).
//
// Every case asserts on Functions AND SymbolHashes, because in both issues the
// wrong-symbol and missing-hash halves are separate failures: #281 drops a whole
// later function and leaves the enclosing one unhashed; #282 invents a function out
// of heredoc body text and unhashes the real one.
func TestShellParser_HeredocEligibility(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		want     []string
		wantHash []string // functions that must carry a non-empty body hash
		noHash   []string // names that must NOT appear as a hashed symbol
	}{
		{
			// #281: `((` pushes TWO frameSubshell frames, neither counted by
			// `blanking`, so `<<` was read as a heredoc opener. The delimiter scan
			// took `y` as the terminator and the newline handler then blanked every
			// following line hunting a terminator that never comes — swallowing f's
			// closing brace and all of g.
			name: "arithmetic left-shift is not a heredoc opener (#281)",
			src: "f() {\n" +
				"  ((x << y))\n" +
				"  echo after\n" +
				"}\n" +
				"\n" +
				"g() {\n" +
				"  echo two\n" +
				"}\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
		},
		{
			// #282: frameCmdSub IS counted by `blanking`, so the opener was
			// suppressed and the body never blanked. The first literal `)` in the
			// body then closed the `$(`, and the raw body text was scanned as code —
			// extracting `fakefunc` as a real definition, which is worse than missing
			// one: it enters the snapshot as a symbol that does not exist.
			name: "heredoc inside a command substitution is masked (#282)",
			src: "f() {\n" +
				"  x=$(cat <<EOF\n" +
				")\n" +
				"fakefunc() {\n" +
				"EOF\n" +
				")\n" +
				"  echo done\n" +
				"}\n",
			want:     []string{"f"},
			wantHash: []string{"f"},
			noHash:   []string{"fakefunc"},
		},
		{
			// `$((…))` was already safe, but for the WRONG reason: `$(` pushed a
			// frameCmdSub so `blanking > 0` suppressed the opener. Once frameCmdSub
			// stops counting, its safety has to come from the arithmetic rule
			// instead. Pinned because the basis changes even though the outcome
			// does not — the case that would silently regress.
			//
			// The shift amount is an IDENTIFIER, not a literal. `<< 2` would pass
			// even with the fix removed: the delimiter scan rejects a leading digit,
			// so no heredoc is ever pushed and the case proves nothing. `<< shift`
			// yields a real delimiter and actually exercises the rule.
			name: "arithmetic expansion left-shift is not a heredoc opener",
			src: "f() {\n" +
				"  y=$((x << shift))\n" +
				"  echo after\n" +
				"}\n" +
				"g() { echo two; }\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
		},
		{
			// The other reading of `((`: a subshell opening a subshell, which is NOT
			// arithmetic. Nothing here is a heredoc either way, so what this pins is
			// that the arithmetic heuristic does not disturb frame push/pop — if it
			// desynced the stack, g would be swallowed exactly as in #281.
			name: "adjacent-paren subshell still parses",
			src: "f() {\n" +
				"  ((echo a) | cat)\n" +
				"  ( (echo b) | cat )\n" +
				"}\n" +
				"g() { echo two; }\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
		},
		{
			// `( (` is two frames pushed two bytes apart, not an arithmetic `((`, and
			// a heredoc inside it is real and must be masked.
			//
			// This case does NOT pin the offset-adjacency comparison, and an earlier
			// version of this comment claimed it did — review checked and the claim
			// was false. What actually carries this is that arithShift only suppresses
			// inside a blanking frame, which this is not. The survivor is recorded in
			// inArithNest rather than asserted here.
			name: "non-adjacent nested subshells are not arithmetic",
			src: "f() {\n" +
				"  ( (cat <<EOF\n" +
				"fake_nested() {\n" +
				"EOF\n" +
				"  ) )\n" +
				"  echo done\n" +
				"}\n" +
				"g() { echo two; }\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
			noHash:   []string{"fake_nested"},
		},
		{
			// Review of #285 caught this as a REGRESSION the first version of the fix
			// introduced: `((cat <<EOF` is a subshell opening a subshell, not
			// arithmetic, and suppressing its heredoc is not a harmless miss.
			// frameSubshell is STRUCTURAL — its contents are kept as code — so the
			// unmasked body gets scanned and `fake_adj() {` becomes a definition that
			// does not exist. Same failure mode as #282, reached from the other side.
			//
			// A second review round then killed the same-line `))` confirmation that
			// first fixed it: it could not tell this from arithmetic when the closer
			// happened to be `))`, which the next case covers. arithShift no longer
			// suppresses in a structural frame at all.
			name: "adjacent-paren pipeline heredoc is real, not arithmetic",
			src: "f() {\n" +
				"  ((cat <<EOF\n" +
				"fake_adj() {\n" +
				"  echo hi\n" +
				"}\n" +
				"EOF\n" +
				") | tr a-z A-Z)\n" +
				"  echo done\n" +
				"}\n" +
				"g() { echo two; }\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
			noHash:   []string{"fake_adj"},
		},
		{
			// Also from the #285 review. A parenthesised subexpression inside
			// arithmetic pushes its own frame, so the arithmetic opener is no longer
			// the top pair — cmdSub@n, subshell@n+2, subshell@m. A top-pair-only
			// check re-opened #281 on exactly the spelling the arithmetic rule was
			// added to cover: `bit` was taken as a heredoc delimiter and every
			// following line blanked to EOF, dropping g entirely.
			//
			// Both spellings, because the bare `((` form was broken BEFORE this fix
			// too (blanking == 0 there), so it is a pre-existing bug the stack walk
			// closes rather than a regression it avoids.
			name: "parenthesised subexpression inside arithmetic",
			src: "f() {\n" +
				"  x=$(( (1 << bit) ))\n" +
				"  (( m = (1 << bit) ))\n" +
				"  echo after\n" +
				"}\n" +
				"g() { echo two; }\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
		},
		{
			// Multi-line `$((…))`: carried by arithShift's `blanking > 0` rule — inside
			// a command substitution the contents are masked wholesale, so suppressing
			// needs no confirmation and a missed heredoc emits nothing.
			//
			// Pinned because it is a no-regression-vs-master property: before the fix
			// `$(` made blanking > 0 and the opener was suppressed here.
			name: "multi-line arithmetic expansion is still not a heredoc",
			src: "f() {\n" +
				"  y=$((\n" +
				"    x << shift\n" +
				"  ))\n" +
				"  echo after\n" +
				"}\n" +
				"g() { echo two; }\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
		},
		{
			// Second review round, finding 1. The same-line `))` confirmation that
			// fixed the case above could not tell this from arithmetic: a subshell
			// pipeline whose own closer IS `))` on the opener's line. It suppressed a
			// real heredoc in a structural frame, so the body was scanned and
			// `fake_h() {` was emitted — the very failure the confirmation was added
			// to prevent, on a one-character variation of its own fixture.
			//
			// The lesson the fix took: stop guessing from syntax. arithShift now only
			// suppresses inside a blanking frame, and the bare `((x << y))` case is
			// carried downstream by the terminator lookahead, which is a fact about
			// the file rather than a guess about the grammar.
			name: "subshell pipeline whose own closer is )) is not arithmetic",
			src: "f() {\n" +
				"  ((cat <<EOF) | (tr a-z A-Z))\n" +
				"fake_h() {\n" +
				"  :\n" +
				"}\n" +
				"EOF\n" +
				"  echo done\n" +
				"}\n" +
				"g() { echo two; }\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
			noHash:   []string{"fake_h"},
		},
		{
			// Second review round, finding 2. Widening eligibility into `$(…)` made
			// the body-blanking loop reachable there, and it blanked forward
			// unconditionally — so a left-shift written any way OTHER than `((`
			// (which arithShift does not recognise) took `n` as a delimiter and
			// erased the rest of the file. #281's whole-file loss, relocated.
			//
			// Pins the terminator lookahead: no `n` line exists, so the heredoc is
			// abandoned and nothing is masked.
			name: "non-arithmetic left shift inside a command substitution",
			src: "f() {\n" +
				"  x=$(let y=1<<n)\n" +
				"  echo after\n" +
				"}\n" +
				"g() { echo two; }\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
		},
		{
			// Second review round, finding 3: #282 was only half closed. `"$(…)"` is
			// the commoner spelling than a bare `$(…)`, and counting any quoting frame
			// anywhere on the stack left it ineligible — so a `"` in the heredoc body
			// popped the double quote and the body was scanned as code. Broken on
			// master too, so this is a widening rather than a regression fix.
			//
			// Pins codeLevel answering on the INNERMOST frame instead of a count.
			//
			// The body carries a `"`, and it has to: without one the enclosing
			// frameDouble masks the body anyway and the case passes even with the fix
			// reverted. The quote is what pops out of the double quote and exposes
			// the body as code — verified by mutation, which is how the vacuous
			// version was caught.
			name: "heredoc inside a double-quoted command substitution",
			src: "f() {\n" +
				"  x=\"$(cat <<EOF\n" +
				"fake_m() {\n" +
				"  echo \"quoted\"\n" +
				"}\n" +
				"EOF\n" +
				")\"\n" +
				"  echo done\n" +
				"}\n" +
				"g() { echo two; }\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
			noHash:   []string{"fake_m"},
		},
		{
			// What keeps arithShift load-bearing once the terminator lookahead exists.
			// `shift` is a real bash builtin that commonly appears on a line of its
			// own, so a file can contain a line that exactly matches the operand name
			// — the lookahead then FINDS a "terminator" and blanks everything between,
			// swallowing g. Recognising `$((…))` as arithmetic is what stops the
			// opener from ever being taken.
			//
			// Added because mutation scoring showed arithShift was otherwise dead:
			// disabling it entirely left the suite green.
			// The `shift` line must be at column 0 and the assertion must reach
			// SymbolHashes: an indented operand line never matches the terminator, and
			// f still appears in Functions either way — only its body hash is lost
			// when the region through the false terminator is blanked. Both mistakes
			// were in the first draft of this case, and mutation is what exposed them.
			name: "arithmetic operand also appearing as a bare command line",
			src: "f() {\n" +
				"  y=$((x << shift))\n" +
				"  echo after\n" +
				"}\n" +
				"shift\n" +
				"h() {\n" +
				"  echo tail\n" +
				"}\n",
			want:     []string{"f", "h"},
			wantHash: []string{"f", "h"},
		},
		{
			// The ordinary top-level heredoc must not regress: the fix widens where a
			// heredoc is recognised, and the risk of widening is that the common case
			// starts behaving differently.
			name: "plain top-level heredoc still masks its body",
			src: "f() {\n" +
				"  cat <<EOF\n" +
				"buried() {\n" +
				"EOF\n" +
				"}\n" +
				"g() { echo two; }\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
			noHash:   []string{"buried"},
		},
		{
			// The two heredoc variants with their own rules — `<<-` tab stripping and
			// a quoted delimiter — exercised on the path #282 opens up. A fix that
			// only handled the bare form would leave these two broken inside `$(…)`.
			name: "dash and quoted heredocs inside a command substitution",
			src: "f() {\n" +
				"  a=$(cat <<-'END'\n" +
				"\tburied_dash() {\n" +
				"\tEND\n" +
				")\n" +
				"  echo done\n" +
				"}\n" +
				"g() { echo two; }\n",
			want:     []string{"f", "g"},
			wantHash: []string{"f", "g"},
			noHash:   []string{"buried_dash"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs, err := NewShellParser().Parse(tt.src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fs.Functions, tt.want) {
				t.Errorf("Functions = %v, want %v", fs.Functions, tt.want)
			}
			for _, name := range tt.wantHash {
				if fs.SymbolHashes["function:"+name] == "" {
					t.Errorf("%s has no body hash — its closing brace was masked away, so a body edit would be invisible to diff", name)
				}
			}
			for _, name := range tt.noHash {
				if _, ok := fs.SymbolHashes["function:"+name]; ok {
					t.Errorf("%s was hashed — heredoc body text entered the symbol table as a real definition", name)
				}
			}
		})
	}
}
