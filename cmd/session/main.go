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
//        ai-session validate [path]
//        ai-session review [--trace] [--n=5] [--force] [project-dir]
//
// Reads the Claude Code JSONL session log and writes a structured handoff.md.
// Session ID defaults to .ai/checkpoint.json. Output defaults to .ai/handoff.md.
// Set RUNECHO_CLASSIFIER_KEY to enable haiku narrative summarization.
func main() {
	if len(os.Args) > 1 && os.Args[1] == "validate" {
		runValidate(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "review" {
		runReview(os.Args[2:])
		return
	}

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

	// IR diff + current hash (best-effort — silent on failure)
	irDiff := runIRDiff(absRoot)
	irHash := readIRHash(absRoot)

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
	if err := session.WriteHandoff(outPath, fact, summary, irDiff, irHash); err != nil {
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

func readIRHash(root string) string {
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
	if len(ir.RootHash) > 8 {
		return ir.RootHash[:8]
	}
	return ir.RootHash
}

// runValidate checks a handoff.md for structural correctness.
// Exit 0 = valid, 1 = warnings, 2 = fatal errors.
func runValidate(args []string) {
	path := ".ai/handoff.md"
	if len(args) > 0 {
		path = args[0]
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ Cannot read %s: %v\n", path, err)
		os.Exit(2)
	}
	content := string(data)
	lines := strings.Split(content, "\n")

	exitCode := 0
	warn := func(format string, a ...interface{}) {
		fmt.Printf("✗ "+format+"\n", a...)
		if exitCode < 1 {
			exitCode = 1
		}
	}
	fatal := func(format string, a ...interface{}) {
		fmt.Printf("✗ "+format+"\n", a...)
		exitCode = 2
	}
	ok := func(format string, a ...interface{}) {
		fmt.Printf("✓ "+format+"\n", a...)
	}

	// Check 1: front-matter present
	if len(lines) < 3 || lines[0] != "---" {
		fatal("Front-matter missing (file must start with ---)")
	} else {
		fmEnd := -1
		for i := 1; i < len(lines); i++ {
			if lines[i] == "---" {
				fmEnd = i
				break
			}
		}
		if fmEnd < 0 {
			fatal("Front-matter not closed (missing closing ---)")
		} else {
			ok("Front-matter present (lines 1–%d)", fmEnd+1)

			// Check 2: required keys
			fmBlock := strings.Join(lines[1:fmEnd], "\n")
			required := []string{"session_id", "ir_hash", "status", "next_session_intent"}
			allKeys := true
			for _, k := range required {
				if !strings.Contains(fmBlock, k+":") {
					warn("Required front-matter key missing: %s", k)
					allKeys = false
				}
			}
			if allKeys {
				ok("Required keys: %s", strings.Join(required, ", "))
			}

			// Check 3: ir_hash drift
			currentHash := readIRHash(".")
			if currentHash != "" {
				fmHash := extractFMValue(fmBlock, "ir_hash")
				fmHash = strings.Trim(fmHash, `"'`)
				if fmHash == "" {
					warn("ir_hash missing from front-matter")
				} else if fmHash != currentHash {
					warn("IR hash mismatch: handoff=%s, current=%s", fmHash, currentHash)
				} else {
					ok("IR hash matches current (%s)", currentHash)
				}
			}

			// Check 4: tasks_touched IDs exist in tasks.json
			tasksTouchedRaw := extractFMValue(fmBlock, "tasks_touched")
			if tasksTouchedRaw != "" && tasksTouchedRaw != "[]" {
				checkTasksTouched(tasksTouchedRaw, warn, ok)
			}
		}
	}

	// Check 5: required markdown sections
	requiredSections := []string{"# Session Handoff", "## Files Changed", "## Accomplished", "## Next Steps"}
	allSections := true
	for _, sec := range requiredSections {
		if !strings.Contains(content, sec) {
			warn("Required section missing: %s", sec)
			allSections = false
		}
	}
	if allSections {
		ok("Required sections present")
	}

	os.Exit(exitCode)
}

func extractFMValue(fmBlock, key string) string {
	for _, line := range strings.Split(fmBlock, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		}
	}
	return ""
}

func checkTasksTouched(raw string, warn, ok func(string, ...interface{})) {
	tasksPath := ".ai/tasks.json"
	data, err := os.ReadFile(tasksPath)
	if err != nil {
		// tasks.json absent — skip cross-check
		return
	}
	var tasks struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &tasks); err != nil {
		return
	}
	known := make(map[string]bool)
	for _, t := range tasks.Tasks {
		known[t.ID] = true
	}
	// Raw is like ["1","2"] — simple string extraction
	raw = strings.Trim(raw, "[]")
	refs := strings.Split(raw, ",")
	missing := false
	for _, r := range refs {
		id := strings.Trim(strings.TrimSpace(r), `"'`)
		if id != "" && !known[id] {
			warn("tasks_touched references unknown task ID: %s", id)
			missing = true
		}
	}
	if !missing {
		ok("tasks_touched IDs valid")
	}
}

// runReview prints a session review report for the given project directory.
func runReview(args []string) {
	fs := flag.NewFlagSet("ai-session review", flag.ExitOnError)
	trace := fs.Bool("trace", false, "group output by task across sessions")
	n := fs.Int("n", 5, "session window for cost trend")
	force := fs.Bool("force", false, "print even if not actionable")
	fs.Parse(args)

	root := "."
	if rest := fs.Args(); len(rest) > 0 {
		root = rest[0]
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fatalf("cannot resolve root %q: %v", root, err)
	}

	entries, err := session.ReadProgress(absRoot)
	if err != nil {
		fatalf("reading progress: %v", err)
	}
	faults, err := session.ReadFaults(absRoot)
	if err != nil {
		fatalf("reading faults: %v", err)
	}
	tasks, err := session.ReadReviewTasks(absRoot)
	if err != nil {
		// non-fatal
		fmt.Fprintf(os.Stderr, "ai-session review: warning: reading tasks: %v\n", err)
	}

	// Apply session window to entries
	if *n > 0 && len(entries) > *n {
		entries = entries[len(entries)-*n:]
	}

	report := session.BuildReport(entries, faults, tasks)

	if !*force && !session.IsActionable(report) {
		// Silent when nothing worth surfacing
		return
	}

	fmt.Println(session.FormatReport(report, *trace))
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
