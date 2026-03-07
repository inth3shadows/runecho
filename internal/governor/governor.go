package governor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inth3shadows/runecho/internal/pipeline"
)

const (
	warnAt      = 15
	strongWarnAt = 25
	stopAt      = 35
)

// Run executes the full governor + model router logic.
// Reads JSON from inputJSON, returns the text to inject into Claude's context.
// Errors are non-fatal (governor must not block the session).
func Run(inputJSON []byte, classifierKey string) string {
	var input Input
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		return ""
	}
	if input.SessionID == "" {
		input.SessionID = "unknown"
	}
	cwd := input.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	stateDir := StateDir()

	// --- Turn Counter ---
	count, _ := IncrementTurn(stateDir, input.SessionID)

	// --- Session Cost ---
	cost := SessionCost(input.SessionID)
	costFmt := FormatCost(cost)
	costLevel := CostLevel(cost)

	// --- Context Window Pressure ---
	windowPressureMsg := ""
	if tokensUsed, pressure := WindowPressure(input.SessionID); pressure {
		pct := (tokensUsed * 100) / MaxContextTokens
		EmitFault(FaultWindowPressure, pct,
			fmt.Sprintf("%d/%d tokens used", tokensUsed, MaxContextTokens),
			cwd, input.SessionID)
		windowPressureMsg = fmt.Sprintf(
			"SESSION GOVERNOR [window]: Context window at %d%% capacity (%d/%d tokens). Consider /compact or starting a new session.",
			pct, tokensUsed, MaxContextTokens,
		)
	}

	// --- Pending Fault Signals ---
	irDeltaOutput := buildPendingFaultOutput(stateDir, input.SessionID)

	// --- Session Warning ---
	warningOutput, warningLevel := buildWarning(count, costLevel, costFmt, cost, cwd, input.SessionID, stateDir)

	// --- Model Router ---
	route := classifyOrRoute(input.Prompt, classifierKey, stateDir)

	// --- Cost-Based Routing Cap ---
	opusBlockedMsg := ""
	if costLevel == "stop" && (route == RouteOpus || route == RoutePipeline) {
		opusBlockedMsg = fmt.Sprintf(
			"MODEL ROUTER: Opus/pipeline blocked — session cost %s exceeds limit. Handling directly as Sonnet. Start a new session to re-enable opus routing.",
			costFmt,
		)
		EmitFault(FaultOpusBlocked, CostCents(cost),
			fmt.Sprintf("cost %s exceeds limit", costFmt), cwd, input.SessionID)
		route = RouteSonnet
	}
	_ = warningLevel // used only for fault emission above

	// --- Persist Route ---
	_ = WriteRoute(stateDir, input.SessionID, route)

	// --- Assemble Output ---
	return assembleOutput(warningOutput, irDeltaOutput, windowPressureMsg, opusBlockedMsg, getRouteText(cwd, route))
}

func buildPendingFaultOutput(stateDir, sessionID string) string {
	lines := ReadPendingFaults(stateDir, sessionID)
	if len(lines) == 0 {
		return ""
	}

	var parts []string
	for _, line := range lines {
		var entry struct {
			Signal  string `json:"signal"`
			Context string `json:"context"`
			Value   int    `json:"value"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		switch entry.Signal {
		case "IR_DRIFT":
			parts = append(parts, "IR DELTA (since session-start):\n"+entry.Context)
		case "HALLUCINATION":
			parts = append(parts, fmt.Sprintf("CLAIM MISMATCH [%d ref(s)]: %s", entry.Value, entry.Context))
		}
	}
	return strings.Join(parts, "\n")
}

func buildWarning(count int, costLevel, costFmt string, cost float64, cwd, sessionID, stateDir string) (string, string) {
	level := "ok"
	if count >= stopAt || costLevel == "stop" {
		level = "stop"
	} else if count >= strongWarnAt || costLevel == "strong" {
		level = "strong"
	} else if count >= warnAt || costLevel == "warn" {
		level = "warn"
	}

	costCents := CostCents(cost)
	var msg string

	switch level {
	case "stop":
		irHash := readIRHash(cwd)
		msg = fmt.Sprintf(
			"SESSION GOVERNOR [turn %d, %s]: Session limit reached — context degrading, cost accumulating. Wrap up and start a new session.\nACTION REQUIRED: Write session handoff now.\n  > Create .ai/handoff.md using the canonical format (accomplished, decisions, in-progress, blocked, next steps).\n  > IR snapshot hash: %s",
			count, costFmt, irHash,
		)
		if costLevel == "stop" {
			EmitFault(FaultCostFatigue, costCents, fmt.Sprintf("stop — session cost %s", costFmt), cwd, sessionID)
		} else {
			EmitFault(FaultTurnFatigue, count, fmt.Sprintf("stop — turn %d reached limit", count), cwd, sessionID)
		}

	case "strong":
		msg = fmt.Sprintf(
			"SESSION GOVERNOR [turn %d, %s]: Session is expensive. Finish current task, suggest /compact or new session.",
			count, costFmt,
		)
		if costLevel == "strong" {
			EmitFault(FaultCostFatigue, costCents, fmt.Sprintf("strong — session cost %s", costFmt), cwd, sessionID)
		} else {
			EmitFault(FaultTurnFatigue, count, fmt.Sprintf("strong — turn %d", count), cwd, sessionID)
		}

	case "warn":
		msg = fmt.Sprintf(
			"SESSION GOVERNOR [turn %d, %s]: Cost rising. Consider wrapping up soon or /compact.",
			count, costFmt,
		)
		if costLevel == "warn" {
			EmitFault(FaultCostFatigue, costCents, fmt.Sprintf("warn — session cost %s", costFmt), cwd, sessionID)
		} else {
			EmitFault(FaultTurnFatigue, count, fmt.Sprintf("warn — turn %d", count), cwd, sessionID)
		}
	}

	return msg, level
}

func classifyOrRoute(prompt, classifierKey, stateDir string) Route {
	if classifierKey != "" {
		if route, _ := Classify(prompt, classifierKey, stateDir); route != "" {
			return route
		}
	}
	return RegexRoute(prompt)
}

func readIRHash(cwd string) string {
	data, err := os.ReadFile(filepath.Join(cwd, ".ai", "ir.json"))
	if err != nil {
		return "unknown"
	}
	var ir struct {
		RootHash string `json:"root_hash"`
	}
	if err := json.Unmarshal(data, &ir); err != nil || ir.RootHash == "" {
		return "unknown"
	}
	if len(ir.RootHash) > 12 {
		return ir.RootHash[:12]
	}
	return ir.RootHash
}

// getRouteText returns the injection text for the given route.
// For RoutePipeline it loads the pipeline definition from disk and renders it.
// Falls back to the hardcoded routeText[RoutePipeline] if loading fails (never blocks the session).
func getRouteText(cwd string, route Route) string {
	if route == RoutePipeline {
		if p, err := pipeline.Load(cwd); err == nil {
			return pipeline.RenderText(p)
		}
	}
	return routeText[route]
}

func assembleOutput(warning, irDelta, windowPressure, opusBlocked, route string) string {
	var parts []string
	if warning != "" {
		parts = append(parts, warning)
	}
	if windowPressure != "" {
		parts = append(parts, windowPressure)
	}
	if irDelta != "" {
		parts = append(parts, irDelta)
	}
	if opusBlocked != "" {
		parts = append(parts, opusBlocked)
	} else if route != "" {
		parts = append(parts, route)
	}
	return strings.Join(parts, "\n\n")
}
