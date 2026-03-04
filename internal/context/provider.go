// Package context assembles per-session context blocks for Claude Code injection.
// Each provider contributes a named block; the compiler budget-gates the output.
package context

// Request is passed to every provider when assembling context.
type Request struct {
	Workspace string // absolute path to project root
	SessionID string
	Prompt    string // current user prompt (for relevance scoring)
	Budget    int    // approximate token budget for the entire compiled output
}

// Result is what a provider returns.
type Result struct {
	Name    string // provider identifier (ir, handoff, tasks, gitdiff, churn)
	Content string // formatted markdown block; empty if nothing to show
	Tokens  int    // estimated token count
}

// Provider generates one block of context.
type Provider interface {
	Name() string
	Provide(req Request) (Result, error)
}

// estimateTokens approximates token count: 1 token ≈ 4 chars.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}
