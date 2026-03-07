package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inth3shadows/runecho/internal/task"
)

// roundtrip sends one JSON-RPC line to an in-process server and returns the decoded response.
func roundtrip(t *testing.T, srv *Server, method string, params any) map[string]any {
	t.Helper()

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}

	reqLine, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var out bytes.Buffer
	if err := srv.Serve(strings.NewReader(string(reqLine)+"\n"), &out); err != nil {
		t.Fatalf("Serve error: %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\nraw: %s", err, out.String())
	}
	return resp
}

func initParams() map[string]any {
	return map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	}
}

func TestInitialize(t *testing.T) {
	srv := NewServer(t.TempDir(), "test", "0.0.0")
	resp := roundtrip(t, srv, "initialize", initParams())

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got: %v", resp)
	}
	if got := result["protocolVersion"]; got != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", got)
	}
}

func TestToolsList(t *testing.T) {
	srv := NewServer(t.TempDir(), "test", "0.0.0")
	resp := roundtrip(t, srv, "tools/list", map[string]any{})

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result: %v", resp)
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("expected tools array: %v", result)
	}
	if len(tools) != 7 {
		t.Errorf("expected 7 tools, got %d", len(tools))
	}
	// Each tool must have an inputSchema.
	for _, raw := range tools {
		entry := raw.(map[string]any)
		if entry["inputSchema"] == nil {
			t.Errorf("tool %v missing inputSchema", entry["name"])
		}
	}
}

func seedTasks(t *testing.T, dir string, tasks []task.Task) {
	t.Helper()
	db := task.TaskDB{
		Updated: time.Now().UTC().Format(time.RFC3339),
		Tasks:   tasks,
	}
	if err := task.Save(dir, db); err != nil {
		t.Fatalf("seed tasks: %v", err)
	}
}

func TestTaskListAndUpdate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339)
	seedTasks(t, dir, []task.Task{
		{ID: "1", Title: "Alpha", Status: "todo", Added: now, Updated: now},
		{ID: "2", Title: "Beta", Status: "done", Added: now, Updated: now},
	})

	srv := NewServer(dir, "test", "0.0.0")

	// List all tasks.
	resp := roundtrip(t, srv, "tools/call", map[string]any{
		"name":      "runecho_task_list",
		"arguments": map[string]any{},
	})
	result := resp["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var listOut map[string]any
	json.Unmarshal([]byte(content), &listOut) //nolint:errcheck
	if count := listOut["count"].(float64); count != 2 {
		t.Errorf("expected 2 tasks, got %v", count)
	}

	// Update task 1 to done.
	resp2 := roundtrip(t, srv, "tools/call", map[string]any{
		"name":      "runecho_task_update",
		"arguments": map[string]any{"id": "1", "status": "done"},
	})
	r2 := resp2["result"].(map[string]any)
	if r2["isError"].(bool) {
		t.Fatalf("unexpected error in task_update: %v", r2)
	}

	// Verify via task_list filtered to todo.
	resp3 := roundtrip(t, srv, "tools/call", map[string]any{
		"name":      "runecho_task_list",
		"arguments": map[string]any{"status": "todo"},
	})
	r3content := resp3["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var listOut3 map[string]any
	json.Unmarshal([]byte(r3content), &listOut3) //nolint:errcheck
	if count := listOut3["count"].(float64); count != 0 {
		t.Errorf("expected 0 todo tasks after update, got %v", count)
	}
}

func TestTaskNext_EmptyList(t *testing.T) {
	dir := t.TempDir()
	srv := NewServer(dir, "test", "0.0.0")

	resp := roundtrip(t, srv, "tools/call", map[string]any{
		"name":      "runecho_task_next",
		"arguments": map[string]any{},
	})
	content := resp["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var out map[string]any
	json.Unmarshal([]byte(content), &out) //nolint:errcheck
	if out["found"].(bool) {
		t.Error("expected found=false on empty task list")
	}
}

func TestUnknownMethod(t *testing.T) {
	srv := NewServer(t.TempDir(), "test", "0.0.0")
	resp := roundtrip(t, srv, "nosuchmethod", nil)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error object: %v", resp)
	}
	if code := errObj["code"].(float64); code != ErrMethodNotFound {
		t.Errorf("expected code %d, got %v", ErrMethodNotFound, code)
	}
}

func TestMalformedJSON(t *testing.T) {
	srv := NewServer(t.TempDir(), "test", "0.0.0")
	var out bytes.Buffer
	srv.Serve(strings.NewReader("{not valid json}\n"), &out) //nolint:errcheck

	var resp map[string]any
	json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp) //nolint:errcheck
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error: %v", resp)
	}
	if code := errObj["code"].(float64); code != ErrParse {
		t.Errorf("expected ErrParse %d, got %v", ErrParse, code)
	}
}

// writeJSONL writes a slice of objects as JSONL to a file.
func writeJSONL(t *testing.T, path string, records []any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, rec := range records {
		line, _ := json.Marshal(rec)
		fmt.Fprintf(f, "%s\n", line)
	}
}

func TestSessionStatus(t *testing.T) {
	dir := t.TempDir()
	writeJSONL(t, filepath.Join(dir, ".ai", "faults.jsonl"), []any{
		map[string]any{"signal": "COST_WARN", "session_id": "s1", "ts": "2026-01-01T00:00:00Z"},
		map[string]any{"signal": "COST_WARN", "session_id": "s1", "ts": "2026-01-01T00:01:00Z"},
		map[string]any{"signal": "VERIFY_FAIL", "session_id": "s2", "ts": "2026-01-01T00:02:00Z"},
	})

	srv := NewServer(dir, "test", "0.0.0")
	resp := roundtrip(t, srv, "tools/call", map[string]any{
		"name":      "runecho_session_status",
		"arguments": map[string]any{},
	})
	content := resp["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var out map[string]any
	json.Unmarshal([]byte(content), &out) //nolint:errcheck
	if count := out["fault_count"].(float64); count != 3 {
		t.Errorf("expected 3 faults, got %v", count)
	}
}
