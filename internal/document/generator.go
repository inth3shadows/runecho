package document

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	documentModel = "claude-haiku-4-5-20251001"
	apiURL        = "https://api.anthropic.com/v1/messages"
	apiVersion    = "2023-06-01"
)

var systemPrompt = `You are a technical documentation writer. Rules:
- Output raw markdown only. No surrounding code fences.
- Use only the provided context. Do not invent features, flags, or behaviors.
- Be specific: real command names, real file paths, real flag names from the source.
- Be concise. No filler sentences. No "Getting Started is easy!" style padding.
- If information is insufficient for a section, write a TODO placeholder rather than guessing.`

// createBudgets maps docType → maxTokens for create mode.
var createBudgets = map[string]int{
	"README":    1000,
	"TECHNICAL": 2000,
	"USAGE":     2000,
}

// updateBudget returns a token budget for update mode sized to the existing doc.
// Estimate: existing bytes / 4 chars-per-token, plus 30% buffer, floor 800, cap 8000 (haiku max is 8192).
func updateBudget(existing string) int {
	if len(existing) == 0 {
		return 800
	}
	budget := len(existing)/4*13/10 // × 1.3
	if budget < 800 {
		budget = 800
	}
	if budget > 8000 {
		budget = 8000
	}
	return budget
}

// Generate produces docs for all configured DocTypes. Parallel when >1 type.
func Generate(ctx *ProjectContext, statuses map[string]DocStatus, apiKey string) (DocSet, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("RUNECHO_CLASSIFIER_KEY not set")
	}

	result := make(DocSet, len(ctx.DocTypes))

	if len(ctx.DocTypes) > 1 {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, fn := range ctx.DocTypes {
			fn := fn
			dt := strings.TrimSuffix(fn, ".md")
			st := statuses[fn]
			if st.DirtyGit {
				fmt.Fprintf(os.Stderr, "ai-document: warning: %s has uncommitted changes, skipping\n", fn)
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				content, err := generateDoc(ctx, dt, st, apiKey)
				if err != nil {
					fmt.Fprintf(os.Stderr, "ai-document: warning: %s generation failed: %v\n", dt, err)
					return
				}
				mu.Lock()
				result[fn] = content
				mu.Unlock()
			}()
		}
		wg.Wait()
	} else if len(ctx.DocTypes) == 1 {
		fn := ctx.DocTypes[0]
		dt := strings.TrimSuffix(fn, ".md")
		st := statuses[fn]
		if !st.DirtyGit {
			content, err := generateDoc(ctx, dt, st, apiKey)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ai-document: warning: %s generation failed: %v\n", dt, err)
			} else {
				result[fn] = content
			}
		}
	}

	return result, nil
}

func generateDoc(ctx *ProjectContext, docType string, status DocStatus, apiKey string) (string, error) {
	prompt := buildPrompt(ctx, docType, status)
	var budget int
	if status.RunMode == RunModeUpdate {
		budget = updateBudget(ExistingDoc(ctx, docType))
	} else {
		budget = createBudgets[docType]
		if budget == 0 {
			budget = 800
		}
	}
	return callHaiku(systemPrompt, prompt, budget, apiKey)
}

func buildPrompt(ctx *ProjectContext, docType string, status DocStatus) string {
	var sb strings.Builder
	if status.RunMode == RunModeUpdate {
		buildUpdatePrompt(&sb, ctx, docType)
	} else {
		buildCreatePrompt(&sb, ctx, docType)
	}
	return sb.String()
}

func buildCreatePrompt(sb *strings.Builder, ctx *ProjectContext, docType string) {
	sb.WriteString(fmt.Sprintf("PROJECT: %s (%s)\n", ctx.Name, TypeString(ctx.Type)))
	if ctx.Description != "" {
		sb.WriteString(fmt.Sprintf("DESCRIPTION: %s\n", ctx.Description))
	}

	sb.WriteString("\nFILE TREE:\n")
	sb.WriteString(FormatFileTree(ctx.FileTree))
	sb.WriteString("\n")

	if len(ctx.SourceFiles) > 0 {
		sb.WriteString("\nKEY SOURCE FILES:\n")
		for _, sf := range ctx.SourceFiles {
			sb.WriteString(fmt.Sprintf("--- %s ---\n%s\n", sf.Path, sf.Content))
		}
	}

	if ctx.RecentCommits != "" {
		sb.WriteString(fmt.Sprintf("\nGIT LOG (last 10):\n%s\n", ctx.RecentCommits))
	}

	companions := companionDocInfo(ctx, docType)

	switch docType {
	case "README":
		sb.WriteString(`
Generate README.md.

Structure:
# {Project Name}

One sentence: what this is and why it exists.

## Quick Start
Exact install/build commands for a new user. Use real paths and commands from the source.

## What It Does
- Bullet per key capability. Each bullet: what the feature does, not how.

## Commands
Table or list of CLI commands with 1-line description each. Only include commands evident in the source.
` + companions + `
Keep it under 80 lines. No badges, no shields, no license section unless evident in source.`)

	case "TECHNICAL":
		sb.WriteString(`
Generate TECHNICAL.md.

Structure:
# Technical Architecture

## Overview
2-3 sentences: what the system does and its design philosophy.

## Architecture
ASCII diagram showing major components and data flow. Use box-drawing characters.
Example format:
` + "```" + `
[component] ---> [component] ---> [output]
     |                |
     v                v
  [store]         [store]
` + "```" + `

## Components
For each package/module in the source tree:
- **{name}** — 1-line responsibility. Key types/functions if evident.

## Data Flow
Numbered steps: what happens when the primary operation runs. Be specific about file paths and data formats.

## Configuration
Table of config files, env vars, and their purposes. Only what's evident in source.

## Dependencies
List external dependencies with version and purpose. Read from go.mod/package.json/etc.

Keep it under 150 lines. Prioritize accuracy over completeness.`)

	case "USAGE":
		sb.WriteString(`
Generate USAGE.md.

Structure:
# Usage Guide

## Prerequisites
Exact versions, tools, env vars required. Be specific (e.g., "Go 1.24+", not "Go").

## Installation
Step-by-step commands. Copy-pasteable. Include build commands if applicable.

## Commands
For EACH command binary or subcommand evident in source:
### {command name}
Purpose (1 line).
` + "```" + `
{command} [flags] [args]
` + "```" + `
Flags:
- ` + "`--flag`" + ` — description (default: value)

Example:
` + "```sh" + `
{real example with real args}
` + "```" + `

## Common Workflows
3-5 numbered workflows showing how commands chain together for real tasks.

## Environment Variables
Table: name, required/optional, description.

Keep it under 150 lines. Every example must use real command names and flags from the source.`)
	}
}

func buildUpdatePrompt(sb *strings.Builder, ctx *ProjectContext, docType string) {
	sb.WriteString(fmt.Sprintf("PROJECT: %s (%s)\n", ctx.Name, TypeString(ctx.Type)))

	sb.WriteString("\nCHANGES THIS SESSION:\n")
	sb.WriteString(ctx.IRDiff)
	sb.WriteString("\n")

	existing := ExistingDoc(ctx, docType)
	sb.WriteString(fmt.Sprintf("\nCURRENT %s.md:\n%s\n", docType, existing))

	companions := companionDocInfo(ctx, docType)

	switch docType {
	case "README":
		sb.WriteString(`
TASK: Update README.md to reflect the changes above.

Rules:
- Only modify sections affected by the structural changes.
- If a new command or feature was added, add it to the appropriate section.
- If a symbol was removed, remove references to it.
- Preserve all accurate existing content verbatim.
- Do not rewrite sections unrelated to the changes.
- Output the COMPLETE updated file, not a partial diff.
` + companions)

	case "TECHNICAL":
		sb.WriteString(`
TASK: Update TECHNICAL.md to reflect the changes above.

Rules:
- If new files/packages appear in the diff, add them to Components.
- If data flow changed, update the Data Flow section.
- If the architecture diagram is affected, update it.
- Preserve all accurate existing content verbatim.
- Do not rewrite sections unrelated to the changes.
- Output the COMPLETE updated file, not a partial diff.`)

	case "USAGE":
		sb.WriteString(`
TASK: Update USAGE.md to reflect the changes above.

Rules:
- If new commands or flags appear in the diff, add them with examples.
- If commands were removed, remove their sections.
- If command behavior changed, update the description and examples.
- Preserve all accurate existing content verbatim.
- Do not rewrite sections unrelated to the changes.
- Output the COMPLETE updated file, not a partial diff.`)
	}
}

// companionDocInfo returns a cross-linking hint for README prompts.
func companionDocInfo(ctx *ProjectContext, docType string) string {
	if docType != "README" {
		return ""
	}
	companions := make(map[string]bool)
	if ctx.ExistingTechnical != "" {
		companions["TECHNICAL.md"] = true
	}
	if ctx.ExistingUsage != "" {
		companions["USAGE.md"] = true
	}
	for _, dt := range ctx.DocTypes {
		if dt == "TECHNICAL.md" || dt == "USAGE.md" {
			companions[dt] = true
		}
	}
	if len(companions) == 0 {
		return ""
	}
	var names []string
	for c := range companions {
		names = append(names, c)
	}
	sort.Strings(names)
	return fmt.Sprintf("\nAdd a \"See Also\" section at the bottom linking to: %s.", strings.Join(names, ", "))
}

func callHaiku(systemP, userP string, maxTokens int, apiKey string) (string, error) {
	reqBody := map[string]interface{}{
		"model":      documentModel,
		"max_tokens": maxTokens,
		"system":     systemP,
		"messages":   []map[string]interface{}{{"role": "user", "content": userP}},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", apiVersion)
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("haiku call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errBody strings.Builder
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		errBody.Write(buf[:n])
		return "", fmt.Errorf("haiku API returned %d: %s", resp.StatusCode, errBody.String())
	}

	var apiResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(apiResp.Content) == 0 {
		return "", fmt.Errorf("empty response from haiku")
	}

	return strings.TrimSpace(apiResp.Content[0].Text), nil
}
