package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inth3shadows/runecho/internal/contract"
	"github.com/inth3shadows/runecho/internal/gitutil"
	"github.com/inth3shadows/runecho/internal/ir"
	"github.com/inth3shadows/runecho/internal/snapshot"
)

// hookCase is one replayable fixture for a HOOK-LEVEL guard check. Eight checks
// — dangling-refs, dropped-import, duplicate-symbol, file-scope, contract,
// call-shape, qualified, deps-go — need old-vs-new edit text PLUS an enrolled
// snapshot store or an on-disk module, and are reachable ONLY through the hook
// entry point (runHookMode). The published corpus in internal/guard drives
// guard.Run in-process, which cannot reach them, so the catch-rate it reports
// describes one check of nine (#227). This harness closes that gap by replaying
// such cases as data through runHookMode.
//
// Keep that count honest when a check is added. It is this corpus's own claim
// about what it covers, and the whole thesis here is that unmeasured coverage is
// the hazard — a stale denominator is the same failure in prose.
//
// Phase 1 covered duplicate-symbol; phase 2 added dangling-refs, which enrolls
// a refs index (Refs) so a deleted def with a live cross-file referrer can be
// detected. Phase 3 adds dropped-import, which needs no snapshot enrollment at
// all (Enroll/Refs are both empty) — its true positives instead rely on
// addInFileDefs folding the pre-edit on-disk file's OWN import into the known
// set, which is what keeps the always-on additive check silent so the ask can
// be attributed to dropped-import alone. Phase 4 adds file-scope, which inverts
// the enrollment's role: the symbol is enrolled in ANOTHER file so it is known
// repo-wide (satisfying the check's firewall, and keeping the additive check
// silent) while being unresolvable in the edited file — the ask can then only
// come from file-scope. Phase 4 also added the Tool/EditOld/EditNew shape, so a
// fixture can replay a HUNK-scoped Edit rather than a whole-file Write: that is
// what lets the abstain true-negatives (star import, dynamic binding) put their
// marker OUTSIDE the edited hunk and thereby pin the pre-edit file read, each
// paired with an identical-hunk control that does ask. Phase 5 completes the set
// with contract, whose enrollment lives in two places at once — a file in the
// worktree AND an activation row keyed to a session — so Contract/Session/
// ActivateSession exist to express "activated, but for a different session",
// which is the leak that would make every concurrent agent in a shared store
// answer for a scope it never accepted. That completed the six checks that
// existed then. Call-shape arrived with #243 and brought its own set; qualified
// and deps-go were added last, and needed the `files` field, because their
// preconditions live on disk (a go.mod, a vendor tree) rather than in the
// snapshot store.
//
// # Every fixture here earns its place by mutation, not by argument
//
// EVERY set here has now been scored by breaking one behaviour of the check at a
// time and recording which fixtures failed — file-scope and contract before they
// merged, duplicate/dangling/dropped-import in a follow-up sweep. Cases that caught nothing were deleted
// rather than kept as documentation — a fixture that no defect can fail is a
// claim of coverage the corpus does not have, which is the exact failure #227
// exists to fix. Three of the first draft's cases went that way, and the gaps
// the scoring exposed (the added-text abstain arm, the no-context bail, and the
// firewall) became the three that replaced them. Adding a fixture here without
// naming the change it would catch is how the set silts back up.
//
// What the scoring says the corpus does NOT cover, stated here rather than
// implied away. First, dropped-import's Go exclusion: removing the language gate
// outright leaves every fixture green, because a dropped Go import surfaces as a
// qualified reference that never reads as an unqualified use — the fixture that
// claimed to cover it was cut for catching nothing, and nothing replaced it.
// Second, file-scope's Python-only restriction: the pure function
// and the hook wrapper each gate on it, so removing either alone changes nothing
// observable and removing both is caught by no fixture — the internal/guard unit
// suite covers it instead. Third, the contract section of a MERGED ask (a
// contract firing alongside symbol violations in one message). Every ask a
// fixture can produce today takes the contract-only early exit, and the merged
// shape is inexpressible: its flag-off state is an ask (from the always-on
// check) whose text legitimately CHANGES when the gate opens, which is neither
// probe mode. A third mode would be needed, and that is a follow-up, not a
// silent omission.
type hookCase struct {
	Name   string              `json:"name"`
	Desc   string              `json:"desc,omitempty"`
	Check  string              `json:"check"`  // gated check under test, e.g. "duplicate"
	Flags  []string            `json:"flags"`  // env "K=V" that gate the check
	Enroll map[string][]string `json:"enroll"` // snapshot symbols: repo-relative file -> names
	// Refs is the snapshot's refs index: repo-relative file -> the bare call
	// targets that file references. dangling-refs resolves "who still references
	// the deleted def" through it (RefsToName), so a fixture proving a live
	// cross-file referrer must enroll that referrer's Refs, not just its Symbols.
	Refs map[string][]string `json:"refs,omitempty"`
	// Files are extra worktree files written before the hook runs: repo-relative
	// path -> content. Unlike Enroll (which populates the snapshot store), these
	// are read off DISK by the check itself, which is the only way to express the
	// two Go checks' preconditions. qualified needs a go.mod, because it resolves
	// the module path to tell a same-repo import from an external one and returns
	// nil when that path is empty. deps-go needs a go.mod plus a vendor tree with
	// modules.txt: a vendored build resolves imports from vendor/ and nothing
	// else, which makes the fixture hermetic — no module cache, no GOROOT, no
	// network — so "the package resolved and lacks this symbol" is a fact the
	// fixture states rather than one it inherits from the machine.
	Files map[string]string `json:"files,omitempty"`
	File  string            `json:"file"` // edited file, repo-relative
	Old   string            `json:"old"`  // on-disk content BEFORE the edit
	New   string            `json:"new"`  // content being written (Write only)
	// Tool is the PreToolUse tool replayed; "" means Write, under which the whole
	// New content IS the added-lines set. Set it to "Edit" with EditOld/EditNew
	// for a HUNK-scoped replay: the only shape that can prove a signal is read
	// from the pre-edit file ON DISK rather than from the added text. Under Write
	// those two are the same bytes, so a fixture whose marker sits in both cannot
	// tell which one the check consumed — it would pass even with the whole-file
	// read deleted. Any fixture claiming to exercise whole-file context (an
	// abstain marker, a pre-existing binding) MUST be an Edit, and should be
	// paired with a control whose hunk is identical and whose file lacks the
	// marker, so the difference in outcome is attributable to the file text.
	Tool       string   `json:"tool,omitempty"`
	EditOld    string   `json:"edit_old,omitempty"`
	EditNew    string   `json:"edit_new,omitempty"`
	ExpectAsk  bool     `json:"expect_ask"`
	ExpectSyms []string `json:"expect_symbols,omitempty"`
	// ExpectLogReason pins the decision-log bucket, which is a DIFFERENT claim
	// from the ask text and was never checked. Three checks appended into the
	// additive check's slice and so logged as "violations"; every fixture stayed
	// green because none of them looked at the log (#268). Optional, because a
	// fixture whose bucket is not the point should not be forced to restate it.
	ExpectLogReason string `json:"expect_log_reason,omitempty"`
	// ExpectNoLearnSymbols pins that this ask trains learned-allow on NOTHING.
	// Only the additive hallucination check's findings may be learn-eligible: the
	// learned set is folded into guard.Run's known-set, so learning a name from a
	// file-scope ask ("real symbol, wrong scope") would teach the guard that the
	// name resolves and silence a genuine hallucination of it until the TTL runs
	// out. Code review found learnSyms still reading off the MERGED violation
	// slice, which is exactly what firedChecks exists to stop.
	ExpectNoLearnSymbols bool `json:"expect_no_learn_symbols,omitempty"`
	// AskWithoutFlag inverts the isolation probe for the one case the probe cannot
	// express: a fixture whose ask comes from an ALWAYS-ON check by design, where
	// what is being pinned is that the gated check adds NOTHING to it. The default
	// probe asserts flag-off is silent; with this set it asserts flag-off asks, and
	// then that flag-on produces the byte-identical reason. That is what makes a
	// suppression rule — the file-scope firewall, which must leave names absent
	// from the repo to the additive check — detectable at all. Without it, removing
	// the firewall only DOUBLES an existing report, which an ask/no-ask assertion
	// cannot see.
	AskWithoutFlag bool `json:"ask_without_flag,omitempty"`
	// NoPreEditFile omits the on-disk pre-edit file entirely, which is the real
	// shape of a Write that CREATES a file. It is not the same as an empty Old: an
	// existing empty file still yields one (blank) line, so the checks run against
	// it, while a missing file yields none and the whole-file-context bail engages.
	// Only that second shape exercises the bail, so a fixture claiming to pin it
	// must use this rather than `"old": ""`.
	NoPreEditFile bool `json:"no_pre_edit_file,omitempty"`
	// Contract is a contract file's body, written to .runecho/contracts/<its name>
	// and ACTIVATED for ActivateSession. It is the enrollment shape the contract
	// check needs, and unlike every other check's it lives in two places at once:
	// a file in the worktree and a row in the store. Activation is what makes it
	// live — a contract file that is merely present governs nothing.
	Contract string `json:"contract,omitempty"`
	// Session is the session_id the edit is attributed to; ActivateSession is the
	// session the contract was activated for (defaults to Session). They are
	// separate fields so a fixture can prove a contract governs ONLY its own
	// session — a contract leaking across sessions would make every concurrent
	// agent in the same repo answer for a scope it never accepted.
	Session         string `json:"session,omitempty"`
	ActivateSession string `json:"activate_session,omitempty"`
	// EnrolledDefs pins how many snapshot files DefsOfName resolves for a symbol,
	// via the guard's OWN store-resolution path. It is the anti-vacuous guard for
	// TRUE-NEGATIVE fixtures: a filter-drop TN must prove its candidate is actually
	// reachable (count >= 1) so its silence is a real rule drop, not a typo'd path
	// or Kind that enrolled nothing; the not-defined-elsewhere TN pins count 0 so
	// its silence is a genuine absence, not a global enrollment failure. Without
	// this, a TN can pass "for the wrong reason" — exactly the #227 hazard.
	EnrolledDefs map[string]int `json:"enrolled_defs,omitempty"`
	// EnrolledRefs is the dangling-refs analog of EnrolledDefs: it pins how many
	// snapshot files reference a symbol via the guard's own RefsToName path. A
	// self-only-referrer TN stays silent because self-exclusion drops the sole
	// referrer, NOT because the refs index is empty — pinning count 1 proves the
	// referrer was actually enrolled. A count of 0 would say the opposite — that
	// the silence is a genuine absence — but no dangling fixture pins 0 today: the
	// one that did caught no defect and was cut. EnrolledDefs still has a 0 pin
	// (filescope's invented symbol), so the zero case is exercised on that side only.
	EnrolledRefs map[string]int `json:"enrolled_refs,omitempty"`
}

func TestHookCorpus(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "hookcorpus", "*.json"))
	if err != nil {
		t.Fatalf("glob hook corpus: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no hook corpus fixtures found under testdata/hookcorpus")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var cases []hookCase
		if err := json.Unmarshal(data, &cases); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, c := range cases {
			c := c
			t.Run(c.Name, func(t *testing.T) { runHookCase(t, c) })
		}
	}
}

func runHookCase(t *testing.T, c hookCase) {
	if len(c.Flags) == 0 {
		t.Fatalf("%s: no gating flags — a hook fixture that runs with the check off proves nothing", c.Name)
	}

	// A temp repo with the edited file on disk holding its PRE-edit content:
	// wholeFileText reads this to diff old-vs-new (PreToolUse fires before the
	// write lands).
	root := t.TempDir()
	gitInit(t, root)
	// Resolve symlinks (macOS /var -> /private/var) before anything derives a path
	// from root: repo resolution keys on the git common dir, and enrolling one
	// spelling while looking up another abstains every check silently.
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	for rel, content := range c.Files {
		if rel == c.File {
			t.Fatalf("%s: `files` may not contain the edited file — use `old` for its pre-edit content", c.Name)
		}
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	edited := filepath.Join(root, filepath.FromSlash(c.File))
	if err := os.MkdirAll(filepath.Dir(edited), 0o755); err != nil {
		t.Fatal(err)
	}
	if c.NoPreEditFile {
		if c.Old != "" {
			t.Fatalf("%s: no_pre_edit_file and a non-empty `old` are contradictory", c.Name)
		}
	} else if err := os.WriteFile(edited, []byte(c.Old), 0o644); err != nil {
		t.Fatal(err)
	}

	top := enrollSnapshot(t, root, c.Enroll, c.Refs)
	if c.Contract != "" {
		activateContract(t, top, c)
	}
	setFlags := flagController(t, c.Flags)
	body := hookBody(t, c, edited)

	// Structural anti-vacuous probe: resolve each pinned symbol through the guard's
	// OWN store path (openLatestSnapshot → DefsOfName), the exact lookup the check
	// uses. This is what proves a silent true-negative is silent for its INTENDED
	// reason (a rule dropped a reachable candidate) rather than because enrollment
	// quietly resolved nothing.
	for sym, want := range c.EnrolledDefs {
		if got := probeDefsCount(t, edited, sym); got != want {
			t.Fatalf("enrollment probe: DefsOfName(%q) resolved %d file(s), want %d — the fixture's ask/silence would be for the wrong reason", sym, got, want)
		}
	}
	for sym, want := range c.EnrolledRefs {
		if got := probeRefsCount(t, edited, sym); got != want {
			t.Fatalf("enrollment probe: RefsToName(%q) resolved %d file(s), want %d — the fixture's ask/silence would be for the wrong reason", sym, got, want)
		}
	}

	if c.ExpectAsk {
		// Anti-vacuous proof (#227's central hazard): with the gating flag OFF the
		// ask must NOT appear. If it does, the fixture is not isolating this check —
		// the ask is coming from somewhere else and the fixture would report a
		// vacuous pass. Only after proving flag-off is silent do we trust flag-on.
		setFlags(false)
		_, _, off := runHook(t, body)
		switch {
		case c.AskWithoutFlag && off.Hook.PermissionDec != "ask":
			t.Fatalf("flag-off did not ask — this fixture pins that the %s check adds nothing to an always-on ask, so the always-on ask must exist first", c.Check)
		case !c.AskWithoutFlag && off.Hook.PermissionDec == "ask":
			t.Fatalf("flag-off produced an ask (%q) — fixture does not isolate the %s check",
				off.Hook.PermissionReason, c.Check)
		}
		setFlags(true)
		_, _, d := runHook(t, body)
		if d.Hook.PermissionDec != "ask" {
			t.Fatalf("flag-on: expected an ask from the %s check, got a defer", c.Check)
		}
		// The suppression case: turning the check ON must not change the message by
		// one byte. Any added line means the gated check reported a name it was
		// supposed to leave alone.
		if c.AskWithoutFlag && d.Hook.PermissionReason != off.Hook.PermissionReason {
			t.Fatalf("flag-on changed the ask — the %s check reported a name it should have suppressed.\nflag-off:\n%s\nflag-on:\n%s",
				c.Check, off.Hook.PermissionReason, d.Hook.PermissionReason)
		}
		for _, s := range c.ExpectSyms {
			if !strings.Contains(d.Hook.PermissionReason, s) {
				t.Errorf("ask reason does not name expected symbol %q:\n%s", s, d.Hook.PermissionReason)
			}
		}
		if c.ExpectNoLearnSymbols {
			// A nil record must fail, not pass. This is a safety pin — it asserts an
			// ask did NOT teach learned-allow — so "no decision was logged" is the
			// one outcome that cannot be read as agreement. Treating nil as a pass
			// would let a harness change that stops exporting RUNECHO_HOME turn
			// every pin on this field green while learn_symbols went unchecked:
			// exactly the fixture-pins-nothing failure #227 exists to exclude.
			rec := readLastDecisionLog(t)
			if rec == nil {
				t.Fatalf("no decision logged, want an ask with no learn_symbols")
			}
			if ls, ok := rec["learn_symbols"]; ok {
				if arr, isArr := ls.([]any); !isArr || len(arr) > 0 {
					t.Errorf("ask trained learned-allow on %v — only additive-check "+
						"findings are learn-eligible; learning this name would silence a "+
						"later genuine hallucination of it", ls)
				}
			}
		}
		if c.ExpectLogReason != "" {
			rec := readLastDecisionLog(t)
			if rec == nil {
				t.Fatalf("no decision logged, want reason %q", c.ExpectLogReason)
			}
			if got := rec["reason"]; got != c.ExpectLogReason {
				t.Errorf("decision-log reason = %v, want %q — guardstats and fpreport "+
					"bucket on this exact string, so the wrong one makes this check's "+
					"rate unmeasurable and inflates the bucket it lands in", got, c.ExpectLogReason)
			}
		}
	} else {
		// True negative: even with the check ON, no ask.
		setFlags(true)
		if _, _, d := runHook(t, body); d.Hook.PermissionDec == "ask" {
			t.Errorf("expected no ask, got:\n%s", d.Hook.PermissionReason)
		}
	}
}

// activateContract writes the fixture's contract into the worktree and activates
// it for the session, against the snapshot store enrollSnapshot already stood up.
// Both halves are required and they are separate failures: a contract file that
// is present but not activated governs nothing (that is the "no active contract"
// abstain), and an activation naming a session the edit does not carry governs
// nothing either. The activation records the file's hash, which is what lets the
// ask disclose drift when the file changes underneath it.
func activateContract(t *testing.T, top string, c hookCase) {
	t.Helper()
	cdir := filepath.Join(top, contract.Dir)
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	cpath := filepath.Join(cdir, "scope")
	if err := os.WriteFile(cpath, []byte(c.Contract), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := contract.Load(cpath)
	if err != nil {
		t.Fatalf("%s: contract body does not load: %v", c.Name, err)
	}
	db, err := snapshot.Open(filepath.Join(os.Getenv("RUNECHO_HOME"), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo, _, ok := db.ResolveRepo(top)
	if !ok {
		t.Fatalf("%s: enrolled repo did not resolve — the contract would govern nothing", c.Name)
	}
	sess := c.ActivateSession
	if sess == "" {
		sess = c.Session
	}
	if sess == "" {
		t.Fatalf("%s: a contract fixture needs a session to activate for", c.Name)
	}
	if err := db.ActivateContract(repo.ID, sess, parsed.Name, parsed.Path, parsed.Hash); err != nil {
		t.Fatalf("%s: ActivateContract: %v", c.Name, err)
	}
}

// hookBody renders the PreToolUse payload for a fixture. Write is the default and
// sends the whole New content; Edit sends a hunk (EditOld/EditNew), which is what
// separates "the check read the pre-edit file on disk" from "the check read the
// added text" — under Write those are the same bytes and the distinction is
// untestable. New is rejected on an Edit fixture because it would be silently
// ignored, and a fixture author who set it would believe it was being replayed.
func hookBody(t *testing.T, c hookCase, editedAbs string) string {
	t.Helper()
	return withSession(t, c.Session, rawHookBody(t, c, editedAbs))
}

// withSession splices session_id into a rendered payload. The contract check is
// the only one that reads it — a contract is activated per session, so an edit
// carrying no session id, or a different one, is governed by nothing. Done here
// rather than in payload()/payloadOld() so the shared helpers stay as the rest
// of the suite already uses them.
func withSession(t *testing.T, session, body string) string {
	t.Helper()
	if session == "" {
		return body
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	m["session_id"] = session
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func rawHookBody(t *testing.T, c hookCase, editedAbs string) string {
	t.Helper()
	switch c.Tool {
	case "", "Write":
		return payload(t, "Write", editedAbs, "", c.New, nil)
	case "Edit":
		// EditNew may be empty: an Edit that removes a line and adds nothing is a
		// real payload, and it is the canonical dropped-import shape. EditOld may
		// not — without it the hunk is not locatable in the pre-edit file.
		if c.EditOld == "" {
			t.Fatalf("%s: an Edit fixture needs edit_old", c.Name)
		}
		if c.New != "" {
			t.Fatalf("%s: `new` is ignored for an Edit fixture — put the hunk in edit_old/edit_new", c.Name)
		}
		if !strings.Contains(c.Old, c.EditOld) {
			t.Fatalf("%s: edit_old is not present in the pre-edit file — the hunk would not be locatable", c.Name)
		}
		return payloadOld(t, "Edit", editedAbs, c.EditOld, c.EditNew, "", nil)
	default:
		t.Fatalf("%s: unsupported tool %q (want Write or Edit)", c.Name, c.Tool)
		return ""
	}
}

// probeDefsCount resolves how many snapshot files define sym, through the guard's
// production store path (openLatestSnapshot → DefsOfName) — the same resolution
// the checks use. A store that won't resolve at all is a hard failure: it means
// the fixture enrolled nothing, so any downstream silence would be vacuous.
func probeDefsCount(t *testing.T, editedAbs, sym string) int {
	t.Helper()
	db, snapID, _, ok, _ := openLatestSnapshot(filepath.Dir(editedAbs), editedAbs)
	if !ok {
		t.Fatalf("probe: store/snapshot not resolvable for %s — enrollment did not take", editedAbs)
	}
	defer db.Close()
	paths, err := db.DefsOfName(snapID, sym)
	if err != nil {
		t.Fatalf("probe DefsOfName(%q): %v", sym, err)
	}
	return len(paths)
}

// probeRefsCount resolves how many snapshot files reference sym, through the
// guard's production store path (openLatestSnapshot → RefsToName) — the exact
// lookup checkDanglingRefs uses. It is the refs-index analog of probeDefsCount:
// a store that won't resolve at all is a hard failure, since any downstream
// silence would then be vacuous.
func probeRefsCount(t *testing.T, editedAbs, sym string) int {
	t.Helper()
	db, snapID, _, ok, _ := openLatestSnapshot(filepath.Dir(editedAbs), editedAbs)
	if !ok {
		t.Fatalf("probe: store/snapshot not resolvable for %s — enrollment did not take", editedAbs)
	}
	defer db.Close()
	paths, err := db.RefsToName(snapID, sym)
	if err != nil {
		t.Fatalf("probe RefsToName(%q): %v", sym, err)
	}
	return len(paths)
}

// flagController captures each gating env var's original value once, restores it
// on cleanup, and returns a setter that toggles all of them on/off — so a single
// fixture can be replayed with the check off (isolation proof) then on.
func flagController(t *testing.T, flags []string) func(on bool) {
	t.Helper()
	type kv struct {
		k, v, orig string
		had        bool
	}
	parsed := make([]kv, 0, len(flags))
	for _, f := range flags {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			t.Fatalf("bad flag %q (want K=V)", f)
		}
		orig, had := os.LookupEnv(k)
		parsed = append(parsed, kv{k, v, orig, had})
	}
	t.Cleanup(func() {
		for _, p := range parsed {
			if p.had {
				os.Setenv(p.k, p.orig)
			} else {
				os.Unsetenv(p.k)
			}
		}
	})
	return func(on bool) {
		for _, p := range parsed {
			switch {
			case on:
				os.Setenv(p.k, p.v)
			case defaultOnFlags[p.k]:
				// Unsetting would leave this flag at its true default — ON, since
				// #314 — so the isolation probe below would see the check still
				// fire and misread that as "the fixture doesn't isolate this
				// check". The explicit opt-out value is what "off" means for a
				// default-on flag.
				os.Setenv(p.k, "0")
			default:
				os.Unsetenv(p.k)
			}
		}
	}
}

// defaultOnFlags names every gating env var whose enabled() reads "!= 0"
// rather than "== 1" — i.e. checks that ship on by default and are disabled by
// an explicit opt-out. Keep in sync with cmd/runecho-guard's *Enabled functions;
// a flag missing from here would make flagController(false) a no-op for it.
var defaultOnFlags = map[string]bool{
	"RUNECHO_GUARD_QUALIFIED": true,
}

// enrollSnapshot stands up a temp central store ($RUNECHO_HOME) and saves one
// snapshot whose per-file symbol sets are `files` (repo-relative path -> names)
// and whose per-file refs index is `refs` (repo-relative path -> referenced
// names). It generalizes enrolledStore (single hardcoded file) to the multi-file
// layout the hook-only checks need — duplicate-symbol resolves candidates by
// DefsOfName (the Symbols side), dangling-refs by RefsToName (the Refs side),
// both keyed on the enrolled file paths. A file may appear in `files`, in `refs`,
// or in both; the union of their keys is enrolled.
func enrollSnapshot(t *testing.T, root string, files, refs map[string][]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("RUNECHO_HOME", home)

	db, err := snapshot.Open(filepath.Join(home, "history.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	defer db.Close()

	top, err := gitutil.TopLevel(root)
	if err != nil {
		t.Fatalf("gitutil.TopLevel: %v", err)
	}
	id, err := db.EnrollRepo("r", top, top, 0)
	if err != nil {
		t.Fatalf("EnrollRepo: %v", err)
	}
	if cd, err := gitutil.CommonDir(top); err == nil {
		_ = db.SetRepoCommonDir(id, cd)
	}

	fileIR := make(map[string]ir.FileIR, len(files)+len(refs))
	for path, syms := range files {
		f := fileIR[path]
		f.Hash = "h_" + path
		f.Symbols = funcsToSymbols(syms)
		fileIR[path] = f
	}
	for path, rs := range refs {
		f := fileIR[path]
		if f.Hash == "" {
			f.Hash = "h_" + path
		}
		f.Refs = rs
		fileIR[path] = f
	}
	irData := &ir.IR{Version: ir.IRVersion, Files: fileIR}
	if _, err := db.SaveSnapshot(id, "sess", "test", top, irData); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	return top
}
