package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Server is an MCP stdio server.
type Server struct {
	workspace   string
	name        string
	version     string
	registry    *Registry
}

// NewServer creates a Server with all RunEcho tools registered.
func NewServer(workspace, name, version string) *Server {
	s := &Server{
		workspace: workspace,
		name:      name,
		version:   version,
		registry:  newRegistry(),
	}
	registerTaskTools(s.registry)
	registerSessionTools(s.registry)
	registerProvenanceTools(s.registry)
	registerContextTools(s.registry)
	return s
}

// Serve reads newline-delimited JSON-RPC from r and writes responses to w.
// Logs go to stderr (visible in Claude Code's MCP panel).
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1 MB
	enc := json.NewEncoder(w)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			enc.Encode(errResp(nil, ErrParse, err.Error())) //nolint:errcheck
			continue
		}

		resp := s.dispatch(req)
		if resp != nil {
			enc.Encode(resp) //nolint:errcheck
		}
	}
	return scanner.Err()
}

func (s *Server) dispatch(req Request) *Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)

	case "initialized":
		// Notification: no response.
		return nil

	case "tools/list":
		return okResp(req.ID, s.registry.listResult())

	case "tools/call":
		result, err := s.registry.call(s.workspace, req.Params)
		if err != nil {
			// Tool-not-found is ErrMethodNotFound; other dispatch errors are ErrInvalidParams.
			code := ErrInvalidParams
			return errResp(req.ID, code, err.Error())
		}
		return okResp(req.ID, result)

	case "shutdown":
		return okResp(req.ID, map[string]any{})

	default:
		return errResp(req.ID, ErrMethodNotFound,
			fmt.Sprintf("method not found: %s", req.Method))
	}
}

func (s *Server) handleInitialize(req Request) *Response {
	result := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]string{
			"name":    s.name,
			"version": s.version,
		},
	}
	return okResp(req.ID, result)
}
