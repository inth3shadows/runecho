package context

import (
	"os"
	"path/filepath"
	"strings"
)

// ChurnProvider reads the churn cache and uses it for scoring context (no direct output).
// It is a passive provider — its data feeds IR relevance scoring, not a standalone block.
// If --providers=churn is explicitly requested, it surfaces the raw churn list.
type ChurnProvider struct{}

func (p *ChurnProvider) Name() string { return "churn" }

func (p *ChurnProvider) Provide(req Request) (Result, error) {
	data, err := os.ReadFile(filepath.Join(req.Workspace, ".ai", "churn-cache.txt"))
	if err != nil || len(data) == 0 {
		return Result{Name: p.Name()}, nil
	}

	// Churn data is consumed by the IR provider for scoring; don't inject separately
	// unless explicitly requested as the sole provider.
	content := strings.TrimSpace(string(data))
	return Result{
		Name:    p.Name(),
		Content: content,
		Tokens:  estimateTokens(content),
	}, nil
}
