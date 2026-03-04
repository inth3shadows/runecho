package pipeline

// Pipeline is a declarative definition of a multi-step model routing pipeline.
type Pipeline struct {
	Name        string  `yaml:"name"`
	Description string  `yaml:"description,omitempty"`
	Stages      []Stage `yaml:"stages"`
}

// Stage is one phase of a pipeline.
type Stage struct {
	ID          string `yaml:"id"`
	Model       string `yaml:"model"`             // haiku | sonnet | opus
	TokenBudget int    `yaml:"token_budget,omitempty"`
	Scope       string `yaml:"scope,omitempty"`   // glob (informational in M5)
	Verify      string `yaml:"verify,omitempty"`  // shell cmd (informational in M5)
	Description string `yaml:"description,omitempty"`
}

// Envelope records the execution of a pipeline-routed session.
type Envelope struct {
	SessionID   string        `json:"session_id"`
	Pipeline    string        `json:"pipeline"`
	Timestamp   string        `json:"timestamp"`
	IRHashStart string        `json:"ir_hash_start"`
	IRHashEnd   string        `json:"ir_hash_end"`
	CostUSD     float64       `json:"cost_usd"`
	DurationMS  int64         `json:"duration_ms"`
	Stages      []StageResult `json:"stages"`
	Faults      []string      `json:"faults"`  // signal names from faults.jsonl for this session
	Status      string        `json:"status"`  // "complete" | "abandoned"
}

// StageResult records the outcome of one stage within an Envelope.
type StageResult struct {
	StageID  string  `json:"stage_id"`
	Model    string  `json:"model"`
	CostUSD  float64 `json:"cost_usd"`  // 0.0 in M5, populated in M7
	Skipped  bool    `json:"skipped"`
	VerifyOK *bool   `json:"verify_ok"` // nil = not run
}
