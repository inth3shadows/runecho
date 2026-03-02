package session

import "time"

// Fact is ground-truth data extracted from the Claude Code JSONL session log.
// All fields are derived from actual tool calls and API responses — no LLM inference.
type Fact struct {
	SessionID       string
	StartTime       time.Time
	EndTime         time.Time
	TurnCount       int
	Model           string         // dominant model (most turns)
	Models          map[string]int // model → turn count
	FilesEdited     []string       // paths from Edit tool calls
	FilesCreated    []string       // paths from Write tool calls
	Commands        []string       // description or truncated command from Bash tool calls
	InputTokens     int64
	OutputTokens    int64
	CacheReads      int64
	TotalDurationMs int64
	AssistantText   []string // last 3 assistant text blocks, for haiku context
}

// Summary is the narrative produced by haiku, grounded in Fact data.
type Summary struct {
	Accomplished []string `json:"accomplished"`
	Decisions    []string `json:"decisions"`
	Open         []string `json:"open"`
	NextSteps    []string `json:"next_steps"`
}

// CostEstimate returns an approximate USD cost using claude-sonnet-4-6 rates.
// Rates: $3/1M input, $15/1M output, $0.30/1M cache read.
func CostEstimate(f *Fact) float64 {
	return float64(f.InputTokens)*3.0/1_000_000 +
		float64(f.OutputTokens)*15.0/1_000_000 +
		float64(f.CacheReads)*0.30/1_000_000
}
