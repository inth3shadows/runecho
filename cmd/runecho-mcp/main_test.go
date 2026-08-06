// main_test.go — a smoke test for the MCP binary's wiring.
//
// This package shipped with zero tests. It is 52 lines of wiring, which is
// exactly why: nothing in it looked worth testing. But it is one of the three
// binaries RunEcho ships, and the class of defect it can carry is not a logic
// bug — it is a feature that is present, builds, passes every other package's
// tests, and is inert. #299 shipped precisely that (an outcome recorder wired
// into no config), and nothing here would have caught it either.
//
// So these drive the real `run` seam over real pipes and assert the three
// things a client depends on: the server answers, it advertises the tools the
// oracle is supposed to register, and stdout carries nothing but JSON-RPC.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// rpc frames the requests, runs the server to EOF, and returns the decoded
// responses plus whatever went to stderr.
func rpc(t *testing.T, home string, requests ...string) (resps []map[string]any, stderr string, code int) {
	t.Helper()
	t.Setenv("RUNECHO_HOME", home)

	var outBuf, errBuf bytes.Buffer
	code = run(strings.NewReader(strings.Join(requests, "\n")+"\n"), &outBuf, &errBuf)

	for _, line := range strings.Split(strings.TrimSpace(outBuf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout line is not JSON — stdio framing is corrupt: %q\nfull stdout:\n%s", line, outBuf.String())
		}
		resps = append(resps, m)
	}
	return resps, errBuf.String(), code
}

// A fresh store is the first-run case every new user hits: the DB does not
// exist yet and must be created, migrated and served in one go.
func TestServesInitializeOnAFreshStore(t *testing.T) {
	resps, _, code := rpc(t, t.TempDir(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if code != 0 {
		t.Fatalf("run returned %d, want 0", code)
	}
	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1: %v", len(resps), resps)
	}
	r := resps[0]
	if r["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v", r["jsonrpc"])
	}
	if _, isErr := r["error"]; isErr {
		t.Fatalf("initialize returned an error: %v", r["error"])
	}
	result, ok := r["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result object: %v", r)
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok || info["name"] != "runecho" {
		t.Errorf("serverInfo = %v, want name \"runecho\"", result["serverInfo"])
	}
	if result["protocolVersion"] == "" || result["protocolVersion"] == nil {
		t.Errorf("no protocolVersion advertised: %v", result)
	}
}

// The oracle's tools must actually be registered. `run` could open the store,
// serve, and answer initialize perfectly while never calling Register — the
// server would be live and useless, and every other test in the repo would pass.
func TestOracleToolsAreRegistered(t *testing.T) {
	resps, _, code := rpc(t, t.TempDir(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if code != 0 {
		t.Fatalf("run returned %d, want 0", code)
	}
	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2", len(resps))
	}
	result, ok := resps[1]["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list has no result: %v", resps[1])
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("no tools registered — the oracle is wired to nothing: %v", result)
	}
	got := map[string]bool{}
	for _, ti := range tools {
		if m, ok := ti.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				got[n] = true
			}
		}
	}
	// The documented read-only surface. A missing one means a tool stopped
	// shipping without anything failing.
	for _, want := range []string{"structure", "diff", "hash", "status", "locate", "health"} {
		if !got[want] {
			t.Errorf("tool %q is not registered; got %v", want, keys(got))
		}
	}
}

// Diagnostics must never reach stdout: this is a stdio transport, and one stray
// write corrupts the frame stream for every client. rpc() fails on a non-JSON
// stdout line, which pins that half. This pins the other, and it has to be
// asserted rather than assumed: routing the log to io.Discard keeps stdout
// perfectly clean and silently costs the operator the only signal that a client
// is sending malformed frames.
func TestDiagnosticsGoToStderrNotStdout(t *testing.T) {
	resps, stderr, code := rpc(t, t.TempDir(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`not json at all`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if code != 0 {
		t.Fatalf("run returned %d, want 0 — a malformed frame must not kill the server", code)
	}
	// The malformed line must not have cost the following request its answer.
	if len(resps) < 2 {
		t.Fatalf("got %d responses, want at least 2 — a bad frame swallowed a good one: %v", len(resps), resps)
	}
	if !strings.Contains(stderr, "parse error") {
		t.Errorf("stderr does not report the malformed frame, so the operator has no signal: %q", stderr)
	}
}

// An unusable store directory is a startup failure, not a silent degraded serve:
// a client that connects successfully to a server with no index gets confident
// empty answers, which is worse than a refusal.
func TestUnopenableStoreExitsNonZero(t *testing.T) {
	home := t.TempDir()
	// RUNECHO_HOME pointing at a path that cannot be a directory.
	blocked := home + "/not-a-dir"
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := runWithHome(t, blocked+"/nested")
	if code == 0 {
		t.Error("run returned 0 with an unusable store dir; want a non-zero exit")
	}
}

func runWithHome(t *testing.T, home string) int {
	t.Helper()
	t.Setenv("RUNECHO_HOME", home)
	var out, errBuf bytes.Buffer
	return run(strings.NewReader(""), &out, &errBuf)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
