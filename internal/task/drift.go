package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const FaultsFile = ".ai/faults.jsonl"

// DriftEntry is written to .ai/faults.jsonl with Signal="DRIFT_AFFECTED"
// when IR structural changes intersect a task's scope globs.
type DriftEntry struct {
	Signal    string   `json:"signal"`
	TaskID    string   `json:"task_id"`
	TaskTitle string   `json:"task_title"`
	Scope     string   `json:"scope"`
	Files     []string `json:"files"`
	SessionID string   `json:"session_id"`
	Timestamp string   `json:"timestamp"`
}

// CheckDrift intersects changedFiles with all non-done tasks that have scopes.
// Returns one DriftEntry per affected task (files that match any scope glob).
func CheckDrift(tasks []Task, changedFiles []string) []DriftEntry {
	var results []DriftEntry
	for _, t := range tasks {
		if t.Status == "done" || t.Scope == "" {
			continue
		}
		globs := splitGlobs(t.Scope)
		var matched []string
		for _, f := range changedFiles {
			if matchesAny(f, globs) {
				matched = append(matched, f)
			}
		}
		if len(matched) > 0 {
			results = append(results, DriftEntry{
				Signal:    "DRIFT_AFFECTED",
				TaskID:    t.ID,
				TaskTitle: t.Title,
				Scope:     t.Scope,
				Files:     matched,
			})
		}
	}
	return results
}

// AppendDriftFaults appends DriftEntry records to .ai/faults.jsonl under root.
// Idempotent: skips entries where (task_id, session_id) already exists in the file.
func AppendDriftFaults(root string, entries []DriftEntry, sessionID string) error {
	if len(entries) == 0 {
		return nil
	}

	faultsPath := filepath.Join(root, FaultsFile)
	if err := os.MkdirAll(filepath.Dir(faultsPath), 0o755); err != nil {
		return fmt.Errorf("mkdir faults dir: %w", err)
	}

	// Read existing entries to build idempotency set.
	existing := make(map[string]bool)
	if data, err := os.ReadFile(faultsPath); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var e DriftEntry
			if json.Unmarshal([]byte(line), &e) == nil && e.Signal == "DRIFT_AFFECTED" {
				existing[e.TaskID+"|"+e.SessionID] = true
			}
		}
	}

	f, err := os.OpenFile(faultsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open faults file: %w", err)
	}
	defer f.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	for i := range entries {
		entries[i].SessionID = sessionID
		entries[i].Timestamp = ts
		key := entries[i].TaskID + "|" + sessionID
		if existing[key] {
			continue
		}
		line, err := json.Marshal(entries[i])
		if err != nil {
			return fmt.Errorf("marshal drift entry: %w", err)
		}
		if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
			return fmt.Errorf("write drift entry: %w", err)
		}
	}
	return nil
}

// LoadDriftFaults reads all DRIFT_AFFECTED entries from .ai/faults.jsonl for the given taskID.
// Returns entries sorted chronologically (file order). Returns nil if the file doesn't exist.
func LoadDriftFaults(root, taskID string) ([]DriftEntry, error) {
	faultsPath := filepath.Join(root, FaultsFile)
	data, err := os.ReadFile(faultsPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read faults file: %w", err)
	}

	var results []DriftEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e DriftEntry
		if json.Unmarshal([]byte(line), &e) == nil && e.Signal == "DRIFT_AFFECTED" && e.TaskID == taskID {
			results = append(results, e)
		}
	}
	return results, nil
}

// splitGlobs splits a comma-separated scope string into individual glob patterns.
func splitGlobs(scope string) []string {
	parts := strings.Split(scope, ",")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// matchesAny returns true if filePath matches any of the given glob patterns.
// Uses filepath.Match for each glob; also checks if the file is under a directory
// glob expressed as "dir/**" by falling back to a prefix check when ** is present.
func matchesAny(filePath string, globs []string) bool {
	// Normalize to forward slashes for consistent matching.
	normalized := filepath.ToSlash(filePath)
	for _, g := range globs {
		g = filepath.ToSlash(g)
		// Direct match.
		if ok, _ := filepath.Match(g, normalized); ok {
			return true
		}
		// Handle ** glob: strip /** suffix and do a prefix match.
		if strings.Contains(g, "**") {
			prefix := strings.TrimSuffix(g, "/**")
			prefix = strings.TrimSuffix(prefix, "**")
			prefix = strings.TrimRight(prefix, "/")
			if prefix != "" && strings.HasPrefix(normalized, prefix+"/") {
				return true
			}
			if prefix == "" {
				return true // ** matches everything
			}
		}
	}
	return false
}
