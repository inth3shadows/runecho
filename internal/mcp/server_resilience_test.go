package mcp

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
)

// A recovered tool panic must (a) still return an isError result to the client
// and (b) emit one diagnostic line on the injected log sink — the client sees a
// terse string, the operator gets the panic value.
func TestServe_ToolPanicLogged(t *testing.T) {
	var logSink strings.Builder
	s := NewServer("t", "0").WithLogWriter(&logSink)
	s.Register(Tool{Name: "boom", Handler: func(json.RawMessage) (string, error) {
		panic("kaboom")
	}})
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{}}}` + "\n"

	var out strings.Builder
	if err := s.Serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve crashed on tool panic: %v", err)
	}

	// (a) client still gets isError.
	resps := countResponses(t, out.String())
	if len(resps) != 1 {
		t.Fatalf("expected 1 response, got %d: %s", len(resps), out.String())
	}
	res, ok := resps[0]["result"].(map[string]any)
	if !ok || res["isError"] != true {
		t.Errorf("panicking tool should return isError result, got %v", resps[0])
	}

	// (b) the panic is logged with the tool name and the panic value.
	logged := logSink.String()
	if !strings.Contains(logged, "boom") || !strings.Contains(logged, "kaboom") {
		t.Errorf("panic log should name the tool and panic value, got %q", logged)
	}
	// The isError result legitimately carries the panic message (MCP convention).
	// What must NOT leak into stdout is the operator log line — its "runecho-mcp:"
	// prefix would corrupt the JSON-RPC frame stream.
	if strings.Contains(out.String(), "runecho-mcp:") {
		t.Errorf("operator log line leaked into stdout JSON-RPC stream: %s", out.String())
	}
}

// An oversized frame must emit a diagnostic on the injected sink (and still be
// answered with -32600 on the wire — covered by TestServe_OversizedFrameSurvives).
func TestServe_OversizedFrameLogged(t *testing.T) {
	var logSink strings.Builder
	s := NewServer("t", "0").WithLogWriter(&logSink)
	big := strings.Repeat("x", maxRequestBytes+1024)
	input := `{"jsonrpc":"2.0","id":1,"method":"ping","params":"` + big + `"}` + "\n"

	var out strings.Builder
	if err := s.Serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error on oversized frame: %v", err)
	}
	if !strings.Contains(logSink.String(), "oversized") {
		t.Errorf("oversized frame should be logged, got %q", logSink.String())
	}
}

// A malformed JSON frame must emit a parse-error diagnostic on the injected sink.
func TestServe_ParseErrorLogged(t *testing.T) {
	var logSink strings.Builder
	s := NewServer("t", "0").WithLogWriter(&logSink)
	input := "{not json}\n"

	var out strings.Builder
	if err := s.Serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error on parse error: %v", err)
	}
	if !strings.Contains(logSink.String(), "parse error") {
		t.Errorf("parse error should be logged, got %q", logSink.String())
	}
}

// countResponses parses newline-delimited JSON-RPC responses out of raw output.
func countResponses(t *testing.T, raw string) []map[string]any {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	var out []map[string]any
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("non-JSON response line: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

// F1: an oversized frame must be answered once (-32600) and the server must keep
// serving the requests that follow — never exit on "token too long".
func TestServe_OversizedFrameSurvives(t *testing.T) {
	s := NewServer("t", "0")
	big := strings.Repeat("x", maxRequestBytes+1024)
	input := `{"jsonrpc":"2.0","id":1,"method":"ping","params":"` + big + `"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"

	var out strings.Builder
	if err := s.Serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve returned error on oversized frame: %v", err)
	}

	resps := countResponses(t, out.String())
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses (oversized error + ping id=2), got %d: %s", len(resps), out.String())
	}
	if e, ok := resps[0]["error"].(map[string]any); !ok || e["code"].(float64) != -32600 {
		t.Errorf("first response should be -32600 request too large, got %v", resps[0])
	}
	if resps[1]["id"].(float64) != 2 {
		t.Errorf("second request (id=2) was not served; got %v", resps[1])
	}
}

// A frame too long for the 64KB reader buffer but under the cap must still be
// read whole and dispatched (no spurious truncation).
func TestServe_LargeButValidFrame(t *testing.T) {
	s := NewServer("t", "0")
	pad := strings.Repeat("y", 200*1024) // > reader buffer, < cap
	input := `{"jsonrpc":"2.0","id":7,"method":"ping","params":{"pad":"` + pad + `"}}` + "\n"

	var out strings.Builder
	if err := s.Serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve error: %v", err)
	}
	resps := countResponses(t, out.String())
	if len(resps) != 1 || resps[0]["id"].(float64) != 7 {
		t.Fatalf("large-but-valid frame not served correctly: %s", out.String())
	}
}

// A final frame with no trailing newline must still be processed.
func TestServe_NoTrailingNewline(t *testing.T) {
	s := NewServer("t", "0")
	var out strings.Builder
	if err := s.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"ping"}`), &out); err != nil {
		t.Fatalf("Serve error: %v", err)
	}
	resps := countResponses(t, out.String())
	if len(resps) != 1 || resps[0]["id"].(float64) != 3 {
		t.Fatalf("newline-less final frame not served: %s", out.String())
	}
}

// F6: a panicking tool must surface as an isError result, not crash the server,
// and following requests must still be served.
func TestServe_ToolPanicRecovered(t *testing.T) {
	s := NewServer("t", "0")
	s.Register(Tool{Name: "boom", Handler: func(json.RawMessage) (string, error) {
		panic("kaboom")
	}})
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n"

	var out strings.Builder
	if err := s.Serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve crashed on tool panic: %v", err)
	}
	resps := countResponses(t, out.String())
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses after tool panic, got %d: %s", len(resps), out.String())
	}
	res, ok := resps[0]["result"].(map[string]any)
	if !ok || res["isError"] != true {
		t.Errorf("panicking tool should return isError result, got %v", resps[0])
	}
	if resps[1]["id"].(float64) != 2 {
		t.Errorf("request after panic was not served: %v", resps[1])
	}
}
