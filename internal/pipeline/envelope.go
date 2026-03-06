package pipeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inth3shadows/runecho/internal/schema"
)

// AppendEnvelope appends env to .ai/executions.jsonl.
// Idempotent: no-op if session_id already present.
func AppendEnvelope(root string, env schema.Envelope) error {
	path := filepath.Join(root, ".ai", "executions.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pipeline: envelope mkdir: %w", err)
	}

	// Idempotency guard
	if sessionExists(path, env.SessionID) {
		return nil
	}

	line, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("pipeline: envelope marshal: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("pipeline: envelope open: %w", err)
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", line)
	return err
}

// ReadEnvelopes reads all envelopes from .ai/executions.jsonl.
func ReadEnvelopes(root string) ([]schema.Envelope, error) {
	path := filepath.Join(root, ".ai", "executions.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("pipeline: read envelopes: %w", err)
	}
	defer f.Close()

	var out []schema.Envelope
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e schema.Envelope
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // skip malformed lines
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// FaultsForSession reads .ai/faults.jsonl and returns the signal names
// for entries matching sessionID.
func FaultsForSession(root, sessionID string) []string {
	path := filepath.Join(root, ".ai", "faults.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var signals []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var entry struct {
			Signal    string `json:"signal"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.SessionID == sessionID && entry.Signal != "" && !seen[entry.Signal] {
			signals = append(signals, entry.Signal)
			seen[entry.Signal] = true
		}
	}
	return signals
}

// sessionExists checks whether session_id is already present in the JSONL file.
func sessionExists(path, sessionID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	needle := `"session_id":"` + sessionID + `"`
	for sc.Scan() {
		if strings.Contains(sc.Text(), needle) {
			return true
		}
	}
	return false
}
