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
	// Shell has no imports/classes/exports and no body hashes (documented).
	if len(fs.Imports) != 0 || len(fs.Classes) != 0 || len(fs.Exports) != 0 {
		t.Errorf("imports/classes/exports should be empty, got %v / %v / %v", fs.Imports, fs.Classes, fs.Exports)
	}
	if fs.SymbolHashes != nil {
		t.Errorf("SymbolHashes should be nil (regex parser, no body span), got %v", fs.SymbolHashes)
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
