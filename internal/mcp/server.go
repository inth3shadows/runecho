// Package mcp implements a minimal MCP (Model Context Protocol) server over
// stdio: newline-delimited JSON-RPC 2.0. It is deliberately small — just enough
// to expose RunEcho's read-only truth-oracle tools to Claude Code and Codex.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// DefaultProtocolVersion is returned when the client does not request one.
const DefaultProtocolVersion = "2025-06-18"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Tool is a callable MCP tool. Handler receives the raw JSON arguments object
// and returns the text payload (typically JSON) or an error (surfaced to the
// client as an isError tool result, not a transport error).
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(args json.RawMessage) (string, error)
}

// Server is a minimal stdio MCP server.
type Server struct {
	name    string
	version string
	tools   map[string]Tool
	order   []string
}

// NewServer creates a server advertising the given name/version.
func NewServer(name, version string) *Server {
	return &Server{name: name, version: version, tools: map[string]Tool{}}
}

// Register adds a tool. Registration order is the tools/list order.
func (s *Server) Register(t Tool) {
	if _, dup := s.tools[t.Name]; !dup {
		s.order = append(s.order, t.Name)
	}
	s.tools[t.Name] = t
}

// maxRequestBytes bounds a single JSON-RPC request frame. A larger frame is
// answered with one error and then dropped — an oversized (or malformed-huge)
// message must never take the oracle down or wedge the stream.
const maxRequestBytes = 10 * 1024 * 1024

// Serve reads newline-delimited JSON-RPC requests from in and writes responses
// to out until EOF. Notifications (no id) are processed without a reply. The
// loop is resilient: oversized frames, parse errors, and tool panics are all
// reported and serving continues.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	r := bufio.NewReaderSize(in, 64*1024)
	enc := json.NewEncoder(out)

	for {
		line, tooLong, err := readFrame(r, maxRequestBytes)

		switch {
		case tooLong:
			_ = enc.Encode(errFrame(-32600, "request too large"))
		case len(bytes.TrimSpace(line)) == 0:
			// blank line (or clean EOF) — nothing to do
		default:
			var req request
			if e := json.Unmarshal(line, &req); e != nil {
				_ = enc.Encode(errFrame(-32700, "parse error"))
				break
			}
			resp, reply := s.handle(req)
			if reply {
				if e := enc.Encode(resp); e != nil {
					return fmt.Errorf("write response: %w", e)
				}
			}
		}

		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// readFrame reads one newline-delimited frame. If the frame exceeds max bytes it
// is discarded up to the next newline and tooLong is reported, so a giant line
// never overflows memory or kills the loop. The returned err is io.EOF when the
// stream ended (line may still hold a final, newline-less frame).
func readFrame(r *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, e := r.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > max {
				tooLong = true
				buf = nil // stop accumulating; we won't parse an oversized frame
			} else {
				buf = append(buf, chunk...) // copy before the next ReadSlice reuses the buffer
			}
		}
		if e == bufio.ErrBufferFull {
			continue // line longer than the reader buffer; keep draining to '\n'
		}
		return buf, tooLong, e
	}
}

// errFrame builds an id-less JSON-RPC error response (used before a request id
// is known: parse errors and oversized frames).
func errFrame(code int, msg string) response {
	return response{JSONRPC: "2.0", ID: json.RawMessage("null"),
		Error: &rpcError{Code: code, Message: msg}}
}

// handle dispatches one request. The bool is false for notifications (no reply).
func (s *Server) handle(req request) (response, bool) {
	isNotification := len(req.ID) == 0
	switch req.Method {
	case "initialize":
		return s.ok(req.ID, s.initResult(req.Params)), !isNotification
	case "notifications/initialized", "notifications/cancelled":
		return response{}, false
	case "ping":
		return s.ok(req.ID, map[string]any{}), !isNotification
	case "tools/list":
		return s.ok(req.ID, s.listTools()), !isNotification
	case "tools/call":
		if isNotification {
			return response{}, false
		}
		return s.callTool(req.ID, req.Params)
	default:
		if isNotification {
			return response{}, false
		}
		return s.errResp(req.ID, -32601, "method not found: "+req.Method), true
	}
}

func (s *Server) initResult(params json.RawMessage) map[string]any {
	ver := DefaultProtocolVersion
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
			ver = p.ProtocolVersion // echo client's version
		}
	}
	return map[string]any{
		"protocolVersion": ver,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": s.name, "version": s.version},
	}
}

func (s *Server) listTools() map[string]any {
	tools := make([]map[string]any, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
		})
	}
	return map[string]any{"tools": tools}
}

func (s *Server) callTool(id, params json.RawMessage) (response, bool) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return s.errResp(id, -32602, "invalid params"), true
	}
	t, ok := s.tools[p.Name]
	if !ok {
		return s.errResp(id, -32602, "unknown tool: "+p.Name), true
	}
	text, err := invoke(t, p.Arguments)
	if err != nil {
		// Tool-level failure: MCP convention is a result with isError=true so the
		// model sees the message, not a JSON-RPC transport error.
		return s.ok(id, toolResult(err.Error(), true)), true
	}
	return s.ok(id, toolResult(text, false)), true
}

// invoke runs a tool handler, converting a panic into an error. The serve loop
// has no other recovery, so one misbehaving tool must not crash the oracle.
func invoke(t Tool, args json.RawMessage) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %q panicked: %v", t.Name, r)
		}
	}()
	return t.Handler(args)
}

func toolResult(text string, isErr bool) map[string]any {
	r := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	if isErr {
		r["isError"] = true
	}
	return r
}

func (s *Server) ok(id json.RawMessage, result any) response {
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *Server) errResp(id json.RawMessage, code int, msg string) response {
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}
