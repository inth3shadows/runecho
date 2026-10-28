package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// transcriptTailBytes bounds how much of a transcript transcriptModel reads.
// Transcripts grow to tens of MB over a long session; the model is on the
// newest assistant entry, so only the tail matters. A tail holding no complete
// assistant entry (one tool result larger than this) records no model, which
// reports as "unrecorded" — never as a wrong one.
const transcriptTailBytes = 1 << 20

// transcriptModel returns the model of the newest assistant entry in the Claude
// Code transcript at path (#529), or "" on any error or miss. No tool-event
// hook payload names the model (only SessionStart may, and not always), so the
// transcript is the one place a PreToolUse or PostToolUse fire can learn it.
//
// The newest entry may be the turn BEFORE the one issuing this tool call if
// Claude Code has not written that turn yet; the two differ only across a
// mid-turn model switch.
func transcriptModel(path string) string {
	if path == "" {
		return ""
	}
	f, err := openTranscript(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	// Regular files only: a device or FIFO at this path would block the read.
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	off := fi.Size() - transcriptTailBytes
	if off < 0 {
		off = 0
	}
	buf := make([]byte, fi.Size()-off)
	n, err := f.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return ""
	}
	// A file truncated since Stat reads short; only n bytes are real.
	buf = buf[:n]
	// Newest line first. When the window starts mid-line, that first fragment
	// is skipped by the Unmarshal below: a proper suffix of one JSON object
	// always carries the object's unmatched closing brace, so it never parses.
	for len(buf) > 0 {
		var line []byte
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			line, buf = buf[i+1:], buf[:i]
		} else {
			line, buf = buf, nil
		}
		// Cheap pre-filter: the tail is mostly large tool results, and fully
		// decoding each one to read its type is most of the cost of this scan.
		// Claude Code writes compact JSON; a spelling this misses records no
		// model, never a wrong one.
		if !bytes.Contains(line, []byte(`"type":"assistant"`)) {
			continue
		}
		var e struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "assistant" {
			continue
		}
		// "<synthetic>" marks an entry the harness wrote itself (an API error,
		// an interrupt); no model issued it.
		if e.Message.Model == "" || e.Message.Model == "<synthetic>" {
			continue
		}
		return e.Message.Model
	}
	return ""
}

// modelTranscriptPath picks the transcript holding the model that issued a tool
// call. A subagent's fire carries the PARENT session's transcript_path (see
// decisionRecord.Session), but the subagent runs its own model and writes its
// own transcript at <parent without .jsonl>/subagents/agent-<id>.jsonl.
// Reading the parent's would label a Haiku subagent's edit with the session's
// Opus — a silent mislabel, which is worse than no label, so a subagent whose
// transcript is missing records none rather than falling back. An agent id
// that is not a plain file-name component records none either.
func modelTranscriptPath(transcriptPath, agentID string) string {
	if transcriptPath == "" || agentID == "" {
		return transcriptPath
	}
	if agentID == "." || agentID == ".." || strings.ContainsAny(agentID, `/\`) {
		return ""
	}
	name := "agent-" + agentID + ".jsonl"
	if filepath.Base(transcriptPath) == name {
		return transcriptPath
	}
	subagents := filepath.Join(strings.TrimSuffix(transcriptPath, ".jsonl"), "subagents")
	direct := filepath.Join(subagents, name)
	if _, err := os.Stat(direct); err == nil {
		return direct
	}
	// A workflow's subagents write one level down, under
	// workflows/<workflow id>/ (#531 review: ~26% of subagent transcripts).
	// Agent ids are unique per session, so at most one match is expected;
	// more than one is ambiguous and records none.
	if m, _ := filepath.Glob(filepath.Join(subagents, "workflows", "*", name)); len(m) == 1 {
		return m[0]
	}
	return direct
}

// modelSource is the transcript a hook process's records take their model
// from, resolved at most once and only when a record is written.
type modelSource struct {
	path  string
	once  sync.Once
	model string
	done  atomic.Bool // set after model is written, so a reader that sees it may read model
}

func (m *modelSource) get() string {
	m.once.Do(func() {
		m.model = transcriptModel(m.path)
		m.done.Store(true)
	})
	return m.model
}

// decisionModel is decisionSession's counterpart for the model (#529): one
// funnel in logDecision, process-wide, atomic for deferOnPanic's timeout
// branch. Resolution is lazy so the transcript read happens in logDecision —
// after the decision is written — and never on the decision's latency path.
var decisionModel atomic.Pointer[modelSource]

// setDecisionModel records where the model of the payload being served lives.
// Called with ("", "") before the payload is decoded, like setDecisionSession,
// so no record inherits an earlier payload's model.
func setDecisionModel(transcriptPath, agentID string) {
	decisionModel.Store(&modelSource{path: modelTranscriptPath(transcriptPath, agentID)})
}

// abandonDecisionModel stops later records from waiting on the transcript read.
// deferOnPanic's timeout branch calls it before logging: the hook body may be
// stuck inside that very read (a hung filesystem), and get() would block the
// timeout record behind sync.Once — defeating the timeout (#531 review). A read
// that already finished is kept; an unfinished one records no model.
func abandonDecisionModel() {
	model := ""
	if m := decisionModel.Load(); m != nil && m.done.Load() {
		model = m.model
	}
	r := &modelSource{model: model}
	r.once.Do(func() {})
	r.done.Store(true)
	decisionModel.Store(r)
}
