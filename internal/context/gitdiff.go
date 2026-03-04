package context

import (
	"os/exec"
	"strings"
)

// GitDiffProvider injects a compact structural diff from the prior session-end snapshot.
// It runs ai-ir diff --since=session-end --compact to get the diff line.
type GitDiffProvider struct{}

func (p *GitDiffProvider) Name() string { return "gitdiff" }

func (p *GitDiffProvider) Provide(req Request) (Result, error) {
	// Check if ai-ir is available and history.db exists
	aiir, err := exec.LookPath("ai-ir")
	if err != nil {
		return Result{Name: p.Name()}, nil
	}

	cmd := exec.Command(aiir, "diff", "--since=session-end", "--compact", req.Workspace)
	out, err := cmd.Output()
	if err != nil {
		return Result{Name: p.Name()}, nil
	}

	diffLine := strings.TrimSpace(string(out))
	if diffLine == "" {
		return Result{Name: p.Name()}, nil
	}

	return Result{
		Name:    p.Name(),
		Content: diffLine,
		Tokens:  estimateTokens(diffLine),
	}, nil
}
