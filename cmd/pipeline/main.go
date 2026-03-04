package main

// Usage:
//   ai-pipeline render [--pipeline=default] [root]
//   ai-pipeline envelope --session=<id> [--pipeline=default] [--ir-start=<hash>] [--ir-end=<hash>] [--cost=<usd>] [--duration=<ms>] [--status=complete] [root]

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/inth3shadows/runecho/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "render":
		runRender(os.Args[2:])
	case "envelope":
		runEnvelope(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "ai-pipeline: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  ai-pipeline render [--pipeline=default] [root]")
	fmt.Fprintln(os.Stderr, "  ai-pipeline envelope --session=<id> [--pipeline=default] [--ir-start=<hash>] [--ir-end=<hash>] [--cost=<usd>] [--duration=<ms>] [--status=complete] [root]")
}

func runRender(args []string) {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	pipelineName := fs.String("pipeline", "default", "pipeline name (reads .ai/pipelines/<name>.yaml)")
	fs.Parse(args)

	root := projectRoot()
	if len(fs.Args()) > 0 {
		root = fs.Args()[0]
	}

	p, err := pipeline.LoadNamed(root, *pipelineName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-pipeline render: %v\n", err)
		os.Exit(1)
	}
	if err := pipeline.Validate(p); err != nil {
		fmt.Fprintf(os.Stderr, "ai-pipeline render: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(pipeline.RenderText(p))
}

func runEnvelope(args []string) {
	fs := flag.NewFlagSet("envelope", flag.ExitOnError)
	sessionID := fs.String("session", "", "session ID (required)")
	pipelineName := fs.String("pipeline", "default", "pipeline name")
	irStart := fs.String("ir-start", "", "IR hash at session start (reads checkpoint.json if omitted)")
	irEnd := fs.String("ir-end", "", "IR hash at session end (reads ir.json if omitted)")
	costUSD := fs.Float64("cost", 0.0, "session cost in USD")
	durationMS := fs.Int64("duration", 0, "session duration in milliseconds")
	status := fs.String("status", "complete", "envelope status: complete | abandoned")
	fs.Parse(args)

	if *sessionID == "" {
		fmt.Fprintln(os.Stderr, "ai-pipeline envelope: --session is required")
		os.Exit(1)
	}

	root := projectRoot()
	if len(fs.Args()) > 0 {
		root = fs.Args()[0]
	}

	// Load pipeline to build StageResults.
	p, err := pipeline.LoadNamed(root, *pipelineName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-pipeline envelope: load pipeline: %v\n", err)
		os.Exit(1)
	}

	// Resolve ir_hash_start.
	irHashStart := *irStart
	if irHashStart == "" {
		irHashStart = readCheckpointIRHash(root)
	}

	// Resolve ir_hash_end.
	irHashEnd := *irEnd
	if irHashEnd == "" {
		irHashEnd = readIRHashEnd(root)
	}

	// Build StageResults (CostUSD = 0.0 in M5).
	stages := make([]pipeline.StageResult, len(p.Stages))
	for i, s := range p.Stages {
		stages[i] = pipeline.StageResult{
			StageID: s.ID,
			Model:   s.Model,
		}
	}

	// Collect faults for this session.
	faults := pipeline.FaultsForSession(root, *sessionID)
	if faults == nil {
		faults = []string{}
	}

	env := pipeline.Envelope{
		SessionID:   *sessionID,
		Pipeline:    *pipelineName,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		IRHashStart: irHashStart,
		IRHashEnd:   irHashEnd,
		CostUSD:     *costUSD,
		DurationMS:  *durationMS,
		Stages:      stages,
		Faults:      faults,
		Status:      *status,
	}

	if err := pipeline.AppendEnvelope(root, env); err != nil {
		fmt.Fprintf(os.Stderr, "ai-pipeline envelope: %v\n", err)
		os.Exit(1)
	}
}

func readCheckpointIRHash(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".ai", "checkpoint.json"))
	if err != nil {
		return ""
	}
	var cp struct {
		IRHash string `json:"ir_hash"`
	}
	if err := json.Unmarshal(data, &cp); err != nil {
		return ""
	}
	return cp.IRHash
}

func readIRHashEnd(root string) string {
	data, err := os.ReadFile(filepath.Join(root, ".ai", "ir.json"))
	if err != nil {
		return ""
	}
	var ir struct {
		RootHash string `json:"root_hash"`
	}
	if err := json.Unmarshal(data, &ir); err != nil {
		return ""
	}
	h := ir.RootHash
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// projectRoot walks up from CWD looking for a .ai directory, fallback to CWD.
func projectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".ai")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	cwd, _ := os.Getwd()
	return cwd
}
