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
// against Contract: every event must have a hook using Matcher, invoking its
// required mode, with WantHookTimeout set. Returns one violation string per
// problem found (nil if none), or an error if raw is not valid JSON.
func CheckChannel(raw []byte) (violations []string, err error) {
	var cfg ClaudeHookFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	for event, mode := range Contract {
		entries, ok := cfg.Hooks[event]
		if !ok || len(entries) == 0 {
			violations = append(violations, fmt.Sprintf(
				"no %s hook — the guard degrades silently without it (stops measuring, stops learning)", event))
			continue
		}
		var found bool
		for _, entry := range entries {
			if entry.Matcher != Matcher {
				violations = append(violations, fmt.Sprintf(
					"%s matcher is %q, want %q", event, entry.Matcher, Matcher))
			}
			for _, h := range entry.Hooks {
				if !strings.Contains(h.Command, mode) {
					continue
				}
				found = true
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
