package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/inth3shadows/runecho/internal/session"
)

// Usage: ai-session [--session=<id>] [--out=<path>] [project-dir]
//
// Reads the Claude Code JSONL session log and writes a structured handoff.md.
// Session ID defaults to .ai/checkpoint.json. Output defaults to .ai/handoff.md.
// Set RUNECHO_CLASSIFIER_KEY to enable haiku narrative summarization.
func main() {
	fs := flag.NewFlagSet("ai-session", flag.ExitOnError)
	sessionID := fs.String("session", "", "Claude Code session ID (default: from .ai/checkpoint.json)")
	out := fs.String("out", "", "output path for handoff.md (default: .ai/handoff.md)")
	fs.Parse(os.Args[1:])

	root := "."
	if args := fs.Args(); len(args) > 0 {
		root = args[0]
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fatalf("cannot resolve root %q: %v", root, err)
	}

	// Resolve session ID
	sid := *sessionID
	if sid == "" {
		sid = sessionIDFromCheckpoint(absRoot)
	}
	if sid == "" {
		fatalf("no session ID: pass --session=<id> or ensure .ai/checkpoint.json exists")
	}

	// Resolve output path
	outPath := *out
	if outPath == "" {
		outPath = filepath.Join(absRoot, ".ai", "handoff.md")
	}

	// Find and parse the JSONL log
	jsonlPath, err := session.FindJSONL(sid, absRoot)
	if err != nil {
		fatalf("cannot find session log: %v", err)
	}
	fmt.Fprintf(os.Stderr, "ai-session: reading %s\n", jsonlPath)

	fact, err := session.Parse(jsonlPath)
	if err != nil {
		fatalf("failed to parse session log: %v", err)
	}

	// IR diff (best-effort — silent on failure)
	irDiff := runIRDiff(absRoot)

	// Haiku narrative (best-effort — factual-only if key absent or call fails)
	apiKey := os.Getenv("RUNECHO_CLASSIFIER_KEY")
	var summary *session.Summary
	if apiKey != "" {
		fmt.Fprintln(os.Stderr, "ai-session: generating narrative via haiku...")
		summary, err = session.Summarize(fact, apiKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ai-session: warning: haiku failed: %v\n", err)
		}
	}

	// Write handoff
	if err := session.WriteHandoff(outPath, fact, summary, irDiff); err != nil {
		fatalf("failed to write handoff: %v", err)
	}
	fmt.Fprintf(os.Stderr, "ai-session: handoff written to %s\n", outPath)

	// Cost summary to stdout (for session-end.sh to surface)
	cost := session.CostEstimate(fact)
	fmt.Printf("Session %s: %d turns, ~$%.2f (%dk in / %dk out / %dk cache)\n",
		shortID(sid), fact.TurnCount, cost,
		fact.InputTokens/1000, fact.OutputTokens/1000, fact.CacheReads/1000)
}

func sessionIDFromCheckpoint(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".ai", "checkpoint.json"))
	if err != nil {
		return ""
	}
	var cp struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &cp); err != nil {
		return ""
	}
	return cp.SessionID
}

func runIRDiff(root string) string {
	cmd := exec.Command("ai-ir", "diff", "--since=session-start", "--compact", root)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ai-session: "+format+"\n", args...)
	os.Exit(1)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
