package context

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HandoffProvider injects the previous session handoff and GUPP directive.
type HandoffProvider struct{}

func (p *HandoffProvider) Name() string { return "handoff" }

func (p *HandoffProvider) Provide(req Request) (Result, error) {
	handoffFile := filepath.Join(req.Workspace, ".ai", "handoff.md")
	info, err := os.Stat(handoffFile)
	if err != nil {
		return Result{Name: p.Name()}, nil
	}

	// Staleness gate: skip if older than 7 days
	if time.Since(info.ModTime()) > 7*24*time.Hour {
		return Result{Name: p.Name()}, nil
	}

	content, err := os.ReadFile(handoffFile)
	if err != nil {
		return Result{Name: p.Name()}, nil
	}

	dateStr := info.ModTime().Format("2006-01-02")

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("PREVIOUS SESSION HANDOFF [%s]:\n", dateStr))
	sb.Write(content)

	// GUPP directive: next_session_intent from front-matter
	intent := extractFrontMatterField(string(content), "next_session_intent")
	if intent != "" {
		sb.WriteString("\nHANDOFF DIRECTIVE:\n")
		sb.WriteString(fmt.Sprintf("  Prior intent: %s\n", intent))
		sb.WriteString("  → Acknowledge this context and confirm your plan before proceeding.\n")
	}

	out := strings.TrimRight(sb.String(), "\n")
	return Result{
		Name:    p.Name(),
		Content: out,
		Tokens:  estimateTokens(out),
	}, nil
}

// extractFrontMatterField reads a YAML front-matter field from a markdown file.
// Front-matter is bounded by --- lines at the top.
func extractFrontMatterField(content, field string) string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	inFM := false
	fmCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			fmCount++
			if fmCount == 1 {
				inFM = true
				continue
			}
			break // second --- ends front-matter
		}
		if inFM {
			prefix := field + ":"
			if strings.HasPrefix(line, prefix) {
				val := strings.TrimSpace(line[len(prefix):])
				val = strings.Trim(val, `"'`)
				return val
			}
		}
	}
	return ""
}
