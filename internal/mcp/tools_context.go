package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	ctx "github.com/inth3shadows/runecho/internal/context"
)

func registerContextTools(r *Registry) {
	r.register(ToolDef{
		Name:        "runecho_context_compile",
		Description: "Compile a fresh IR+task context block for the workspace (same output as ai-context).",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]Property{
				"workspace": {Type: "string", Description: "Project root (overrides server default)"},
				"budget":    {Type: "string", Description: "Approximate token budget (default 4000)"},
				"providers": {Type: "string", Description: "Comma-separated provider list (default: all)"},
				"prompt":    {Type: "string", Description: "User prompt for relevance scoring"},
				"session":   {Type: "string", Description: "Session ID"},
			},
		},
		Handler: handleContextCompile,
	})
}

func handleContextCompile(workspace string, params json.RawMessage) (string, error) {
	var p struct {
		Budget    int    `json:"budget"`
		Providers string `json:"providers"`
		Prompt    string `json:"prompt"`
		Session   string `json:"session"`
	}
	json.Unmarshal(params, &p) //nolint:errcheck

	budget := p.Budget
	if budget <= 0 {
		budget = 4000
	}

	req := ctx.Request{
		Workspace: workspace,
		SessionID: p.Session,
		Prompt:    p.Prompt,
		Budget:    budget,
	}

	var providerList []string
	if p.Providers != "" {
		for _, name := range strings.Split(p.Providers, ",") {
			name = strings.TrimSpace(name)
			if name != "" {
				providerList = append(providerList, name)
			}
		}
	}

	compiler := ctx.NewCompiler()
	output, err := compiler.Compile(req, providerList)
	if err != nil {
		return "", fmt.Errorf("compile: %w", err)
	}

	data, err := json.Marshal(map[string]any{"context": output, "length": len(output)})
	if err != nil {
		return "", err
	}
	return string(data), nil
}
