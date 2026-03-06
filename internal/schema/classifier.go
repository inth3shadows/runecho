package schema

// ClassifierEntry is one record in {stateDir}/classifier-log.jsonl.
// Records a single model-routing classification attempt.
type ClassifierEntry struct {
	Ts        string `json:"ts"`
	Prompt    string `json:"prompt"`
	Route     string `json:"route"`
	Source    string `json:"source"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}
