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
	Output    string `json:"output"` // max 500 chars, combined stderr+stdout
}
