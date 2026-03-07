package context

import (
	"fmt"
	"strings"

	"github.com/inth3shadows/runecho/internal/task"
)

// TasksProvider injects the active task queue.
type TasksProvider struct{}

func (p *TasksProvider) Name() string { return "tasks" }

func (p *TasksProvider) Provide(req Request) (Result, error) {
	db, err := task.Load(req.Workspace)
	if err != nil {
		return Result{Name: p.Name()}, nil
	}

	var active []task.Task
	for _, t := range db.Tasks {
		if t.Status != "done" {
			active = append(active, t)
		}
	}
	if len(active) == 0 {
		return Result{Name: p.Name()}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("TASK QUEUE [%d active]:\n", len(active)))
	for _, t := range active {
		line := fmt.Sprintf("  [%s] #%s: %s", t.Status, t.ID, t.Title)
		if len(t.BlockedBy) > 0 {
			line += fmt.Sprintf(" (blocked by #%s)", strings.Join(t.BlockedBy, ","))
		}
		sb.WriteString(line + "\n")
	}

	out := strings.TrimRight(sb.String(), "\n")
	return Result{
		Name:    p.Name(),
		Content: out,
		Tokens:  estimateTokens(out),
	}, nil
}
