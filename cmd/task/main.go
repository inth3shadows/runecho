package main

// Usage:
//   ai-task add "<title>" [--blocked-by=<id>] [--scope=<glob>] [--verify=<cmd>]
//   ai-task update <id> <status>                         status ∈ {pending, in-progress, done}
//   ai-task list [--status=<s>]                          tabular; no filter = all non-done
//   ai-task next                                         first unblocked non-done task by id; exit 1 if none
//   ai-task contract [--task-id=<id>] [root]             write .ai/CONTRACT.yaml from active (or specified) task
//   ai-task verify <id> [--session=<sid>] [root]         run task.Verify, write .ai/results.jsonl; exit 0/1/2
//   ai-task sync [--quiet]                               create task from .ai/CONTRACT.yaml if not already present
//   ai-task drift-check [--session=<id>] [root]          intersect IR changes with task scopes; emit DRIFT_AFFECTED faults
//   ai-task replan <id> [root]                           print task scope + current IR diff + DRIFT_AFFECTED faults

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/contract"
	"github.com/inth3shadows/runecho/internal/schema"
	"github.com/inth3shadows/runecho/internal/session"
	"github.com/inth3shadows/runecho/internal/task"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "add":
		runAdd(os.Args[2:])
	case "update":
		runUpdate(os.Args[2:])
	case "list":
		runList(os.Args[2:])
	case "next":
		runNext()
	case "contract":
		runContract(os.Args[2:])
	case "verify":
		runVerify(os.Args[2:])
	case "sync":
		runSync(os.Args[2:])
	case "drift-check":
		runDriftCheck(os.Args[2:])
	case "replan":
		runReplan(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "ai-task: unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  ai-task add \"<title>\" [--blocked-by=<id>] [--scope=<glob>] [--verify=<cmd>]")
	fmt.Fprintln(os.Stderr, "  ai-task update <id> <status>   # pending | in-progress | done")
	fmt.Fprintln(os.Stderr, "  ai-task list [--status=<s>]")
	fmt.Fprintln(os.Stderr, "  ai-task next")
	fmt.Fprintln(os.Stderr, "  ai-task contract [--task-id=<id>] [root]")
	fmt.Fprintln(os.Stderr, "  ai-task verify <id> [--session=<sid>] [root]")
	fmt.Fprintln(os.Stderr, "  ai-task sync [--quiet]")
	fmt.Fprintln(os.Stderr, "  ai-task drift-check [--session=<id>] [root]")
	fmt.Fprintln(os.Stderr, "  ai-task replan <id> [root]")
}

// mustLoad loads the task DB from root, exiting on error.
func mustLoad(root string) task.TaskDB {
	db, err := task.Load(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-task: failed to load tasks: %v\n", err)
		os.Exit(1)
	}
	return db
}

// mustSave saves the task DB to root, exiting on error.
func mustSave(root string, db task.TaskDB) {
	if err := task.Save(root, db); err != nil {
		fmt.Fprintf(os.Stderr, "ai-task: failed to save tasks: %v\n", err)
		os.Exit(1)
	}
}

// runAdd appends a new pending task.
func runAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	blockedBy := fs.String("blocked-by", "", "ID of task that blocks this one")
	scope := fs.String("scope", "", "glob pattern(s) for allowed file paths, e.g. internal/auth/*")
	verify := fs.String("verify", "", "shell command to validate completion, e.g. go test ./internal/auth/...")
	fs.Parse(hoistFlags(args))

	title := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if title == "" {
		fmt.Fprintln(os.Stderr, "ai-task add: title required")
		os.Exit(1)
	}

	root := projectRoot()
	db := mustLoad(root)
	now := time.Now().UTC().Format(time.RFC3339)
	nextID := fmt.Sprintf("%d", task.MaxID(db.Tasks)+1)

	t := task.Task{
		ID:      nextID,
		Status:  "pending",
		Title:   title,
		Added:   now,
		Updated: now,
	}
	if *blockedBy != "" {
		t.BlockedBy = *blockedBy
	}
	if *scope != "" {
		t.Scope = *scope
	}
	if *verify != "" {
		t.Verify = *verify
	}
	db.Tasks = append(db.Tasks, t)
	db.Updated = now
	mustSave(root, db)
	fmt.Printf("Added task #%s: %s\n", nextID, title)
}

// runUpdate changes the status of a task by ID.
func runUpdate(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "ai-task update: requires <id> <status>")
		os.Exit(1)
	}
	id := args[0]
	status := args[1]
	validStatuses := map[string]bool{"pending": true, "in-progress": true, "done": true}
	if !validStatuses[status] {
		fmt.Fprintf(os.Stderr, "ai-task update: invalid status %q (use: pending, in-progress, done)\n", status)
		os.Exit(1)
	}

	root := projectRoot()
	db := mustLoad(root)
	now := time.Now().UTC().Format(time.RFC3339)
	found := false
	for i := range db.Tasks {
		if db.Tasks[i].ID == id {
			db.Tasks[i].Status = status
			db.Tasks[i].Updated = now
			found = true
			fmt.Printf("Task #%s → %s\n", id, status)
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "ai-task update: task #%s not found\n", id)
		os.Exit(1)
	}
	db.Updated = now
	mustSave(root, db)
}

// runList prints tasks in tabular form.
// Default: all non-done. --status=<s> filters to exact status.
func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	statusFilter := fs.String("status", "", "filter by status (pending, in-progress, done, all)")
	fs.Parse(args)

	db := mustLoad(projectRoot())
	fmt.Printf("%-5s  %-12s  %s\n", "ID", "STATUS", "TITLE")
	fmt.Println(strings.Repeat("-", 60))
	for _, t := range db.Tasks {
		if *statusFilter == "" || *statusFilter == "all" {
			if t.Status == "done" {
				continue
			}
		} else if t.Status != *statusFilter {
			continue
		}
		blocked := ""
		if t.BlockedBy != "" {
			blocked = fmt.Sprintf(" (blocked by #%s)", t.BlockedBy)
		}
		fmt.Printf("%-5s  %-12s  %s%s\n", t.ID, t.Status, t.Title, blocked)
	}
}

// runNext prints the first unblocked, non-done task by id order and exits 0.
// Exits 1 with no output if no such task exists.
func runNext() {
	db := mustLoad(projectRoot())

	done := make(map[string]bool)
	for _, t := range db.Tasks {
		if t.Status == "done" {
			done[t.ID] = true
		}
	}

	for _, t := range task.SortByID(db.Tasks) {
		if t.Status == "done" {
			continue
		}
		if t.BlockedBy != "" && !done[t.BlockedBy] {
			continue
		}
		fmt.Printf("#%s: %s [%s]\n", t.ID, t.Title, t.Status)
		os.Exit(0)
	}
	os.Exit(1)
}

// runContract generates .ai/CONTRACT.yaml from the active (or specified) task.
func runContract(args []string) {
	fs := flag.NewFlagSet("contract", flag.ExitOnError)
	taskID := fs.String("task-id", "", "ID of task to use (default: active task)")
	fs.Parse(hoistFlags(args))

	root := projectRoot()
	if len(fs.Args()) > 0 {
		root = fs.Args()[0]
	}

	db := mustLoad(root)

	done := make(map[string]bool)
	for _, t := range db.Tasks {
		if t.Status == "done" {
			done[t.ID] = true
		}
	}

	var t *task.Task
	if *taskID != "" {
		for i := range db.Tasks {
			if db.Tasks[i].ID == *taskID {
				t = &db.Tasks[i]
				break
			}
		}
		if t == nil {
			fmt.Fprintf(os.Stderr, "ai-task contract: task #%s not found\n", *taskID)
			os.Exit(1)
		}
	} else {
		for i := range db.Tasks {
			candidate := &db.Tasks[i]
			if candidate.Status == "done" {
				continue
			}
			if candidate.BlockedBy != "" && !done[candidate.BlockedBy] {
				continue
			}
			t = candidate
			break
		}
	}

	if t == nil {
		fmt.Fprintln(os.Stderr, "ai-task contract: no active task found (use --task-id to specify)")
		os.Exit(1)
	}

	c := contract.FromTask(t.ID, t.Title, t.Scope, t.Verify)

	data, err := contract.Marshal(c)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-task contract: marshal failed: %v\n", err)
		os.Exit(1)
	}

	outDir := filepath.Join(root, ".ai")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "ai-task contract: mkdir failed: %v\n", err)
		os.Exit(1)
	}
	outPath := filepath.Join(outDir, "CONTRACT.yaml")
	tmp := outPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "ai-task contract: write failed: %v\n", err)
		os.Exit(1)
	}
	if err := os.Rename(tmp, outPath); err != nil {
		fmt.Fprintf(os.Stderr, "ai-task contract: rename failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Contract written to %s\n", outPath)
}

// runVerify runs task.Verify for the given task ID, writes a VerifyEntry to .ai/results.jsonl.
// Exit codes: 0 = passed, 1 = failed, 2 = no verify cmd.
func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	sessionID := fs.String("session", "", "session ID for deduplication")
	fs.Parse(hoistFlags(args))

	if len(fs.Args()) < 1 {
		fmt.Fprintln(os.Stderr, "ai-task verify: requires <id>")
		os.Exit(1)
	}
	taskID := fs.Args()[0]

	root := projectRoot()
	if len(fs.Args()) > 1 {
		root = fs.Args()[1]
	}

	db := mustLoad(root)
	var t *task.Task
	for i := range db.Tasks {
		if db.Tasks[i].ID == taskID {
			t = &db.Tasks[i]
			break
		}
	}
	if t == nil {
		fmt.Fprintf(os.Stderr, "ai-task verify: task #%s not found\n", taskID)
		os.Exit(1)
	}
	if t.Verify == "" {
		os.Exit(2) // no verify cmd — not an error
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, "bash", "-c", t.Verify)
	cmd.Dir = root
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	passed := runErr == nil
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	truncate := func(s string) string {
		if len(s) > 500 {
			return s[:500]
		}
		return s
	}

	stdoutStr := truncate(stdoutBuf.String())
	stderrStr := truncate(stderrBuf.String())
	// Combined output for backward-compat Output field.
	combined := stdoutBuf.String() + stderrBuf.String()
	output := truncate(combined)

	sid := *sessionID
	if sid == "" {
		sid = os.Getenv("CLAUDE_SESSION_ID")
		if sid == "" {
			sid = "unknown"
		}
	}

	entry := schema.VerifyEntry{
		TaskID:    taskID,
		SessionID: sid,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Cmd:       t.Verify,
		Passed:    passed,
		ExitCode:  exitCode,
		Output:    output,
		Stdout:    stdoutStr,
		Stderr:    stderrStr,
	}
	if err := session.AppendVerify(root, entry); err != nil {
		fmt.Fprintf(os.Stderr, "ai-task verify: failed to write results: %v\n", err)
		os.Exit(1)
	}

	if !passed {
		os.Exit(1)
	}
}

// runSync reads .ai/CONTRACT.yaml and creates a pending task if no non-done task
// with the same title already exists. Idempotent: safe to call every turn.
func runSync(args []string) {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	quiet := fs.Bool("quiet", false, "suppress output when no action is taken")
	fs.Parse(args)

	root := projectRoot()
	c, err := contract.Parse(filepath.Join(root, ".ai", "CONTRACT.yaml"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-task sync: %v\n", err)
		os.Exit(1)
	}
	if c == nil {
		return // no CONTRACT.yaml — nothing to do
	}
	if c.Title == "" {
		fmt.Fprintln(os.Stderr, "ai-task sync: CONTRACT.yaml missing title")
		os.Exit(1)
	}

	db := mustLoad(root)

	// Idempotency: skip if a non-done task with this title already exists.
	for _, t := range db.Tasks {
		if t.Status != "done" && t.Title == c.Title {
			if !*quiet {
				fmt.Printf("TASK SYNC: already exists #%s — %s\n", t.ID, t.Title)
			}
			return
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	nextID := fmt.Sprintf("%d", task.MaxID(db.Tasks)+1)
	t := task.Task{
		ID:      nextID,
		Status:  "pending",
		Title:   c.Title,
		Added:   now,
		Updated: now,
		Verify:  c.Verify,
	}
	if len(c.Scope) > 0 {
		t.Scope = strings.Join(c.Scope, ",")
	}
	db.Tasks = append(db.Tasks, t)
	db.Updated = now
	mustSave(root, db)
	fmt.Printf("TASK SYNC: created #%s from CONTRACT.yaml — %s\n", nextID, c.Title)
}

// hoistFlags moves --flag=value and --flag value arguments before positional
// arguments so that flag.Parse works regardless of argument order.
func hoistFlags(args []string) []string {
	var flags, positional []string
	i := 0
	for i < len(args) {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i += 2
				continue
			}
		} else {
			positional = append(positional, a)
		}
		i++
	}
	return append(flags, positional...)
}

// runDriftCheck intersects the two most recent IR snapshots' changed files with
// active task scopes and appends DRIFT_AFFECTED faults to .ai/faults.jsonl.
func runDriftCheck(args []string) {
	fs := flag.NewFlagSet("drift-check", flag.ExitOnError)
	sessionID := fs.String("session", "", "session ID for deduplication (default: CLAUDE_SESSION_ID env)")
	fs.Parse(hoistFlags(args))

	root := projectRoot()
	if len(fs.Args()) > 0 {
		root = fs.Args()[0]
	}

	sid := *sessionID
	if sid == "" {
		sid = os.Getenv("CLAUDE_SESSION_ID")
		if sid == "" {
			sid = fmt.Sprintf("drift-%d", time.Now().Unix())
		}
	}

	if err := session.RunDriftCheck(root, sid); err != nil {
		fmt.Fprintf(os.Stderr, "ai-task drift-check: %v\n", err)
		// Exit 0 — drift check failures must not block the session.
	}
}

// runReplan prints a task's scope/verify alongside the current IR diff and any
// DRIFT_AFFECTED faults. Never fails loudly — exits 0 in all cases.
func runReplan(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "ai-task replan: requires <id>")
		os.Exit(1)
	}
	taskID := args[0]

	root := projectRoot()
	if len(args) > 1 {
		root = args[1]
	}

	db := mustLoad(root)
	var t *task.Task
	for i := range db.Tasks {
		if db.Tasks[i].ID == taskID {
			t = &db.Tasks[i]
			break
		}
	}
	if t == nil {
		fmt.Fprintf(os.Stderr, "ai-task replan: task #%s not found\n", taskID)
		os.Exit(1)
	}

	fmt.Printf("TASK #%s: %s\n", t.ID, t.Title)
	fmt.Printf("Status: %s\n", t.Status)
	if t.Scope != "" {
		fmt.Printf("Scope:  %s\n", t.Scope)
	} else {
		fmt.Println("Scope:  (none)")
	}
	if t.Verify != "" {
		fmt.Printf("Verify: %s\n", t.Verify)
	}
	fmt.Println()

	// Run ai-ir diff --since=session-start and print output.
	cmd := exec.Command("ai-ir", "diff", "--since=session-start", root)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		// Not fatal — may have no session-start snapshot.
		if buf.Len() == 0 {
			fmt.Println("IR DIFF: (no session-start snapshot found)")
		} else {
			fmt.Print(buf.String())
		}
	} else {
		if buf.Len() == 0 {
			fmt.Println("IR DIFF: (no changes since session-start)")
		} else {
			fmt.Print(buf.String())
		}
	}
	fmt.Println()

	// Print DRIFT_AFFECTED faults for this task.
	faults, err := task.LoadDriftFaults(root, taskID)
	if err != nil || len(faults) == 0 {
		fmt.Println("DRIFT FAULTS: none")
	} else {
		fmt.Printf("DRIFT FAULTS (%d):\n", len(faults))
		for _, f := range faults {
			fmt.Printf("  session=%s files=%s\n", f.SessionID, strings.Join(f.Files, ", "))
		}
	}
	fmt.Println()
	fmt.Println("Confirm scope is still valid. Edit tasks.json directly to update scope/verify fields.")
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
