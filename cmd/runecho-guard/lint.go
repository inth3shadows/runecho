package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/guard"
)

// lintEnabled gates the pre-write ruff lint substrate (RUNECHO_GUARD_LINT=1,
// default OFF; #333). Ships default-off because ruff's own measured 19ms
// p50/23ms p99 (issue's table, verified independently in this repo) is
// ~1.6x the guard's entire ~12ms hook budget for one check — there is no
// path to default-on that does not first go through a measured
// async/best-effort design.
func lintEnabled() bool { return os.Getenv("RUNECHO_GUARD_LINT") == "1" }

// lintTimeout bounds the ruff subprocess. Distinct from #332's guardTimeout
// (the whole-hook backstop) — this is a per-check deadline, generous
// relative to the measured ~23ms p99 (~100x headroom) while still being a
// real backstop against a pathological hang. A var, not a const, for the
// same reason guardTimeout is one: tests shrink it instead of sleeping the
// real value.
var lintTimeout = 2 * time.Second

// lintSelect is ruff's --select argument, and lintSelectedRules is the same
// set as a membership test for filtering its output. ONE source of truth
// deliberately: --select does not actually bound what ruff reports (see the
// filter in lintFindingsWithReason), so the two must agree or the ask header
// and the reported findings drift apart.
const lintSelect = "F821,F811"

// lintSelectDisplay is lintSelect rendered for the ask header, where a slash
// reads as "or" — these are alternatives a finding can be, not a list the
// reader has to supply. Derived rather than written out so the two can never
// name different rules, and kept separate from lintSelect because that constant
// is ruff's literal --select argument (comma-separated is ruff's syntax) AND
// the source of lintSelectedRules. Editing the constant to fix the display
// would break the actual invocation.
var lintSelectDisplay = strings.ReplaceAll(lintSelect, ",", "/")

var lintSelectedRules = func() map[string]struct{} {
	m := make(map[string]struct{})
	for _, r := range strings.Split(lintSelect, ",") {
		m[r] = struct{}{}
	}
	return m
}()

// lintFinding is one ruff F821/F811 result, with a TRUE file line number
// (via --stdin-filename) rather than the "snippet line N" hunk-relative
// number every other hook-mode check carries — this check only ever sees
// Write payloads, whose content IS the whole file.
type lintFinding struct {
	Line    int
	Rule    string // ruff's "code", e.g. "F821"
	Symbol  string // the flagged identifier, e.g. "helper" — see lintSymbolFromMessage
	Message string
}

// lintSymbolFromMessage pulls the flagged identifier out of ruff's own
// message text — both F821 ("Undefined name `helper`") and F811
// ("Redefinition of unused `fetch` from line 1: ...") name it first inside
// backticks. Falls back to "" (caller substitutes the rule code) rather than
// guessing, since a future ruff version reformatting the message is a
// silent-degrade case, not a crash.
func lintSymbolFromMessage(msg string) string {
	start := strings.IndexByte(msg, '`')
	if start < 0 {
		return ""
	}
	end := strings.IndexByte(msg[start+1:], '`')
	if end < 0 {
		return ""
	}
	return msg[start+1 : start+1+end]
}

// lintSection renders the lint findings into the ask and returns the symbols
// to record, mirroring callShapeSection. Shared by the enrolled path
// (runHookMode) and the degraded/unenrolled one (askWithoutIndex) so the two
// cannot drift — the header names the selected rules, which is only true
// because lintFindingsWithReason filters ruff's output to them.
//
// Falls back to the rule code when Symbol did not parse out of the message:
// decisionRecord.Symbols must not silently lose an entry, and a rule code is a
// worse but honest label. Kept as a fallback rather than a panic because a
// future ruff reformatting its message text is a degrade, not a crash.
func lintSection(sb *strings.Builder, findings []lintFinding) []string {
	if len(findings) == 0 {
		return nil
	}
	fmt.Fprintf(sb, "[runecho-guard] %d ruff finding(s) (%s):\n", len(findings), lintSelectDisplay)
	syms := make([]string, 0, len(findings))
	for _, f := range findings {
		fmt.Fprintf(sb, "  line %d: %s %s\n", f.Line, f.Rule, f.Message)
		if f.Symbol != "" {
			syms = append(syms, f.Symbol)
		} else {
			syms = append(syms, f.Rule)
		}
	}
	return syms
}

// suppressAlreadyReported drops lint findings whose symbol the additive
// hallucination check (guard.Run) already flagged for this same edit. The two
// checks genuinely overlap — F821 "undefined name" and "not found in the
// indexed code" are the same question asked two ways — so without this a real
// hallucination is printed under two headers and counted twice in
// decisionRecord.Symbols.
//
// Compares on Symbol, not Message: the additive check's Violation carries a
// bare name, which is what lintSymbolFromMessage extracts too. A finding whose
// Symbol failed to parse out ("") is never suppressed — an unparsed message
// cannot be proven to be a duplicate, and dropping it would silently lose a
// real finding.
func suppressAlreadyReported(findings []lintFinding, violations []guard.Violation) []lintFinding {
	if len(findings) == 0 || len(violations) == 0 {
		return findings
	}
	reported := make(map[string]struct{}, len(violations))
	for _, v := range violations {
		reported[v.Symbol] = struct{}{}
	}
	out := findings[:0]
	for _, f := range findings {
		if f.Symbol != "" {
			if _, dup := reported[f.Symbol]; dup {
				continue
			}
		}
		out = append(out, f)
	}
	return out
}

// lintFindingsWithReason runs ruff against content (a Write payload's full
// proposed file) and returns findings plus an abstain reason (empty means it
// ran to completion). Mirrors the *WithReason naming #330 established
// (goDepQualifiedViolationsWithReason, fileScopeViolationsWithReason) for the
// same reason: distinguishable coverage, not a bare nil.
//
// --isolated refuses pyproject.toml/ruff.toml discovery so a config file in
// an untrusted tree can't reach the subprocess and silence a real finding —
// measured to cost nothing (issue's own p50/p99 table shows config
// discovery isn't the cost; process start is), so it is not an opt-in.
// No separate exec.LookPath("ruff") here: the caller (runHookMode) already
// resolves it before ever calling this function, so its own Skipped gate
// covers "ruff not found" — a second, independent LookPath here would race
// that one (ruff vanishing from PATH between the two calls) and misreport as
// a clean VerdictOK instead of an abstain. exec.CommandContext resolves the
// bare name itself at Run() time; a not-found error surfaces through the
// same non-ExitError branch below as any other exec failure.
func lintFindingsWithReason(filePath, content string) ([]lintFinding, string) {
	ctx, cancel := context.WithTimeout(context.Background(), lintTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ruff", "check", "--no-cache", "--isolated",
		"--select", lintSelect, "--stdin-filename", filePath, "--output-format", "json", "-")
	cmd.Stdin = strings.NewReader(content)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, "timeout"
	}
	var exitErr *exec.ExitError
	if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode() == 1) {
		// Exit 1 is ruff's normal "found violations" exit, not an error. Any
		// other nonzero exit (or a non-ExitError failure, e.g. the binary
		// vanished mid-run) is genuinely unanswerable.
		return nil, "lint-exec-failed"
	}
	var raw []struct {
		Code     string `json:"code"`
		Message  string `json:"message"`
		Location struct {
			Row int `json:"row"`
		} `json:"location"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, "lint-unparseable-output"
	}
	findings := make([]lintFinding, 0, len(raw))
	for _, r := range raw {
		// --select does NOT bound what ruff reports: since 0.12 it emits
		// `invalid-syntax` diagnostics unconditionally, ignoring the rule
		// selection entirely (verified against 0.16.1). Those are NOT the two
		// questions this check asks, and letting them through breaks two
		// contracts at once: the ask header states "(F821/F811)", and
		// lintSymbolFromMessage would pull punctuation out of a parser message
		// ("Expected `)`, found newline" → ")") straight into
		// decisionRecord.Symbols, which guardstats buckets per-symbol and
		// fpaudit dedups on. A file the interpreter cannot even parse is also
		// not a resolution question — it fails loudly at runtime with or
		// without the guard — so it is dropped rather than relabelled.
		if _, ok := lintSelectedRules[r.Code]; !ok {
			continue
		}
		findings = append(findings, lintFinding{Line: r.Location.Row, Rule: r.Code, Symbol: lintSymbolFromMessage(r.Message), Message: r.Message})
	}
	return findings, ""
}
