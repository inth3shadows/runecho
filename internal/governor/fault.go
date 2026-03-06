package governor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/inth3shadows/runecho/internal/schema"
)

// EmitFault appends a structured fault signal to {workspace}/.ai/faults.jsonl.
// Mirrors the emit_fault() function in fault-emitter.sh.
func EmitFault(signal FaultSignal, value int, ctx, workspace, sessionID string) {
	faultsFile := filepath.Join(workspace, ".ai", "faults.jsonl")
	_ = os.MkdirAll(filepath.Dir(faultsFile), 0o755)

	entry := schema.FaultEntry{
		Signal:    string(signal),
		Value:     float64(value),
		Context:   ctx,
		Workspace: workspace,
		SessionID: sessionID,
		Ts:        time.Now().UTC().Format(time.RFC3339),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}

	f, err := os.OpenFile(faultsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s\n", line)
}
