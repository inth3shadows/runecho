package pipeline

import (
	"fmt"
	"strings"
)

// modelLabel returns the display label used in pipeline injection text.
func modelLabel(model string) string {
	switch model {
	case "haiku":
		return "haiku subagents"
	case "opus":
		return "opus subagent"
	case "sonnet":
		return "you, Sonnet"
	default:
		return model
	}
}

// RenderText produces the MODEL ROUTER injection text for a pipeline.
// For default.yaml the output is equivalent to the hardcoded routeText[RoutePipeline].
func RenderText(p *Pipeline) string {
	var b strings.Builder
	b.WriteString("MODEL ROUTER — MULTI-STEP PIPELINE:\n")
	b.WriteString("  This task has multiple phases. Use this pipeline:\n")
	for i, s := range p.Stages {
		desc := s.Description
		if desc == "" {
			desc = fmt.Sprintf("Run %s stage.", s.ID)
		}
		fmt.Fprintf(&b, "  %d. %s (%s): %s\n", i+1, strings.ToUpper(s.ID), modelLabel(s.Model), desc)
	}
	b.WriteString("  This maximizes quality while minimizing cost. Opus only processes the distilled context, not raw files.")
	return b.String()
}
