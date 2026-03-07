package schema

// HookEntry is one record in .ai/hooks.jsonl.
// Records a single hook execution for latency/reliability telemetry.
type HookEntry struct {
	Ts         string `json:"ts"`
	HookName   string `json:"hook_name"`
	SessionID  string `json:"session_id"`
	ExitCode   int    `json:"exit_code"`
	LatencyMS  int64  `json:"latency_ms"`
	OutputSize int    `json:"output_size"`
}
