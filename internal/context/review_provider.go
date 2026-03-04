package context

import (
	"github.com/inth3shadows/runecho/internal/session"
)

// ReviewProvider injects a SESSION REVIEW block on turn 1 when actionable.
type ReviewProvider struct{}

func (p *ReviewProvider) Name() string { return "review" }

func (p *ReviewProvider) Provide(req Request) (Result, error) {
	entries, err := session.ReadProgress(req.Workspace)
	if err != nil {
		return Result{Name: p.Name()}, nil
	}
	faults, err := session.ReadFaults(req.Workspace)
	if err != nil {
		return Result{Name: p.Name()}, nil
	}
	tasks, _ := session.ReadReviewTasks(req.Workspace)

	report := session.BuildReport(entries, faults, tasks)
	if !session.IsActionable(report) {
		return Result{Name: p.Name()}, nil
	}

	content := session.FormatReport(report, false)
	return Result{
		Name:    p.Name(),
		Content: content,
		Tokens:  estimateTokens(content),
	}, nil
}
