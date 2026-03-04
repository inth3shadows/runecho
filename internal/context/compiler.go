package context

import (
	"strings"
)

// DefaultProviders is the ordered list used when --providers is not specified.
// contract runs first so Claude sees scope constraints before IR context.
// review goes last — only injects when actionable, short-circuits silently otherwise.
var DefaultProviders = []string{"contract", "ir", "gitdiff", "handoff", "tasks", "review"}

// Compiler assembles context blocks from multiple providers within a token budget.
type Compiler struct {
	providers map[string]Provider
}

// NewCompiler creates a compiler with all built-in providers registered.
func NewCompiler() *Compiler {
	c := &Compiler{providers: make(map[string]Provider)}
	for _, p := range []Provider{
		&ContractProvider{},
		&IRProvider{},
		&GitDiffProvider{},
		&HandoffProvider{},
		&TasksProvider{},
		&ChurnProvider{},
		&ReviewProvider{},
	} {
		c.providers[p.Name()] = p
	}
	return c
}

// Compile runs the requested providers and assembles output within the budget.
// Providers are run in order; each consumes from the remaining budget.
// A provider whose output would exceed the remaining budget is skipped.
func (c *Compiler) Compile(req Request, providerNames []string) (string, error) {
	if len(providerNames) == 0 {
		providerNames = DefaultProviders
	}

	remaining := req.Budget
	var blocks []string

	for _, name := range providerNames {
		p, ok := c.providers[name]
		if !ok {
			continue
		}

		result, err := p.Provide(req)
		if err != nil || result.Content == "" {
			continue
		}

		// Budget check: skip if this block alone would blow the budget.
		// IR provider (typically the largest) gets a generous first-pass;
		// smaller providers always fit unless budget is tiny.
		if remaining > 0 && result.Tokens > remaining {
			continue
		}

		blocks = append(blocks, result.Content)
		remaining -= result.Tokens
	}

	return strings.Join(blocks, "\n\n"), nil
}
