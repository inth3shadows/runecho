// Package schema defines canonical Go types for all JSONL files in .ai/.
// This package contains type definitions only — no file I/O.
package schema

// FaultEntry is one record in .ai/faults.jsonl for governor and session fault signals.
// JSON key "ts" is preserved for wire compatibility with existing data.
type FaultEntry struct {
	Signal    string  `json:"signal"`
	SessionID string  `json:"session_id"`
	Ts        string  `json:"ts"`
	Value     float64 `json:"value"`
	Context   string  `json:"context"`
	Workspace string  `json:"workspace,omitempty"`
}

// DriftFaultEntry is one record in .ai/faults.jsonl with Signal="DRIFT_AFFECTED".
// Written by the drift checker; uses a different shape than FaultEntry.
type DriftFaultEntry struct {
	Signal    string   `json:"signal"`
	TaskID    string   `json:"task_id"`
	TaskTitle string   `json:"task_title"`
	Scope     string   `json:"scope"`
	Files     []string `json:"files"`
	SessionID string   `json:"session_id"`
	Timestamp string   `json:"timestamp"`
}
