package governor

// Input is the JSON payload Claude Code sends to UserPromptSubmit hooks.
type Input struct {
	SessionID string `json:"session_id"`
	Prompt    string `json:"prompt"`
	CWD       string `json:"cwd"`
}

// Route represents the model routing decision.
type Route string

const (
	RouteHaiku    Route = "haiku"
	RouteSonnet   Route = "sonnet"
	RouteOpus     Route = "opus"
	RoutePipeline Route = "pipeline"
)

// routeText is the injection text emitted to Claude's context per route.
var routeText = map[Route]string{
	RouteOpus: `MODEL ROUTER: Deep reasoning task. Delegate to an opus subagent (Task tool, model: "opus") for analysis. Use haiku subagents for any file gathering opus needs. Then implement opus's recommendations yourself (Sonnet).`,

	RoutePipeline: `MODEL ROUTER — MULTI-STEP PIPELINE:
  This task has multiple phases. Use this pipeline:
  1. EXPLORE (haiku subagents): Search codebase, read files, gather context. Launch in parallel where possible.
  2. REASON (opus subagent): Feed exploration results into a single opus subagent for architecture/design decisions. Opus returns the plan and key decisions.
  3. IMPLEMENT (you, Sonnet): Write the code yourself based on Opus's design. You have the exploration results and the design in your context.
  This maximizes quality while minimizing cost. Opus only processes the distilled context, not raw files.`,

	RouteHaiku: `MODEL ROUTER: Lightweight task. Delegate to a haiku subagent (Task tool, model: "haiku"). Only synthesize or review the result yourself if needed.`,

	RouteSonnet: "", // Sonnet direct — no message needed
}

// FaultSignal names match the M1 fault taxonomy in fault-emitter.sh.
type FaultSignal string

const (
	FaultTurnFatigue    FaultSignal = "TURN_FATIGUE"
	FaultCostFatigue    FaultSignal = "COST_FATIGUE"
	FaultOpusBlocked    FaultSignal = "OPUS_BLOCKED"
	FaultHookFailure    FaultSignal = "HOOK_FAILURE"
	FaultHookSlow       FaultSignal = "HOOK_SLOW"
	FaultHookFailed     FaultSignal = "HOOK_FAILED"
	FaultWindowPressure FaultSignal = "WINDOW_PRESSURE"
)
