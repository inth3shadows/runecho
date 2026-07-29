// contract_test.go — session-id resolution for `contract activate|deactivate`
// (issue #12, D3a).
//
// These tests pin precedence and the empty-env refusal. They deliberately do
// NOT claim the feature works end-to-end: the live risk is that
// $CLAUDE_CODE_SESSION_ID and the PreToolUse payload's `session_id` are
// different identifiers, in which case activate binds a row the guard never
// reads and every test here still passes. That gate is an acceptance run
// recorded in ~/.claude/plans/runecho-12-contracts.md, not a unit test.
package main

import (
	"strings"
	"testing"
)

func TestResolveSessionID(t *testing.T) {
	cases := []struct {
		name     string
		explicit string
		env      string
		want     string
		wantOK   bool
	}{
		{"explicit wins over env", "explicit-id", "env-id", "explicit-id", true},
		{"env used when flag omitted", "", "env-id", "env-id", true},
		{"env used when flag is whitespace", "   ", "env-id", "env-id", true},
		{"explicit trimmed", "  padded-id  ", "", "padded-id", true},
		{"env trimmed", "", "  env-id\n", "env-id", true},
		{"both empty is a refusal", "", "", "", false},
		{"whitespace env is a refusal", "", "   ", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(sessionEnv, tc.env)
			got, ok := resolveSessionID(tc.explicit)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Fatalf("session = %q, want %q", got, tc.want)
			}
		})
	}
}

// A missing session must fail loudly. Binding to "" would be worse than the
// error: the guard abstains on an empty session id, so activation would report
// success and then never speak again.
func TestContractActivateRefusesEmptySession(t *testing.T) {
	t.Setenv(sessionEnv, "")
	var code int
	_, stderr := captureOutput(func() {
		code = runContractActivate([]string{"some-contract"})
	})
	if code == ExitOK {
		t.Fatal("activate with no session id returned ExitOK; want a non-zero exit")
	}
	if !strings.Contains(stderr, sessionEnv) {
		t.Errorf("stderr does not name %s, so the user cannot tell how to fix it: %q", sessionEnv, stderr)
	}
}

// `activate` takes a contract name, so `deactivate <name>` is the natural typo.
// It is wrong — a binding is keyed by session, not name. While --session was
// mandatory the typo failed on the missing flag; with the flag optional it would
// otherwise deactivate silently and drop the argument on the floor.
func TestContractDeactivateRejectsStrayArgument(t *testing.T) {
	t.Setenv(sessionEnv, "some-session-id")
	var code int
	_, stderr := captureOutput(func() {
		code = runContractDeactivate([]string{"contracts-d1"})
	})
	if code == ExitOK {
		t.Fatal("deactivate with a stray contract name returned ExitOK; the argument was silently ignored")
	}
	if !strings.Contains(stderr, "contracts-d1") {
		t.Errorf("stderr does not echo the ignored argument, so the typo is not obvious: %q", stderr)
	}
}

// `check` must resolve the session the same way activate does, or a user who
// activated with no flag is told to paste the UUID activate just proved is
// knowable.
func TestContractCheckUsesEnvSession(t *testing.T) {
	const refusal = "Specify --contract"

	t.Setenv(sessionEnv, "")
	_, stderrEmpty := captureOutput(func() {
		resolveCheckContract(t.TempDir(), t.TempDir(), "", "")
	})
	if !strings.Contains(stderrEmpty, refusal) {
		t.Fatalf("with no contract and no session, want the %q refusal; got %q", refusal, stderrEmpty)
	}

	t.Setenv(sessionEnv, "some-session-id")
	_, stderrEnv := captureOutput(func() {
		resolveCheckContract(t.TempDir(), t.TempDir(), "", "")
	})
	if strings.Contains(stderrEnv, refusal) {
		t.Errorf("with $%s set, check still demanded an explicit --session: %q", sessionEnv, stderrEnv)
	}
}

func TestContractDeactivateRefusesEmptySession(t *testing.T) {
	t.Setenv(sessionEnv, "")
	var code int
	_, stderr := captureOutput(func() {
		code = runContractDeactivate(nil)
	})
	if code == ExitOK {
		t.Fatal("deactivate with no session id returned ExitOK; want a non-zero exit")
	}
	if !strings.Contains(stderr, sessionEnv) {
		t.Errorf("stderr does not name %s: %q", sessionEnv, stderr)
	}
}
