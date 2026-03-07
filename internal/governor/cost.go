package governor

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Per-model pricing (USD per token).
var modelRates = []struct {
	match  string
	input  float64
	output float64
	cache  float64
}{
	{"haiku", 0.80 / 1e6, 4.0 / 1e6, 0.08 / 1e6},
	{"opus", 15.0 / 1e6, 75.0 / 1e6, 1.5 / 1e6},
	{"sonnet", 3.0 / 1e6, 15.0 / 1e6, 0.30 / 1e6}, // default / sonnet
}

type sessionUsage struct {
	Type    string `json:"type"`
	Message *struct {
		Model string `json:"model"`
		Usage *struct {
			InputTokens            int `json:"input_tokens"`
			OutputTokens           int `json:"output_tokens"`
			CacheReadInputTokens   int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// SessionCost scans the Claude Code JSONL log for the session and returns
// cumulative cost in USD using per-model token rates.
// Returns 0.0 if the log can't be found or parsed.
func SessionCost(sessionID string) float64 {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}

	// Find JSONL file under ~/.claude/projects/**/<session_id>.jsonl
	// Also check ~/.claude-work/projects/ if CLAUDE_CONFIG_DIR is set.
	dirs := []string{filepath.Join(home, ".claude", "projects")}
	if cfg := os.Getenv("CLAUDE_CONFIG_DIR"); cfg != "" {
		dirs = append(dirs, filepath.Join(cfg, "projects"))
	}

	var jsonlFile string
	for _, dir := range dirs {
		matches, _ := filepath.Glob(filepath.Join(dir, "*", sessionID+".jsonl"))
		if len(matches) > 0 {
			jsonlFile = matches[0]
			break
		}
	}
	if jsonlFile == "" {
		return 0
	}

	f, err := os.Open(jsonlFile)
	if err != nil {
		return 0
	}
	defer f.Close()

	var total float64
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1 MB per line
	for scanner.Scan() {
		var entry sessionUsage
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" || entry.Message == nil || entry.Message.Usage == nil {
			continue
		}
		u := entry.Message.Usage
		rates := ratesForModel(entry.Message.Model)
		total += float64(u.InputTokens)*rates.input +
			float64(u.OutputTokens)*rates.output +
			float64(u.CacheReadInputTokens)*rates.cache
	}
	return math.Round(total*10000) / 10000 // 4 decimal places
}

func ratesForModel(model string) struct{ input, output, cache float64 } {
	lower := strings.ToLower(model)
	for _, r := range modelRates {
		if strings.Contains(lower, r.match) {
			return struct{ input, output, cache float64 }{r.input, r.output, r.cache}
		}
	}
	// Default to sonnet rates
	return struct{ input, output, cache float64 }{3.0 / 1e6, 15.0 / 1e6, 0.30 / 1e6}
}

// CostLevel returns "stop", "strong", "warn", or "ok".
func CostLevel(cost float64) string {
	switch {
	case cost >= 8.00:
		return "stop"
	case cost >= 3.00:
		return "strong"
	case cost >= 1.00:
		return "warn"
	default:
		return "ok"
	}
}

// CostCents returns cost as integer cents (for fault signal value).
func CostCents(cost float64) int {
	return int(math.Round(cost * 100))
}

// MaxContextTokens is Claude's context window size.
const MaxContextTokens = 200_000

// windowPressureThreshold is the fraction at which WINDOW_PRESSURE fires.
const windowPressureThreshold = 0.90

// TokensUsed returns cumulative input+output token count for the session.
// Does not count cache reads (those are replays, not new context).
func TokensUsed(sessionID string) int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}

	dirs := []string{filepath.Join(home, ".claude", "projects")}
	if cfg := os.Getenv("CLAUDE_CONFIG_DIR"); cfg != "" {
		dirs = append(dirs, filepath.Join(cfg, "projects"))
	}

	var jsonlFile string
	for _, dir := range dirs {
		matches, _ := filepath.Glob(filepath.Join(dir, "*", sessionID+".jsonl"))
		if len(matches) > 0 {
			jsonlFile = matches[0]
			break
		}
	}
	if jsonlFile == "" {
		return 0
	}

	f, err := os.Open(jsonlFile)
	if err != nil {
		return 0
	}
	defer f.Close()

	var total int
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var entry sessionUsage
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" || entry.Message == nil || entry.Message.Usage == nil {
			continue
		}
		u := entry.Message.Usage
		total += u.InputTokens + u.OutputTokens
	}
	return total
}

// WindowPressure returns (tokensUsed, pressure). pressure is true when
// tokensUsed >= MaxContextTokens * windowPressureThreshold.
func WindowPressure(sessionID string) (int, bool) {
	used := TokensUsed(sessionID)
	threshold := int(float64(MaxContextTokens) * windowPressureThreshold)
	return used, used >= threshold
}
