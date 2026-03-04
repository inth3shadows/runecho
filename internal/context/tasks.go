package context

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TasksProvider injects the active task queue.
type TasksProvider struct{}

func (p *TasksProvider) Name() string { return "tasks" }

type taskEntry struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Title     string `json:"title"`
	BlockedBy string `json:"blockedBy,omitempty"`
	Scope     string `json:"scope,omitempty"`
	Verify    string `json:"verify,omitempty"`
}

type taskDB struct {
	Tasks []taskEntry `json:"tasks"`
}

func (p *TasksProvider) Provide(req Request) (Result, error) {
	tasksFile := filepath.Join(req.Workspace, ".ai", "tasks.json")
	data, err := os.ReadFile(tasksFile)
	if err != nil {
		return Result{Name: p.Name()}, nil
	}

	var db taskDB
	if err := json.Unmarshal(data, &db); err != nil {
		return Result{Name: p.Name()}, nil
	}

	var active []taskEntry
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
		if t.BlockedBy != "" {
			line += fmt.Sprintf(" (blocked by #%s)", t.BlockedBy)
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
