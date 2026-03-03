package document

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	documentModel = "claude-haiku-4-5-20251001"
	apiURL        = "https://api.anthropic.com/v1/messages"
	apiVersion    = "2023-06-01"
)

var systemPrompt = "You are a technical documentation writer. Generate clean, accurate markdown for a software project. Use only the provided context — do not invent features not evident in the source. Output raw markdown only, no surrounding code fences."

// tokenBudgets maps (docType, runMode) → maxTokens.
var tokenBudgets = map[string]map[RunMode]int{
	"README":    {RunModeCreate: 1000, RunModeUpdate: 600},
	"TECHNICAL": {RunModeCreate: 2000, RunModeUpdate: 1000},
	"USAGE":     {RunModeCreate: 2000, RunModeUpdate: 1000},
}

// Generate determines which docs to generate and calls haiku in parallel (work) or sequentially (personal).
// Returns error only if apiKey is empty.
func Generate(ctx *ProjectContext, statuses map[string]DocStatus, apiKey string) (*DocSet, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("RUNECHO_CLASSIFIER_KEY not set")
	}

	docTypes := []string{"README"}
	if ctx.Mode == ModeWork {
		docTypes = []string{"README", "TECHNICAL", "USAGE"}
	}

	results := make(map[string]string, len(docTypes))

	if ctx.Mode == ModeWork {
		// Parallel generation for work mode
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, dt := range docTypes {
			dt := dt
			fn := DocFilename(dt)
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
					fmt.Printf("warning: ai-document: %s generation failed: %v\n", dt, err)
					return
				}
				mu.Lock()
				results[dt] = content
				mu.Unlock()
			}()
		}
		wg.Wait()
	} else {
		// Sequential for personal/unknown
		fn := DocFilename("README")
		st := statuses[fn]
		if !st.DirtyGit {
			content, err := generateDoc(ctx, "README", st, apiKey)
			if err != nil {
				fmt.Printf("warning: ai-document: README generation failed: %v\n", err)
			} else {
				results["README"] = content
			}
		}
	}

	return &DocSet{
		Readme:    results["README"],
		Technical: results["TECHNICAL"],
		Usage:     results["USAGE"],
	}, nil
}

func generateDoc(ctx *ProjectContext, docType string, status DocStatus, apiKey string) (string, error) {
	prompt := buildPrompt(ctx, docType, status)
	budget := tokenBudgets[docType][status.RunMode]
	if budget == 0 {
		budget = 800
	}
	return callHaiku(systemPrompt, prompt, budget, apiKey)
}

func buildPrompt(ctx *ProjectContext, docType string, status DocStatus) string {
	var sb strings.Builder

	sectionInstructions := map[string]string{
		"README":    "Sections: project name, 1-2 sentence description, Quick Start, What It Does (bullets). If work project, add links to TECHNICAL.md and USAGE.md at the bottom.",
		"TECHNICAL": "Sections: Architecture, Components, Data Flow, Configuration, Dependencies, Troubleshooting.",
		"USAGE":     "Sections: Prerequisites, Installation, Commands (with examples), FAQ.",
	}

	if status.RunMode == RunModeUpdate {
		existing := ExistingDoc(ctx, docType)
		sb.WriteString(fmt.Sprintf("PROJECT: %s (%s)\n\n", ctx.Name, TypeString(ctx.Type)))
		sb.WriteString("CHANGES THIS SESSION (structural diff):\n")
		sb.WriteString(ctx.IRDiff)
		sb.WriteString("\n\nCURRENT ")
		sb.WriteString(docType)
		sb.WriteString(":\n")
		sb.WriteString(existing)
		sb.WriteString("\n\nUpdate the documentation above to reflect the session changes. ")
		sb.WriteString("Only modify sections affected by the changes. Preserve accurate existing content.\n")
		sb.WriteString(sectionInstructions[docType])
	} else {
		// Create mode
		sb.WriteString(fmt.Sprintf("PROJECT: %s (%s)\n", ctx.Name, TypeString(ctx.Type)))
		sb.WriteString(fmt.Sprintf("DESCRIPTION: %s\n\n", Describe(ctx)))
		sb.WriteString("FILE TREE:\n")
		sb.WriteString(FormatFileTree(ctx.FileTree))
		sb.WriteString("\n")
		if len(ctx.SourceFiles) > 0 {
			sb.WriteString("\nKEY SOURCE FILES:\n")
			for _, sf := range ctx.SourceFiles {
				sb.WriteString(fmt.Sprintf("--- %s ---\n", sf.Path))
				sb.WriteString(sf.Content)
				sb.WriteString("\n")
			}
		}
		if ctx.RecentCommits != "" {
			sb.WriteString("\nGIT LOG (last 10):\n")
			sb.WriteString(ctx.RecentCommits)
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("\nGenerate %s for this project.\n", docType))
		sb.WriteString(sectionInstructions[docType])
	}

	return sb.String()
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
