package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
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
		"--select", "F821,F811", "--stdin-filename", filePath, "--output-format", "json", "-")
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
		findings = append(findings, lintFinding{Line: r.Location.Row, Rule: r.Code, Symbol: lintSymbolFromMessage(r.Message), Message: r.Message})
	}
	return findings, ""
}
