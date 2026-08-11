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
	Message string
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
func lintFindingsWithReason(filePath, content string) ([]lintFinding, string) {
	ruffBin, err := exec.LookPath("ruff")
	if err != nil {
		return nil, "" // caller's Skipped gate covers "ruff not found"
	}
	ctx, cancel := context.WithTimeout(context.Background(), lintTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ruffBin, "check", "--no-cache", "--isolated",
		"--select", "F821,F811", "--stdin-filename", filePath, "--output-format", "json", "-")
	cmd.Stdin = strings.NewReader(content)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
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
		findings = append(findings, lintFinding{Line: r.Location.Row, Rule: r.Code, Message: r.Message})
	}
	return findings, ""
}
