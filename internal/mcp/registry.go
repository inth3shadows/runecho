package mcp

import (
	"encoding/json"
	"fmt"
)

// InputSchema is a JSON Schema object describing a tool's parameters.
type InputSchema struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties,omitempty"`
	Required   []string            `json:"required,omitempty"`
}

// Property describes one field in an InputSchema.
type Property struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// ToolDef is a registered MCP tool.
type ToolDef struct {
	Name        string
	Description string
	InputSchema InputSchema
	// Handler receives the resolved workspace and the raw params JSON.
	// It returns a JSON string to embed in the MCP content response.
	Handler func(workspace string, params json.RawMessage) (string, error)
}

// Registry holds all registered tools.
type Registry struct {
	tools     []ToolDef
	byName    map[string]*ToolDef
}

func newRegistry() *Registry {
	return &Registry{byName: make(map[string]*ToolDef)}
}

func (r *Registry) register(t ToolDef) {
	r.tools = append(r.tools, t)
	r.byName[t.Name] = &r.tools[len(r.tools)-1]
}

// listResult returns the tools/list result shape.
func (r *Registry) listResult() any {
	type toolEntry struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		InputSchema InputSchema `json:"inputSchema"`
	}
	entries := make([]toolEntry, len(r.tools))
	for i, t := range r.tools {
		entries[i] = toolEntry{t.Name, t.Description, t.InputSchema}
	}
	return map[string]any{"tools": entries}
}

// call dispatches a tools/call request.
func (r *Registry) call(defaultWorkspace string, params json.RawMessage) (any, error) {
	// params shape: {"name": "...", "arguments": {...}}
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	def, ok := r.byName[p.Name]
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", p.Name)
	}

	// Resolve workspace: extract from arguments if present, else use default.
	workspace := defaultWorkspace
	if len(p.Arguments) > 0 {
		var args map[string]json.RawMessage
		if err := json.Unmarshal(p.Arguments, &args); err == nil {
			if raw, ok := args["workspace"]; ok {
				var ws string
				if err := json.Unmarshal(raw, &ws); err == nil && ws != "" {
					workspace = ws
				}
			}
		}
	}

	text, err := def.Handler(workspace, p.Arguments)
	isError := err != nil
	if isError {
		text = err.Error()
	}

	result := map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
		"isError": isError,
	}
	return result, nil
}
