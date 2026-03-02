package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	summarizerModel = "claude-haiku-4-5-20251001"
	apiURL          = "https://api.anthropic.com/v1/messages"
	apiVersion      = "2023-06-01"
)

// Summarize calls haiku with the extracted facts to produce a narrative summary.
// Returns nil summary (no error) if apiKey is empty — caller handles factual-only mode.
func Summarize(fact *Fact, apiKey string) (*Summary, error) {
	if apiKey == "" {
		return nil, nil
	}

	prompt := buildPrompt(fact)

	reqBody := map[string]interface{}{
		"model":      summarizerModel,
		"max_tokens": 800,
		"system":     "You are a concise session summarizer for a software development AI assistant. Extract structured information from the provided session facts and transcript. Be factual and brief. Respond with valid JSON only — no markdown, no code fences.",
		"messages":   []map[string]interface{}{{"role": "user", "content": prompt}},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", apiVersion)
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("haiku call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errBody strings.Builder
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		errBody.Write(buf[:n])
		return nil, fmt.Errorf("haiku API returned %d: %s", resp.StatusCode, errBody.String())
	}

	var apiResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(apiResp.Content) == 0 {
		return nil, fmt.Errorf("empty response from haiku")
	}

	text := stripFences(strings.TrimSpace(apiResp.Content[0].Text))

	var summary Summary
	if err := json.Unmarshal([]byte(text), &summary); err != nil {
		return nil, fmt.Errorf("parse haiku JSON: %w\nraw: %s", err, text)
	}

	return &summary, nil
}

func buildPrompt(fact *Fact) string {
	var sb strings.Builder

	sb.WriteString("SESSION FACTS (ground truth from tool call logs):\n")
	sb.WriteString(fmt.Sprintf("- Turns: %d, Duration: %s\n", fact.TurnCount, FormatDuration(fact.TotalDurationMs)))
	sb.WriteString(fmt.Sprintf("- Tokens: %dk input / %dk output\n", fact.InputTokens/1000, fact.OutputTokens/1000))

	if len(fact.FilesEdited) > 0 {
		sb.WriteString(fmt.Sprintf("- Files edited: %s\n", strings.Join(fact.FilesEdited, ", ")))
	}
	if len(fact.FilesCreated) > 0 {
		sb.WriteString(fmt.Sprintf("- Files created: %s\n", strings.Join(fact.FilesCreated, ", ")))
	}
	if len(fact.Commands) > 0 {
		sb.WriteString(fmt.Sprintf("- Commands run: %s\n", strings.Join(fact.Commands, "; ")))
	}

	if len(fact.AssistantText) > 0 {
		sb.WriteString("\nLAST ASSISTANT MESSAGES (most recent last):\n")
		for _, t := range fact.AssistantText {
			sb.WriteString("---\n")
			sb.WriteString(t)
			sb.WriteString("\n")
		}
	}

	sb.WriteString(`
Respond with JSON in this exact format (no markdown, no code fences):
{
  "accomplished": ["one-line bullet per completed task, max 4"],
  "decisions": ["key decisions made with brief rationale, max 4"],
  "open": ["in-progress items or unresolved questions, max 4"],
  "next_steps": ["concrete next actions, max 4"]
}
If a section is empty, use [].`)

	return sb.String()
}

// stripFences removes markdown code fences (```json ... ```) if present.
func stripFences(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence line
	if idx := strings.Index(s, "\n"); idx >= 0 {
		s = strings.TrimSpace(s[idx+1:])
	}
	// Drop the closing fence
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

// FormatDuration formats milliseconds as a human-readable duration string.
func FormatDuration(ms int64) string {
	if ms == 0 {
		return "unknown"
	}
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}
