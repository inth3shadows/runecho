package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/inth3shadows/runecho/internal/provenance"
)

func registerProvenanceTools(r *Registry) {
	r.register(ToolDef{
		Name:        "runecho_provenance_export",
		Description: "Export the full session provenance trace for a task (sessions, cost, faults, verify results).",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"workspace": {Type: "string", Description: "Project root (overrides server default)"},
				"task_id":   {Type: "string", Description: "Task ID to export provenance for"},
			},
			Required: []string{"task_id"},
		},
		Handler: handleProvenanceExport,
	})
}

func handleProvenanceExport(workspace string, params json.RawMessage) (string, error) {
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if p.TaskID == "" {
		return "", fmt.Errorf("task_id is required")
	}

	prov, err := provenance.Export(workspace, p.TaskID)
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(prov)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
