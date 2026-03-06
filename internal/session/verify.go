package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VerifyEntry records one verify run for a task in a session.
// Stored append-only in .ai/results.jsonl, deduplicated by TaskID+SessionID.
type VerifyEntry struct {
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id"`
	Timestamp string `json:"timestamp"`
	Cmd       string `json:"cmd"`
	Passed    bool   `json:"passed"`
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output"` // max 500 chars, combined stderr+stdout
}

// AppendVerify appends e to <root>/.ai/results.jsonl.
// Idempotent: skips if task_id+session_id already present.
func AppendVerify(root string, e VerifyEntry) error {
	existing, err := ReadVerify(root)
	if err != nil {
		return err
	}
	for _, ex := range existing {
		if ex.TaskID == e.TaskID && ex.SessionID == e.SessionID {
			return nil
		}
	}

	path := filepath.Join(root, ".ai", "results.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%s\n", data)
	return err
}

// ReadVerify reads all entries from <root>/.ai/results.jsonl.
// Returns nil (not error) if file doesn't exist.
func ReadVerify(root string) ([]VerifyEntry, error) {
	path := filepath.Join(root, ".ai", "results.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []VerifyEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e VerifyEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			fmt.Fprintf(os.Stderr, "verify: skipping malformed line: %v\n", err)
			continue
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}
