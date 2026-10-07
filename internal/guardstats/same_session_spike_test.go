// Spike: was a premature ask's symbol defined later in the SAME session?
//
// WHY THIS EXISTS. premature_latency_spike_test.go measures how long a
// premature finding stayed wrong, but in commit time: it cannot say whether the
// callee was written by the same agent in the same session, which is the number
// that decides whether an early ask is noise (the agent was about to write the
// callee anyway) or a catch. Until #458 no decision record carried a session,
// so that was unmeasurable. This is the measurement.
//
// METHOD. For every ask the git oracle rates premature, find the transcript of
// the session that raised it (the record's `session` is sha256 of the session
// id, and a transcript's filename is that id), then look at the Edit, Write and
// MultiEdit calls that followed the ask. The first whose new text defines the
// symbol decides the bucket:
//
//	same-turn       no user prompt between the ask and that edit
//	same-session    a later turn of the same session
//	not-in-session  no such edit in the session
//	no-transcript   the session's transcript is gone
//	no-session      the ask predates #458 or its payload had no session id
//
// LIMITS, printed with every result because they bound what it can mean:
//
//   - Edits made through a shell command are invisible here, as they are to the
//     guard. A callee written that way lands in not-in-session. So
//     not-in-session is an UPPER bound and the two "same" buckets LOWER bounds.
//   - "Defines" is a per-language regex (definesSymbol), not a parser: this
//     package imports no parser, and a spike should not add one. It can miss a
//     form and it can match a non-definition.
//   - A turn boundary is any user-side entry that is not a tool result. That
//     includes a background-task notification, which is right for this
//     question (the agent had stopped) but is not "the human typed something".
//   - Whether a resumed session keeps its id is unverified; if it does not, one
//     working session splits in two here.
//
// Test-only, like its sibling: no production code reads any of this.
package guardstats

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// sessionEdit is one Edit/Write/MultiEdit tool call from a transcript: when it
// was issued, the file, and the text it introduced.
type sessionEdit struct {
	TS   time.Time
	File string
	Text string
}

// sessionTranscript is what the spike needs from one session: its edits (the
// main thread's and its subagents') and the times a new turn began.
type sessionTranscript struct {
	Edits   []sessionEdit
	Prompts []time.Time
}

// sessionFiles is a session's main transcript and its subagents' transcripts.
type sessionFiles struct {
	Main string
	Subs []string
}

// sessionTagOf mirrors cmd/runecho-guard's contractSessionTag, which this
// package cannot import. TestSessionTagOf_MatchesTheGuard pins the two to the
// same literal the guard's own test pins.
func sessionTagOf(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:6])
}

// transcriptsByTag indexes every session transcript under projectsDir by the
// tag the guard would have logged for it. A transcript is <session id>.jsonl
// directly inside a project directory; its subagents' transcripts are any
// .jsonl below <session id>/subagents/.
func transcriptsByTag(projectsDir string) map[string]sessionFiles {
	out := map[string]sessionFiles{}
	mains, _ := filepath.Glob(filepath.Join(projectsDir, "*", "*.jsonl"))
	for _, m := range mains {
		id := strings.TrimSuffix(filepath.Base(m), ".jsonl")
		sf := sessionFiles{Main: m}
		subDir := filepath.Join(filepath.Dir(m), id, "subagents")
		_ = filepath.WalkDir(subDir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
				sf.Subs = append(sf.Subs, p)
			}
			return nil
		})
		out[sessionTagOf(id)] = sf
	}
	return out
}

// readSessionEdits appends the edits found in one transcript file to st, and,
// when prompts is true, the turn boundaries. Subagent transcripts are read with
// prompts false: their user-side entries are the parent's task brief, not a
// point where the session's agent stopped.
func readSessionEdits(path string, st *sessionTranscript, prompts bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	type block struct {
		Type  string `json:"type"`
		Name  string `json:"name"`
		Input struct {
			FilePath  string `json:"file_path"`
			Content   string `json:"content"`
			NewString string `json:"new_string"`
			Edits     []struct {
				NewString string `json:"new_string"`
			} `json:"edits"`
		} `json:"input"`
	}
	// ReadString, not Scanner: one transcript line can hold a whole file's
	// content, and Scanner stops for good at its first over-long token.
	r := bufio.NewReader(f)
	for {
		line, readErr := r.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			var e struct {
				Type      string    `json:"type"`
				Timestamp time.Time `json:"timestamp"`
				Message   struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(line), &e) == nil && !e.Timestamp.IsZero() {
				var blocks []block
				var text string
				if json.Unmarshal(e.Message.Content, &blocks) != nil {
					_ = json.Unmarshal(e.Message.Content, &text)
				}
				switch e.Type {
				case "user":
					isToolResult := false
					for _, b := range blocks {
						if b.Type == "tool_result" {
							isToolResult = true
						}
					}
					if prompts && !isToolResult && (text != "" || len(blocks) > 0) {
						st.Prompts = append(st.Prompts, e.Timestamp)
					}
				case "assistant":
					for _, b := range blocks {
						if b.Type != "tool_use" || b.Input.FilePath == "" {
							continue
						}
						var body string
						switch b.Name {
						case "Write":
							body = b.Input.Content
						case "Edit":
							body = b.Input.NewString
						case "MultiEdit":
							var parts []string
							for _, op := range b.Input.Edits {
								parts = append(parts, op.NewString)
							}
							body = strings.Join(parts, "\n")
						default:
							continue
						}
						st.Edits = append(st.Edits, sessionEdit{TS: e.Timestamp, File: b.Input.FilePath, Text: body})
					}
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

// loadSession reads a session's main transcript and its subagents', sorted.
func loadSession(sf sessionFiles) (sessionTranscript, error) {
	var st sessionTranscript
	if err := readSessionEdits(sf.Main, &st, true); err != nil {
		return st, err
	}
	for _, s := range sf.Subs {
		_ = readSessionEdits(s, &st, false)
	}
	sort.SliceStable(st.Edits, func(i, j int) bool { return st.Edits[i].TS.Before(st.Edits[j].TS) })
	sort.Slice(st.Prompts, func(i, j int) bool { return st.Prompts[i].Before(st.Prompts[j]) })
	return st, nil
}

// definesSymbol reports whether text contains what looks like a definition of
// sym in lang. A heuristic: see the file header. A qualified name ("pkg.Func",
// "Recv.Method") is matched on its last component.
func definesSymbol(lang, text, sym string) bool {
	if i := strings.LastIndex(sym, "."); i >= 0 {
		sym = sym[i+1:]
	}
	if sym == "" {
		return false
	}
	q := regexp.QuoteMeta(sym)
	var pats []string
	switch lang {
	case "go":
		pats = []string{
			`\bfunc\s+(\([^)]*\)\s*)?` + q + `\b`,
			`\btype\s+` + q + `\b`,
			`\b(var|const)\s+` + q + `\b`,
			`(?m)^\s*` + q + `\s*(,[^=\n]*)?:=`,
		}
	case "py":
		pats = []string{
			`(?m)^\s*(async\s+)?def\s+` + q + `\b`,
			`(?m)^\s*class\s+` + q + `\b`,
			`(?m)^` + q + `\s*(:[^=\n]+)?=[^=]`,
		}
	case "js", "ts":
		pats = []string{
			`\bfunction\s*\*?\s*` + q + `\b`,
			`\b(class|interface|type|enum)\s+` + q + `\b`,
			`\b(const|let|var)\s+` + q + `\b`,
			`(?m)^\s*((async|static|export|public|private|protected)\s+)*` + q + `\s*\([^)]*\)\s*(:[^{\n]+)?\{`,
		}
	default:
		return false
	}
	for _, p := range pats {
		if regexp.MustCompile(p).MatchString(text) {
			return true
		}
	}
	return false
}

const (
	bucketSameTurn     = "same-turn"
	bucketSameSession  = "same-session"
	bucketNotInSession = "not-in-session"
	bucketNoTranscript = "no-transcript"
	bucketNoSession    = "no-session"
)

// classifySameSession finds the first edit strictly after askTS that defines
// sym and says whether a turn boundary fell between the ask and it. The edit
// the ask itself was about carries the reference, not the definition, and an
// edit at or before the ask cannot have answered it.
func classifySameSession(askTS time.Time, lang, sym string, st sessionTranscript) (bucket string, def sessionEdit) {
	for _, e := range st.Edits {
		if !e.TS.After(askTS) || !definesSymbol(lang, e.Text, sym) {
			continue
		}
		for _, p := range st.Prompts {
			if p.After(askTS) && !p.After(e.TS) {
				return bucketSameSession, e
			}
		}
		return bucketSameTurn, e
	}
	return bucketNotInSession, sessionEdit{}
}

func TestSpikeSameSessionResolution(t *testing.T) {
	if os.Getenv("RUNECHO_SPIKE_SAME_SESSION") != "1" {
		t.Skip("set RUNECHO_SPIKE_SAME_SESSION=1 to run the same-session spike against a real decision log and real transcripts")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home dir: %v", err)
	}
	logPath := os.Getenv("RUNECHO_SPIKE_DECISIONS_LOG")
	if logPath == "" {
		logPath = filepath.Join(home, ".runecho", "decisions.jsonl")
	}
	projects := os.Getenv("RUNECHO_SPIKE_TRANSCRIPTS")
	if projects == "" {
		projects = filepath.Join(home, ".claude", "projects")
	}
	decisions, err := Load(logPath)
	if err != nil {
		t.Skipf("no decision log at %s: %v", logPath, err)
	}
	days := 30
	if v := os.Getenv("RUNECHO_SPIKE_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("RUNECHO_SPIKE_DAYS must be a positive integer, got %q", v)
		}
		days = n
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	// An ask's session, by the (time, file) pair a finding carries.
	type askKey struct {
		ts   int64
		file string
	}
	sessionOf := map[askKey]string{}
	asks, asksWithSession := 0, 0
	for _, d := range decisions {
		if d.Decision != "ask" || d.Mode != "hook" || d.TS.Before(since) {
			continue
		}
		asks++
		if d.Session != "" {
			asksWithSession++
			sessionOf[askKey{d.TS.Unix(), d.File}] = d.Session
		}
	}

	stats := Audit(decisions, since, GitOracle{})
	index := transcriptsByTag(projects)
	loaded := map[string]*sessionTranscript{}
	counts := map[string]int{}
	sameFile := 0
	var examples []string
	premature := 0
	for _, f := range stats.Findings {
		if f.Verdict != VerdictPremature {
			continue
		}
		premature++
		tag := sessionOf[askKey{f.TS.Unix(), f.File}]
		if tag == "" {
			counts[bucketNoSession]++
			continue
		}
		st, ok := loaded[tag]
		if !ok {
			if sf, found := index[tag]; found {
				if tr, err := loadSession(sf); err == nil {
					st = &tr
				}
			}
			loaded[tag] = st
		}
		if st == nil {
			counts[bucketNoTranscript]++
			continue
		}
		bucket, def := classifySameSession(f.TS, f.Lang, f.Symbol, *st)
		counts[bucket]++
		if bucket != bucketNotInSession && def.File == f.File {
			sameFile++
		}
		if len(examples) < 10 {
			examples = append(examples, fmt.Sprintf("%s %s %s -> %s", f.TS.Format(time.RFC3339), f.Lang, f.Symbol, bucket))
		}
	}

	judged := counts[bucketSameTurn] + counts[bucketSameSession] + counts[bucketNotInSession]
	var b strings.Builder
	fmt.Fprintf(&b, "window=%dd hook asks=%d (with a session: %d) premature symbols=%d transcripts indexed=%d\n",
		days, asks, asksWithSession, premature, len(index))
	fmt.Fprintf(&b, "JUDGED: %d premature symbols had a session AND a transcript\n", judged)
	if judged < 30 {
		fmt.Fprintf(&b, "TOO FEW TO READ: below 30 judged symbols the shares are noise. This run checks the method, not the question.\n")
	}
	for _, k := range []string{bucketSameTurn, bucketSameSession, bucketNotInSession, bucketNoTranscript, bucketNoSession} {
		share := ""
		if judged > 0 && (k == bucketSameTurn || k == bucketSameSession || k == bucketNotInSession) {
			share = fmt.Sprintf("  %.1f%% of judged", 100*float64(counts[k])/float64(judged))
		}
		fmt.Fprintf(&b, "  %-15s %4d%s\n", k, counts[k], share)
	}
	fmt.Fprintf(&b, "defined in the same file as the ask: %d\n", sameFile)
	fmt.Fprintf(&b, "LIMITS: shell-made edits are invisible, so not-in-session is an upper bound and same-turn/same-session lower bounds; \"defines\" is a regex; a turn boundary is any non-tool-result user entry.\n")
	for _, e := range examples {
		fmt.Fprintf(&b, "  %s\n", e)
	}
	t.Log("\n" + b.String())
}

// TestSpikeSameSessionProbe classifies ONE hand-picked (session, symbol, time)
// against that session's real transcript. It exists because the main spike can
// only exercise the transcript path once asks carry a session; this lets the
// path be checked on real files before then, and a surprising bucket be
// inspected afterwards.
//
//	RUNECHO_SPIKE_PROBE='<session id>,<lang>,<symbol>,<RFC3339 ask time>'
func TestSpikeSameSessionProbe(t *testing.T) {
	spec := os.Getenv("RUNECHO_SPIKE_PROBE")
	if spec == "" {
		t.Skip("set RUNECHO_SPIKE_PROBE='<session id>,<lang>,<symbol>,<RFC3339 ask time>' to classify one case against a real transcript")
	}
	parts := strings.Split(spec, ",")
	if len(parts) != 4 {
		t.Fatalf("RUNECHO_SPIKE_PROBE needs 4 comma-separated fields, got %d", len(parts))
	}
	askTS := mustTime(t, parts[3])
	projects := os.Getenv("RUNECHO_SPIKE_TRANSCRIPTS")
	if projects == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("resolve home dir: %v", err)
		}
		projects = filepath.Join(home, ".claude", "projects")
	}
	tag := sessionTagOf(parts[0])
	sf, ok := transcriptsByTag(projects)[tag]
	if !ok {
		t.Fatalf("no transcript for session tag %s under %s", tag, projects)
	}
	st, err := loadSession(sf)
	if err != nil {
		t.Fatal(err)
	}
	bucket, def := classifySameSession(askTS, parts[1], parts[2], st)
	t.Logf("tag=%s subagent transcripts=%d edits=%d turn boundaries=%d -> %s", tag, len(sf.Subs), len(st.Edits), len(st.Prompts), bucket)
	if bucket != bucketNotInSession {
		t.Logf("defined by an edit to %s at %s", def.File, def.TS.Format(time.RFC3339))
	}
}

// writeTranscript writes JSONL transcript lines to path, creating parents.
func writeTranscript(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func trUser(ts, text string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":%q}}`, ts, text)
}

func trToolResult(ts string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","content":"ok"}]}}`, ts)
}

func trWrite(ts, file, content string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":%q,"content":%q}}]}}`, ts, file, content)
}

func trEdit(ts, file, newString string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":%q,"old_string":"x","new_string":%q}}]}}`, ts, file, newString)
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// The tag the spike computes for a transcript must be the tag the guard logs.
// Same literal as cmd/runecho-guard's TestDecisionSessionTag_IsPinned.
func TestSessionTagOf_MatchesTheGuard(t *testing.T) {
	if got, want := sessionTagOf("11111111-2222-3333-4444-555555555555"), "666ff6ccaa5b"; got != want {
		t.Errorf("sessionTagOf = %q, want %q", got, want)
	}
}

func TestTranscriptsByTag_IndexesByHashedStemAndAttachesSubagents(t *testing.T) {
	root := t.TempDir()
	const id = "11111111-2222-3333-4444-555555555555"
	writeTranscript(t, filepath.Join(root, "proj", id+".jsonl"), trUser("2026-10-07T10:00:00Z", "go"))
	writeTranscript(t, filepath.Join(root, "proj", id, "subagents", "agent-a1.jsonl"), trUser("2026-10-07T10:00:01Z", "brief"))
	writeTranscript(t, filepath.Join(root, "proj", id, "subagents", "workflows", "w1", "agent-a2.jsonl"), trUser("2026-10-07T10:00:02Z", "brief"))
	writeTranscript(t, filepath.Join(root, "proj", "other-session.jsonl"), trUser("2026-10-07T10:00:00Z", "go"))

	idx := transcriptsByTag(root)
	sf, ok := idx["666ff6ccaa5b"]
	if !ok {
		t.Fatalf("no entry for the session's tag; have %v", idx)
	}
	if filepath.Base(sf.Main) != id+".jsonl" || len(sf.Subs) != 2 {
		t.Errorf("got main %q and %d subagent transcripts, want the session's own and 2", sf.Main, len(sf.Subs))
	}
	if len(idx) != 2 {
		t.Errorf("indexed %d sessions, want 2 (subagent files are not sessions)", len(idx))
	}
}

func TestReadSessionEdits_EditsAndTurnBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, path,
		trUser("2026-10-07T10:00:00Z", "please add it"),
		trWrite("2026-10-07T10:00:05Z", "/r/a.go", "package a\nfunc Foo() {}\n"),
		trToolResult("2026-10-07T10:00:06Z"),
		trEdit("2026-10-07T10:00:07Z", "/r/b.go", "func Bar() {}"),
		`not json`,
		trUser("2026-10-07T10:05:00Z", "now the next thing"),
	)
	var st sessionTranscript
	if err := readSessionEdits(path, &st, true); err != nil {
		t.Fatal(err)
	}
	if len(st.Edits) != 2 || st.Edits[0].File != "/r/a.go" || !strings.Contains(st.Edits[0].Text, "func Foo") || st.Edits[1].Text != "func Bar() {}" {
		t.Errorf("edits = %+v, want the Write then the Edit with their new text", st.Edits)
	}
	if len(st.Prompts) != 2 {
		t.Errorf("turn boundaries = %d, want 2: a tool result is not one", len(st.Prompts))
	}

	var sub sessionTranscript
	if err := readSessionEdits(path, &sub, false); err != nil {
		t.Fatal(err)
	}
	if len(sub.Prompts) != 0 || len(sub.Edits) != 2 {
		t.Errorf("read as a subagent: prompts=%d edits=%d, want 0 and 2", len(sub.Prompts), len(sub.Edits))
	}
}

func TestDefinesSymbol(t *testing.T) {
	for _, tc := range []struct {
		lang, text, sym string
		want            bool
	}{
		{"go", "func Foo() {}", "Foo", true},
		{"go", "func (s *Server) Foo(x int) error {", "Foo", true},
		{"go", "func (s *Server) Foo(x int) error {", "Server.Foo", true},
		{"go", "type Foo struct{}", "Foo", true},
		{"go", "var Foo = 1", "Foo", true},
		{"go", "\tfoo := bar()", "foo", true},
		{"go", "x := Foo()", "Foo", false},
		{"go", "func Foobar() {}", "Foo", false},
		{"py", "def foo(a):\n    pass", "foo", true},
		{"py", "    async def foo(self):", "foo", true},
		{"py", "class Foo(Base):", "Foo", true},
		{"py", "FOO = 3", "FOO", true},
		{"py", "x = foo(1)", "foo", false},
		{"py", "if foo == 3:", "foo", false},
		{"js", "function foo() {}", "foo", true},
		{"js", "const foo = () => 1", "foo", true},
		{"ts", "export class Foo {", "Foo", true},
		{"ts", "interface Foo {", "Foo", true},
		{"ts", "  async foo(a: number): Promise<void> {", "foo", true},
		{"js", "foo(1)", "foo", false},
		{"js", "return foo(1) {", "foo", false},
		{"rb", "def foo", "foo", false},
		{"go", "func Foo() {}", "", false},
	} {
		if got := definesSymbol(tc.lang, tc.text, tc.sym); got != tc.want {
			t.Errorf("definesSymbol(%q, %q, %q) = %v, want %v", tc.lang, tc.text, tc.sym, got, tc.want)
		}
	}
}

func TestClassifySameSession(t *testing.T) {
	ask := mustTime(t, "2026-10-07T10:00:10Z")
	at := func(s string) time.Time { return mustTime(t, s) }
	def := sessionEdit{File: "/r/a.go", Text: "func Foo() {}"}
	call := sessionEdit{File: "/r/b.go", Text: "x := Foo()"}
	with := func(e sessionEdit, ts string) sessionEdit { e.TS = at(ts); return e }

	for name, tc := range map[string]struct {
		st   sessionTranscript
		want string
	}{
		"defined in the same turn": {
			sessionTranscript{Edits: []sessionEdit{with(call, "2026-10-07T10:00:20Z"), with(def, "2026-10-07T10:00:30Z")}, Prompts: []time.Time{at("2026-10-07T10:00:00Z")}},
			bucketSameTurn,
		},
		"defined after the next prompt": {
			sessionTranscript{Edits: []sessionEdit{with(def, "2026-10-07T10:06:00Z")}, Prompts: []time.Time{at("2026-10-07T10:00:00Z"), at("2026-10-07T10:05:00Z")}},
			bucketSameSession,
		},
		"a prompt after the defining edit does not matter": {
			sessionTranscript{Edits: []sessionEdit{with(def, "2026-10-07T10:00:30Z")}, Prompts: []time.Time{at("2026-10-07T10:05:00Z")}},
			bucketSameTurn,
		},
		"only referenced, never defined": {
			sessionTranscript{Edits: []sessionEdit{with(call, "2026-10-07T10:00:20Z")}},
			bucketNotInSession,
		},
		"defined before the ask": {
			sessionTranscript{Edits: []sessionEdit{with(def, "2026-10-07T10:00:05Z")}},
			bucketNotInSession,
		},
		"defined at the very instant of the ask": {
			sessionTranscript{Edits: []sessionEdit{with(def, "2026-10-07T10:00:10Z")}},
			bucketNotInSession,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, e := classifySameSession(ask, "go", "Foo", tc.st)
			if got != tc.want {
				t.Errorf("bucket = %q, want %q", got, tc.want)
			}
			if tc.want != bucketNotInSession && e.File != "/r/a.go" {
				t.Errorf("defining edit = %+v, want the one to /r/a.go", e)
			}
		})
	}
}

// End to end on files: a session whose callee is written by a subagent in the
// same turn, found through the tag index.
func TestLoadSession_SubagentEditsCountInTheParentsTurn(t *testing.T) {
	root := t.TempDir()
	const id = "11111111-2222-3333-4444-555555555555"
	writeTranscript(t, filepath.Join(root, "proj", id+".jsonl"),
		trUser("2026-10-07T10:00:00Z", "do it"),
		trEdit("2026-10-07T10:00:20Z", "/r/b.go", "x := Foo()"),
	)
	writeTranscript(t, filepath.Join(root, "proj", id, "subagents", "agent-a1.jsonl"),
		trUser("2026-10-07T10:00:25Z", "write Foo"),
		trWrite("2026-10-07T10:00:40Z", "/r/a.go", "package a\nfunc Foo() {}\n"),
	)
	st, err := loadSession(transcriptsByTag(root)[sessionTagOf(id)])
	if err != nil {
		t.Fatal(err)
	}
	bucket, def := classifySameSession(mustTime(t, "2026-10-07T10:00:10Z"), "go", "Foo", st)
	if bucket != bucketSameTurn || def.File != "/r/a.go" {
		t.Errorf("bucket=%q def=%+v, want same-turn via the subagent's Write (its task brief is not a turn boundary)", bucket, def)
	}
}
