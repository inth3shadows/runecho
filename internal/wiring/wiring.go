// Package wiring holds the Claude Code hook contract — which events the guard
// must be attached to, with which flag, matcher and timeout — plus the parser
// for the JSON shape every shipping channel uses.
//
// It exists as a package rather than a table inside a test because two
// different consumers need the SAME answer: cmd/runecho-guard's
// hookwiring_test.go (which fails CI when a shipped config drifts) and
// `runecho-ir doctor` (which tells a user whose install is silently inert).
// #331 called this out explicitly — a copy of the table in the command would
// make it the fourth hand-kept copy of a fact that has already shipped wrong
// once, which is the failure mode internal/parser/grammar_subset_test.go and
// KB principle "a comment saying two files mirror each other is not
// synchronization" both exist to prevent.
//
// The wiring gap this encodes is not hypothetical. `--outcome-mode` was a
// working, tested flag that NO shipped config ever invoked, so on every
// external install fpreport had no join key, RUNECHO_GUARD_LEARN could never
// reach its threshold, and the E6 auto-fresh reindex never ran. Nothing
// errored: the guard still asked, still deferred, still exited 0.
package wiring

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Contract maps each Claude Code hook event to the runecho-guard flag that
// event must invoke. This table IS the contract — it is deliberately not
// derived from the guard's flag set, because "which bool flags are hook entry
// points" is not recoverable from source without guessing.
var Contract = map[string]string{
	"PreToolUse":  "--hook-mode",
	"PostToolUse": "--outcome-mode",
}

// Events returns the contract's events in a stable order. Map iteration order
// would make both the test's failure output and doctor's report shuffle between
// runs, which turns a diffable report into a noisy one.
func Events() []string {
	out := make([]string, 0, len(Contract))
	for e := range Contract {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// Matcher is the tool-name matcher every wired event must use. A narrower
// matcher on one event means edits the other event DOES see go unpaired, which
// silently breaks the ask→outcome join fpreport is computed from.
const Matcher = "Edit|Write|MultiEdit"

// Timeout is the outer, Claude-Code-enforced per-invocation timeout (seconds)
// every shipped config must set. It backstops the one degraded state nothing
// else covers: the guard process itself hanging (#332) before its own inner
// guardTimeout can fire. A hook with no timeout blocks the agent's edit
// indefinitely instead of failing open.
const Timeout = 5

// HookFile is the shape of every channel that wires the guard: the plugin's
// hooks.json, a user's or this repo's settings.json, and the block
// `install.sh --print-hook-config` prints for hand-merging.
type HookFile struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			// Timeout is a *int so a config that OMITS the field is
			// distinguishable from one that sets it to a JSON 0 — both decode to
			// the zero value otherwise, and the presence check would go blind to
			// the field simply vanishing from a future edit.
			Timeout *int `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// ParseFile reads and decodes a hook config. A file that is present but invalid
// JSON is reported as an error rather than an empty config on purpose: Claude
// Code ignores a settings.json it cannot parse, so malformed JSON does not
// half-wire the hooks, it silently disables ALL of them — the more dangerous
// state and the one an empty-config fallback would hide.
func ParseFile(path string) (HookFile, error) {
	var hf HookFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return hf, err
	}
	if err := json.Unmarshal(raw, &hf); err != nil {
		return hf, fmt.Errorf("invalid JSON (Claude Code would ignore the whole file, disabling every hook in it): %w", err)
	}
	return hf, nil
}

// Problem is one way a config fails the contract, phrased for a human.
type Problem struct {
	Event  string
	Detail string
}

// Check returns every way hf departs from the contract, in stable event order.
// An empty slice means this channel wires the guard correctly.
//
// Returns problems rather than a bool so both consumers can be specific: the
// test needs to say which assertion failed, and doctor needs to print a remedy
// per line. A bool would collapse "no PostToolUse hook at all" and "timeout is
// 3 instead of 5" into the same output.
func Check(hf HookFile) []Problem {
	var problems []Problem
	for _, event := range Events() {
		mode := Contract[event]
		found, judged := false, false
		for _, entry := range hf.Hooks[event] {
			// Judge ONLY entries that actually invoke the guard. A real
			// ~/.claude/settings.json wires many unrelated tools on the same
			// event — Bash, Read, ExitPlanMode matchers, none of them ours — and
			// an earlier cut of this function reported every one of them as a
			// matcher violation. That was invisible while this logic lived in a
			// test whose fixtures contain nothing but runecho hooks; it surfaced
			// the first time `doctor` was pointed at a real user config.
			if !entryInvokesGuard(entry.Hooks, mode) {
				continue
			}
			judged = true
			if entry.Matcher != Matcher {
				problems = append(problems, Problem{event, fmt.Sprintf(
					"matcher is %q, want %q — a narrower matcher means edits the other event DOES see go unpaired",
					entry.Matcher, Matcher)})
			}
			for _, h := range entry.Hooks {
				if !strings.Contains(h.Command, mode) {
					continue
				}
				found = true
				switch {
				case h.Timeout == nil:
					problems = append(problems, Problem{event,
						"hook has no \"timeout\" — a hang in the guard process blocks the edit indefinitely instead of failing open"})
				case *h.Timeout != Timeout:
					problems = append(problems, Problem{event, fmt.Sprintf(
						"hook timeout = %d, want %d", *h.Timeout, Timeout)})
				}
			}
		}
		switch {
		case !judged:
			problems = append(problems, Problem{event, fmt.Sprintf(
				"no %s hook invoking the guard — it degrades SILENTLY without one: it does not error, it stops measuring and stops learning", event)})
		case !found:
			problems = append(problems, Problem{event, fmt.Sprintf(
				"%s hook exists but never invokes %s", event, mode)})
		}
	}
	return problems
}

// entryInvokesGuard reports whether any command in this entry runs the guard.
// Matches on the binary name OR the contract flag: the plugin channel invokes a
// shim script whose name carries "runecho", while a hand-merged settings.json
// may name an absolute path — both, though, carry the mode flag.
func entryInvokesGuard(hooks []struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout *int   `json:"timeout"`
}, mode string) bool {
	for _, h := range hooks {
		if strings.Contains(h.Command, "runecho") || strings.Contains(h.Command, mode) {
			return true
		}
	}
	return false
}
