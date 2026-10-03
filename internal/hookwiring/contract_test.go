package hookwiring

import (
	"strings"
	"testing"
)

func TestInvokesMode(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"guard.sh --hook-mode", true},
		{`"$CLAUDE_PLUGIN_ROOT/hooks/guard.sh" --hook-mode`, true},
		{"runecho-guard --hook-mode||true", true},
		{`sh -c "exec guard.sh --hook-mode;"`, true},
		{"runecho-guard --hook-mode=1", true},
		{"RUNECHO_MODE=--hook-mode guard.sh", false},
		{`RUNECHO_MODE="--hook-mode" guard.sh`, false},
		{"RUNECHO_MODE='--hook-mode' guard.sh", false},
		{"A=b guard.sh --hook-mode", true},
		{"guard.sh --hook-mode-extra", false},
		{"guard.sh --no-hook-mode", false},
		{"guard.sh", false},
	}
	for _, c := range cases {
		if got := invokesMode(c.cmd, "--hook-mode"); got != c.want {
			t.Errorf("invokesMode(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

// The guard's own entry is still held to the contract: a narrower matcher or a
// missing timeout on it is a violation even though unrelated entries are not.
func TestCheckChannel_GuardEntryStillJudged(t *testing.T) {
	cfg := `{"hooks": {
  "PreToolUse":  [{"matcher": "Edit", "hooks": [{"type": "command", "command": "guard.sh --hook-mode", "timeout": 5}]}],
  "PostToolUse": [{"matcher": "Edit|Write|MultiEdit", "hooks": [{"type": "command", "command": "guard.sh --outcome-mode"}]}]
}}`
	v, err := CheckChannel([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(v, "\n")
	if !strings.Contains(got, `PreToolUse matcher is "Edit"`) || !strings.Contains(got, `PostToolUse hook has no "timeout"`) {
		t.Errorf("violations = %q, want the guard entry's matcher and timeout flagged", got)
	}
}
