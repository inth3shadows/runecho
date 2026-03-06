// Package provenance assembles a task-scoped provenance trace from .ai/ data files.
// Pure consumer — no writes, no side effects.
package provenance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inth3shadows/runecho/internal/schema"
	"github.com/inth3shadows/runecho/internal/task"
)

// SessionRecord is one session that touched the target task.
type SessionRecord struct {
	SessionID   string              `json:"session_id"`
	Date        string              `json:"date"`
	Turns       int                 `json:"turns"`
	CostUSD     float64             `json:"cost_usd"`
	IRHashStart string              `json:"ir_hash_start,omitempty"`
	IRHashEnd   string              `json:"ir_hash_end,omitempty"`
	Drifted     bool                `json:"drifted"`
	Faults      []schema.FaultEntry `json:"faults,omitempty"`
	VerifyOK    *bool               `json:"verify_ok,omitempty"`
	VerifyCmd   string              `json:"verify_cmd,omitempty"`
}

// TaskProvenance is the full provenance trace for one task.
type TaskProvenance struct {
	TaskID       string            `json:"task_id"`
	Title        string            `json:"title"`
	Status       string            `json:"status"`
	Scope        string            `json:"scope,omitempty"`
	Verify       string            `json:"verify,omitempty"`
	Sessions     []SessionRecord   `json:"sessions"`
	TotalCost    float64           `json:"total_cost_usd"`
	TotalTurns   int               `json:"total_turns"`
	FaultSummary map[string]int    `json:"fault_summary"`
}

// TaskSummary is used by the list subcommand.
type TaskSummary struct {
	Task         task.Task
	SessionCount int
	TotalCost    float64
}

// Export builds a TaskProvenance for the given task ID.
// workspace is the project root (parent of .ai/).
func Export(workspace, taskID string) (*TaskProvenance, error) {
	// Load task metadata
	db, err := task.Load(workspace)
	if err != nil {
		return nil, fmt.Errorf("load tasks: %w", err)
	}
	var t *task.Task
	for i := range db.Tasks {
		if db.Tasks[i].ID == taskID {
			t = &db.Tasks[i]
			break
		}
	}
	if t == nil {
		return nil, fmt.Errorf("task %q not found in tasks.json", taskID)
	}

	// Read progress entries (sessions that touched this task)
	progress, err := readProgress(workspace)
	if err != nil {
		return nil, fmt.Errorf("read progress: %w", err)
	}

	// Filter sessions where this task appears in tasks_touched
	var relevant []schema.ProgressEntry
	for _, p := range progress {
		for _, tid := range p.TasksTouched {
			if tid == taskID {
				relevant = append(relevant, p)
				break
			}
		}
	}

	// Read faults, index by session_id
	faults, err := readFaults(workspace)
	if err != nil {
		return nil, fmt.Errorf("read faults: %w", err)
	}
	faultsBySession := make(map[string][]schema.FaultEntry)
	for _, f := range faults {
		faultsBySession[f.SessionID] = append(faultsBySession[f.SessionID], f)
	}

	// Read verify results, index by (task_id, session_id)
	verifyEntries, err := readVerify(workspace)
	if err != nil {
		return nil, fmt.Errorf("read results: %w", err)
	}
	type verifyKey struct{ taskID, sessionID string }
	verifyMap := make(map[verifyKey]schema.VerifyEntry)
	for _, ve := range verifyEntries {
		verifyMap[verifyKey{ve.TaskID, ve.SessionID}] = ve
	}

	// Build session records
	prov := &TaskProvenance{
		TaskID:       t.ID,
		Title:        t.Title,
		Status:       t.Status,
		Scope:        t.Scope,
		Verify:       t.Verify,
		Sessions:     []SessionRecord{},
		FaultSummary: make(map[string]int),
	}

	for _, p := range relevant {
		date := p.Timestamp
		if len(date) > 10 {
			date = date[:10]
		}

		sr := SessionRecord{
			SessionID:   p.SessionID,
			Date:        date,
			Turns:       p.Turns,
			CostUSD:     p.CostUSD,
			IRHashStart: p.IRHashStart,
			IRHashEnd:   p.IRHashEnd,
			Drifted:     p.ScopeDrift.Drifted,
			Faults:      faultsBySession[p.SessionID],
		}

		if ve, ok := verifyMap[verifyKey{taskID, p.SessionID}]; ok {
			passed := ve.Passed
			sr.VerifyOK = &passed
			sr.VerifyCmd = ve.Cmd
		}

		prov.Sessions = append(prov.Sessions, sr)
		prov.TotalCost += p.CostUSD
		prov.TotalTurns += p.Turns

		for _, f := range sr.Faults {
			prov.FaultSummary[f.Signal]++
		}
	}

	return prov, nil
}

// List returns a summary for every task that has at least one progress entry.
func List(workspace string) ([]TaskSummary, error) {
	db, err := task.Load(workspace)
	if err != nil {
		return nil, fmt.Errorf("load tasks: %w", err)
	}

	progress, err := readProgress(workspace)
	if err != nil {
		return nil, fmt.Errorf("read progress: %w", err)
	}

	// Count sessions and cost per task
	sessionCount := make(map[string]int)
	totalCost := make(map[string]float64)
	for _, p := range progress {
		seen := make(map[string]bool)
		for _, tid := range p.TasksTouched {
			if !seen[tid] {
				sessionCount[tid]++
				totalCost[tid] += p.CostUSD
				seen[tid] = true
			}
		}
	}

	var out []TaskSummary
	for _, t := range db.Tasks {
		if sessionCount[t.ID] == 0 {
			continue
		}
		out = append(out, TaskSummary{
			Task:         t,
			SessionCount: sessionCount[t.ID],
			TotalCost:    totalCost[t.ID],
		})
	}
	return out, nil
}

// FormatText renders a TaskProvenance as a human-readable text report.
func FormatText(p *TaskProvenance) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "PROVENANCE: Task #%s — %s\n", p.TaskID, p.Title)
	fmt.Fprintf(&sb, "  Status: %s", p.Status)
	if p.Scope != "" {
		fmt.Fprintf(&sb, " | Scope: %s", p.Scope)
	}
	fmt.Fprintln(&sb)
	if p.Verify != "" {
		fmt.Fprintf(&sb, "  Verify: %s\n", p.Verify)
	}
	fmt.Fprintf(&sb, "  Sessions: %d | Turns: %d | Cost: $%.4f\n",
		len(p.Sessions), p.TotalTurns, p.TotalCost)

	if len(p.FaultSummary) > 0 {
		var parts []string
		for sig, count := range p.FaultSummary {
			parts = append(parts, fmt.Sprintf("%s×%d", sig, count))
		}
		fmt.Fprintf(&sb, "  Faults: %s\n", strings.Join(parts, " "))
	}

	if len(p.Sessions) > 0 {
		fmt.Fprintln(&sb, "")
		fmt.Fprintln(&sb, "  Session timeline:")
		for _, s := range p.Sessions {
			verify := ""
			if s.VerifyOK != nil {
				if *s.VerifyOK {
					verify = " [verify:PASS]"
				} else {
					verify = " [verify:FAIL]"
				}
			}
			drift := ""
			if s.Drifted {
				drift = " [DRIFT]"
			}
			faultStr := ""
			if len(s.Faults) > 0 {
				sigs := make(map[string]int)
				for _, f := range s.Faults {
					sigs[f.Signal]++
				}
				var parts []string
				for sig, n := range sigs {
					parts = append(parts, fmt.Sprintf("%s×%d", sig, n))
				}
				faultStr = " faults=[" + strings.Join(parts, ",") + "]"
			}
			fmt.Fprintf(&sb, "    %s  %s  %d turns  $%.4f%s%s%s\n",
				s.Date, shortID(s.SessionID), s.Turns, s.CostUSD,
				verify, drift, faultStr)
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func readProgress(workspace string) ([]schema.ProgressEntry, error) {
	path := filepath.Join(workspace, ".ai", "progress.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	seen := make(map[string]bool)
	var entries []schema.ProgressEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e schema.ProgressEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			fmt.Fprintf(os.Stderr, "provenance: skipping malformed progress line: %v\n", err)
			continue
		}
		if seen[e.SessionID] {
			continue
		}
		seen[e.SessionID] = true
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}

func readFaults(workspace string) ([]schema.FaultEntry, error) {
	path := filepath.Join(workspace, ".ai", "faults.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []schema.FaultEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e schema.FaultEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			fmt.Fprintf(os.Stderr, "provenance: skipping malformed fault line: %v\n", err)
			continue
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}

func readVerify(workspace string) ([]schema.VerifyEntry, error) {
	path := filepath.Join(workspace, ".ai", "results.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []schema.VerifyEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e schema.VerifyEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			fmt.Fprintf(os.Stderr, "provenance: skipping malformed verify line: %v\n", err)
			continue
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}
