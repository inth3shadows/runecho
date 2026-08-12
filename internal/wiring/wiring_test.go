package wiring

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write drops a config into a temp dir and returns its path.
func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

// goodConfig wires both contract events correctly.
const goodConfig = `{"hooks":{
  "PreToolUse":[{"matcher":"Edit|Write|MultiEdit","hooks":[{"type":"command","command":"runecho-guard --hook-mode","timeout":5}]}],
  "PostToolUse":[{"matcher":"Edit|Write|MultiEdit","hooks":[{"type":"command","command":"runecho-guard --outcome-mode","timeout":5}]}]
}}`

func TestCheck_GoodConfigHasNoProblems(t *testing.T) {
	hf, err := ParseFile(write(t, goodConfig))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if p := Check(hf); len(p) != 0 {
		t.Fatalf("Check = %v, want no problems", p)
	}
}

// TestCheck_IgnoresOtherToolsHooks is the regression that matters. This logic
// used to live in a test whose fixtures contained runecho hooks and nothing
// else, so it judged EVERY entry on the event. The first time it was pointed at
// a real ~/.claude/settings.json — which wires Bash, Read and ExitPlanMode hooks
// for unrelated tools on the same events — it reported each of them as a matcher
// violation. A diagnostic that cries wolf on a correct install is worse than no
// diagnostic, because the one real line is buried in it.
func TestCheck_IgnoresOtherToolsHooks(t *testing.T) {
	cfg := `{"hooks":{
	  "PreToolUse":[
	    {"matcher":"Bash","hooks":[{"type":"command","command":"some-other-tool.sh"}]},
	    {"matcher":"Read","hooks":[{"type":"command","command":"cred-path-block.sh"}]},
	    {"matcher":"Edit|Write|MultiEdit","hooks":[{"type":"command","command":"runecho-guard --hook-mode","timeout":5}]}],
	  "PostToolUse":[
	    {"matcher":"ExitPlanMode","hooks":[{"type":"command","command":"plan-logger.sh"}]},
	    {"matcher":"Edit|Write|MultiEdit","hooks":[{"type":"command","command":"runecho-guard --outcome-mode","timeout":5}]}]
	}}`
	hf, err := ParseFile(write(t, cfg))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if p := Check(hf); len(p) != 0 {
		t.Fatalf("Check flagged another tool's hooks: %v", p)
	}
}

func TestCheck_CatchesContractBreaks(t *testing.T) {
	cases := []struct {
		name, cfg, want string
	}{
		{
			// The gap that actually shipped: PreToolUse wired, PostToolUse absent.
			// The guard still asks and still exits 0; it just records no outcome,
			// so fpreport has no join key and learned-allow can never fire.
			name: "PostToolUse missing entirely",
			cfg: `{"hooks":{"PreToolUse":[{"matcher":"Edit|Write|MultiEdit",
			  "hooks":[{"type":"command","command":"runecho-guard --hook-mode","timeout":5}]}]}}`,
			want: "no PostToolUse hook invoking the guard",
		},
		{
			name: "guard wired on a narrower matcher",
			cfg: strings.Replace(goodConfig, `"PreToolUse":[{"matcher":"Edit|Write|MultiEdit"`,
				`"PreToolUse":[{"matcher":"Write"`, 1),
			want: `matcher is "Write"`,
		},
		{
			name: "guard hook with no timeout",
			cfg:  strings.Replace(goodConfig, `--hook-mode","timeout":5`, `--hook-mode"`, 1),
			want: `no "timeout"`,
		},
		{
			name: "guard hook with the wrong timeout",
			cfg:  strings.Replace(goodConfig, `--hook-mode","timeout":5`, `--hook-mode","timeout":3`, 1),
			want: "timeout = 3, want 5",
		},
		{
			// An event wired to the guard but invoking the WRONG mode — e.g. a
			// hand-merge that pasted --hook-mode under both events.
			name: "PostToolUse invokes the wrong mode",
			cfg: strings.Replace(goodConfig, `runecho-guard --outcome-mode`,
				`runecho-guard --hook-mode`, 1),
			want: "never invokes --outcome-mode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hf, err := ParseFile(write(t, tc.cfg))
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			problems := Check(hf)
			for _, p := range problems {
				if strings.Contains(p.Detail, tc.want) {
					return
				}
			}
			t.Fatalf("Check = %v, want a problem containing %q", problems, tc.want)
		})
	}
}

// TestParseFile_MalformedIsAnError pins the more dangerous half: Claude Code
// ignores a settings.json it cannot parse, so malformed JSON does not
// half-wire the hooks, it silently disables every hook in the file. Decoding to
// an empty config would report that as "no hooks found" and send the user
// looking for a missing entry instead of a syntax error.
func TestParseFile_MalformedIsAnError(t *testing.T) {
	_, err := ParseFile(write(t, `{"hooks": {`))
	if err == nil {
		t.Fatal("ParseFile on malformed JSON returned no error")
	}
	if !strings.Contains(err.Error(), "disabling every hook") {
		t.Fatalf("error %q does not explain the blast radius", err)
	}
}

func TestParseFile_MissingIsNotExist(t *testing.T) {
	_, err := ParseFile(filepath.Join(t.TempDir(), "absent.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v, want an IsNotExist error so callers can skip absent channels", err)
	}
}

// TestEventsIsStable guards the report against map-iteration shuffle: a doctor
// report that reorders between runs cannot be diffed.
func TestEventsIsStable(t *testing.T) {
	first := Events()
	for range 20 {
		got := Events()
		for i := range first {
			if got[i] != first[i] {
				t.Fatalf("Events() order is unstable: %v then %v", first, got)
			}
		}
	}
	if len(first) != len(Contract) {
		t.Fatalf("Events() returned %d of %d contract events", len(first), len(Contract))
	}
}
