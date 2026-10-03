// Package hookwiring is the shared event→required-flag contract every shipped
// Claude Code hook config channel must satisfy, plus the check that validates
// a channel's JSON against it.
//
// It exists because the guard attaches to Claude Code through TWO hook
// events, and every channel that ships a config must wire BOTH. Nothing
// enforced that, and the omission shipped: `--outcome-mode` existed as a
// working, tested flag that NO shipped config ever invoked. Only the author's
// hand-edited personal settings.json called it, so on every external install:
//
//   - `runecho-ir fpreport` had no join key. Its approval rate is computed by
//     matching an ask to an outcome record, and outcome records are written
//     ONLY by --outcome-mode. Every ask was unrated; the report was empty.
//   - RUNECHO_GUARD_LEARN could never fire — it trains on approvals it never saw.
//   - The E6 auto-fresh reindex never ran, leaving the stale-IR false-positive
//     class live for every user.
//
// None of that surfaces as an error. The guard still asks, still defers,
// still exits 0; it just silently stops learning and stops measuring.
//
// Lifted out of cmd/runecho-guard/hookwiring_test.go on 2026-08-12 (#331) so
// that test and `runecho-ir doctor` — which answers the same question for an
// already-installed binary, not just a source checkout — read one table and
// one check instead of two hand-kept copies.
package hookwiring

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Contract maps each Claude Code hook event to the flag the guard's shipped
// config must invoke on it. Deliberately NOT derived from main.go's flag set:
// "which bool flags are hook entry points" is not recoverable from source
// without guessing. This table IS the contract.
var Contract = map[string]string{
	"PreToolUse":  "--hook-mode",
	"PostToolUse": "--outcome-mode",
}

// Matcher is the tool-name matcher every shipped config must use. A narrower
// matcher means edits that the other event DOES see go unpaired.
const Matcher = "Edit|Write|MultiEdit"

// WantHookTimeout is the outer, Claude-Code-enforced per-hook-invocation
// timeout (in seconds) every shipped config must set. It exists as a backstop
// for the one degraded state nothing else covers: the guard process itself
// hanging (#332) — a stalled disk read, a pathological parse, anything that
// blocks before the guard's own inner timeout gets a chance to fire.
const WantHookTimeout = 5

// ClaudeHookFile is the shape shared by settings.json and the plugin's
// hooks.json.
type ClaudeHookFile struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			// Timeout is a *int so a config that omits the field is
			// distinguishable from one that sets it to a JSON 0.
			Timeout *int `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// CheckChannel validates raw (the JSON content of one hook-config channel)
// against Contract: every event must have a hook invoking its required mode,
// in an entry using Matcher, with WantHookTimeout set. Only entries that invoke
// the guard are judged — a user's own hooks under the same event (a Bash
// matcher, another tool) are theirs, and flagging them taught people to ignore
// doctor (audit, 2026-10-03). Returns one violation string per problem, in a
// fixed order (nil if none), or an error if raw is not valid JSON.
func CheckChannel(raw []byte) (violations []string, err error) {
	var cfg ClaudeHookFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	for _, event := range slices.Sorted(maps.Keys(Contract)) {
		mode := Contract[event]
		entries := cfg.Hooks[event]
		if len(entries) == 0 {
			violations = append(violations, fmt.Sprintf(
				"no %s hook — the guard degrades silently without it (stops measuring, stops learning)", event))
			continue
		}
		var found bool
		for _, entry := range entries {
			matcherFlagged := false
			for _, h := range entry.Hooks {
				if !invokesMode(h.Command, mode) {
					continue
				}
				found = true
				if entry.Matcher != Matcher && !matcherFlagged {
					matcherFlagged = true
					violations = append(violations, fmt.Sprintf(
						"%s matcher is %q, want %q", event, entry.Matcher, Matcher))
				}
				if h.Timeout == nil {
					violations = append(violations, fmt.Sprintf(
						"%s hook has no \"timeout\" — a hang blocks the edit indefinitely instead of failing open", event))
				} else if *h.Timeout != WantHookTimeout {
					violations = append(violations, fmt.Sprintf(
						"%s hook timeout = %d, want %d", event, *h.Timeout, WantHookTimeout))
				}
			}
		}
		if !found {
			violations = append(violations, fmt.Sprintf("%s hook exists but never invokes %s", event, mode))
		}
	}
	return violations, nil
}

// invokesMode reports whether command contains mode as a whole flag: not
// preceded or followed by a character that could continue a flag name
// (letter, digit, '-', '_') and not the value of an assignment ('=' before
// it). Shell punctuation and quotes around it are fine —
// `--hook-mode||true`, `"…" --hook-mode;`, `--hook-mode=1` all count — while a
// different flag that merely contains the text (`--hook-mode-x`) does not.
func invokesMode(command, mode string) bool {
	flagChar := func(b byte) bool {
		return b == '-' || b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
	}
	for i := 0; ; {
		j := strings.Index(command[i:], mode)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(mode)
		// '=' before it, directly or before an opening quote, makes it an
		// assignment's value (VAR=--hook-mode, VAR="--hook-mode"), not a flag.
		assigned := start > 0 && command[start-1] == '=' ||
			start > 1 && (command[start-1] == '"' || command[start-1] == '\'') && command[start-2] == '='
		if (start == 0 || !flagChar(command[start-1])) && !assigned && (end == len(command) || !flagChar(command[end])) {
			return true
		}
		i = start + 1
	}
}
