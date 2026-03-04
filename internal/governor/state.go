package governor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// StateDir returns ~/.claude/hooks/.governor-state, using CLAUDE_CONFIG_DIR if set.
func StateDir() string {
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".claude")
	}
	return filepath.Join(base, "hooks", ".governor-state")
}

// IncrementTurn reads the previous weighted turn count, adds the weight for the
// last route, writes the new count, and returns it.
//
// Weight: pipeline=5, opus=3, haiku/sonnet=1. This prevents cheap rename
// sessions and expensive opus-pipeline sessions from looking identical.
func IncrementTurn(stateDir, sessionID string) (int, error) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return 0, err
	}

	// Read last route for weight
	routeFile := filepath.Join(stateDir, sessionID+".route")
	lastRoute := "sonnet"
	if data, err := os.ReadFile(routeFile); err == nil {
		lastRoute = strings.TrimSpace(string(data))
	}
	weight := 1
	switch lastRoute {
	case "pipeline":
		weight = 5
	case "opus":
		weight = 3
	}

	// Read current count
	countFile := filepath.Join(stateDir, sessionID)
	count := 0
	if data, err := os.ReadFile(countFile); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			count = n
		}
	}
	count += weight

	// Write new count
	if err := os.WriteFile(countFile, []byte(strconv.Itoa(count)), 0o644); err != nil {
		return count, err
	}

	// Prune state files older than 1 day
	pruneStateDir(stateDir)

	return count, nil
}

// WriteRoute persists the routing decision for the next turn's weight calculation
// and for model-enforcer.sh to read.
func WriteRoute(stateDir, sessionID string, route Route) error {
	routeFile := filepath.Join(stateDir, sessionID+".route")
	return os.WriteFile(routeFile, []byte(string(route)), 0o644)
}

// ReadPendingFaults reads and clears the pending faults queue written by stop-checkpoint.sh.
// Returns lines as raw JSON strings.
func ReadPendingFaults(stateDir, sessionID string) []string {
	pendingFile := filepath.Join(stateDir, sessionID+".pending-faults")
	data, err := os.ReadFile(pendingFile)
	if err != nil {
		return nil
	}
	_ = os.Remove(pendingFile)
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func pruneStateDir(stateDir string) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(stateDir, e.Name()))
		}
	}
}

// FormatCost formats a float cost as "~$X.XX".
func FormatCost(cost float64) string {
	return fmt.Sprintf("~$%.2f", cost)
}
