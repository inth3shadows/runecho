package schema

// VerifyEntry is one record in .ai/results.jsonl.
// Records one verify run for a task in a session.
type VerifyEntry struct {
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id"`
	Timestamp string `json:"timestamp"`
	Cmd       string `json:"cmd"`
	Passed    bool   `json:"passed"`
	ExitCode  int    `json:"exit_code"`
	Output     string `json:"output"`                  // max 500 chars, combined stderr+stdout (kept for compat)
	Stdout     string `json:"stdout,omitempty"`        // max 500 chars
	Stderr     string `json:"stderr,omitempty"`        // max 500 chars
	OutputHash string `json:"output_hash,omitempty"`   // SHA256 hex of full combined stdout+stderr (before truncation)
	OutputPath string `json:"output_path,omitempty"`   // relative path to full output sidecar file
}
