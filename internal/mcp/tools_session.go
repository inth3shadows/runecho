package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/inth3shadows/runecho/internal/session"
)

func registerSessionTools(r *Registry) {
	r.register(ToolDef{
		Name:        "runecho_session_status",
		Description: "Return fault count and the most recent progress entry for the workspace.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"workspace": {Type: "string", Description: "Project root (overrides server default)"},
			},
		},
		Handler: handleSessionStatus,
	})

	r.register(ToolDef{
		Name:        "runecho_fault_list",
		Description: "List faults from .ai/faults.jsonl, optionally filtered by session ID or signal.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"workspace":  {Type: "string", Description: "Project root (overrides server default)"},
				"session_id": {Type: "string", Description: "Filter to a specific session ID"},
				"signal":     {Type: "string", Description: "Filter by signal name (e.g. COST_WARN, VERIFY_FAIL, DRIFT_AFFECTED)"},
			},
		},
		Handler: handleFaultList,
	})
}

func handleSessionStatus(workspace string, params json.RawMessage) (string, error) {
	faults, err := session.ReadFaults(workspace)
	if err != nil {
		return "", fmt.Errorf("read faults: %w", err)
	}

	progress, err := session.ReadProgress(workspace)
	if err != nil {
		return "", fmt.Errorf("read progress: %w", err)
	}

	var lastProgress any
	if len(progress) > 0 {
		lastProgress = progress[len(progress)-1]
	}

	// Fault summary by signal.
	summary := make(map[string]int)
	for _, f := range faults {
		summary[f.Signal]++
	}

	out := map[string]any{
		"fault_count":      len(faults),
		"fault_summary":    summary,
		"progress_entries": len(progress),
		"last_progress":    lastProgress,
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func handleFaultList(workspace string, params json.RawMessage) (string, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Signal    string `json:"signal"`
	}
	json.Unmarshal(params, &p) //nolint:errcheck

	faults, err := session.ReadFaults(workspace)
	if err != nil {
		return "", fmt.Errorf("read faults: %w", err)
	}

	var filtered []any
	for _, f := range faults {
		if p.SessionID != "" && f.SessionID != p.SessionID {
			continue
		}
		if p.Signal != "" && f.Signal != p.Signal {
			continue
		}
		filtered = append(filtered, f)
	}
	if filtered == nil {
		filtered = []any{}
	}

	out := map[string]any{"faults": filtered, "count": len(filtered)}
	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
