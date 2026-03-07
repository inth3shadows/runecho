package mcp

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/inth3shadows/runecho/internal/task"
)

func registerTaskTools(r *Registry) {
	r.register(ToolDef{
		Name:        "runecho_task_list",
		Description: "List tasks from the RunEcho task ledger, optionally filtered by status.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"workspace": {Type: "string", Description: "Project root (overrides server default)"},
				"status":    {Type: "string", Description: "Filter by status: todo, in-progress, done, blocked (omit for all)"},
			},
		},
		Handler: handleTaskList,
	})

	r.register(ToolDef{
		Name:        "runecho_task_next",
		Description: "Return the next unblocked todo task (lowest ID with status=todo and no blockedBy).",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"workspace": {Type: "string", Description: "Project root (overrides server default)"},
			},
		},
		Handler: handleTaskNext,
	})

	r.register(ToolDef{
		Name:        "runecho_task_update",
		Description: "Update a task's status (todo, in-progress, done, blocked).",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"workspace": {Type: "string", Description: "Project root (overrides server default)"},
				"id":        {Type: "string", Description: "Task ID"},
				"status":    {Type: "string", Description: "New status: todo, in-progress, done, blocked"},
			},
			Required: []string{"id", "status"},
		},
		Handler: handleTaskUpdate,
	})
}

func handleTaskList(workspace string, params json.RawMessage) (string, error) {
	var p struct {
		Status string `json:"status"`
	}
	json.Unmarshal(params, &p) //nolint:errcheck

	db, err := task.Load(workspace)
	if err != nil {
		return "", fmt.Errorf("load tasks: %w", err)
	}

	tasks := db.Tasks
	if p.Status != "" {
		var filtered []task.Task
		for _, t := range tasks {
			if t.Status == p.Status {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}
	if tasks == nil {
		tasks = []task.Task{}
	}

	out := map[string]any{"tasks": tasks, "count": len(tasks)}
	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func handleTaskNext(workspace string, params json.RawMessage) (string, error) {
	db, err := task.Load(workspace)
	if err != nil {
		return "", fmt.Errorf("load tasks: %w", err)
	}

	sorted := task.SortByID(db.Tasks)
	for _, t := range sorted {
		if t.Status == "todo" && len(t.BlockedBy) == 0 {
			data, err := json.Marshal(map[string]any{"found": true, "task": t})
			if err != nil {
				return "", err
			}
			return string(data), nil
		}
	}

	data, _ := json.Marshal(map[string]any{"found": false})
	return string(data), nil
}

func handleTaskUpdate(workspace string, params json.RawMessage) (string, error) {
	var p struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if p.ID == "" || p.Status == "" {
		return "", fmt.Errorf("id and status are required")
	}

	db, err := task.Load(workspace)
	if err != nil {
		return "", fmt.Errorf("load tasks: %w", err)
	}

	var updated *task.Task
	for i := range db.Tasks {
		if db.Tasks[i].ID == p.ID {
			db.Tasks[i].Status = p.Status
			db.Tasks[i].Updated = time.Now().UTC().Format(time.RFC3339)
			updated = &db.Tasks[i]
			break
		}
	}
	if updated == nil {
		return "", fmt.Errorf("task %q not found", p.ID)
	}

	db.Updated = time.Now().UTC().Format(time.RFC3339)
	if err := task.Save(workspace, db); err != nil {
		return "", fmt.Errorf("save tasks: %w", err)
	}

	data, err := json.Marshal(map[string]any{"ok": true, "task": updated})
	if err != nil {
		return "", err
	}
	return string(data), nil
}
