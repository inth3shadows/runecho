package session

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
	"unicode/utf8"
)

// rawEntry is the outer envelope of each JSONL line.
type rawEntry struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	Timestamp  string          `json:"timestamp"`
	SessionID  string          `json:"sessionId"`
	IsSidechain bool           `json:"isSidechain"`
	DurationMs int64           `json:"durationMs"`
	Message    json.RawMessage `json:"message"`
}

// rawMessage is the content of an assistant message.
type rawMessage struct {
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   struct {
		InputTokens     int64 `json:"input_tokens"`
		OutputTokens    int64 `json:"output_tokens"`
		CacheReadTokens int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// rawBlock is a content block within an assistant message.
type rawBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type filePathInput struct {
	FilePath string `json:"file_path"`
}

type bashInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// Parse reads a Claude Code JSONL session log and extracts ground-truth facts.
func Parse(path string) (*Fact, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fact := &Fact{Models: make(map[string]int)}

	filesEdited := make(map[string]bool)
	filesCreated := make(map[string]bool)
	commandsSeen := make(map[string]bool)
	var assistantTexts []string

	scanner := bufio.NewScanner(f)
	// JSONL lines can be large (full tool outputs in assistant messages)
	const maxLine = 10 * 1024 * 1024
	scanner.Buffer(make([]byte, 64*1024), maxLine)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry rawEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		// Track time range
		if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
			if fact.StartTime.IsZero() || ts.Before(fact.StartTime) {
				fact.StartTime = ts
			}
			if ts.After(fact.EndTime) {
				fact.EndTime = ts
			}
		}

		if fact.SessionID == "" && entry.SessionID != "" {
			fact.SessionID = entry.SessionID
		}

		switch entry.Type {
		case "assistant":
			if len(entry.Message) == 0 {
				continue
			}
			var msg rawMessage
			if err := json.Unmarshal(entry.Message, &msg); err != nil {
				continue
			}

			// Track model usage (count all turns including sidechains — they cost money)
			if msg.Model != "" {
				fact.Models[msg.Model]++
			}

			// Accumulate tokens
			fact.InputTokens += msg.Usage.InputTokens
			fact.OutputTokens += msg.Usage.OutputTokens
			fact.CacheReads += msg.Usage.CacheReadTokens

			// Parse content blocks
			var blocks []rawBlock
			if err := json.Unmarshal(msg.Content, &blocks); err != nil {
				continue
			}

			var turnText []byte
			for _, block := range blocks {
				switch block.Type {
				case "text":
					turnText = append(turnText, block.Text...)

				case "tool_use":
					switch block.Name {
					case "Edit":
						var inp filePathInput
						if err := json.Unmarshal(block.Input, &inp); err == nil && inp.FilePath != "" {
							filesEdited[inp.FilePath] = true
						}

					case "Write":
						var inp filePathInput
						if err := json.Unmarshal(block.Input, &inp); err == nil && inp.FilePath != "" {
							filesCreated[inp.FilePath] = true
						}

					case "Bash":
						var inp bashInput
						if err := json.Unmarshal(block.Input, &inp); err == nil {
							label := inp.Description
							if label == "" {
								label = truncate(inp.Command, 80)
							}
							if label != "" && !commandsSeen[label] && len(fact.Commands) < 15 {
								commandsSeen[label] = true
								fact.Commands = append(fact.Commands, label)
							}
						}
					}
				}
			}

			// Collect assistant text for haiku context (skip sidechains)
			if !entry.IsSidechain {
				if text := string(turnText); text != "" {
					assistantTexts = append(assistantTexts, truncate(text, 600))
				}
				fact.TurnCount++
			}

		case "system":
			if entry.Subtype == "turn_duration" {
				fact.TotalDurationMs += entry.DurationMs
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Build file lists: Write takes precedence over Edit for the same path
	for p := range filesCreated {
		fact.FilesCreated = append(fact.FilesCreated, p)
	}
	for p := range filesEdited {
		if !filesCreated[p] {
			fact.FilesEdited = append(fact.FilesEdited, p)
		}
	}

	// Last 3 assistant text blocks for haiku context
	if len(assistantTexts) > 3 {
		fact.AssistantText = assistantTexts[len(assistantTexts)-3:]
	} else {
		fact.AssistantText = assistantTexts
	}

	fact.Model = dominantModel(fact.Models)

	return fact, nil
}

func dominantModel(models map[string]int) string {
	best, bestCount := "", 0
	for m, c := range models {
		if c > bestCount {
			best, bestCount = m, c
		}
	}
	return best
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for i := max; i > 0; i-- {
		if utf8.RuneStart(s[i]) {
			return s[:i] + "..."
		}
	}
	return s[:max] + "..."
}
