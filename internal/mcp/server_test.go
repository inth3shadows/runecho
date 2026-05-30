package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// drive feeds newline-delimited requests through Serve and returns the decoded
// responses (in order).
func drive(t *testing.T, lines ...string) []map[string]any {
	t.Helper()
	s := NewServer("test", "9.9")
	s.Register(Tool{
		Name:        "echo",
		Description: "echoes its text arg",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(args json.RawMessage) (string, error) {
			var a struct {
				Text string `json:"text"`
				Fail bool   `json:"fail"`
			}
			_ = json.Unmarshal(args, &a)
			if a.Fail {
				return "", errBoom
			}
			return a.Text, nil
		},
	})
	var out strings.Builder
	if err := s.Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resps []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode response %q: %v", line, err)
		}
		resps = append(resps, m)
	}
	return resps
}

var errBoom = boomError{}

type boomError struct{}

func (boomError) Error() string { return "boom" }

func TestInitialize(t *testing.T) {
	r := drive(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	if len(r) != 1 {
		t.Fatalf("want 1 response, got %d", len(r))
	}
	res := r[0]["result"].(map[string]any)
	if res["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want echoed 2024-11-05", res["protocolVersion"])
	}
	if si := res["serverInfo"].(map[string]any); si["name"] != "test" {
		t.Errorf("serverInfo.name = %v", si["name"])
	}
}

func TestNotificationNoReply(t *testing.T) {
	// initialize (reply) + initialized notification (no reply) → exactly 1 response.
	r := drive(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	)
	if len(r) != 1 {
		t.Fatalf("notification should not produce a reply; got %d responses", len(r))
	}
}

func TestToolsList(t *testing.T) {
	r := drive(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := r[0]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" {
		t.Fatalf("tools/list = %v", tools)
	}
}

func TestToolCallSuccessAndError(t *testing.T) {
	r := drive(t,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{"fail":true}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"missing","arguments":{}}}`,
	)
	// success
	ok := r[0]["result"].(map[string]any)
	if txt := ok["content"].([]any)[0].(map[string]any)["text"]; txt != "hi" {
		t.Errorf("echo text = %v", txt)
	}
	if _, isErr := ok["isError"]; isErr {
		t.Error("success result should not set isError")
	}
	// handler error → isError result (not a transport error)
	boom := r[1]["result"].(map[string]any)
	if boom["isError"] != true {
		t.Error("handler error should set isError=true")
	}
	// unknown tool → JSON-RPC error
	if _, ok := r[2]["error"]; !ok {
		t.Error("unknown tool should be a JSON-RPC error")
	}
}

func TestUnknownMethod(t *testing.T) {
	r := drive(t, `{"jsonrpc":"2.0","id":9,"method":"does/not/exist"}`)
	if _, ok := r[0]["error"]; !ok {
		t.Fatal("unknown method should return a JSON-RPC error")
	}
}
