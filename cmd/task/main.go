package main

// Usage:
//   ai-task add "<title>" [--blocked-by=<id>] [--scope=<glob>] [--verify=<cmd>]
//   ai-task update <id> <status>                 status ∈ {pending, in-progress, done}
//   ai-task list [--status=<s>]                  tabular; no filter = all non-done
//   ai-task next                                 first unblocked non-done task by id; exit 1 if none
//   ai-task contract [--task-id=<id>] [root]     write .ai/CONTRACT.yaml from active (or specified) task
//   ai-task verify <id> [--session=<sid>] [root] run task.Verify, write .ai/results.jsonl; exit 0/1/2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/contract"
	"github.com/inth3shadows/runecho/internal/session"
)

const tasksFile = ".ai/tasks.json"

type Task struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Title     string `json:"title"`
	Added     string `json:"added"`
	Updated   string `json:"updated"`
	BlockedBy string `json:"blockedBy,omitempty"`
	Scope     string `json:"scope,omitempty"`  // glob pattern(s) for allowed file paths
	Verify    string `json:"verify,omitempty"` // shell command to validate completion
}

type TaskDB struct {
	Updated string `json:"updated"`
	Tasks   []Task `json:"tasks"`
}

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
}

// runAdd appends a new pending task.
func runAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	blockedBy := fs.String("blocked-by", "", "ID of task that blocks this one")
	scope := fs.String("scope", "", "glob pattern(s) for allowed file paths, e.g. internal/auth/*")
	verify := fs.String("verify", "", "shell command to validate completion, e.g. go test ./internal/auth/...")
	// Go's flag package stops at first non-flag arg, so hoist flag args before positional ones.
	fs.Parse(hoistFlags(args))

	title := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if title == "" {
		fmt.Fprintln(os.Stderr, "ai-task add: title required")
		os.Exit(1)
	}

	db := loadOrInit()
	now := time.Now().UTC().Format(time.RFC3339)
	nextID := strconv.Itoa(maxID(db.Tasks) + 1)

	t := Task{
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
	mustSave(db)
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

	db := loadOrInit()
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
	mustSave(db)
}

// runList prints tasks in tabular form.
// Default: all non-done. --status=<s> filters to exact status.
func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	statusFilter := fs.String("status", "", "filter by status (pending, in-progress, done, all)")
	fs.Parse(args)

	db := loadOrInit()
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
	db := loadOrInit()

	// Build set of done IDs — a blocked task is unblocked if its blocker is done.
	done := make(map[string]bool)
	for _, t := range db.Tasks {
		if t.Status == "done" {
			done[t.ID] = true
		}
	}

	// Sort by numeric ID (IDs are auto-incremented integers as strings).
	tasks := sortByID(db.Tasks)
	for _, t := range tasks {
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

	// Optional positional arg overrides project root.
	root := projectRoot()
	if len(fs.Args()) > 0 {
		root = fs.Args()[0]
	}

	db := loadOrInitAt(root)

	done := make(map[string]bool)
	for _, t := range db.Tasks {
		if t.Status == "done" {
			done[t.ID] = true
		}
	}

	var task *Task
	if *taskID != "" {
		for i := range db.Tasks {
			if db.Tasks[i].ID == *taskID {
				task = &db.Tasks[i]
				break
			}
		}
		if task == nil {
			fmt.Fprintf(os.Stderr, "ai-task contract: task #%s not found\n", *taskID)
			os.Exit(1)
		}
	} else {
		// Find first active unblocked task.
		for i := range db.Tasks {
			t := &db.Tasks[i]
			if t.Status == "done" {
				continue
			}
			if t.BlockedBy != "" && !done[t.BlockedBy] {
				continue
			}
			task = t
			break
		}
	}

	if task == nil {
		fmt.Fprintln(os.Stderr, "ai-task contract: no active task found (use --task-id to specify)")
		os.Exit(1)
	}

	c := contract.FromTask(task.ID, task.Title, task.Scope, task.Verify)

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

	db := loadOrInitAt(root)
	var task *Task
	for i := range db.Tasks {
		if db.Tasks[i].ID == taskID {
			task = &db.Tasks[i]
			break
		}
	}
	if task == nil {
		fmt.Fprintf(os.Stderr, "ai-task verify: task #%s not found\n", taskID)
		os.Exit(1)
	}
	if task.Verify == "" {
		os.Exit(2) // no verify cmd — not an error
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, "bash", "-c", task.Verify)
	cmd.Dir = root
	cmd.Stdout = &buf
	cmd.Stderr = &buf

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

	output := buf.String()
	if len(output) > 500 {
		output = output[:500]
	}

	sid := *sessionID
	if sid == "" {
		sid = os.Getenv("CLAUDE_SESSION_ID")
		if sid == "" {
			sid = "unknown"
		}
	}

	entry := session.VerifyEntry{
		TaskID:    taskID,
		SessionID: sid,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Cmd:       task.Verify,
		Passed:    passed,
		ExitCode:  exitCode,
		Output:    output,
	}
	if err := session.AppendVerify(root, entry); err != nil {
		fmt.Fprintf(os.Stderr, "ai-task verify: failed to write results: %v\n", err)
		os.Exit(1)
	}

	if !passed {
		os.Exit(1)
	}
}

func loadOrInit() TaskDB {
	return loadOrInitAt(projectRoot())
}

func loadOrInitAt(root string) TaskDB {
	path := filepath.Join(root, tasksFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return TaskDB{Updated: time.Now().UTC().Format(time.RFC3339), Tasks: []Task{}}
	}
	var db TaskDB
	if err := json.Unmarshal(data, &db); err != nil {
		fmt.Fprintf(os.Stderr, "ai-task: failed to parse %s: %v\n", path, err)
		os.Exit(1)
	}
	return db
}

func mustSave(db TaskDB) {
	root := projectRoot()
	path := filepath.Join(root, tasksFile)
	tmp := path + ".tmp"

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "ai-task: mkdir failed: %v\n", err)
		os.Exit(1)
	}

	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-task: marshal failed: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "ai-task: write failed: %v\n", err)
		os.Exit(1)
	}
	if err := os.Rename(tmp, path); err != nil {
		fmt.Fprintf(os.Stderr, "ai-task: rename failed: %v\n", err)
		os.Exit(1)
	}
}

func maxID(tasks []Task) int {
	max := 0
	for _, t := range tasks {
		n, err := strconv.Atoi(t.ID)
		if err == nil && n > max {
			max = n
		}
	}
	return max
}

func sortByID(tasks []Task) []Task {
	out := make([]Task, len(tasks))
	copy(out, tasks)
	// Simple insertion sort — task list is small.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			ai, _ := strconv.Atoi(out[j-1].ID)
			bi, _ := strconv.Atoi(out[j].ID)
			if ai > bi {
				out[j-1], out[j] = out[j], out[j-1]
			} else {
				break
			}
		}
	}
	return out
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
			// If the flag has no '=' it may consume the next arg as its value.
			// Check: does the flag registered without '='? We can't know without
			// the FlagSet at this point, so only consume next arg if it doesn't
			// start with '-' AND the current flag has no '='.
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
