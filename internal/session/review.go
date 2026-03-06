package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inth3shadows/runecho/internal/schema"
)

// ReviewTask is a minimal task descriptor used for review analysis.
// Kept local to avoid importing cmd/task (main package).
type ReviewTask struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Title  string `json:"title"`
}

// StuckTask describes a non-done task that has appeared across multiple sessions.
type StuckTask struct {
	TaskID       string
	Title        string
	SessionCount int
	Status       string
}

// ReviewReport is the aggregated result of session analysis.
type ReviewReport struct {
	Sessions     []schema.ProgressEntry
	StuckTasks   []StuckTask
	CostTrend    []float64      // last 5 sessions
	TotalCost    float64
	DriftCount   int
	FaultSummary map[string]int // signal → count
}

// ReadProgress reads .ai/progress.jsonl, deduplicates by session_id, tolerates partial lines.
// Returns nil slice (not an error) if the file does not exist.
func ReadProgress(workspace string) ([]schema.ProgressEntry, error) {
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
			fmt.Fprintf(os.Stderr, "review: skipping malformed progress line: %v\n", err)
			continue
		}
		if seen[e.SessionID] {
			continue
		}
		seen[e.SessionID] = true
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Join with results.jsonl to populate VerifyPassed + VerifyCmd (keyed by session_id).
	verifyEntries, _ := ReadVerify(workspace) // non-fatal if missing
	verifyBySession := make(map[string]schema.VerifyEntry, len(verifyEntries))
	for _, ve := range verifyEntries {
		verifyBySession[ve.SessionID] = ve
	}
	for i := range entries {
		if ve, ok := verifyBySession[entries[i].SessionID]; ok {
			passed := ve.Passed
			entries[i].VerifyPassed = &passed
			entries[i].VerifyCmd = ve.Cmd
		}
	}

	return entries, nil
}

// ReadFaults reads .ai/faults.jsonl, tolerates partial lines.
// Returns nil slice (not an error) if the file does not exist.
func ReadFaults(workspace string) ([]schema.FaultEntry, error) {
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
			fmt.Fprintf(os.Stderr, "review: skipping malformed fault line: %v\n", err)
			continue
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}

// ReadReviewTasks reads tasks.json and returns minimal task descriptors for review.
// Returns nil slice (not an error) if the file does not exist.
func ReadReviewTasks(workspace string) ([]ReviewTask, error) {
	path := filepath.Join(workspace, ".ai", "tasks.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var db struct {
		Tasks []ReviewTask `json:"tasks"`
	}
	if err := json.Unmarshal(data, &db); err != nil {
		return nil, err
	}
	return db.Tasks, nil
}

// BuildReport aggregates sessions, faults, and tasks into a ReviewReport.
// stuck = task not done AND appears in tasks_touched across 3+ distinct sessions.
func BuildReport(entries []schema.ProgressEntry, faults []schema.FaultEntry, tasks []ReviewTask) ReviewReport {
	r := ReviewReport{
		Sessions:     entries,
		FaultSummary: make(map[string]int),
	}

	// Fault summary
	for _, f := range faults {
		r.FaultSummary[f.Signal]++
	}

	// Cost trend (last 5 sessions) + total
	n := len(entries)
	start := n - 5
	if start < 0 {
		start = 0
	}
	for i := start; i < n; i++ {
		r.CostTrend = append(r.CostTrend, entries[i].CostUSD)
	}
	for _, e := range entries {
		r.TotalCost += e.CostUSD
	}

	// Drift count
	for _, e := range entries {
		if e.ScopeDrift.Drifted {
			r.DriftCount++
		}
	}

	// Stuck tasks: count how many distinct sessions each task appeared in
	taskSessionCount := make(map[string]int)
	for _, e := range entries {
		seen := make(map[string]bool)
		for _, tid := range e.TasksTouched {
			if !seen[tid] {
				taskSessionCount[tid]++
				seen[tid] = true
			}
		}
	}

	taskMap := make(map[string]ReviewTask)
	for _, t := range tasks {
		taskMap[t.ID] = t
	}

	for tid, count := range taskSessionCount {
		if count < 3 {
			continue
		}
		t, ok := taskMap[tid]
		if !ok {
			r.StuckTasks = append(r.StuckTasks, StuckTask{
				TaskID:       tid,
				Title:        "(unknown)",
				SessionCount: count,
				Status:       "unknown",
			})
			continue
		}
		if t.Status == "done" {
			continue
		}
		r.StuckTasks = append(r.StuckTasks, StuckTask{
			TaskID:       tid,
			Title:        t.Title,
			SessionCount: count,
			Status:       t.Status,
		})
	}

	sort.Slice(r.StuckTasks, func(i, j int) bool {
		return r.StuckTasks[i].SessionCount > r.StuckTasks[j].SessionCount
	})

	return r
}

// IsActionable returns true if the report warrants surfacing to Claude.
func IsActionable(r ReviewReport) bool {
	if len(r.StuckTasks) > 0 {
		return true
	}
	// Cost trend rising sharply: last session > 2× average of prior sessions in window
	if len(r.CostTrend) >= 2 {
		last := r.CostTrend[len(r.CostTrend)-1]
		prior := r.CostTrend[:len(r.CostTrend)-1]
		var sum float64
		for _, c := range prior {
			sum += c
		}
		avg := sum / float64(len(prior))
		if avg > 0 && last > 2*avg {
			return true
		}
	}
	// Drift in any of the last 3 sessions
	n := len(r.Sessions)
	start := n - 3
	if start < 0 {
		start = 0
	}
	for i := start; i < n; i++ {
		if r.Sessions[i].ScopeDrift.Drifted {
			return true
		}
	}
	return false
}

// FormatReport renders a ReviewReport for display.
// trace=true produces a per-task session timeline; default is a ≤10-line summary.
func FormatReport(r ReviewReport, trace bool) string {
	if len(r.Sessions) == 0 {
		return "SESSION REVIEW: no sessions recorded yet."
	}

	var sb strings.Builder

	if trace {
		sb.WriteString(fmt.Sprintf("SESSION REVIEW [trace — %d sessions]\n", len(r.Sessions)))
		// Group sessions by task ID
		taskSessions := make(map[string][]schema.ProgressEntry)
		for _, e := range r.Sessions {
			if len(e.TasksTouched) == 0 {
				taskSessions["(untagged)"] = append(taskSessions["(untagged)"], e)
				continue
			}
			for _, tid := range e.TasksTouched {
				taskSessions[tid] = append(taskSessions[tid], e)
			}
		}
		var tids []string
		for tid := range taskSessions {
			tids = append(tids, tid)
		}
		sort.Strings(tids)
		for _, tid := range tids {
			sessions := taskSessions[tid]
			sb.WriteString(fmt.Sprintf("  Task #%s (%d sessions):\n", tid, len(sessions)))
			for _, e := range sessions {
				ts := e.Timestamp
				if len(ts) > 10 {
					ts = ts[:10]
				}
				drift := ""
				if e.ScopeDrift.Drifted {
					drift = " [DRIFT]"
				}
				sb.WriteString(fmt.Sprintf("    %s | %d turns | $%.2f%s\n",
					ts, e.Turns, e.CostUSD, drift))
			}
		}
	} else {
		sb.WriteString(fmt.Sprintf("SESSION REVIEW [%d sessions, $%.2f total]\n",
			len(r.Sessions), r.TotalCost))

		if len(r.StuckTasks) > 0 {
			sb.WriteString("  Stuck tasks:\n")
			for _, t := range r.StuckTasks {
				sb.WriteString(fmt.Sprintf("    #%s %q — %d sessions, status=%s\n",
					t.TaskID, t.Title, t.SessionCount, t.Status))
			}
		}

		if len(r.CostTrend) > 0 {
			var parts []string
			for _, c := range r.CostTrend {
				parts = append(parts, fmt.Sprintf("$%.2f", c))
			}
			sb.WriteString(fmt.Sprintf("  Cost trend (last %d): %s\n",
				len(r.CostTrend), strings.Join(parts, " → ")))
		}

		if len(r.FaultSummary) > 0 {
			type kv struct {
				k string
				v int
			}
			var sorted []kv
			for k, v := range r.FaultSummary {
				sorted = append(sorted, kv{k, v})
			}
			sort.Slice(sorted, func(i, j int) bool {
				return sorted[i].v > sorted[j].v
			})
			var parts []string
			for _, p := range sorted {
				parts = append(parts, fmt.Sprintf("%s×%d", p.k, p.v))
			}
			sb.WriteString("  Fault signals: " + strings.Join(parts, " ") + "\n")
		}

		if r.DriftCount > 0 {
			sb.WriteString(fmt.Sprintf("  Scope drift: %d session(s)\n", r.DriftCount))
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}
