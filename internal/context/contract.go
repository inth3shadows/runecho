package context

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inth3shadows/runecho/internal/contract"
	"github.com/inth3shadows/runecho/internal/task"
)

// ContractProvider emits a SESSION CONTRACT block. It first looks for
// .ai/CONTRACT.yaml (tool-generated or Claude-written); if absent, it
// falls back to deriving a contract from the active task's scope/verify fields.
type ContractProvider struct{}

func (p *ContractProvider) Name() string { return "contract" }

func (p *ContractProvider) Provide(req Request) (Result, error) {
	c, taskID, err := p.loadWithTaskID(req.Workspace)
	if err != nil {
		return Result{Name: p.Name()}, nil
	}
	if c == nil || (len(c.Scope) == 0 && c.Verify == "") {
		return Result{Name: p.Name()}, nil
	}

	advisory := loadDriftAdvisory(req.Workspace, taskID)
	out := formatContract(c, advisory)
	return Result{
		Name:    p.Name(),
		Content: out,
		Tokens:  estimateTokens(out),
	}, nil
}

// load returns the contract to use, preferring CONTRACT.yaml over task-derived.
func (p *ContractProvider) load(workspace string) (*contract.Contract, error) {
	c, _, err := p.loadWithTaskID(workspace)
	return c, err
}

// loadWithTaskID returns the contract and active task ID (if task-derived).
func (p *ContractProvider) loadWithTaskID(workspace string) (*contract.Contract, string, error) {
	yamlPath := filepath.Join(workspace, ".ai", "CONTRACT.yaml")
	c, err := contract.Parse(yamlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "contract provider: parse error: %v\n", err)
		// Fall through to task-derived on parse error.
	}
	if c != nil {
		// Validate but don't block on errors — log and continue.
		if errs := contract.Validate(c); len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "contract provider: validation warning: %v\n", e)
			}
		}
		return c, "", nil
	}

	// Fall back to active task fields.
	taskContract, taskID, err2 := p.fromActiveTaskWithID(workspace)
	return taskContract, taskID, err2
}

// fromActiveTask reads tasks.json and derives a contract from the active task.
func (p *ContractProvider) fromActiveTask(workspace string) (*contract.Contract, error) {
	c, _, err := p.fromActiveTaskWithID(workspace)
	return c, err
}

// fromActiveTaskWithID reads tasks.json and derives a contract from the active task,
// also returning the task ID for drift advisory lookup.
func (p *ContractProvider) fromActiveTaskWithID(workspace string) (*contract.Contract, string, error) {
	tasksFile := filepath.Join(workspace, ".ai", "tasks.json")
	data, err := os.ReadFile(tasksFile)
	if err != nil {
		return nil, "", nil
	}

	var db taskDB
	if err := json.Unmarshal(data, &db); err != nil {
		return nil, "", nil
	}

	done := make(map[string]bool)
	for _, t := range db.Tasks {
		if t.Status == "done" {
			done[t.ID] = true
		}
	}

	for i := range db.Tasks {
		t := &db.Tasks[i]
		if t.Status == "done" {
			continue
		}
		if t.BlockedBy != "" && !done[t.BlockedBy] {
			continue
		}
		if t.Scope == "" && t.Verify == "" {
			return nil, "", nil
		}
		return contract.FromTask(t.ID, t.Title, t.Scope, t.Verify), t.ID, nil
	}
	return nil, "", nil
}

// formatContract renders a Contract as the SESSION CONTRACT block.
// advisory is optional drift advisory text appended after the contract fields.
func formatContract(c *contract.Contract, advisory string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("SESSION CONTRACT [%s]:\n", c.Title))

	if len(c.Scope) > 0 {
		sb.WriteString(fmt.Sprintf("%-13s%s\n", "Scope:", c.Scope[0]))
		for _, s := range c.Scope[1:] {
			sb.WriteString(fmt.Sprintf("%-13s%s\n", "", s))
		}
		sb.WriteString(fmt.Sprintf("%-13s%s\n", "", "Files outside scope must not be modified without explicit approval."))
	}

	if c.Verify != "" {
		sb.WriteString(fmt.Sprintf("%-13s%s\n", "Verify:", c.Verify))
	}

	for _, s := range c.Success {
		sb.WriteString(fmt.Sprintf("%-13s%s\n", "Success:", s))
	}

	for _, a := range c.Assumptions {
		sb.WriteString(fmt.Sprintf("%-13s%s\n", "Assumptions:", a))
	}

	for _, ng := range c.NonGoals {
		sb.WriteString(fmt.Sprintf("%-13s%s\n", "Non-goals:", ng))
	}

	if advisory != "" {
		sb.WriteString("\n")
		sb.WriteString(advisory)
	}

	return strings.TrimRight(sb.String(), "\n")
}

// loadDriftAdvisory reads .ai/faults.jsonl and returns formatted DRIFT ADVISORY
// text for the given taskID. Returns "" if no recent DRIFT_AFFECTED entries exist.
// "Recent" is defined as the last 20 entries in the file for this task.
func loadDriftAdvisory(workspace, taskID string) string {
	if taskID == "" {
		return ""
	}
	entries, err := task.LoadDriftFaults(workspace, taskID)
	if err != nil || len(entries) == 0 {
		return ""
	}

	// Take the most recent entry (last in file order).
	e := entries[len(entries)-1]

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("DRIFT ADVISORY [%s]:\n", e.TaskTitle))
	if len(e.Files) > 0 {
		sb.WriteString(fmt.Sprintf("  %-9s%s\n", "Changed:", strings.Join(e.Files, ", ")))
	}
	sb.WriteString(fmt.Sprintf("  %-9s%s\n", "Scope:", e.Scope))
	sb.WriteString(fmt.Sprintf("  %-9s%s\n", "Action:", "Re-evaluate task scope. Run `ai-task replan "+taskID+"` to review."))
	return strings.TrimRight(sb.String(), "\n")
}
