package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/schema"
)

// AppendProgress appends one JSONL line to <root>/.ai/progress.jsonl.
// Idempotent: skips if session_id already present in the file.
func AppendProgress(root string, e schema.ProgressEntry) error {
	ledger := filepath.Join(root, ".ai", "progress.jsonl")

	// Idempotency guard
	if f, err := os.Open(ledger); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), `"session_id":"`+e.SessionID+`"`) {
				return nil
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(ledger), 0755); err != nil {
		return err
	}

	// Fill timestamp if not set
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(e)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(ledger, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", data)
	return err
}

// AppendFault appends one JSONL line to <root>/.ai/faults.jsonl.
// Also writes to the governor pending-faults file for VERIFY_FAIL signals
// so the governor can inject the fault into Claude's context on the next turn.
func AppendFault(root string, e schema.FaultEntry) error {
	faultsFile := filepath.Join(root, ".ai", "faults.jsonl")
	if err := os.MkdirAll(filepath.Dir(faultsFile), 0755); err != nil {
		return err
	}

	if e.Ts == "" {
		e.Ts = time.Now().UTC().Format(time.RFC3339)
	}

	data, err := json.Marshal(e)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(faultsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = fmt.Fprintf(f, "%s\n", data); err != nil {
		return err
	}

	// Queue VERIFY_FAIL for context injection (mirrors fault-emitter.sh behavior)
	if e.Signal == "VERIFY_FAIL" && e.SessionID != "" {
		homeDir, _ := os.UserHomeDir()
		stateDir := filepath.Join(homeDir, ".claude", "hooks", ".governor-state")
		pendingFile := filepath.Join(stateDir, e.SessionID+".pending-faults")
		pending := map[string]interface{}{
			"signal":  e.Signal,
			"value":   e.Value,
			"context": e.Context,
		}
		pdata, _ := json.Marshal(pending)
		if pf, err := os.OpenFile(pendingFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			fmt.Fprintf(pf, "%s\n", pdata)
			pf.Close()
		}
	}

	return nil
}
