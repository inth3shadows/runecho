package parser

import (
	"reflect"
	"testing"
)

// TestShellParser_281_282_AcceptanceCases pins the two known, tracked bugs in
// the current maskShell heuristic (#281, #282) as the acceptance contract for
// the tree-sitter-bash rewrite (see
// ~/.claude/plans/runecho-shell-tree-sitter-bash.md). Both are t.Skip'd: three
// rounds of patching maskShell's frame-stack heuristic (closed, unmerged PR
// #285) each fixed one and introduced a new regression, so these stay red
// against the byte-masker on purpose rather than getting a fourth heuristic
// patch. Un-skip both once ShellParser is backed by the tree-sitter-bash AST
// walk — an AST should pass them with no special-casing.
func TestShellParser_281_282_AcceptanceCases(t *testing.T) {
	t.Run("281_bare_arith_double_paren_not_a_heredoc", func(t *testing.T) {
		// blanking (maskShell's heredoc-opener guard) counts only the
		// s/d/b/c/p frames, not frameSubshell — so a bare `((x << y))`
		// arithmetic compound reads its `<<` as a heredoc opener and the
		// terminator scan never finds delimiter `y`, blanking the rest of
		// the file including f's closing `}` and all of g.
		src := "f() {\n  ((x << y))\n  echo after\n}\n\ng() {\n  echo two\n}\n"
		fs, err := NewShellParser().Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"f", "g"}
		if !reflect.DeepEqual(fs.Functions, want) {
			t.Errorf("Functions = %v, want %v", fs.Functions, want)
		}
		for _, name := range want {
			if fs.SymbolHashes["function:"+name] == "" {
				t.Errorf("expected a body hash for %q, got none (hashes=%v)", name, fs.SymbolHashes)
			}
		}
	})

	t.Run("282_heredoc_inside_command_substitution_not_leaked_as_symbol", func(t *testing.T) {
		// maskShell counts frameCmdSub ($(...)) toward blanking, so the
		// heredoc-opener guard and body-blanker are both wrongly suppressed
		// inside a command substitution: no heredoc is recognized, the first
		// literal ')' inside the body closes $(...) early, and the leaked
		// heredoc body text ("fakefunc() {") is read as a real definition.
		src := "f() {\n  x=$(cat <<EOF\n)\nfakefunc() {\nEOF\n)\n  echo done\n}\n"
		fs, err := NewShellParser().Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"f"}
		if !reflect.DeepEqual(fs.Functions, want) {
			t.Errorf("Functions = %v, want %v (fakefunc is heredoc body text, not a definition)", fs.Functions, want)
		}
		if fs.SymbolHashes["function:f"] == "" {
			t.Error("expected a body hash for f, got none")
		}
	})
}
