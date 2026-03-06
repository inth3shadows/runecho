package schema

// ProgressEntry is one record in .ai/progress.jsonl.
type ProgressEntry struct {
	SessionID    string     `json:"session_id"`
	Timestamp    string     `json:"timestamp"`
	Turns        int        `json:"turns"`
	CostUSD      float64    `json:"cost_usd"`
	IRHashStart  string     `json:"ir_hash_start"`
	IRHashEnd    string     `json:"ir_hash_end"`
	FilesChanged int        `json:"files_changed"`
	TasksTouched []string   `json:"tasks_touched"`
	HandoffPath  string     `json:"handoff_path"`
	Status       string     `json:"status"`
	ScopeDrift   ScopeDrift `json:"scope_drift"`
	VerifyPassed *bool      `json:"verify_passed,omitempty"`
	VerifyCmd    string     `json:"verify_cmd,omitempty"`
}

// ScopeDrift captures scope violation metadata embedded in a ProgressEntry.
type ScopeDrift struct {
	Drifted   bool     `json:"drifted"`
	Files     []string `json:"files"`
	TaskScope string   `json:"task_scope"`
	TaskID    string   `json:"task_id"`
}
