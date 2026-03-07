package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const TasksFile = ".ai/tasks.json"

type Task struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Title     string   `json:"title"`
	Added     string   `json:"added"`
	Updated   string   `json:"updated"`
	BlockedBy []string `json:"blockedBy,omitempty"`
	Scope     string   `json:"scope,omitempty"`  // glob pattern(s) for allowed file paths
	Verify    string   `json:"verify,omitempty"` // shell command to validate completion
}

// UnmarshalJSON handles backward compat where blockedBy may be a string or []string.
func (t *Task) UnmarshalJSON(data []byte) error {
	type TaskAlias Task
	var aux struct {
		TaskAlias
		BlockedBy json.RawMessage `json:"blockedBy"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*t = Task(aux.TaskAlias)
	if len(aux.BlockedBy) > 0 && string(aux.BlockedBy) != "null" {
		var sl []string
		if err := json.Unmarshal(aux.BlockedBy, &sl); err == nil {
			t.BlockedBy = sl
		} else {
			var s string
			if err := json.Unmarshal(aux.BlockedBy, &s); err != nil {
				return fmt.Errorf("blockedBy: expected string or []string")
			}
			if s != "" {
				t.BlockedBy = []string{s}
			}
		}
	}
	return nil
}

// IsBlocked reports whether all of t's dependencies are satisfied (done).
func (t Task) IsBlocked(done map[string]bool) bool {
	for _, dep := range t.BlockedBy {
		if !done[dep] {
			return true
		}
	}
	return false
}

type TaskDB struct {
	Updated string `json:"updated"`
	Tasks   []Task `json:"tasks"`
}

// Load reads TasksFile from root. Returns an empty TaskDB if the file does not exist.
func Load(root string) (TaskDB, error) {
	path := filepath.Join(root, TasksFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TaskDB{Updated: time.Now().UTC().Format(time.RFC3339), Tasks: []Task{}}, nil
		}
		return TaskDB{}, err
	}
	var db TaskDB
	if err := json.Unmarshal(data, &db); err != nil {
		return TaskDB{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return db, nil
}

// Save atomically writes db to TasksFile under root.
func Save(root string, db TaskDB) error {
	path := filepath.Join(root, TasksFile)
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MaxID returns the highest numeric task ID in the list, or 0 if empty.
func MaxID(tasks []Task) int {
	max := 0
	for _, t := range tasks {
		n, err := strconv.Atoi(t.ID)
		if err == nil && n > max {
			max = n
		}
	}
	return max
}

// SortByID returns a copy of tasks sorted by numeric ID ascending.
func SortByID(tasks []Task) []Task {
	out := make([]Task, len(tasks))
	copy(out, tasks)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			ai, _ := strconv.Atoi(out[j-1].ID)
			bi, _ := strconv.Atoi(out[j].ID)
			if ai > bi {
				out[j-1], out[j] = out[j], out[j-1]
			} else {
				break
			}
		}
	}
	return out
}
