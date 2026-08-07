// Minimal hand-rolled gopls client for the #323 spike (oracle_spike_test.go).
//
// WHY HAND-ROLLED. No LSP-client dependency exists anywhere in this repo's
// go.mod, and the already-installed lsp-go bridge (mcp-language-server
// wrapping gopls) is reachable only from inside a Claude Code MCP session —
// the guard binary this spike is asking about is a standalone git-hook CLI
// process with no MCP context, so any oracle call has to speak raw LSP
// (Content-Length-framed JSON-RPC 2.0 over stdio) to the gopls binary
// directly. This is the smallest client that can do that: initialize,
// didOpen, hover, shutdown. It is deliberately _test.go-only — a spike
// answering "would this be worth building", not a shipped dependency.
//
// SPIKE-QUALITY, ON PURPOSE. No per-request timeout: a hung gopls hangs this
// test. Acceptable because this file only ever runs when RUNECHO_SPIKE_GOPLS
// is explicitly set — never in CI, never by default.
package guard_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

type rpcResult struct {
	result json.RawMessage
	errMsg string
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// goplsClient speaks LSP over one gopls subprocess's stdio.
type goplsClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcResult
}

func startGoplsClient(bin string) (*goplsClient, error) {
	cmd := exec.Command(bin) // bare `gopls`, no subcommand: LSP-over-stdio is its default mode
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("gopls stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("gopls stdout pipe: %w", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start gopls: %w", err)
	}
	c := &goplsClient{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		pending: make(map[int64]chan rpcResult),
	}
	go c.readLoop()
	return c, nil
}

func writeFrame(w io.Writer, payload []byte) error {
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if rest, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				return nil, fmt.Errorf("bad Content-Length header %q: %w", line, err)
			}
			length = n
		}
	}
	if length < 0 {
		return nil, fmt.Errorf("frame with no Content-Length header")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// readLoop dispatches responses to their waiting call(), auto-replies to any
// server->client request with a null result (gopls sends
// client/registerCapability and window/workDoneProgress/create; a minimal
// client that never answers them risks gopls stalling on the handshake), and
// drops server->client notifications ($/progress, window/logMessage) — none
// of which this spike needs.
func (c *goplsClient) readLoop() {
	for {
		raw, err := readFrame(c.stdout)
		if err != nil {
			return
		}
		var env rpcEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		switch {
		case env.ID != nil && env.Method == "": // response to our call
			c.mu.Lock()
			ch, ok := c.pending[*env.ID]
			if ok {
				delete(c.pending, *env.ID)
			}
			c.mu.Unlock()
			if !ok {
				continue
			}
			res := rpcResult{result: env.Result}
			if env.Error != nil {
				res.errMsg = env.Error.Message
			}
			ch <- res
		case env.ID != nil && env.Method != "": // server->client request
			_ = c.reply(*env.ID)
		default: // notification, ignored
		}
	}
}

func (c *goplsClient) reply(id int64) error {
	b, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int64           `json:"id"`
		Result  json.RawMessage `json:"result"`
	}{"2.0", id, json.RawMessage("null")})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeFrame(c.stdin, b)
}

func (c *goplsClient) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResult, 1)
	c.pending[id] = ch
	b, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int64  `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", id, method, params})
	if err != nil {
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	werr := writeFrame(c.stdin, b)
	c.mu.Unlock()
	if werr != nil {
		return nil, werr
	}
	res := <-ch
	if res.errMsg != "" {
		return nil, fmt.Errorf("gopls: %s", res.errMsg)
	}
	return res.result, nil
}

func (c *goplsClient) notify(method string, params any) error {
	b, err := json.Marshal(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{"2.0", method, params})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeFrame(c.stdin, b)
}

func (c *goplsClient) initialize(rootURI string) error {
	params := map[string]any{
		"processId": nil,
		"rootUri":   rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"hover": map[string]any{
					"contentFormat": []string{"plaintext", "markdown"},
				},
			},
		},
	}
	if _, err := c.call("initialize", params); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return c.notify("initialized", map[string]any{})
}

func (c *goplsClient) didOpen(uri, text string) error {
	return c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": "go",
			"version":    1,
			"text":       text,
		},
	})
}

// utf16Offset converts a byte offset into s (which must land on a rune
// boundary) into the UTF-16 code-unit offset the LSP position protocol
// requires. A byte offset used directly would drift on any line with
// non-ASCII content (e.g. a string literal) before the target column —
// the corpus this spike runs against is not guaranteed ASCII-only.
func utf16Offset(s string, byteOffset int) int {
	off := 0
	for i, r := range s {
		if i >= byteOffset {
			break
		}
		if n := utf16.RuneLen(r); n > 0 {
			off += n
		} else {
			off++
		}
	}
	return off
}

// hover returns whether gopls resolved a non-empty type/symbol description at
// (line, char) — 0-based, UTF-16-code-unit character offset per the LSP spec.
// Callers must convert byte offsets via utf16Offset before calling this.
func (c *goplsClient) hover(uri string, line, char int) (resolved bool, raw json.RawMessage, err error) {
	res, err := c.call("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line, "character": char},
	})
	if err != nil {
		return false, nil, err
	}
	if len(res) == 0 || string(res) == "null" {
		return false, res, nil
	}
	var body struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(res, &body); err != nil {
		return false, res, err
	}
	contents := strings.TrimSpace(string(body.Contents))
	resolved = contents != "" && contents != "null" && contents != `""`
	return resolved, res, nil
}

// close shuts gopls down cleanly, killing the process if it does not exit.
func (c *goplsClient) close() {
	_, _ = c.call("shutdown", nil)
	_ = c.notify("exit", nil)
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
}
