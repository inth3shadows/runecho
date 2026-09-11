package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inth3shadows/runecho/internal/store"
)

// #392 — the enrollment notice. An edit that lands in a git repo nobody has run
// `runecho-ir repo add` on gets NO symbol validation, and before this the guard
// said so to nobody: the arm deferred silently, so the failure mode was a user
// who believed they were guarded and was not. The census that motivated this put
// 9.1% of all hook events on that arm.
//
// Posture: notice ONCE per repo, never again. Auto-enrolling was rejected (the
// guard does not reach into a repo the user did not offer it), and so was
// nagging on every edit (an advisory that fires 200 times is an advisory nobody
// reads). Three consequences of "once" shape everything below:
//
//   - The dedupe key is the git COMMON-DIR, not the worktree top-level. In the
//     claudew/codexw layout a repo is a bare `.bare` plus a fresh
//     `claude-<ts>` worktree per session, so a top-level key would fire on the
//     first edit of every session — exactly the nagging this avoids. It is also
//     the key `repo add` stores as common_dir, so "noticed" and "enrolled" are
//     keyed on the same string by construction.
//   - The channel is additionalContext, not stderr. A hook that exits 0 has its
//     stderr routed to the debug log and nowhere else — Claude never sees it.
//     Only exit 2 surfaces stderr, and exit 2 BLOCKS the edit, which is the one
//     thing this must never do.
//   - Emission is gated on the marker actually being RECORDED. A store directory
//     that cannot be written would otherwise turn "once" into "every edit", so
//     an unwritable marker buys silence rather than a nag.
//
// That last invariant is deliberately ONE-directional: no record means no
// notice, but a record does NOT prove the notice was delivered. runWithTimeout
// (main.go) buffers the hook's output and discards the buffer on a panic or on
// the 4s deadline, both of which can land after the marker is written — so a
// first edit unlucky enough to time out spends the notice without showing it.
// The alternative, recording only after a confirmed write to the wire, cannot
// be expressed from in here (the flush happens two frames up) and would fail in
// the other direction, which is the one that nags. A lost notice costs one
// advisory; a lost marker costs one on every edit forever.
//
// The marker file lives in RUNECHO_HOME beside learned-allow.json — the
// precedent in this package for guard-owned state — and never in the repo, so a
// hook can never mutate a git-tracked file behind the user's back.

// enrollNoticeFile is the marker filename inside RUNECHO_HOME.
const enrollNoticeFile = "enroll-notices.json"

// maxEnrollNotices caps the marker file so it cannot grow without bound on a box
// that visits many repos. Past it the oldest entry by first_seen is evicted,
// which means that repo can be noticed a second time some day — a bounded,
// acceptable cost for a bounded file.
const maxEnrollNotices = 256

// enrollNoticeEntry records that a repo has been told about once.
type enrollNoticeEntry struct {
	FirstSeen string `json:"first_seen"` // RFC3339 UTC — also the eviction order
	Top       string `json:"top"`        // worktree top at notice time, for debugging
}

// enrollNotices is the on-disk marker: git common-dir -> entry.
type enrollNotices struct {
	V     int                          `json:"v"`
	Repos map[string]enrollNoticeEntry `json:"repos"`
}

// enrollNoticeEnabled reports whether the notice is active. Default ON — unlike
// every other RUNECHO_GUARD_* gate, because this one is not a check that can be
// wrong about code: it either names a fact (this repo is unenrolled) or stays
// quiet, and it can only ever fire once per repo. RUNECHO_GUARD_ENROLL_NOTICE=0
// turns it off; any other value, including unset, leaves it on.
func enrollNoticeEnabled() bool { return os.Getenv("RUNECHO_GUARD_ENROLL_NOTICE") != "0" }

// loadEnrollNotices reads the marker from dir. Fail-open in the direction of
// SILENCE is not available here — an unreadable marker has to mean "not yet
// noticed", or a corrupt file would suppress the notice forever — so a missing,
// oversized or unparseable file yields an empty store and the repo gets one
// more notice, after which the file is rewritten whole and self-heals.
//
// Read without the lock, deliberately: this runs on every unenrolled edit, and
// AtomicWriteFile renames its temp into place, so an unlocked reader sees a
// whole file or none, never a torn one. The locked re-read in
// recordEnrollNotice is what actually makes "once" hold.
func loadEnrollNotices(dir string) enrollNotices {
	en := enrollNotices{V: 1, Repos: map[string]enrollNoticeEntry{}}
	path := filepath.Join(dir, enrollNoticeFile)
	if fi, err := os.Stat(path); err != nil || fi.Size() > maxEnrollNoticeBytes {
		return en
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return en
	}
	var parsed enrollNotices
	if json.Unmarshal(b, &parsed) != nil || parsed.Repos == nil {
		return en
	}
	parsed.V = 1
	return parsed
}

// maxEnrollNoticeBytes bounds what this hot read path will parse. maxEnrollNotices
// entries of a long path plus a timestamp is far under it; anything larger is not
// a file this program wrote.
const maxEnrollNoticeBytes = 1 << 20

// saveEnrollNotices writes the marker atomically (temp + rename) so a crash or a
// concurrent writer can never leave an unparseable file. Returns the error
// because the caller's emit decision depends on it: no record, no notice.
func saveEnrollNotices(dir string, en enrollNotices) error {
	en.V = 1
	b, err := json.Marshal(en)
	if err != nil {
		return err
	}
	return store.AtomicWriteFile(filepath.Join(dir, enrollNoticeFile), b)
}

// recordEnrollNotice inserts key under a cross-process lock and reports whether
// THIS call is the one that recorded it. False means either somebody else got
// there first (already noticed) or the write failed — both of which must end in
// silence, which is why one bool covers both.
//
// The lock closes the window the atomic rename cannot: two sessions editing the
// same unenrolled repo at once, or the user-settings and project-settings hook
// entries both firing on one edit, would otherwise each read "not noticed" and
// each emit. WithFileLock is fail-open and a no-op on non-Unix, so the honest
// guarantee is "exactly once where the lock can be taken, at most one duplicate
// otherwise".
func recordEnrollNotice(dir, key, top string, now time.Time) bool {
	recorded := false
	store.WithFileLock(filepath.Join(dir, enrollNoticeFile+".lock"), func() {
		en := loadEnrollNotices(dir)
		if en.Repos == nil {
			en.Repos = map[string]enrollNoticeEntry{}
		}
		if _, seen := en.Repos[key]; seen {
			return
		}
		en.Repos[key] = enrollNoticeEntry{
			FirstSeen: now.UTC().Format(time.RFC3339),
			Top:       top,
		}
		evictOldestEnrollNotices(&en, key)
		if saveEnrollNotices(dir, en) == nil {
			recorded = true
		}
	})
	return recorded
}

// evictOldestEnrollNotices trims the marker to maxEnrollNotices, dropping the
// oldest first_seen first and never the entry just inserted (keep). RFC3339 UTC
// is fixed-width, so a string compare IS the time order; an entry with a
// missing or malformed stamp sorts first and is evicted first, which is the
// right disposal for a record that cannot be dated.
func evictOldestEnrollNotices(en *enrollNotices, keep string) {
	for len(en.Repos) > maxEnrollNotices {
		oldestKey, oldestAt := "", ""
		for k, e := range en.Repos {
			if k == keep {
				continue
			}
			if oldestKey == "" || e.FirstSeen < oldestAt {
				oldestKey, oldestAt = k, e.FirstSeen
			}
		}
		if oldestKey == "" {
			return // nothing evictable left
		}
		delete(en.Repos, oldestKey)
	}
}

// enrollShellQuote wraps s in single quotes for a POSIX shell, escaping an
// embedded quote with the standard close-escape-reopen sequence. Same function
// as runecho-ir's shellQuote, duplicated because both live in package main;
// runechoDir is duplicated across three commands for the same reason.
func enrollShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// enrollCommandForm says which shape of enrollment instruction the notice may
// give for a particular path.
type enrollCommandForm int

const (
	// enrollCommandReady — the path can be handed over as a ready-to-run command.
	enrollCommandReady enrollCommandForm = iota
	// enrollCommandLinkedWorktree — the path is a linked (often per-session)
	// worktree, so naming it would prescribe an enrollment that outlives the
	// directory.
	enrollCommandLinkedWorktree
	// enrollCommandUnprintable — the path cannot be reproduced verbatim inside
	// this message without the reproduction being wrong.
	enrollCommandUnprintable
)

// enrollCommandFor decides what the notice is allowed to say about enrolling
// top, and returns the shell-quoted argument when a full command is safe.
//
// The linked-worktree case is the important one, and it is not about hostile
// input at all. `repo add` stores the path VERBATIM, and in the claudew layout
// this feature was built for, `--show-toplevel` is a per-session worktree that
// is deleted at session end. A sibling worktree then resolves to that dead root
// through the common-dir tier, and the guard does not degrade there — it BLOCKS
// every commit until `runecho-ir repo prune-missing` runs (main.go's dirExists
// check, which #369/#370 added after 516 of 617 enrolments on one machine were
// found pointing at deleted trees). A notice that prescribes that is worse than
// one that prescribes nothing, so a linked worktree gets told what to enrol
// instead of being handed a path.
//
// The test is structural rather than a guess about naming: a repo's OWN
// worktree holds its common-dir as a direct child (<top>/.git), while a linked
// worktree's common-dir lives under a different parent (<container>/.bare, or
// the main worktree's .git).
//
// The other two cases are about reproduction:
//
//   - Sanitizing changed the path, so the sanitized form is not the path:
//     `repo add '/home/…(truncated)'` looks runnable and silently is not, and a
//     truncated argument is the one shape shell-quoting cannot make correct.
//   - The path contains a backtick. The command is wrapped in a markdown code
//     span, and a backtick inside SPLITS it: a directory named
//     "x` and then run `rm -rf ~/.runecho" renders to a reader as two code
//     spans, the second one a command nobody wrote. The shell-quoting invariant
//     holds at the byte level and is defeated at the presentation level by the
//     delimiters this message itself adds.
func enrollCommandFor(top, commonDir string) (string, enrollCommandForm) {
	if sanitizeReasonPath(top) != top || strings.ContainsRune(top, '`') {
		return "", enrollCommandUnprintable
	}
	if commonDir == "" || filepath.Dir(filepath.Clean(commonDir)) != filepath.Clean(top) {
		return "", enrollCommandLinkedWorktree
	}
	return enrollShellQuote(top), enrollCommandReady
}

// enrollNoticeText is what the agent reads. "offer it; do not run it
// unprompted" is load-bearing, not politeness: on a box where runecho-ir is
// allowlisted, an agent that reads an advisory naming a command can simply run
// it — which would reintroduce the auto-enroll posture that was rejected,
// sideways. The user's own permission prompt is meant to be the gate, and that
// sentence is what routes the decision back to them. It appears in all three
// forms below for that reason.
//
// The path is treated as hostile, because it is: on POSIX a directory name may
// contain anything but '/' and NUL, and this string reaches a model. The prose
// occurrence is rendered with %q — sanitizeReasonPath does not touch '.' or
// ordinary words, so a directory named "x. Symbol validation is ON" would
// otherwise read as continuous prose in a message whose next sentence is a real
// instruction, and quoting makes the boundary visible (#212: repo file paths
// are attacker text). What the command occurrence may say is enrollCommandFor's
// decision.
func enrollNoticeText(top, commonDir string) string {
	safe := sanitizeReasonPath(top)
	const head = "[runecho-guard] This git repo is not enrolled in RunEcho, so symbol validation is OFF for edits under %q. "
	const offer = "— offer it; do not run it unprompted. "
	const tail = "One-time notice per repo (RUNECHO_GUARD_ENROLL_NOTICE=0 disables it); every later edit here is silent."

	arg, form := enrollCommandFor(top, commonDir)
	switch form {
	case enrollCommandReady:
		return fmt.Sprintf(head+"If the user wants it guarded, the command is `runecho-ir repo add %s` "+offer+tail, safe, arg)
	case enrollCommandLinkedWorktree:
		return fmt.Sprintf(head+"If the user wants it guarded, the command is `runecho-ir repo add <dir>` "+offer+
			"That path above is a linked worktree, so ask which directory to enrol — usually the repository's main worktree, not a "+
			"per-session one. Enrolling a worktree that is later deleted makes the guard block commits in every sibling worktree "+
			"until `runecho-ir repo prune-missing` is run. "+tail, safe)
	default:
		return fmt.Sprintf(head+"If the user wants it guarded, `runecho-ir repo add .` run from that directory will do it "+offer+
			"(The path above is shown sanitized, so it is not repeated here as a command argument.) "+tail, safe)
	}
}

// enrollNotice returns the advisory to attach to an unenrolled edit, or "" to
// stay silent. Gate order is cheapest-first, and the order matters: gate 3 is
// what keeps the notice off non-git directories (scratch edits under /tmp were
// the largest single slice of the census's no-repo events) at zero I/O cost,
// before any file is touched.
//
//  1. this really is the unenrolled arm
//  2. the feature is on
//  3. dir is inside a git worktree at all — both identities present
//  4. RUNECHO_HOME resolves
//  5. not already noticed (unlocked fast read)
//  6. the marker records now (locked re-check + write); if it does not, silence
//
// There is deliberately no "skip anything under os.TempDir()" gate. Every test
// here runs in t.TempDir(), so such a gate would make the notice unfirable in
// tests while every test asserting silence still passed — the self-concealing
// shape that has bitten this repo before.
func enrollNotice(res lookupResult, now time.Time) string {
	if !res.NoRepo || !enrollNoticeEnabled() {
		return ""
	}
	if res.GitCommonDir == "" || res.GitTopLevel == "" {
		return ""
	}
	dir, err := runechoDir()
	if err != nil {
		return ""
	}
	key := filepath.Clean(res.GitCommonDir)
	if _, seen := loadEnrollNotices(dir).Repos[key]; seen {
		return ""
	}
	if !recordEnrollNotice(dir, key, res.GitTopLevel, now) {
		return ""
	}
	return enrollNoticeText(res.GitTopLevel, res.GitCommonDir)
}
