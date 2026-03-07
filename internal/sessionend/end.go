// Package sessionend implements the session-end orchestration pipeline.
// It replicates all stages of hooks/session-end.sh in Go.
package sessionend

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/schema"
	"github.com/inth3shadows/runecho/internal/session"
	"github.com/inth3shadows/runecho/internal/task"
)

// event is the JSON envelope sent by Claude Code on SessionEnd.
type event struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Reason    string `json:"reason"`
}

// checkpoint mirrors the fields we read from .ai/checkpoint.json.
type checkpoint struct {
	SessionID string `json:"session_id"`
	Turn      int    `json:"turn"`
	Ts        string `json:"ts"`
	IRHash    string `json:"ir_hash"`
	LastMsg   string `json:"last_assistant_message"`
}

// Run is the main entry point. It reads the session-end event JSON and
// executes all 7 stages. All stages are non-fatal: any error is logged to
// stderr and execution continues. Run always returns nil so the binary
// can exit 0.
func Run(input []byte) error {
	ev := parseEvent(input)
	cwd := ev.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	sid := ev.SessionID

	handoffPath := filepath.Join(cwd, ".ai", "handoff.md")
	checkpointPath := filepath.Join(cwd, ".ai", "checkpoint.json")

	// --- Stage 1: Scope drift detection ---
	activeTask, scopeDrift := detectScopeDrift(cwd, sid)

	// --- Stage 2: IR snapshot ---
	runIRSnapshot(cwd, sid)

	// --- Stage 3: Pipeline envelope ---
	runPipelineEnvelope(cwd, sid)

	// --- Stage 4: Verify ---
	verifyPassed := runVerify(cwd, sid, activeTask)

	// --- Stage 5: IR verify summary ---
	verifySummary := runIRVerify(cwd, sid)

	// --- Stage 6: Handoff generation (primary path) ---
	// If handoff already exists, skip generation and just record progress.
	if fileExists(handoffPath) {
		_ = appendProgress(cwd, sid, handoffPath, checkpointPath, scopeDrift, 0, verifyPassed)
		return nil
	}

	// Try ai-session first.
	if sid != "" {
		cost, ok := tryAISession(cwd, sid, handoffPath)
		if ok {
			// Background: ai-document update (non-fatal, fire-and-forget)
			go runAIDocument(cwd, verifySummary)

			_ = appendProgress(cwd, sid, handoffPath, checkpointPath, scopeDrift, cost, verifyPassed)

			// Validate handoff (warn only)
			runValidate(handoffPath)
			return nil
		}
	}

	// --- Stage 7: Checkpoint fallback ---
	writeFallbackHandoff(cwd, handoffPath, checkpointPath, verifySummary, ev.Reason)
	_ = appendProgress(cwd, sid, handoffPath, checkpointPath, scopeDrift, 0, verifyPassed)
	return nil
}

// --- helpers ---

func parseEvent(input []byte) event {
	var ev event
	_ = json.Unmarshal(input, &ev)
	return ev
}

// detectScopeDrift implements Stage 1.
// Returns the active task (may be nil) and a ScopeDrift value.
func detectScopeDrift(cwd, sid string) (*task.Task, schema.ScopeDrift) {
	defaultDrift := schema.ScopeDrift{Drifted: false, Files: []string{}, TaskScope: ""}

	db, err := task.Load(cwd)
	if err != nil {
		return nil, defaultDrift
	}

	// First non-done, non-blocked task that has a scope.
	var activeTask *task.Task
	for i := range db.Tasks {
		t := &db.Tasks[i]
		if t.Status == "done" || len(t.BlockedBy) > 0 {
			continue
		}
		if t.Scope != "" {
			activeTask = t
			break
		}
	}

	if activeTask == nil {
		return nil, defaultDrift
	}

	// Get changed files from git.
	changed := gitChangedFiles(cwd)
	if len(changed) == 0 {
		return activeTask, defaultDrift
	}

	// Check each changed file against the comma-separated scope globs.
	globs := splitGlobs(activeTask.Scope)
	var driftFiles []string
	for _, file := range changed {
		if !matchesAnyGlob(file, globs) {
			driftFiles = append(driftFiles, file)
		}
	}

	if len(driftFiles) == 0 {
		return activeTask, defaultDrift
	}

	// Emit SCOPE_DRIFT fault.
	summary := strings.Join(driftFiles, ",")
	_ = session.AppendFault(cwd, schema.FaultEntry{
		Signal:    "SCOPE_DRIFT",
		SessionID: sid,
		Value:     float64(len(driftFiles)),
		Context:   summary,
		Ts:        now(),
	})

	return activeTask, schema.ScopeDrift{
		Drifted:   true,
		Files:     driftFiles,
		TaskScope: activeTask.Scope,
		TaskID:    activeTask.ID,
	}
}

// gitChangedFiles runs git diff --name-only HEAD. Falls back to HEAD~1.
func gitChangedFiles(cwd string) []string {
	out, err := exec.Command("git", "-C", cwd, "diff", "--name-only", "HEAD").Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		out, err = exec.Command("git", "-C", cwd, "diff", "--name-only", "HEAD~1").Output()
		if err != nil {
			return nil
		}
	}
	var files []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		if f := strings.TrimSpace(scanner.Text()); f != "" {
			files = append(files, f)
		}
	}
	return files
}

// splitGlobs splits a comma-separated scope string into trimmed globs.
func splitGlobs(scope string) []string {
	parts := strings.Split(scope, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if g := strings.TrimSpace(p); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// matchesAnyGlob returns true if path matches any of the given globs using filepath.Match.
func matchesAnyGlob(path string, globs []string) bool {
	for _, g := range globs {
		if ok, _ := filepath.Match(g, path); ok {
			return true
		}
		// Also try matching just the base name for simple patterns.
		if ok, _ := filepath.Match(g, filepath.Base(path)); ok {
			return true
		}
	}
	return false
}

// runIRSnapshot implements Stage 2: re-index + snapshot + churn cache.
func runIRSnapshot(cwd, sid string) {
	if !commandExists("ai-ir") {
		return
	}
	if !fileExists(filepath.Join(cwd, ".ai", "ir.json")) {
		return
	}

	// Re-index to capture final file state.
	_ = exec.Command("ai-ir", cwd).Run()

	// Snapshot.
	snapshotArgs := []string{"snapshot", "--label=session-end"}
	if sid != "" {
		snapshotArgs = append(snapshotArgs, "--session="+sid)
	}
	snapshotArgs = append(snapshotArgs, cwd)
	_ = exec.Command("ai-ir", snapshotArgs...).Run()

	// Churn cache.
	churnOut, err := exec.Command("ai-ir", "churn", "--compact", "--n=20", cwd).Output()
	if err == nil {
		churnPath := filepath.Join(cwd, ".ai", "churn-cache.txt")
		_ = os.WriteFile(churnPath, churnOut, 0644)
	}
}

// runPipelineEnvelope implements Stage 3.
func runPipelineEnvelope(cwd, sid string) {
	if !commandExists("ai-pipeline") || sid == "" {
		return
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	routeFile := filepath.Join(homeDir, ".claude", "hooks", ".governor-state", sid+".route")
	routeBytes, err := os.ReadFile(routeFile)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(routeBytes)) != "pipeline" {
		return
	}
	_ = exec.Command("ai-pipeline", "envelope",
		"--session="+sid,
		"--pipeline=default",
		"--status=complete",
		cwd,
	).Run()
}

// runVerify implements Stage 4. Returns nil (not run), ptr true (passed), ptr false (failed).
func runVerify(cwd, sid string, activeTask *task.Task) *bool {
	if !commandExists("ai-task") || activeTask == nil {
		return nil
	}
	args := []string{"verify", activeTask.ID}
	if sid != "" {
		args = append(args, "--session="+sid)
	}
	args = append(args, cwd)

	cmd := exec.Command("ai-task", args...)
	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	switch exitCode {
	case 0:
		v := true
		return &v
	case 1:
		v := false
		_ = session.AppendFault(cwd, schema.FaultEntry{
			Signal:    "VERIFY_FAIL",
			SessionID: sid,
			Value:     1,
			Context:   fmt.Sprintf("task %s verify failed", activeTask.ID),
			Ts:        now(),
		})
		return &v
	default:
		// exit 2 = no verify cmd; leave unset
		return nil
	}
}

// runIRVerify implements Stage 5. Returns stdout of `ai-ir verify`.
func runIRVerify(cwd, sid string) string {
	if !commandExists("ai-ir") {
		return ""
	}
	if !fileExists(filepath.Join(cwd, ".ai", "history.db")) {
		return ""
	}
	args := []string{"verify"}
	if sid != "" {
		args = append(args, "--session="+sid)
	}
	args = append(args, cwd)
	out, err := exec.Command("ai-ir", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// tryAISession implements Stage 6 primary path. Returns (cost, ok).
func tryAISession(cwd, sid, handoffPath string) (float64, bool) {
	if !commandExists("ai-session") {
		return 0, false
	}
	out, err := exec.Command("ai-session",
		"--session="+sid,
		"--out="+handoffPath,
		cwd,
	).Output()
	if err != nil {
		return 0, false
	}
	cost := extractCost(string(out))
	return cost, true
}

// extractCost parses "~$1.23" from ai-session stdout.
var costRe = regexp.MustCompile(`~\$([0-9]+\.[0-9]+)`)

func extractCost(s string) float64 {
	m := costRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	var f float64
	fmt.Sscanf(m[1], "%f", &f)
	return f
}

// runAIDocument runs ai-document in the background (fire-and-forget).
func runAIDocument(cwd, verifySummary string) {
	if !commandExists("ai-document") {
		return
	}
	args := []string{}
	if verifySummary != "" {
		args = append(args, "--ir-diff="+verifySummary)
	}
	args = append(args, cwd)
	_ = exec.Command("ai-document", args...).Run()
}

// runValidate runs `ai-session validate <path>` and logs any warnings to stderr.
func runValidate(handoffPath string) {
	if !commandExists("ai-session") {
		return
	}
	out, err := exec.Command("ai-session", "validate", handoffPath).CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			fmt.Fprintf(os.Stderr, "session-end: WARNING: handoff validation fatal error — check %s\n%s\n",
				handoffPath, strings.TrimSpace(string(out)))
		}
	}
}

// writeFallbackHandoff implements Stage 7: minimal handoff from checkpoint.json.
func writeFallbackHandoff(cwd, handoffPath, checkpointPath, verifySummary, reason string) {
	data, err := os.ReadFile(checkpointPath)
	if err != nil {
		return
	}
	var cp checkpoint
	_ = json.Unmarshal(data, &cp)

	turn := cp.Turn
	ts := cp.Ts
	if ts == "" {
		ts = now()
	}
	irHash := cp.IRHash
	lastMsg := cp.LastMsg
	if reason == "" {
		reason = "unknown"
	}

	content := fmt.Sprintf(`# Session Handoff (fallback — ai-session unavailable)
**Date:** %s
**IR snapshot:** %s
**Session length:** ~%d turns
**Termination reason:** %s

## Accomplished
- (install ai-session for ground-truth handoffs)
- Last message: %s

## Next Steps
1. Review git log for changes made this session
2. Re-orient with IR context on next session start

## Structural Changes
%s
`, ts, irHash, turn, reason, lastMsg, fallbackVerify(verifySummary))

	_ = os.MkdirAll(filepath.Dir(handoffPath), 0755)
	_ = os.WriteFile(handoffPath, []byte(content), 0644)
}

func fallbackVerify(s string) string {
	if s == "" {
		return "(no session-start snapshot)"
	}
	return s
}

// appendProgress builds and appends a ProgressEntry from available data.
func appendProgress(cwd, sid, handoffPath, checkpointPath string, drift schema.ScopeDrift, cost float64, verifyPassed *bool) error {
	e := schema.ProgressEntry{
		SessionID:   sid,
		Timestamp:   now(),
		CostUSD:     cost,
		HandoffPath: handoffPath,
		ScopeDrift:  drift,
		VerifyPassed: verifyPassed,
		TasksTouched: []string{},
		Status:      "unknown",
	}

	// Read front-matter from handoff.
	if data, err := os.ReadFile(handoffPath); err == nil {
		parseFrontMatter(string(data), &e)
	}

	// Fill from checkpoint.
	if data, err := os.ReadFile(checkpointPath); err == nil {
		var cp checkpoint
		if json.Unmarshal(data, &cp) == nil {
			e.IRHashStart = cp.IRHash
			if e.Turns == 0 {
				e.Turns = cp.Turn
			}
		}
	}

	// Current IR hash end.
	e.IRHashEnd = readIRHashEnd(cwd)

	return session.AppendProgress(cwd, e)
}

// parseFrontMatter reads status, tasks_touched, files_changed, turns from handoff YAML front-matter.
func parseFrontMatter(content string, e *schema.ProgressEntry) {
	// Find the front-matter block between the first two "---" lines.
	lines := strings.Split(content, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return
	}
	var fmLines []string
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			break
		}
		fmLines = append(fmLines, lines[i])
	}
	fm := strings.Join(fmLines, "\n")

	if v := fmValue(fm, "status"); v != "" {
		e.Status = strings.Trim(v, `"'`)
	}
	if v := fmValue(fm, "tasks_touched"); v != "" && v != "[]" {
		e.TasksTouched = parseStringArray(v)
	}
	if v := fmValue(fm, "files_changed"); v != "" && v != "[]" {
		arr := parseStringArray(v)
		e.FilesChanged = len(arr)
	}
}

// fmValue extracts the value of a YAML key from a front-matter string.
func fmValue(fm, key string) string {
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		}
	}
	return ""
}

// parseStringArray parses a simple JSON or YAML inline string array.
// Handles: ["a","b"], [a, b], a b, etc.
func parseStringArray(s string) []string {
	s = strings.TrimSpace(s)
	// Try JSON array first.
	if strings.HasPrefix(s, "[") {
		var arr []string
		if json.Unmarshal([]byte(s), &arr) == nil {
			return arr
		}
		// Manual extraction of quoted strings.
		s = strings.Trim(s, "[]")
	}
	var result []string
	for _, part := range strings.Split(s, ",") {
		v := strings.Trim(strings.TrimSpace(part), `"'`)
		if v != "" {
			result = append(result, v)
		}
	}
	return result
}

// readIRHashEnd reads root_hash from .ai/ir.json, returning first 8 chars.
func readIRHashEnd(cwd string) string {
	data, err := os.ReadFile(filepath.Join(cwd, ".ai", "ir.json"))
	if err != nil {
		return ""
	}
	var ir struct {
		RootHash string `json:"root_hash"`
	}
	if json.Unmarshal(data, &ir) != nil {
		return ""
	}
	if len(ir.RootHash) > 8 {
		return ir.RootHash[:8]
	}
	return ir.RootHash
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
