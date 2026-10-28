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
// id, and a transcript's filename is that id; askSessions joins a finding back
// to its record), then look at the Edit, Write and MultiEdit calls that
// followed the ask. The first one that counts (see below) and whose new text
// defines the symbol decides the bucket:
//
//	same-turn       no turn boundary between the ask and that edit
//	same-session    a turn boundary fell between them
//	not-in-session  no such edit in the session
//	no-transcript   the session's transcript (or one of its subagents') is gone
//	                or unreadable, or the session id exists under more than one
//	                project directory
//	no-session      the ask predates #458 or its payload had no session id; or its
//	                key (stamp to the nanosecond, file, reason) is shared by
//	                another kept ask with a different session or none, or one of
//	                its own duplicate fires (within 5 s) names another session
//
// WHAT COUNTS AS AN EDIT AFTER THE ASK (classifySameSession, readSessionEdits).
//
//   - Its timestamp is at or after the START OF THE SECOND AFTER the ask's. The
//     guard stamps asks at one-second resolution (truncated) and transcripts at
//     milliseconds, so an edit stamped inside the ask's own second may be the
//     very edit that raised the ask, or a parallel call issued with it. Those
//     are dropped. A genuine answer issued within that second is lost too.
//   - Its tool_result is not an error (a rejected or failed Edit/Write defines
//     nothing). An edit whose result never appears in the transcript still
//     counts.
//   - It lies under the ask file's root, and, for a language the oracle has a
//     pathspec for (langPathspec), has one of that language's extensions. The
//     root is the one GitOracle.Worktree resolves for the ask's file (repoRootOf);
//     when it resolves none, only the ask's own file counts. An unknown language
//     has no extension restriction, as in the oracle.
//   - Its new text matches the git oracle's declPatterns (any such file) or
//     bindPatterns (only the ask's own file); a ScopeFile finding counts only
//     edits to the ask's own file. The same regexes, applied per line as git
//     grep does, to an edit's new text, which is a fragment.
//   - bindPatterns' bare-name import-member forms are gated, in the oracle, on
//     the whole file opening a multi-line import. Here the file is the last Write
//     to it before the edit, else the file on disk now (later than the session,
//     so it can over-credit), else nothing; a MultiEdit/Edit-only session
//     against a vanished file therefore misses such members.
//   - Inherited oracle gaps, not fixed here: a column-0 `foo == 3` reads as a
//     Python binding, and callback-style `  foo(function () {` in the ask's own
//     file reads as a JS method definition.
//
// TURN BOUNDARY (readSessionEdits). A user-side entry of the main transcript
// that is not a tool_result, not isSidechain, not a compaction summary, carries
// no sourceToolUseID, has non-empty content, and, when it is isMeta, arrives
// while the agent is idle (the last assistant entry issued no tool call). So a
// skill body mid-turn is not a boundary; a peer message or scheduled wake-up
// that arrives at rest is, like a background-task notification. A non-meta
// entry always counts, including one typed mid-turn or after an interruption. A
// boundary p separates ask and edit when from <= p <= edit TS, with from the
// start of the second after the ask's. Subagent transcripts contribute edits
// but no boundaries.
//
// LIMITS, printed with every result because they bound what it can mean. Errors
// run BOTH ways, so no bucket is a bound:
//
//   - Edits made through a shell command are invisible here, as they are to the
//     guard. A callee written that way lands in not-in-session (this pushes
//     towards not-in-session).
//   - The regexes (above) are not a parser and can match a non-definition or
//     miss a form (pushes towards the "same" buckets, or away, respectively).
//   - A callee written earlier in the session but not yet committed may be
//     rated premature by the oracle and then lands in not-in-session.
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
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// sessionEdit is one Edit/Write/MultiEdit tool call from a transcript: when it
// was issued, the file, and the text it introduced.
type sessionEdit struct {
	TS   time.Time
	File string
	Text string
	ID   string // the tool_use id, used to find its tool_result
	// Whole marks a Write: Text is the file's entire new content, which is what
	// the oracle's per-file questions (an open multi-line import) are asked of.
	Whole bool
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
	// Dups are further main transcripts of the same session id under other
	// project directories. loadSession refuses such a session rather than
	// silently picking one.
	Dups []string
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
		tag := sessionTagOf(id)
		if prev, dup := out[tag]; dup {
			prev.Dups = append(prev.Dups, m)
			out[tag] = prev
			continue
		}
		out[tag] = sf
	}
	return out
}

// readSessionEdits appends the edits found in one transcript file to st, and,
// when prompts is true, the turn boundaries (see the file header for the rule).
// Subagent transcripts are read with prompts false: their user-side entries are
// the parent's task brief, not a point where the session's agent stopped. An
// edit whose tool_result is an error is dropped.
func readSessionEdits(path string, st *sessionTranscript, prompts bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	type block struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		ID        string `json:"id"`
		ToolUseID string `json:"tool_use_id"`
		IsError   bool   `json:"is_error"`
		Input     struct {
			FilePath  string `json:"file_path"`
			Content   string `json:"content"`
			NewString string `json:"new_string"`
			Edits     []struct {
				NewString string `json:"new_string"`
			} `json:"edits"`
		} `json:"input"`
	}
	start := len(st.Edits)
	failed := map[string]bool{}
	// pending: the last assistant entry issued a tool call, so the agent is
	// mid-turn and a meta entry now (a skill body) is not a new turn.
	pending := false
	finish := func() {
		kept := st.Edits[:start]
		for _, e := range st.Edits[start:] {
			if e.ID == "" || !failed[e.ID] {
				kept = append(kept, e)
			}
		}
		st.Edits = kept
	}
	// ReadString, not Scanner: one transcript line can hold a whole file's
	// content, and Scanner stops for good at its first over-long token.
	r := bufio.NewReader(f)
	for {
		line, readErr := r.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			var e struct {
				Type             string    `json:"type"`
				Timestamp        time.Time `json:"timestamp"`
				IsMeta           bool      `json:"isMeta"`
				IsSidechain      bool      `json:"isSidechain"`
				IsCompactSummary bool      `json:"isCompactSummary"`
				SourceToolUseID  string    `json:"sourceToolUseID"`
				Message          struct {
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
							if b.IsError && b.ToolUseID != "" {
								failed[b.ToolUseID] = true
							}
						}
					}
					notABoundary := e.IsSidechain || e.IsCompactSummary || e.SourceToolUseID != "" || (e.IsMeta && pending)
					if prompts && !isToolResult && !notABoundary && (text != "" || len(blocks) > 0) {
						st.Prompts = append(st.Prompts, e.Timestamp)
					}
				case "assistant":
					pending = false
					for _, b := range blocks {
						if b.Type == "tool_use" {
							pending = true
						}
					}
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
						st.Edits = append(st.Edits, sessionEdit{TS: e.Timestamp, File: b.Input.FilePath, Text: body, ID: b.ID, Whole: b.Name == "Write"})
					}
				}
			}
		}
		if readErr != nil {
			finish()
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

// loadSession reads a session's main transcript and its subagents', sorted. A
// session id found under more than one project directory is refused: which copy
// is the real one is not knowable here.
func loadSession(sf sessionFiles) (sessionTranscript, error) {
	var st sessionTranscript
	if len(sf.Dups) > 0 {
		return st, fmt.Errorf("session transcript exists in %d project directories: %s and %s", 1+len(sf.Dups), sf.Main, strings.Join(sf.Dups, ", "))
	}
	if err := readSessionEdits(sf.Main, &st, true); err != nil {
		return st, err
	}
	for _, s := range sf.Subs {
		if err := readSessionEdits(s, &st, false); err != nil {
			return st, err
		}
	}
	sort.SliceStable(st.Edits, func(i, j int) bool { return st.Edits[i].TS.Before(st.Edits[j].TS) })
	sort.Slice(st.Prompts, func(i, j int) bool { return st.Prompts[i].Before(st.Prompts[j]) })
	return st, nil
}

// sameSessionAsk is what classifySameSession needs to know about one premature
// finding.
type sameSessionAsk struct {
	TS     time.Time  // the ask's stamp, second resolution
	Lang   string     // as the guard logs it: go, js (incl. ts/tsx/jsx/gs) or py
	Symbol string     // possibly qualified; its last component is searched
	Scope  ClaimScope // the finding's own question; "" is read as ScopeRepo
	File   string     // the edited file the ask was about
	// Root is the repo root of File. When empty, only File itself can define the
	// symbol; when File is empty too (the probe) nothing is restricted.
	Root string
}

// spikeRegexps caches the compiled oracle patterns: they are searched once per
// line per edit per finding.
var spikeRegexps sync.Map

// spikeRegexp compiles an oracle pattern for Go's engine. The oracle's patterns
// are POSIX EREs that git grep applies to ONE LINE at a time, so they are
// compiled without (?m) and only ever given a single line (matchesAny): under
// RE2 a [[:space:]]* or [^=]* would otherwise run across a newline.
func spikeRegexp(pat string) *regexp.Regexp {
	if v, ok := spikeRegexps.Load(pat); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile(pat)
	spikeRegexps.Store(pat, re)
	return re
}

// matchesAny reports whether any pattern matches any single line of text, which
// is what `git grep -E -e p1 -e p2` does.
func matchesAny(pats []string, text string) bool {
	for _, line := range strings.Split(text, "\n") {
		for _, p := range pats {
			if spikeRegexp(p).MatchString(line) {
				return true
			}
		}
	}
	return false
}

// repoRootOf is the root the ask's file lived under, taken from the same
// resolver the audit used (GitOracle.Worktree), or "" when that cannot place
// the file. Worktree may substitute a SIBLING worktree when the recorded one is
// gone; the file is then still <recorded root>/<rel>, so the recorded root is
// recovered by cutting rel off the file path, and edits made in that vanished
// worktree still sit under it.
func repoRootOf(file string) string {
	root, rel, err := GitOracle{}.Worktree(file)
	if err != nil {
		return ""
	}
	if rel != "" {
		if cut := strings.TrimSuffix(filepath.ToSlash(file), "/"+rel); cut != filepath.ToSlash(file) {
			return filepath.FromSlash(cut)
		}
	}
	return root
}

// editCanDefine applies the file-level rules from the header: which edits may
// define the ask's symbol at all.
func editCanDefine(ask sameSessionAsk, e sessionEdit) bool {
	sameFile := e.File == ask.File
	if ask.Scope == ScopeFile && !sameFile {
		return false
	}
	switch {
	case ask.Root != "":
		if !strings.HasPrefix(e.File, strings.TrimSuffix(ask.Root, "/")+"/") {
			return false
		}
	case ask.File != "":
		if !sameFile {
			return false
		}
	}
	globs, known := langPathspec[ask.Lang]
	if !known {
		// The oracle searches an unknown language's default pattern with no
		// pathspec, so any file is eligible.
		return true
	}
	for _, g := range globs {
		if ok, _ := filepath.Match(g, filepath.Base(e.File)); ok {
			return true
		}
	}
	return false
}

// definesSymbol reports whether e introduces a definition of the ask's symbol,
// by the git oracle's own patterns: declPatterns anywhere editCanDefine allows,
// bindPatterns only in the ask's own file. A qualified name is matched on its
// last component, as the oracle does. fileText is the best-known whole content
// of the ask's file at the time of e (see knownFileText); the oracle gates the
// bare-name import-member patterns on the WHOLE file opening a multi-line
// import, and an edit fragment seldom contains the opener.
func definesSymbol(ask sameSessionAsk, e sessionEdit, fileText string) bool {
	needle := ask.Symbol
	if i := strings.LastIndexByte(needle, '.'); i >= 0 {
		needle = needle[i+1:]
	}
	if !identRe.MatchString(needle) || !editCanDefine(ask, e) {
		return false
	}
	if matchesAny(declPatterns(ask.Lang, needle), e.Text) {
		return true
	}
	if e.File != ask.File {
		return false
	}
	inImport := false
	if opener, ok := multilineImportOpener[ask.Lang]; ok {
		inImport = matchesAny([]string{opener}, fileText) || matchesAny([]string{opener}, e.Text)
	}
	return matchesAny(bindPatterns(ask.Lang, needle, inImport), e.Text)
}

const (
	bucketSameTurn     = "same-turn"
	bucketSameSession  = "same-session"
	bucketNotInSession = "not-in-session"
	bucketNoTranscript = "no-transcript"
	bucketNoSession    = "no-session"
)

// knownFileText is the best available whole content of the ask's file as of
// time at: the last Write to it before then, else the file on disk now (which
// is later than the session, so it can over-credit an import block added after
// the edit), else "".
func knownFileText(ask sameSessionAsk, st sessionTranscript, at time.Time) string {
	var last *sessionEdit
	for i := range st.Edits {
		if e := &st.Edits[i]; e.Whole && e.File == ask.File && e.TS.Before(at) {
			last = e
		}
	}
	if last != nil {
		return last.Text
	}
	if ask.File != "" {
		if b, err := os.ReadFile(ask.File); err == nil {
			return string(b)
		}
	}
	return ""
}

// classifySameSession finds the first edit that counts (file header) and
// defines the symbol, and says whether a turn boundary fell between the ask and
// it. The ask's stamp is truncated to the second, so edits and boundaries before
// the start of the NEXT second are both skipped: an edit in that second may be
// the one that raised the ask, and a prompt in it cannot be a reply to it.
func classifySameSession(ask sameSessionAsk, st sessionTranscript) (bucket string, def sessionEdit) {
	from := ask.TS.Truncate(time.Second).Add(time.Second)
	for _, e := range st.Edits {
		if e.TS.Before(from) || !definesSymbol(ask, e, knownFileText(ask, st, e.TS)) {
			continue
		}
		for _, p := range st.Prompts {
			if !p.Before(from) && !p.After(e.TS) {
				return bucketSameSession, e
			}
		}
		return bucketSameTurn, e
	}
	return bucketNotInSession, sessionEdit{}
}

// askKey identifies a kept (post-collapse) ask. Seconds are not unique across
// sessions, so the full stamp, file and reason all take part; two different
// sessions still sharing one key are ambiguous and resolve to no session.
type askKey struct {
	ts           int64
	file, reason string
}

func keyOf(ts time.Time, file, reason string) askKey {
	return askKey{ts.UnixNano(), file, reason}
}

// askSessions maps each ask Audit would keep to its session tag. It collapses
// duplicate hook fires exactly as Audit does (same filter, same prev, same
// isDuplicateFire) and keeps the FIRST fire's stamp, which is the stamp the
// finding carries. Only a true duplicate fire may lend its session to the kept
// ask that lacks one; two fires of one ask naming different sessions make that
// ask ambiguous. Then, per key, kept asks that disagree (including one with a
// session and one without) make the key ambiguous, which resolves to "" and so
// to no-session. asks equals Audit's stats.Asks; withSession counts kept asks
// (not keys) that resolved to a session.
func askSessions(decisions []Decision, since time.Time) (m map[askKey]string, asks, withSession int) {
	type kept struct {
		key      askKey
		session  string
		conflict bool
	}
	var list []kept
	var prev *Decision
	for i := range decisions {
		d := &decisions[i]
		if d.Decision != "ask" || d.Mode != "hook" || d.TS.Before(since) {
			continue
		}
		if prev != nil && isDuplicateFire(*prev, *d) {
			last := &list[len(list)-1]
			switch {
			case d.Session == "":
			case last.session == "":
				last.session = d.Session
			case last.session != d.Session:
				last.conflict = true
			}
			continue
		}
		prev = d
		list = append(list, kept{key: keyOf(d.TS, d.File, d.Reason), session: d.Session})
	}
	m = map[askKey]string{}
	// An ambiguous key is stored as "", which differs from every real session,
	// so a later ask on it can never revive it.
	for _, k := range list {
		if cur, seen := m[k.key]; k.conflict || (seen && cur != k.session) {
			m[k.key] = ""
			continue
		}
		m[k.key] = k.session
	}
	for _, k := range list {
		if m[k.key] != "" {
			withSession++
		}
	}
	return m, len(list), withSession
}

// groupBySession orders findings so those of one session are adjacent (stable
// within a session), letting the caller hold one transcript in memory at a time.
func groupBySession(fs []AuditFinding, sessionOf map[askKey]string) []AuditFinding {
	out := append([]AuditFinding(nil), fs...)
	sort.SliceStable(out, func(i, j int) bool {
		return sessionOf[keyOf(out[i].TS, out[i].File, out[i].Reason)] < sessionOf[keyOf(out[j].TS, out[j].File, out[j].Reason)]
	})
	return out
}

// resolveFinding buckets one premature finding. loaded caches per session tag
// (a nil entry means "tried, could not load").
func resolveFinding(f AuditFinding, sessionOf map[askKey]string, index map[string]sessionFiles, loaded map[string]*sessionTranscript, rootOf func(file string) string) (string, sessionEdit) {
	tag := sessionOf[keyOf(f.TS, f.File, f.Reason)]
	if tag == "" {
		return bucketNoSession, sessionEdit{}
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
		return bucketNoTranscript, sessionEdit{}
	}
	return classifySameSession(sameSessionAsk{
		TS: f.TS, Lang: f.Lang, Symbol: f.Symbol, Scope: f.Scope, File: f.File, Root: rootOf(f.File),
	}, *st)
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

	sessionOf, asks, asksWithSession := askSessions(decisions, since)

	stats := Audit(decisions, since, GitOracle{})
	index := transcriptsByTag(projects)
	loaded := map[string]*sessionTranscript{}
	roots := map[string]string{}
	rootOf := func(file string) string {
		r, ok := roots[file]
		if !ok {
			r = repoRootOf(file)
			roots[file] = r
		}
		return r
	}
	counts := map[string]int{}
	sameFile := 0
	var examples []string
	var prem []AuditFinding
	for _, f := range stats.Findings {
		if f.Verdict == VerdictPremature {
			prem = append(prem, f)
		}
	}
	premature := len(prem)
	lastTag := ""
	for _, f := range groupBySession(prem, sessionOf) {
		// One transcript in memory at a time: findings arrive grouped by session.
		if tag := sessionOf[keyOf(f.TS, f.File, f.Reason)]; tag != lastTag {
			clear(loaded)
			lastTag = tag
		}
		bucket, def := resolveFinding(f, sessionOf, index, loaded, rootOf)
		counts[bucket]++
		if (bucket == bucketSameTurn || bucket == bucketSameSession) && def.File == f.File {
			sameFile++
		}
		if len(examples) < 10 {
			examples = append(examples, fmt.Sprintf("%s %s %s -> %s", f.TS.Format(time.RFC3339), f.Lang, f.Symbol, bucket))
		}
	}

	judged := counts[bucketSameTurn] + counts[bucketSameSession] + counts[bucketNotInSession]
	var b strings.Builder
	fmt.Fprintf(&b, "window=%dd hook asks=%d after collapsing duplicate fires (with a session: %d) premature symbols=%d transcripts indexed=%d\n",
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
	fmt.Fprintf(&b, "LIMITS: errors run both ways, no bucket is a bound. Shell-made edits are invisible (towards not-in-session); \"defines\" is the oracle's regexes over edit fragments (false matches and misses); edits and turn boundaries inside the ask's own second are dropped; the import-block check uses the last known whole file (see the file header).\n")
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
//	RUNECHO_SPIKE_PROBE='<session id>,<lang>,<symbol>,<RFC3339 ask time>[,<ask file>]'
//
// Without an ask file nothing is restricted by file or repo root, and the scope
// is repo-wide, so bindings (own-file only) never count; with one, the
// classification is the main test's.
func TestSpikeSameSessionProbe(t *testing.T) {
	spec := os.Getenv("RUNECHO_SPIKE_PROBE")
	if spec == "" {
		t.Skip("set RUNECHO_SPIKE_PROBE='<session id>,<lang>,<symbol>,<RFC3339 ask time>[,<ask file>]' to classify one case against a real transcript")
	}
	parts := strings.Split(spec, ",")
	if len(parts) != 4 && len(parts) != 5 {
		t.Fatalf("RUNECHO_SPIKE_PROBE needs 4 or 5 comma-separated fields, got %d", len(parts))
	}
	ask := sameSessionAsk{TS: mustTime(t, parts[3]), Lang: parts[1], Symbol: parts[2]}
	if len(parts) == 5 {
		ask.File = parts[4]
		ask.Root = repoRootOf(ask.File)
	}
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
	bucket, def := classifySameSession(ask, st)
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

func trWrite(ts, file, content string) string { return trWriteID(ts, "", file, content) }

func trWriteID(ts, id, file, content string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":"Write","input":{"file_path":%q,"content":%q}}]}}`, ts, id, file, content)
}

func trEdit(ts, file, newString string) string { return trEditID(ts, "", file, newString) }

func trEditID(ts, id, file, newString string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":"Edit","input":{"file_path":%q,"old_string":"x","new_string":%q}}]}}`, ts, id, file, newString)
}

func trMultiEdit(ts, file string, newStrings ...string) string {
	var ops []string
	for _, n := range newStrings {
		ops = append(ops, fmt.Sprintf(`{"old_string":"x","new_string":%q}`, n))
	}
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":"m1","name":"MultiEdit","input":{"file_path":%q,"edits":[%s]}}]}}`, ts, file, strings.Join(ops, ","))
}

func trResult(ts, id string, isErr bool) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"is_error":%t,"content":"r"}]}}`, ts, id, isErr)
}

// trUserWith is a user text entry carrying extra top-level JSON fields, e.g.
// `"isMeta":true`.
func trAssistantText(ts, text string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"text","text":%q}]}}`, ts, text)
}

func trToolUse(ts, name string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":"t1","name":%q,"input":{}}]}}`, ts, name)
}

func trUserWith(ts, text, extra string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,%s,"message":{"content":%q}}`, ts, extra, text)
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

// Mutation: delete the `failed[...] = true` line (or the filter in finish) ->
// the rejected Write below stays in Edits and this FAILS.
func TestReadSessionEdits_DropsEditsWhoseResultIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, path,
		trWriteID("2026-10-07T10:00:05Z", "w1", "/r/a.go", "func Foo() {}"),
		trResult("2026-10-07T10:00:06Z", "w1", true),
		trEditID("2026-10-07T10:00:07Z", "e1", "/r/b.go", "func Bar() {}"),
		trResult("2026-10-07T10:00:08Z", "e1", false),
		trEditID("2026-10-07T10:00:09Z", "e2", "/r/c.go", "func Baz() {}"), // no result at all
	)
	var st sessionTranscript
	if err := readSessionEdits(path, &st, true); err != nil {
		t.Fatal(err)
	}
	if len(st.Edits) != 2 || st.Edits[0].File != "/r/b.go" || st.Edits[1].File != "/r/c.go" {
		t.Errorf("edits = %+v, want only the successful one and the one with no result", st.Edits)
	}
	if len(st.Prompts) != 0 {
		t.Errorf("tool results counted as turn boundaries: %d", len(st.Prompts))
	}
}

// Mutation (each row names the clause it kills): drop `e.IsSidechain`,
// `e.IsCompactSummary`, `e.SourceToolUseID != ""` or `(e.IsMeta && pending)` from
// notABoundary; the matching row then counts an entry it must not (or, for the
// idle rows, replace `pending` by `true` and the peer-message row fails).
func TestReadSessionEdits_TurnBoundaryRule(t *testing.T) {
	for name, tc := range map[string]struct {
		lines []string
		want  int
	}{
		"a real prompt":                {[]string{trUser("2026-10-07T10:00:00Z", "hi")}, 1},
		"sidechain":                    {[]string{trUserWith("2026-10-07T10:00:00Z", "x", `"isSidechain":true`)}, 0},
		"compaction summary":           {[]string{trUserWith("2026-10-07T10:00:00Z", "x", `"isCompactSummary":true`)}, 0},
		"sourceToolUseID even if idle": {[]string{trAssistantText("2026-10-07T10:00:00Z", "done"), trUserWith("2026-10-07T10:00:01Z", "x", `"sourceToolUseID":"toolu_1"`)}, 0},
		"skill body mid-turn":          {[]string{trToolUse("2026-10-07T10:00:00Z", "Skill"), trToolResult("2026-10-07T10:00:01Z"), trUserWith("2026-10-07T10:00:02Z", "skill", `"isMeta":true`)}, 0},
		"peer message while idle":      {[]string{trAssistantText("2026-10-07T10:00:00Z", "done"), trUserWith("2026-10-07T10:00:01Z", "Another Claude session sent a message", `"isMeta":true`)}, 1},
		"meta with nothing before":     {[]string{trUserWith("2026-10-07T10:00:01Z", "wake", `"isMeta":true`)}, 1},
		"non-meta prompt mid-turn":     {[]string{trToolUse("2026-10-07T10:00:00Z", "Bash"), trUser("2026-10-07T10:00:01Z", "stop")}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			writeTranscript(t, path, tc.lines...)
			var st sessionTranscript
			if err := readSessionEdits(path, &st, true); err != nil {
				t.Fatal(err)
			}
			if len(st.Prompts) != tc.want {
				t.Errorf("turn boundaries = %d, want %d", len(st.Prompts), tc.want)
			}
		})
	}
}

// Mutation: rename `case "MultiEdit"` -> no edit is read and this FAILS.
func TestReadSessionEdits_MultiEditJoinsItsNewStrings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, path, trMultiEdit("2026-10-07T10:00:05Z", "/r/a.go", "var A = 1", "func Foo() {}"))
	var st sessionTranscript
	if err := readSessionEdits(path, &st, true); err != nil {
		t.Fatal(err)
	}
	if len(st.Edits) != 1 || !strings.Contains(st.Edits[0].Text, "func Foo") || !strings.Contains(st.Edits[0].Text, "var A") {
		t.Errorf("edits = %+v, want one edit holding both new strings", st.Edits)
	}
}

func TestOraclePatternsCompileForEveryLanguage(t *testing.T) {
	for lang := range langPathspec {
		for _, inImport := range []bool{false, true} {
			for _, p := range append(declPatterns(lang, "Foo"), bindPatterns(lang, "Foo", inImport)...) {
				_ = spikeRegexp(p) // panics on a pattern Go's engine rejects
			}
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=spike", "-c", "user.email=spike@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// A linked worktree has a .git FILE, not a directory; the resolver the audit
// uses (GitOracle.Worktree) handles it, which a hand-rolled IsDir walk would not.
func TestRepoRootOf_LiveRepoAndLinkedWorktree(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(base, "main")
	if err := os.MkdirAll(filepath.Join(main, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "-q")
	if err := os.WriteFile(filepath.Join(main, "pkg", "x.go"), []byte("package pkg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "add", "pkg/x.go")
	gitIn(t, main, "commit", "-q", "-m", "init")
	wt := filepath.Join(base, "wt")
	gitIn(t, main, "worktree", "add", "-q", "-b", "side", wt)
	if got := repoRootOf(filepath.Join(main, "pkg", "x.go")); got != main {
		t.Errorf("repoRootOf(main) = %q, want %q", got, main)
	}
	if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
		t.Fatalf("test setup: want a .git FILE in the linked worktree (%v)", err)
	}
	if got := repoRootOf(filepath.Join(wt, "pkg", "x.go")); got != wt {
		t.Errorf("repoRootOf(linked worktree) = %q, want %q", got, wt)
	}
}

// The worktree is gone and Worktree adopts a sibling; the root returned must be
// the VANISHED worktree's (where the session's edits were made), not the
// sibling's, and not the container. Mutation: return `root` unconditionally in
// repoRootOf -> the sibling's path comes back and this FAILS.
func TestRepoRootOf_VanishedWorktreeKeepsItsOwnRoot(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(base, "main")
	if err := os.MkdirAll(filepath.Join(main, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "init", "-q")
	if err := os.WriteFile(filepath.Join(main, "pkg", "x.go"), []byte("package pkg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, main, "add", "pkg/x.go")
	gitIn(t, main, "commit", "-q", "-m", "init")
	gone := filepath.Join(base, "claude-gone")
	if got, want := repoRootOf(filepath.Join(gone, "pkg", "x.go")), gone; got != want {
		t.Errorf("repoRootOf(vanished worktree) = %q, want %q", got, want)
	}
	if got := repoRootOf("/nonexistent-dir-for-spike/x.go"); got != "" {
		t.Errorf("repoRootOf outside any repo = %q, want empty", got)
	}
}

// Each row's mutation is named in the comment on it. The patterns are the git
// oracle's own (declPatterns/bindPatterns), so this pins how the spike applies
// them: scope, file, language, root.
func TestDefinesSymbol(t *testing.T) {
	const root = "/r"
	for _, tc := range []struct {
		name      string
		lang      string
		scope     ClaimScope
		askFile   string
		editFile  string
		text, sym string
		want      bool
	}{
		{"go func", "go", ScopeRepo, "/r/b.go", "/r/a.go", "func Foo() {}", "Foo", true},
		// Multi-line fragment: a definition on a later line is still found.
		{"go func on a later line", "go", ScopeRepo, "/r/b.go", "/r/a.go", "package a\n\nfunc Foo() {}\n", "Foo", true},
		{"go method", "go", ScopeRepo, "/r/b.go", "/r/a.go", "func (s *Server) Foo(x int) error {", "Foo", true},
		{"go qualified", "go", ScopeRepo, "/r/b.go", "/r/a.go", "func (s *Server) Foo(x int) error {", "Server.Foo", true},
		{"go type", "go", ScopeRepo, "/r/b.go", "/r/a.go", "type Foo struct{}", "Foo", true},
		{"go call is not a definition", "go", ScopeRepo, "/r/b.go", "/r/a.go", "x := Foo()", "Foo", false},
		{"go longer name", "go", ScopeRepo, "/r/b.go", "/r/a.go", "func Foobar() {}", "Foo", false},
		{"go empty symbol", "go", ScopeRepo, "/r/b.go", "/r/a.go", "func Foo() {}", "", false},
		{"go unsafe symbol", "go", ScopeRepo, "/r/b.go", "/r/a.go", "func Foo() {}", "Fo.o-", false},
		// A mention in a comment or string is not a definition (the reviewer's A2
		// probes). Mutation: swap in a plain-substring matcher.
		{"go comment", "go", ScopeRepo, "/r/b.go", "/r/a.go", "// func Foo is not written yet", "Foo", false},
		{"go todo comment", "go", ScopeRepo, "/r/b.go", "/r/a.go", "// TODO: add type Foo later", "Foo", false},
		{"go string", "go", ScopeRepo, "/r/b.go", "/r/a.go", `s := "func Foo()"`, "Foo", false},
		// Bindings count only in the ask's own file. Mutation: drop the
		// `e.File != ask.File` early return in definesSymbol.
		{"go binding in the ask's file", "go", ScopeRepo, "/r/a.go", "/r/a.go", "\tfoo := bar()", "foo", true},
		{"go binding elsewhere", "go", ScopeRepo, "/r/b.go", "/r/a.go", "\tfoo := bar()", "foo", false},
		// The Python case from the review: a later `from pathlib import Path` in
		// the ask's own file defines it for a file-scope finding.
		{"py import in the ask's file, file scope", "py", ScopeFile, "/r/a.py", "/r/a.py", "from pathlib import Path", "Path", true},
		{"py import in the ask's file, repo scope", "py", ScopeRepo, "/r/a.py", "/r/a.py", "from pathlib import Path", "Path", true},
		{"py import elsewhere", "py", ScopeRepo, "/r/a.py", "/r/b.py", "from pathlib import Path", "Path", false},
		// Mutation: ignore ask.Scope in editCanDefine.
		{"py def elsewhere, file scope", "py", ScopeFile, "/r/a.py", "/r/b.py", "def Path(): pass", "Path", false},
		{"py def elsewhere, repo scope", "py", ScopeRepo, "/r/a.py", "/r/b.py", "def Path(): pass", "Path", true},
		{"py async method", "py", ScopeRepo, "/r/a.py", "/r/b.py", "    async def foo(self):", "foo", true},
		{"py call", "py", ScopeRepo, "/r/a.py", "/r/b.py", "x = foo(1)", "foo", false},
		{"js function", "js", ScopeRepo, "/r/a.js", "/r/b.js", "function foo() {}", "foo", true},
		// TS arrives as lang js. Mutation: drop *.ts/*.tsx from the extension check.
		{"ts interface under js", "js", ScopeRepo, "/r/a.ts", "/r/b.ts", "export interface Foo {", "Foo", true},
		{"tsx class under js", "js", ScopeRepo, "/r/a.tsx", "/r/b.tsx", "export class Foo {", "Foo", true},
		{"js exported const", "js", ScopeRepo, "/r/a.js", "/r/b.js", "export const foo = () => 1", "foo", true},
		{"js private const elsewhere", "js", ScopeRepo, "/r/a.js", "/r/b.js", "const foo = () => 1", "foo", false},
		{"js call", "js", ScopeRepo, "/r/a.js", "/r/b.js", "foo(1)", "foo", false},
		// callback-style call in ANOTHER file: not a binding there.
		{"js callback call elsewhere", "js", ScopeRepo, "/r/a.js", "/r/b.js", "  foo(function () {", "foo", false},
		// File rules. Mutation: drop the extension check.
		{"plan markdown", "go", ScopeRepo, "/r/a.go", "/r/plan.md", "func Foo() {}", "Foo", false},
		{"other language's file", "go", ScopeRepo, "/r/a.go", "/r/x.py", "func Foo() {}", "Foo", false},
		// Mutation: drop the Root prefix check.
		{"outside the repo root", "go", ScopeRepo, "/r/a.go", "/other/repo/a.go", "func Foo() {}", "Foo", false},
		{"sibling directory with the root as a prefix", "go", ScopeRepo, "/r/a.go", "/rr/a.go", "func Foo() {}", "Foo", false},
		// Unknown language: the oracle's default pattern, no extension limit.
		// Mutation: return false for !known in editCanDefine.
		{"unknown language, default pattern", "rb", ScopeRepo, "/r/a.rb", "/r/b.txt", "foo = 1", "foo", true},
		{"unknown language, a call", "rb", ScopeRepo, "/r/a.rb", "/r/b.rb", "bar(foo)", "foo", false},
		// RE2 classes cross \n; git grep is per line. Mutation: match the whole
		// text at once (drop the strings.Split in matchesAny) or compile with (?m).
		{"py annotation-only lines before an assignment", "py", ScopeRepo, "/r/a.py", "/r/a.py", "class X:\n    foo: int\n    bar: str = \"\"", "foo", false},
		{"py bare annotation then assignment", "py", ScopeRepo, "/r/a.py", "/r/a.py", "foo: int\nbar = 1", "foo", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ask := sameSessionAsk{Lang: tc.lang, Symbol: tc.sym, Scope: tc.scope, File: tc.askFile, Root: root}
			if got := definesSymbol(ask, sessionEdit{File: tc.editFile, Text: tc.text}, ""); got != tc.want {
				t.Errorf("definesSymbol = %v, want %v", got, tc.want)
			}
		})
	}
	// No root known: only the ask's own file can define it.
	ask := sameSessionAsk{Lang: "go", Symbol: "Foo", File: "/gone/a.go"}
	if definesSymbol(ask, sessionEdit{File: "/gone/b.go", Text: "func Foo() {}"}, "") || !definesSymbol(ask, sessionEdit{File: "/gone/a.go", Text: "func Foo() {}"}, "") {
		t.Error("with no repo root, want only the ask's own file to count")
	}
}

// Defects INHERITED from the oracle's patterns, pinned so that a change on the
// oracle side is noticed here rather than silently shifting buckets. These are
// wrong answers on purpose; they are limits, not intent.
func TestDefinesSymbol_InheritedOracleGaps(t *testing.T) {
	ask := sameSessionAsk{Lang: "js", Symbol: "foo", File: "/r/a.js", Root: "/r"}
	if !definesSymbol(ask, sessionEdit{File: "/r/a.js", Text: "  foo(function () {"}, "") {
		t.Error("expected the oracle's method-binding pattern to read a callback call in the ask's own file as a definition; if it no longer does, drop this test and the matching header sentence")
	}
	py := sameSessionAsk{Lang: "py", Symbol: "foo", File: "/r/a.py", Root: "/r"}
	if !definesSymbol(py, sessionEdit{File: "/r/a.py", Text: "foo == 3"}, "") {
		t.Error("expected a column-0 `foo == 3` to read as a Python binding; if the oracle now refuses it, drop this test and the header sentence")
	}
}

// The oracle gates a bare name on its own line (a multi-line import member) on
// the WHOLE file opening a multi-line import; the spike supplies the last known
// whole file. Mutation: `inImport := false` in definesSymbol -> the member rows
// FAIL.
func TestDefinesSymbol_MultilineImportMember(t *testing.T) {
	for name, tc := range map[string]struct {
		lang, file, text, whole string
		want                    bool
	}{
		"py member, opener in the known file": {"py", "/r/a.py", "    Foo,\n", "from m import (\n    Bar,\n)\n", true},
		"py member, no opener anywhere":       {"py", "/r/a.py", "    Foo,\n", "x = 1\n", false},
		"js member, opener in the known file": {"js", "/r/a.js", "  Foo,\n", "import {\n  Bar,\n} from 'm'\n", true},
		"js member, no opener anywhere":       {"js", "/r/a.js", "  Foo,\n", "const x = 1\n", false},
		"py member, opener inside the edit":   {"py", "/r/a.py", "from m import (\n    Foo,\n)\n", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			ask := sameSessionAsk{Lang: tc.lang, Symbol: "Foo", File: tc.file, Root: "/r"}
			if got := definesSymbol(ask, sessionEdit{File: tc.file, Text: tc.text}, tc.whole); got != tc.want {
				t.Errorf("definesSymbol = %v, want %v", got, tc.want)
			}
		})
	}
}

// knownFileText feeds that gate: the last Write before the edit, else the file
// on disk, else nothing. Mutation: drop the `e.TS.Before(at)` clause (a later
// Write leaks in) or skip the disk read.
func TestClassifySameSession_ImportMemberUsesTheKnownFile(t *testing.T) {
	at := func(s string) time.Time { return mustTime(t, s) }
	disk := filepath.Join(t.TempDir(), "a.py")
	if err := os.WriteFile(disk, []byte("from m import (\n    Bar,\n)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ask := sameSessionAsk{TS: at("2026-10-07T10:00:10Z"), Lang: "py", Symbol: "Foo", File: "/r/a.py", Root: "/r"}
	member := sessionEdit{TS: at("2026-10-07T10:00:30Z"), File: "/r/a.py", Text: "    Foo,"}

	withWrite := sessionTranscript{Edits: []sessionEdit{
		{TS: at("2026-10-07T10:00:05Z"), File: "/r/a.py", Text: "from m import (\n    Bar,\n)\n", Whole: true}, member}}
	if got, _ := classifySameSession(ask, withWrite); got != bucketSameTurn {
		t.Errorf("member inside a Written import block: %q, want same-turn", got)
	}
	// A Write AFTER the member must not supply the opener.
	later := sessionTranscript{Edits: []sessionEdit{member,
		{TS: at("2026-10-07T10:00:40Z"), File: "/r/a.py", Text: "from m import (\n    Bar,\n)\n", Whole: true}}}
	if got, _ := classifySameSession(ask, later); got != bucketNotInSession {
		t.Errorf("opener from a later Write leaked in: %q, want not-in-session", got)
	}
	onDisk := ask
	onDisk.File = disk
	onDisk.Root = filepath.Dir(disk)
	m2 := member
	m2.File = disk
	if got, _ := classifySameSession(onDisk, sessionTranscript{Edits: []sessionEdit{m2}}); got != bucketSameTurn {
		t.Errorf("member with the opener on disk: %q, want same-turn", got)
	}
}

func TestClassifySameSession(t *testing.T) {
	at := func(s string) time.Time { return mustTime(t, s) }
	ask := sameSessionAsk{TS: at("2026-10-07T10:00:10Z"), Lang: "go", Symbol: "Foo", File: "/r/b.go", Root: "/r"}
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
		// Mutation: `p.After(ask.TS) && !p.After(e.TS)` -> `p.After(ask.TS) && p.Before(e.TS)`.
		"a prompt stamped exactly at the defining edit is a boundary": {
			sessionTranscript{Edits: []sessionEdit{with(def, "2026-10-07T10:05:00Z")}, Prompts: []time.Time{at("2026-10-07T10:05:00Z")}},
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
		// The guard truncates the ask to the second; the asking edit is stamped
		// 400 ms later. Mutation: `from := ask.TS` (drop Truncate/Add).
		"the asking edit itself, stamped inside the ask's second": {
			sessionTranscript{Edits: []sessionEdit{with(def, "2026-10-07T10:00:10.400Z")}},
			bucketNotInSession,
		},
		// A prompt in the ask's own second cannot be a reply to it. Mutation:
		// `!p.Before(from)` -> `p.After(ask.TS)` in classifySameSession.
		"a prompt inside the ask's second is not a boundary": {
			sessionTranscript{Edits: []sessionEdit{with(def, "2026-10-07T10:00:30Z")}, Prompts: []time.Time{at("2026-10-07T10:00:10.600Z")}},
			bucketSameTurn,
		},
		"an edit at the start of the next second counts": {
			sessionTranscript{Edits: []sessionEdit{with(def, "2026-10-07T10:00:11Z")}},
			bucketSameTurn,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, e := classifySameSession(ask, tc.st)
			if got != tc.want {
				t.Errorf("bucket = %q, want %q", got, tc.want)
			}
			if tc.want != bucketNotInSession && e.File != "/r/a.go" {
				t.Errorf("defining edit = %+v, want the one to /r/a.go", e)
			}
		})
	}

	// A file-scope finding is answered only by its own file.
	fileScoped := ask
	fileScoped.Scope = ScopeFile
	if got, _ := classifySameSession(fileScoped, sessionTranscript{Edits: []sessionEdit{with(def, "2026-10-07T10:00:30Z")}}); got != bucketNotInSession {
		t.Errorf("file-scope finding answered by another file: %q", got)
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
	ask := sameSessionAsk{TS: mustTime(t, "2026-10-07T10:00:10Z"), Lang: "go", Symbol: "Foo", File: "/r/b.go", Root: "/r"}
	bucket, def := classifySameSession(ask, st)
	if bucket != bucketSameTurn || def.File != "/r/a.go" {
		t.Errorf("bucket=%q def=%+v, want same-turn via the subagent's Write (its task brief is not a turn boundary)", bucket, def)
	}
}

// Mutation: delete the sort.SliceStable line in loadSession -> the main
// transcript's later definition is met first and this FAILS.
func TestLoadSession_SortsEditsAcrossFiles(t *testing.T) {
	root := t.TempDir()
	const id = "11111111-2222-3333-4444-555555555555"
	writeTranscript(t, filepath.Join(root, "proj", id+".jsonl"),
		trUser("2026-10-07T10:00:00Z", "do it"),
		trWrite("2026-10-07T10:00:50Z", "/r/late.go", "func Foo() {}"),
	)
	writeTranscript(t, filepath.Join(root, "proj", id, "subagents", "agent-a1.jsonl"),
		trWrite("2026-10-07T10:00:30Z", "/r/early.go", "func Foo() {}"),
	)
	st, err := loadSession(transcriptsByTag(root)[sessionTagOf(id)])
	if err != nil {
		t.Fatal(err)
	}
	ask := sameSessionAsk{TS: mustTime(t, "2026-10-07T10:00:10Z"), Lang: "go", Symbol: "Foo", File: "/r/b.go", Root: "/r"}
	if _, def := classifySameSession(ask, st); def.File != "/r/early.go" {
		t.Errorf("defining edit = %+v, want the earlier one (the subagent's)", def)
	}
}

// Mutation: discard the error from the subagent read in loadSession -> a
// partial session is classified and this FAILS.
func TestLoadSession_AnUnreadableSubagentTranscriptFailsTheSession(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "proj", "s.jsonl")
	writeTranscript(t, main, trUser("2026-10-07T10:00:00Z", "go"))
	if _, err := loadSession(sessionFiles{Main: main, Subs: []string{filepath.Join(root, "missing.jsonl")}}); err == nil {
		t.Error("loadSession ignored an unreadable subagent transcript")
	}
}

// Mutation: drop the sort in groupBySession -> a session's findings are split.
func TestGroupBySession(t *testing.T) {
	ts := mustTime(t, "2026-10-07T10:00:10Z")
	mk := func(file string) AuditFinding { return AuditFinding{TS: ts, File: file, Reason: "r"} }
	sessionOf := map[askKey]string{
		keyOf(ts, "a", "r"): "s2", keyOf(ts, "b", "r"): "s1", keyOf(ts, "c", "r"): "s2", keyOf(ts, "d", "r"): "s1",
	}
	got := groupBySession([]AuditFinding{mk("a"), mk("b"), mk("c"), mk("d")}, sessionOf)
	var files []string
	for _, f := range got {
		files = append(files, f.File)
	}
	if strings.Join(files, "") != "bdac" {
		t.Errorf("order = %v, want b d a c (grouped by session, stable)", files)
	}
}

// Mutation: delete the `prev.Dups = append(...)` branch in transcriptsByTag ->
// the second copy is silently ignored and loadSession succeeds, so this FAILS.
func TestLoadSession_RefusesASessionIdUnderTwoProjectDirs(t *testing.T) {
	root := t.TempDir()
	const id = "11111111-2222-3333-4444-555555555555"
	writeTranscript(t, filepath.Join(root, "proj-a", id+".jsonl"), trUser("2026-10-07T10:00:00Z", "go"))
	writeTranscript(t, filepath.Join(root, "proj-b", id+".jsonl"), trUser("2026-10-07T10:00:00Z", "go"))
	if _, err := loadSession(transcriptsByTag(root)[sessionTagOf(id)]); err == nil {
		t.Error("loadSession accepted a session id found in two project directories")
	}
}

func TestAskSessions(t *testing.T) {
	at := func(s string) time.Time { return mustTime(t, s) }
	since := at("2026-10-07T00:00:00Z")
	ask := func(ts, file, reason, session string, syms ...string) Decision {
		return Decision{TS: at(ts), Mode: "hook", Decision: "ask", File: file, Reason: reason, Symbols: syms, Session: session}
	}
	key := func(ts, file, reason string) askKey { return keyOf(at(ts), file, reason) }

	t.Run("a later duplicate fire supplies the session the first lacked", func(t *testing.T) {
		// Mutation: delete `case last.session == "":`.
		ds := []Decision{
			ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "", "Foo"),
			ask("2026-10-07T10:00:11Z", "/r/a.go", "violations", "aaaaaaaaaaaa", "Foo"),
		}
		m, asks, with := askSessions(ds, since)
		if got := m[key("2026-10-07T10:00:10Z", "/r/a.go", "violations")]; got != "aaaaaaaaaaaa" || asks != 1 || with != 1 {
			t.Errorf("session=%q asks=%d withSession=%d, want the later fire's session, 1 ask (collapsed), 1", got, asks, with)
		}
	})
	t.Run("duplicate fires naming different sessions are ambiguous", func(t *testing.T) {
		// Mutation: delete `case last.session != d.Session:`.
		ds := []Decision{
			ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "aaaaaaaaaaaa", "Foo"),
			ask("2026-10-07T10:00:12Z", "/r/a.go", "violations", "bbbbbbbbbbbb", "Foo"),
		}
		m, asks, with := askSessions(ds, since)
		if got := m[key("2026-10-07T10:00:10Z", "/r/a.go", "violations")]; got != "" || asks != 1 || with != 0 {
			t.Errorf("session=%q asks=%d withSession=%d, want no session, 1 ask, 0", got, asks, with)
		}
	})
	t.Run("two sessions on one file in one second are ambiguous", func(t *testing.T) {
		// Different symbol lists, so not duplicate fires. Mutation: let the
		// second ask overwrite (delete the `seen && cur != k.session` test).
		ds := []Decision{
			ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "aaaaaaaaaaaa", "Foo"),
			ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "bbbbbbbbbbbb", "Bar"),
		}
		m, asks, with := askSessions(ds, since)
		if got := m[key("2026-10-07T10:00:10Z", "/r/a.go", "violations")]; got != "" || asks != 2 || with != 0 {
			t.Errorf("session=%q asks=%d withSession=%d, want no session, 2 asks, 0", got, asks, with)
		}
	})
	t.Run("a sessionless ask never borrows a different ask's session", func(t *testing.T) {
		// Mutation: skip the comparison when k.session == "".
		for name, order := range map[string][]string{"sessionless first": {"", "aaaaaaaaaaaa"}, "sessionless last": {"aaaaaaaaaaaa", ""}} {
			ds := []Decision{
				ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", order[0], "Foo"),
				ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", order[1], "Bar"),
			}
			m, _, with := askSessions(ds, since)
			if got := m[key("2026-10-07T10:00:10Z", "/r/a.go", "violations")]; got != "" || with != 0 {
				t.Errorf("%s: session=%q withSession=%d, want no session, 0", name, got, with)
			}
		}
	})
	t.Run("ambiguity is sticky across a third ask", func(t *testing.T) {
		// A, B, A on one key: the last A must not revive the key. Mutation:
		// store k.session instead of "" on a mismatch.
		ds := []Decision{
			ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "aaaaaaaaaaaa", "Foo"),
			ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "bbbbbbbbbbbb", "Bar"),
			ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "aaaaaaaaaaaa", "Baz"),
		}
		m, asks, with := askSessions(ds, since)
		if got := m[key("2026-10-07T10:00:10Z", "/r/a.go", "violations")]; got != "" || asks != 3 || with != 0 {
			t.Errorf("session=%q asks=%d withSession=%d, want no session, 3 asks, 0", got, asks, with)
		}
	})
	t.Run("withSession counts asks, not keys", func(t *testing.T) {
		// Two kept asks, one key, same session. Mutation: count map entries.
		ds := []Decision{
			ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "aaaaaaaaaaaa", "Foo"),
			ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "aaaaaaaaaaaa", "Bar"),
		}
		if _, asks, with := askSessions(ds, since); asks != 2 || with != 2 {
			t.Errorf("asks=%d withSession=%d, want 2 and 2", asks, with)
		}
	})
	t.Run("same second, different file or reason stay apart", func(t *testing.T) {
		ds := []Decision{
			ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "aaaaaaaaaaaa", "Foo"),
			ask("2026-10-07T10:00:10Z", "/r/b.go", "violations", "bbbbbbbbbbbb", "Foo"),
			ask("2026-10-07T10:00:10Z", "/r/a.go", "lint", "cccccccccccc", "Foo"),
		}
		m, _, with := askSessions(ds, since)
		if m[key("2026-10-07T10:00:10Z", "/r/b.go", "violations")] != "bbbbbbbbbbbb" || with != 3 {
			t.Errorf("m=%v withSession=%d, want three separate sessions", m, with)
		}
	})
	t.Run("window and mode filters match Audit", func(t *testing.T) {
		old := ask("2026-10-06T10:00:10Z", "/r/a.go", "violations", "aaaaaaaaaaaa", "Foo")
		pre := ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "aaaaaaaaaaaa", "Foo")
		pre.Mode = "precommit"
		defer_ := ask("2026-10-07T10:00:10Z", "/r/a.go", "violations", "aaaaaaaaaaaa", "Foo")
		defer_.Decision = "defer"
		if _, asks, _ := askSessions([]Decision{old, pre, defer_}, since); asks != 0 {
			t.Errorf("asks = %d, want 0", asks)
		}
	})
}

func TestResolveFinding(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	askFile, defFile := filepath.Join(repo, "b.go"), filepath.Join(repo, "a.go")
	projects := t.TempDir()
	const id = "11111111-2222-3333-4444-555555555555"
	writeTranscript(t, filepath.Join(projects, "proj", id+".jsonl"),
		trUser("2026-10-07T10:00:00Z", "do it"),
		trWrite("2026-10-07T10:00:30Z", defFile, "func Foo() {}"),
	)
	index := transcriptsByTag(projects)
	ts := mustTime(t, "2026-10-07T10:00:10Z")
	f := AuditFinding{TS: ts, File: askFile, Lang: "go", Symbol: "Foo", Reason: "violations", Scope: ScopeRepo}
	key := keyOf(ts, askFile, "violations")

	for name, tc := range map[string]struct {
		sessionOf map[askKey]string
		want      string
	}{
		"resolved":    {map[askKey]string{key: sessionTagOf(id)}, bucketSameTurn},
		"no session":  {map[askKey]string{key: ""}, bucketNoSession},
		"unknown ask": {map[askKey]string{}, bucketNoSession},
		// Mutation: return bucketNotInSession when st == nil.
		"no transcript": {map[askKey]string{key: "000000000000"}, bucketNoTranscript},
	} {
		t.Run(name, func(t *testing.T) {
			got, _ := resolveFinding(f, tc.sessionOf, index, map[string]*sessionTranscript{}, func(string) string { return repo })
			if got != tc.want {
				t.Errorf("bucket = %q, want %q", got, tc.want)
			}
		})
	}
}

// isDuplicateFire is the pre-#493 previous-record dedupe this spike was
// measured with, kept here so its numbers stay reproducible. Reports use
// duplicateAskFires.
//
// It reports whether cur is the same ask as prev, re-logged by
// another hook wiring. Deliberately strict — same file, same reason, same symbol
// list, within the window — so a genuine repeat ask after an unsuccessful fix is
// still counted.
func isDuplicateFire(prev, cur Decision) bool {
	return prev.File == cur.File &&
		prev.Reason == cur.Reason &&
		sameSymbols(prev.Symbols, cur.Symbols) &&
		cur.TS.Sub(prev.TS) >= 0 &&
		cur.TS.Sub(prev.TS) <= duplicateFireWindow
}

func sameSymbols(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
